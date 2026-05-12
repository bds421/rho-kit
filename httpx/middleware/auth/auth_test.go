package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bds421/rho-kit/security/v2/jwtutil"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

const testUUID = "550e8400-e29b-41d4-a716-446655440000"

// headerOnlyMiddleware is a test helper that enforces the X-User-Id header
// without JWT verification.
func headerOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireHeaderUser(w, r, "test", nil, next)
	})
}

func allowS2SImpersonationForTest() MTLSIdentityOption {
	return WithS2SImpersonationGuard(func(*http.Request, string, string) error {
		return nil
	})
}

func TestWithAllowedSANsClonesInput(t *testing.T) {
	sans := []string{"svc-a.internal", "spiffe://example.org/svc-a"}
	opt := WithAllowedSANs(sans)
	sans[0] = "mutated.internal"
	sans[1] = "spiffe://example.org/mutated"

	var cfg mtlsIdentityConfig
	opt(&cfg)

	if _, ok := cfg.allowedSANDNS["svc-a.internal"]; !ok {
		t.Fatalf("allowed DNS SANs = %v, want svc-a.internal", cfg.allowedSANDNS)
	}
	if _, ok := cfg.allowedSANDNS["mutated.internal"]; ok {
		t.Fatalf("allowed DNS SANs retained mutated input: %v", cfg.allowedSANDNS)
	}
	if _, ok := cfg.allowedSANURIs["spiffe://example.org/svc-a"]; !ok {
		t.Fatalf("allowed URI SANs = %v, want spiffe://example.org/svc-a", cfg.allowedSANURIs)
	}
	if _, ok := cfg.allowedSANURIs["spiffe://example.org/mutated"]; ok {
		t.Fatalf("allowed URI SANs retained mutated input: %v", cfg.allowedSANURIs)
	}
}

func TestWithAllowedCNsClonesInput(t *testing.T) {
	cns := []string{"svc-a"}
	opt := WithAllowedCNs(cns)
	cns[0] = "mutated"

	var cfg mtlsIdentityConfig
	opt(&cfg)

	if _, ok := cfg.allowedCNs["svc-a"]; !ok {
		t.Fatalf("allowed CNs = %v, want svc-a", cfg.allowedCNs)
	}
	if _, ok := cfg.allowedCNs["mutated"]; ok {
		t.Fatalf("allowed CNs retained mutated input: %v", cfg.allowedCNs)
	}
}

// fakePeerCert creates a mock x509.Certificate with the given CN for testing.
func fakePeerCert(cn string) *x509.Certificate {
	return &x509.Certificate{
		Subject:     pkix.Name{CommonName: cn},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
}

// withMTLS sets the TLS connection state with a peer certificate on the request.
// VerifiedChains is also populated so the middleware's chain-verified check
// passes — production traffic only ever reaches the handler when the TLS
// layer has actually validated the chain, so the test fixture must mirror
// that to be representative.
func withMTLS(r *http.Request, cn string) *http.Request {
	cert := fakePeerCert(cn)
	r.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
	return r
}

// withMTLSCert attaches a fully-formed certificate (CN + SANs) to the request.
func withMTLSCert(r *http.Request, cert *x509.Certificate) *http.Request {
	r.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
	return r
}

// --- Header-only helper tests ---

func TestHeaderMode_WithHeader(t *testing.T) {
	var capturedUserID string
	handler := headerOnlyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUserID = UserID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if capturedUserID != testUUID {
		t.Errorf("UserID = %q, want %q", capturedUserID, testUUID)
	}
}

func TestRequireHeaderUser_GuardPanicRejects(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("guard panic escaped: %v", r)
			}
		}()
		requireHeaderUser(rec, req, "cn:backend", func(*http.Request, string, string) error {
			panic("guard failed")
		}, next)
	}()

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if called {
		t.Fatal("next handler must not run when impersonation guard panics")
	}
}

func TestJWT_NilProviderPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil provider")
		}
	}()
	_ = JWT(nil)
}

func TestHeaderMode_WithoutHeader(t *testing.T) {
	called := false
	handler := headerOnlyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if called {
		t.Error("next handler should not be called")
	}
}

