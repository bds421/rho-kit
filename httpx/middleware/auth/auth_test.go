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
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
	"github.com/bds421/rho-kit/security/jwtutil"
)

const testUUID = "550e8400-e29b-41d4-a716-446655440000"

// headerOnlyMiddleware is a test helper that enforces the X-User-Id header
// without JWT verification.
func headerOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireHeaderUser(w, r, next)
	})
}

// fakePeerCert creates a mock x509.Certificate with the given CN for testing.
func fakePeerCert(cn string) *x509.Certificate {
	return &x509.Certificate{
		Subject: pkix.Name{CommonName: cn},
	}
}

// withMTLS sets the TLS connection state with a peer certificate on the request.
func withMTLS(r *http.Request, cn string) *http.Request {
	r.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{fakePeerCert(cn)},
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

func TestRequireUserWithJWT_NilProviderPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil provider")
		}
	}()
	_ = RequireUserWithJWT(nil)
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
	return jwtutil.NewProviderWithKeySet(ks)
}

// --- RequireUserWithJWT tests (JWT-only mode) ---

func TestRequireUserWithJWT_ValidToken(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	var capturedUserID string
	var capturedPerms []string
	var capturedScopes string

	handler := RequireUserWithJWT(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestRequireUserWithJWT_InvalidToken(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	handler := RequireUserWithJWT(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestRequireUserWithJWT_ExpiredToken(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	handler := RequireUserWithJWT(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestRequireUserWithJWT_NonUUIDSubject(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	handler := RequireUserWithJWT(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestRequireUserWithJWT_RejectsHeaderFallback(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := RequireUserWithJWT(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestRequireUserWithJWT_RejectsHeaderEvenWithMTLS(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := RequireUserWithJWT(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	RequireS2SAuth(provider, nil)
}

func TestRequireS2SAuth_ValidJWT(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	var capturedUserID string
	handler := RequireS2SAuth(provider, []string{"backend"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := RequireS2SAuth(provider, []string{"backend"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestRequireS2SAuth_UnknownCN(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	called := false
	handler := RequireS2SAuth(provider, []string{"backend"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := RequireS2SAuth(provider, []string{"backend"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := RequireS2SAuth(provider, []string{"backend"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	handler := RequireS2SAuth(provider, []string{"backend"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestRequireS2SAuth_MultipleCNsAllowed(t *testing.T) {
	key := testKey(t)
	ks, _ := jwtutil.ParseKeySet(testJWKS(t, key, "kid-1"))
	provider := newTestProvider(ks)

	handler := RequireS2SAuth(provider, []string{"backend", "notification-service"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestRequirePermission_NilPermissions_S2S(t *testing.T) {
	called := false
	h := RequirePermission("general:view")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(WithUserID(req.Context(), testUUID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for S2S (nil perms), got %d", rec.Code)
	}
	if !called {
		t.Error("next handler should be called for S2S")
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

func TestPermissionByMethod_NilPermissions_S2S(t *testing.T) {
	called := false
	h := PermissionByMethod("general:view", "general:manage")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(WithUserID(req.Context(), testUUID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for S2S, got %d", rec.Code)
	}
	if !called {
		t.Error("next handler should be called for S2S")
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