func TestHeaderMode_InvalidUUID(t *testing.T) {
	called := false
	handler := headerOnlyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-User-Id", "not-a-uuid")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid UUID, got %d", rec.Code)
	}
	if called {
		t.Error("next handler should not be called for invalid UUID")
	}
}

func TestHeaderMode_EmptyHeader(t *testing.T) {
	handler := headerOnlyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-User-Id", "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for empty header, got %d", rec.Code)
	}
}

func TestHeaderMode_DuplicateHeader(t *testing.T) {
	called := false
	handler := headerOnlyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Add("X-User-Id", testUUID)
	req.Header.Add("X-User-Id", "550e8400-e29b-41d4-a716-446655440001")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for duplicate X-User-Id header, got %d", rec.Code)
	}
	if called {
		t.Error("next handler should not be called for duplicate X-User-Id headers")
	}
}

func TestHeaderMode_RejectsAmbiguousIdentityValues(t *testing.T) {
	tests := map[string]string{
		"edge whitespace":     " " + testUUID + " ",
		"internal whitespace": testUUID[:8] + " " + testUUID[9:],
		"comma combined":      testUUID + ",550e8400-e29b-41d4-a716-446655440001",
		"control":             testUUID + "\n",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			called := false
			handler := headerOnlyMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("X-User-Id", value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
			if called {
				t.Fatal("next handler should not be called for ambiguous X-User-Id")
			}
		})
	}
}

func TestUserID_NoContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	id := UserID(req.Context())
	if id != "" {
		t.Errorf("UserID without context value = %q, want empty", id)
	}
}

// --- JWT helpers ---

func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func testJWKS(t *testing.T, key *ecdsa.PrivateKey, kid string) []byte {
	t.Helper()
	pubJWK, err := jwk.Import(key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	_ = pubJWK.Set(jwk.KeyIDKey, kid)
	_ = pubJWK.Set(jwk.AlgorithmKey, jwa.ES256())
	_ = pubJWK.Set(jwk.KeyUsageKey, "sig")

	set := jwk.NewSet()
	_ = set.AddKey(pubJWK)

	data, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func signJWT(t *testing.T, key *ecdsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()

	tok, err := jwt.NewBuilder().Build()
	if err != nil {
		t.Fatal(err)
	}

	for k, v := range claims {
		switch k {
		case "iat", "exp", "nbf":
			_ = tok.Set(k, time.Unix(toInt64(v), 0))
		default:
			_ = tok.Set(k, v)
		}
	}

	jwkKey, err := jwk.Import(key)
	if err != nil {
		t.Fatal(err)
	}
	_ = jwkKey.Set(jwk.KeyIDKey, kid)

	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.ES256(), jwkKey))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func newTestProvider(ks *jwtutil.KeySet) *jwtutil.Provider {
	return jwtutil.NewProviderWithKeySet(ks,
		jwtutil.WithAllowAnyIssuer(),
		jwtutil.WithAllowAnyAudience(),
	)
}

// --- JWT tests (JWT-only mode) ---

func TestJWT_ValidToken(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	var capturedUserID string
	var capturedPerms []string
	var capturedScopes string

	handler := JWT(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUserID = UserID(r.Context())
		capturedPerms = Permissions(r.Context())
		capturedScopes = Scopes(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	now := time.Now()
	token := signJWT(t, key, "kid-1", map[string]any{
		"sub":         testUUID,
		"permissions": []string{"general:view", "general:manage"},
		"scopes":      "production:manage",
		"iat":         now.Unix(),
		"exp":         now.Add(5 * time.Minute).Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if capturedUserID != testUUID {
		t.Errorf("UserID = %q, want %q", capturedUserID, testUUID)
	}
	if len(capturedPerms) != 2 {
		t.Errorf("Permissions count = %d, want 2", len(capturedPerms))
	}
	if capturedScopes != "production:manage" {
		t.Errorf("Scopes = %q, want production:manage", capturedScopes)
	}
}

func TestJWT_InvalidToken(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	handler := JWT(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer invalid.jwt.token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid JWT, got %d", rec.Code)
	}
}

func TestJWT_RejectsDuplicateAuthorization(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := JWT(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	now := time.Now()
	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": testUUID,
		"iat": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Add("Authorization", "Bearer "+token)
	req.Header.Add("Authorization", "Bearer invalid.jwt.token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for duplicate Authorization headers, got %d", rec.Code)
	}
	if called {
		t.Fatal("next handler should not be called for duplicate Authorization headers")
	}
}

func TestJWT_RejectsAmbiguousAuthorization(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	for name, value := range map[string]string{
		"edge whitespace":  " Bearer token",
		"token whitespace": "Bearer token ",
		"internal space":   "Bearer token extra",
		"comma":            "Bearer token,other",
		"control":          "Bearer token\n",
		"oversized":        "Bearer " + strings.Repeat("a", maxBearerTokenLen+1),
	} {
		t.Run(name, func(t *testing.T) {
			called := false
			handler := JWT(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 for ambiguous Authorization, got %d", rec.Code)
			}
			if called {
				t.Fatal("next handler should not be called for ambiguous Authorization")
			}
		})
	}
}

func TestJWT_ExpiredToken(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	handler := JWT(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": testUUID,
		"exp": time.Now().Add(-5 * time.Minute).Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired JWT, got %d", rec.Code)
	}
}

func TestJWT_NonUUIDSubject(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	handler := JWT(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": "not-a-uuid",
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for non-UUID subject, got %d", rec.Code)
	}
}

func TestJWT_RejectsHeaderFallback(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := JWT(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when header fallback used on JWT-only middleware, got %d", rec.Code)
	}
	if called {
		t.Error("next handler should not be called")
	}
}

func TestJWT_RejectsHeaderEvenWithMTLS(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := JWT(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := withMTLS(httptest.NewRequest(http.MethodGet, "/", nil), "backend")
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 — JWT-only middleware must never accept mTLS S2S, got %d", rec.Code)
	}
	if called {
		t.Error("next handler should not be called")
	}
}

func TestPermissions_NoContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	perms := Permissions(req.Context())
	if perms != nil {
		t.Errorf("Permissions without context = %v, want nil", perms)
	}
}

func TestScopes_NoContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	scopes := Scopes(req.Context())
	if scopes != "" {
		t.Errorf("Scopes without context = %q, want empty", scopes)
	}
}

// --- RequireS2SAuth tests (mTLS-based) ---

func TestRequireS2SAuth_PanicsOnNilProvider(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil provider")
		}
	}()
	RequireS2SAuth(nil, []string{"backend"})
}

func TestRequireS2SAuth_PanicsOnEmptyCNs(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for empty CN list")
		}
	}()
	RequireS2SAuth(provider, nil, allowS2SImpersonationForTest())
}

func TestRequireS2SAuth_PanicsWithoutImpersonationGuard(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when mTLS S2S auth has no impersonation guard")
		}
	}()
	RequireS2SAuth(provider, []string{"backend"})
}

func TestRequireS2SAuth_ValidJWT(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	var capturedUserID string
	handler := RequireS2SAuth(provider, []string{"backend"}, allowS2SImpersonationForTest())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUserID = UserID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	now := time.Now()
	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": testUUID,
		"iat": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if capturedUserID != testUUID {
		t.Errorf("UserID = %q, want %q", capturedUserID, testUUID)
	}
}

func TestRequireS2SAuth_ValidMTLS(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	var capturedUserID string
	handler := RequireS2SAuth(provider, []string{"backend"}, allowS2SImpersonationForTest())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUserID = UserID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := withMTLS(httptest.NewRequest(http.MethodGet, "/", nil), "backend")
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for mTLS S2S, got %d", rec.Code)
	}
	if capturedUserID != testUUID {
		t.Errorf("UserID = %q, want %q", capturedUserID, testUUID)
	}

	perms := Permissions(WithUserID(req.Context(), testUUID))
	if perms != nil {
		t.Errorf("S2S should have nil permissions, got %v", perms)
	}
}

func TestRequireS2SAuth_ImpersonationGuardRejects(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	guardCalled := false
	handler := RequireS2SAuth(provider, []string{"backend"}, WithS2SImpersonationGuard(func(_ *http.Request, identity, userID string) error {
		guardCalled = true
		if identity != "cn:backend" {
			t.Fatalf("identity = %q, want cn:backend", identity)
		}
		if userID != testUUID {
			t.Fatalf("userID = %q, want %q", userID, testUUID)
		}
		return http.ErrAbortHandler
	}))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := withMTLS(httptest.NewRequest(http.MethodGet, "/", nil), "backend")
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for guard rejection, got %d", rec.Code)
	}
	if !guardCalled {
		t.Fatal("impersonation guard was not called")
	}
	if called {
		t.Fatal("next handler must not be called when impersonation guard rejects")
	}
}

func TestRequireS2SAuth_RejectsDuplicateXUserID(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := RequireS2SAuth(provider, []string{"backend"}, allowS2SImpersonationForTest())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := withMTLS(httptest.NewRequest(http.MethodGet, "/", nil), "backend")
	req.Header.Add("X-User-Id", testUUID)
	req.Header.Add("X-User-Id", "550e8400-e29b-41d4-a716-446655440001")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for duplicate X-User-Id headers, got %d", rec.Code)
	}
	if called {
		t.Fatal("next handler should not be called for duplicate X-User-Id headers")
	}
}

func TestRequireS2SAuth_UnknownCN(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := RequireS2SAuth(provider, []string{"backend"}, allowS2SImpersonationForTest())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := withMTLS(httptest.NewRequest(http.MethodGet, "/", nil), "notification-service")
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unknown CN, got %d", rec.Code)
	}
	if called {
		t.Error("next handler should not be called with unknown CN")
	}
}

func TestRequireS2SAuth_NoTLS(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := RequireS2SAuth(provider, []string{"backend"}, allowS2SImpersonationForTest())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without TLS, got %d", rec.Code)
	}
	if called {
		t.Error("next handler should not be called without TLS")
	}
}

func TestRequireS2SAuth_TLSButNoPeerCerts(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := RequireS2SAuth(provider, []string{"backend"}, allowS2SImpersonationForTest())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: nil}
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without peer certs, got %d", rec.Code)
	}
	if called {
		t.Error("next handler should not be called without peer certs")
	}
}

func TestRequireS2SAuth_InvalidJWT(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	handler := RequireS2SAuth(provider, []string{"backend"}, allowS2SImpersonationForTest())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer invalid.jwt.token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid JWT on S2S middleware, got %d", rec.Code)
	}
}

func TestRequireS2SAuth_RejectsMalformedAuthorizationBeforeMTLSFallback(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := RequireS2SAuth(provider, []string{"backend"}, allowS2SImpersonationForTest())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := withMTLS(httptest.NewRequest(http.MethodGet, "/", nil), "backend")
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for malformed Authorization before mTLS fallback, got %d", rec.Code)
	}
	if called {
		t.Fatal("next handler should not be called for malformed Authorization")
	}
}

func TestRequireS2SAuth_RejectsDuplicateAuthorizationBeforeMTLSFallback(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := RequireS2SAuth(provider, []string{"backend"}, allowS2SImpersonationForTest())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	now := time.Now()
	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": testUUID,
		"iat": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	req := withMTLS(httptest.NewRequest(http.MethodGet, "/", nil), "backend")
	req.Header.Add("Authorization", "Bearer "+token)
	req.Header.Add("Authorization", "Bearer invalid.jwt.token")
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for duplicate Authorization before mTLS fallback, got %d", rec.Code)
	}
	if called {
		t.Fatal("next handler should not be called for duplicate Authorization")
	}
}

// TestRequireS2SAuth_MTLS_SetsTrustedMarker locks in the invariant that
// the mTLS branch is what stamps the trusted-S2S marker — and is the only
// thing that does. Without this guarantee, RequirePermission's bypass
// becomes either too broad (every nil-perms request bypasses) or
// inaccessible to legitimate internal callers.
func TestRequireS2SAuth_MTLS_SetsTrustedMarker(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	var sawTrusted bool
	handler := RequireS2SAuth(provider, []string{"backend"}, allowS2SImpersonationForTest())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawTrusted = IsTrustedS2S(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := withMTLS(httptest.NewRequest(http.MethodGet, "/", nil), "backend")
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for mTLS S2S, got %d", rec.Code)
	}
	if !sawTrusted {
		t.Fatal("mTLS S2S branch must set the trusted-S2S marker")
	}
}

// TestRequireS2SAuth_JWT_DoesNotSetTrustedMarker locks in the converse
// invariant: the JWT branch (whether reached via JWT or
// RequireS2SAuth's first-class JWT check) must NOT set the trusted-S2S
// marker. JWT callers are governed by their permissions claim; treating
// them as trusted would let a token issued without permissions bypass RBAC.
func TestRequireS2SAuth_JWT_DoesNotSetTrustedMarker(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	var sawTrusted bool
	handler := RequireS2SAuth(provider, []string{"backend"}, allowS2SImpersonationForTest())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawTrusted = IsTrustedS2S(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	now := time.Now()
	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": testUUID,
		"iat": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for JWT S2S, got %d", rec.Code)
	}
	if sawTrusted {
		t.Fatal("JWT branch must not set the trusted-S2S marker")
	}
}

func TestRequireS2SAuth_MultipleCNsAllowed(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	handler := RequireS2SAuth(provider, []string{"backend", "notification-service"}, allowS2SImpersonationForTest())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, cn := range []string{"backend", "notification-service"} {
		req := withMTLS(httptest.NewRequest(http.MethodGet, "/", nil), cn)
		req.Header.Set("X-User-Id", testUUID)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("CN=%s: expected 200, got %d", cn, rec.Code)
		}
	}
}

// --- RequirePermission tests ---

func TestRequirePermission_Allowed(t *testing.T) {
	called := false
	h := RequirePermission("general:view")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := WithUserID(req.Context(), testUUID)
	ctx = WithPermissions(ctx, []string{"general:view", "general:manage"})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !called {
		t.Error("next handler should be called")
	}
}

func TestRequirePermission_Denied(t *testing.T) {
	called := false
	h := RequirePermission("users:manage")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	ctx := WithUserID(req.Context(), testUUID)
	ctx = WithPermissions(ctx, []string{"general:view"})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if called {
		t.Error("next handler should not be called")
	}
}

// TestRequirePermission_NilPermissions_NoMarker_Denied is the regression
// test for the fail-open bug: a request with no permissions claim and no
// trusted-S2S marker (e.g., a route accidentally mounted without any auth
// middleware in front of it, or a JWT issued without a permissions claim)
// must be rejected.
func TestRequirePermission_NilPermissions_NoMarker_Denied(t *testing.T) {
	called := false
	h := RequirePermission("general:view")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(WithUserID(req.Context(), testUUID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for missing permissions claim without trusted-S2S marker, got %d", rec.Code)
	}
	if called {
		t.Error("next handler must not be called when permissions claim is missing")
	}
}

// TestRequirePermission_NoAuthAtAll_Denied covers the most dangerous
// misconfiguration: RequirePermission mounted on a route with NO auth
// middleware in front. The pre-fix behaviour silently granted access.
func TestRequirePermission_NoAuthAtAll_Denied(t *testing.T) {
	called := false
	h := RequirePermission("general:view")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unauthenticated request, got %d", rec.Code)
	}
	if called {
		t.Error("next handler must not be called for unauthenticated request")
	}
}

func TestRequirePermission_EmptyPermissions(t *testing.T) {
	called := false
	h := RequirePermission("general:view")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := WithUserID(req.Context(), testUUID)
	ctx = WithPermissions(ctx, []string{})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for empty perms, got %d", rec.Code)
	}
	if called {
		t.Error("next handler should not be called for empty perms")
	}
}

// --- PermissionByMethod tests ---

func TestPermissionByMethod_ReadAllowed(t *testing.T) {
	called := false
	h := PermissionByMethod("general:view", "general:manage")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		called = false
		req := httptest.NewRequest(method, "/", nil)
		ctx := WithUserID(req.Context(), testUUID)
		ctx = WithPermissions(ctx, []string{"general:view"})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", method, rec.Code)
		}
		if !called {
			t.Errorf("%s: next handler should be called", method)
		}
	}
}

func TestPermissionByMethod_WriteAllowed(t *testing.T) {
	called := false
	h := PermissionByMethod("general:view", "general:manage")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		called = false
		req := httptest.NewRequest(method, "/", nil)
		ctx := WithUserID(req.Context(), testUUID)
		ctx = WithPermissions(ctx, []string{"general:manage"})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", method, rec.Code)
		}
		if !called {
			t.Errorf("%s: next handler should be called", method)
		}
	}
}

func TestPermissionByMethod_WriteDenied(t *testing.T) {
	h := PermissionByMethod("general:view", "general:manage")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	ctx := WithUserID(req.Context(), testUUID)
	ctx = WithPermissions(ctx, []string{"general:view"})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for write without manage perm, got %d", rec.Code)
	}
}

// TestPermissionByMethod_NilPermissions_NoMarker_Denied is the regression
// test for the by-method fail-open variant.
func TestPermissionByMethod_NilPermissions_NoMarker_Denied(t *testing.T) {
	called := false
	h := PermissionByMethod("general:view", "general:manage")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(WithUserID(req.Context(), testUUID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for missing permissions claim without trusted-S2S marker, got %d", rec.Code)
	}
	if called {
		t.Error("next handler must not be called when permissions claim is missing")
	}
}

// --- WithUserID / WithPermissions tests ---

func TestWithUserID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := WithUserID(req.Context(), testUUID)

	if got := UserID(ctx); got != testUUID {
		t.Errorf("WithUserID: UserID() = %q, want %q", got, testUUID)
	}
}

func TestWithPermissions(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	perms := []string{"general:view", "users:manage"}
	ctx := WithPermissions(req.Context(), perms)

	got := Permissions(ctx)
	if len(got) != 2 || got[0] != "general:view" || got[1] != "users:manage" {
		t.Errorf("WithPermissions: Permissions() = %v, want %v", got, perms)
	}
}

func TestWithPermissions_ClonesInputAndOutput(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	perms := []string{"general:view", "users:manage"}
	ctx := WithPermissions(req.Context(), perms)

	perms[0] = "admin:all"
	got := Permissions(ctx)
	if got[0] != "general:view" {
		t.Fatalf("Permissions() reflected caller mutation: %v", got)
	}
	if !hasPermissionFast(ctx, "general:view") {
		t.Fatal("permission set lost original permission")
	}
	if hasPermissionFast(ctx, "admin:all") {
		t.Fatal("permission set adopted mutated permission")
	}

	got[0] = "mutated"
	again := Permissions(ctx)
	if again[0] != "general:view" {
		t.Fatalf("Permissions() returned mutable context slice: %v", again)
	}
}

func TestRequirePermission_PanicsOnEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty permission name, got none")
		}
	}()
	RequirePermission("")
}

func TestPermissionByMethod_PanicsOnEmpty(t *testing.T) {
	cases := []struct {
		name      string
		readPerm  string
		writePerm string
	}{
		{"empty read", "", "users:write"},
		{"empty write", "users:read", ""},
		{"both empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic, got none")
				}
			}()
			PermissionByMethod(tc.readPerm, tc.writePerm)
		})
	}
}

// --- CN allowlist hardening (R5) ---

// TestRequireS2SAuth_PanicsOnSingleEmptyCN locks in the regression that
// pre-fix accepted []string{""} and authorised any verified SAN-only cert.
func TestRequireS2SAuth_PanicsOnSingleEmptyCN(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for []string{\"\"}")
		}
	}()
	RequireS2SAuth(provider, []string{""}, allowS2SImpersonationForTest())
}

func TestRequireS2SAuth_PanicsOnWhitespaceCN(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for []string{\"  \"}")
		}
	}()
	RequireS2SAuth(provider, []string{"  "}, allowS2SImpersonationForTest())
}

func TestRequireS2SAuth_TrimsCN(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	handler := RequireS2SAuth(provider, []string{"  backend  "}, allowS2SImpersonationForTest())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := withMTLS(httptest.NewRequest(http.MethodGet, "/", nil), "backend")
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with trimmed CN allowlist, got %d", rec.Code)
	}
}

func TestRequireS2SAuth_RejectsSANOnlyCertWithCNAllowlist(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := RequireS2SAuth(provider, []string{"backend"}, allowS2SImpersonationForTest())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: ""},
		DNSNames:    []string{"some-other.internal"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	req := withMTLSCert(httptest.NewRequest(http.MethodGet, "/", nil), cert)
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for SAN-only cert against CN allowlist, got %d", rec.Code)
	}
	if called {
		t.Error("next handler must not be called for SAN-only cert with no SAN allowlist match")
	}
}

// --- SAN-aware identity (RequireS2SAuthWithIdentity) ---

func TestRequireS2SAuthWithIdentity_PanicsOnNilProvider(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil provider")
		}
	}()
	RequireS2SAuthWithIdentity(nil, WithAllowedCNs([]string{"backend"}))
}

func TestRequireS2SAuthWithIdentity_PanicsOnNoIdentities(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when neither CNs nor SANs are supplied")
		}
	}()
	RequireS2SAuthWithIdentity(provider, allowS2SImpersonationForTest())
}

func TestRequireS2SAuthWithIdentity_PanicsWithoutImpersonationGuard(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when mTLS identity auth has no impersonation guard")
		}
	}()
	RequireS2SAuthWithIdentity(provider, WithAllowedSANs([]string{"svc-a.internal"}))
}

func TestRequireS2SAuthWithIdentity_PanicsOnAllEmptyEntries(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when all entries are empty after trimming")
		}
	}()
	RequireS2SAuthWithIdentity(provider,
		allowS2SImpersonationForTest(),
		WithAllowedCNs([]string{"", "  "}),
		WithAllowedSANs([]string{"", "   "}),
	)
}

func TestRequireS2SAuthWithIdentity_PanicsOnInvalidSANEntries(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	for _, san := range []string{
		"svc name.internal",
		"svc/internal",
		"svc_name.internal",
		"-svc.internal",
		"svc-.internal",
		"*.internal",
		string([]byte{'s', 'v', 'c', 0xff}),
		"spiffe://example.org/svc-a?debug=true",
		"spiffe://user@example.org/svc-a",
		"spiffe://example.org/svc-a#frag",
	} {
		t.Run(san, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic for invalid SAN allowlist entry")
				}
			}()
			RequireS2SAuthWithIdentity(provider, WithAllowedSANs([]string{san}))
		})
	}
}

func TestRequireS2SAuthWithIdentity_PanicsOnInvalidCNEntries(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	for _, cn := range []string{
		"svc\nname",
		"svc\tname",
		"svc\x00name",
		string([]byte{'s', 'v', 'c', 0xff}),
	} {
		t.Run(cn, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic for invalid CN allowlist entry")
				}
			}()
			RequireS2SAuthWithIdentity(provider, WithAllowedCNs([]string{cn}))
		})
	}
}

func TestRequireS2SAuthWithIdentity_InvalidIdentityPanicDoesNotEchoValue(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	tests := []struct {
		name string
		want string
		fn   func()
	}{
		{
			name: "uri san",
			want: "middleware: WithAllowedSANs invalid URI SAN",
			fn: func() {
				RequireS2SAuthWithIdentity(provider, WithAllowedSANs([]string{"spiffe://example.org/%zz?token=secret-token"}))
			},
		},
		{
			name: "cn",
			want: "middleware: WithAllowedCNs invalid CN",
			fn: func() {
				RequireS2SAuthWithIdentity(provider, WithAllowedCNs([]string{"svc\nsecret-token"}))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic for invalid identity allowlist entry")
				}
				msg, ok := r.(string)
				if !ok || msg != tt.want {
					t.Fatalf("panic = %#v, want %q", r, tt.want)
				}
				if strings.Contains(msg, "secret-token") || strings.Contains(msg, "token=") || strings.Contains(msg, "%zz") {
					t.Fatalf("panic leaked identity value: %q", msg)
				}
			}()
			tt.fn()
		})
	}
}

func TestRequireS2SAuthWithIdentity_PanicsOnNilOption(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil option")
		}
	}()
	RequireS2SAuthWithIdentity(provider, nil)
}

func TestWithS2SImpersonationGuard_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil impersonation guard")
		}
	}()
	WithS2SImpersonationGuard(nil)
}

func TestRequireS2SAuthWithIdentity_SANDNSMatch(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	handler := RequireS2SAuthWithIdentity(provider,
		allowS2SImpersonationForTest(),
		WithAllowedSANs([]string{"svc-a.internal"}),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: ""},
		DNSNames:    []string{"svc-a.internal"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	req := withMTLSCert(httptest.NewRequest(http.MethodGet, "/", nil), cert)
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for SAN DNS match, got %d", rec.Code)
	}
}

func TestRequireS2SAuthWithIdentity_SANDNSMatchIsCaseInsensitive(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	handler := RequireS2SAuthWithIdentity(provider,
		allowS2SImpersonationForTest(),
		WithAllowedSANs([]string{"SVC-A.INTERNAL"}),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: ""},
		DNSNames:    []string{"svc-a.internal"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	req := withMTLSCert(httptest.NewRequest(http.MethodGet, "/", nil), cert)
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for case-insensitive SAN DNS match, got %d", rec.Code)
	}
}

func TestRequireS2SAuthWithIdentity_SANURIMatch(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	handler := RequireS2SAuthWithIdentity(provider,
		allowS2SImpersonationForTest(),
		WithAllowedSANs([]string{"spiffe://example.org/svc-a"}),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	uri, err := url.Parse("spiffe://example.org/svc-a")
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "ignored"},
		URIs:        []*url.URL{uri},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	req := withMTLSCert(httptest.NewRequest(http.MethodGet, "/", nil), cert)
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for SAN URI match, got %d", rec.Code)
	}
}

func TestRequireS2SAuthWithIdentity_NoMatchOnEither(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := RequireS2SAuthWithIdentity(provider,
		allowS2SImpersonationForTest(),
		WithAllowedCNs([]string{"svc-cn"}),
		WithAllowedSANs([]string{"svc-other.internal"}),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "stranger"},
		DNSNames:    []string{"stranger.internal"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	req := withMTLSCert(httptest.NewRequest(http.MethodGet, "/", nil), cert)
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for cert that matches neither allowlist, got %d", rec.Code)
	}
	if called {
		t.Error("next handler must not be called when neither CN nor SAN matches")
	}
}

func TestRequireS2SAuthWithIdentity_RejectsCertWithoutClientAuthEKU(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	for _, tt := range []struct {
		name string
		eku  []x509.ExtKeyUsage
	}{
		{name: "no EKU", eku: nil},
		{name: "server auth only", eku: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			handler := RequireS2SAuthWithIdentity(provider,
				allowS2SImpersonationForTest(),
				WithAllowedSANs([]string{"svc-a.internal"}),
			)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
			}))

			cert := &x509.Certificate{
				Subject:     pkix.Name{CommonName: ""},
				DNSNames:    []string{"svc-a.internal"},
				ExtKeyUsage: tt.eku,
			}
			req := withMTLSCert(httptest.NewRequest(http.MethodGet, "/", nil), cert)
			req.Header.Set("X-User-Id", testUUID)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 for cert without client-auth EKU, got %d", rec.Code)
			}
			if called {
				t.Fatal("next handler must not be called for a cert without client-auth EKU")
			}
		})
	}
}

func TestRequireS2SAuthWithIdentity_SANPreferredOverCN(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	handler := RequireS2SAuthWithIdentity(provider,
		allowS2SImpersonationForTest(),
		WithAllowedCNs([]string{"svc-cn"}),
		WithAllowedSANs([]string{"svc-san.internal"}),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cert := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "svc-cn"},
		DNSNames:    []string{"svc-san.internal"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	req := withMTLSCert(httptest.NewRequest(http.MethodGet, "/", nil), cert)
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when both CN and SAN match, got %d", rec.Code)
	}
}

func TestRequireS2SAuthWithIdentity_TrimsAndSkipsEmpty(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	// Mix of empty/whitespace and one real entry — handler must still build and accept.
	handler := RequireS2SAuthWithIdentity(provider,
		allowS2SImpersonationForTest(),
		WithAllowedCNs([]string{"", "  ", "  backend  "}),
		WithAllowedSANs([]string{""}),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := withMTLS(httptest.NewRequest(http.MethodGet, "/", nil), "backend")
	req.Header.Set("X-User-Id", testUUID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (real CN survives empty/whitespace siblings), got %d", rec.Code)
	}
}
