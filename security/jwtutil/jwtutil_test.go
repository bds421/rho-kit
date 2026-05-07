package jwtutil

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

func newTestJWKSServer(t *testing.T, jwksData []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksData)
	}))
}

func newTestJWKSServerFunc(t *testing.T, handler func(http.ResponseWriter, *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(handler))
}

func ecdsaPublicKeyPEM(t *testing.T, pub *ecdsa.PublicKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

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

func TestParseKeySet(t *testing.T) {
	key := testKey(t)
	data := testJWKS(t, key, "test-kid")

	ks, err := ParseKeySet(data)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the keyset works by signing and verifying a token.
	now := time.Now()
	token := signJWT(t, key, "test-kid", map[string]any{
		"sub": "user-1",
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	if _, err := ks.Verify(token, now); err != nil {
		t.Fatalf("keyset should verify a valid token: %v", err)
	}
}

func TestParseKeySet_EmptyKeys(t *testing.T) {
	data := []byte(`{"keys":[]}`)
	_, err := ParseKeySet(data)
	if err == nil {
		t.Fatal("expected error for empty keys")
	}
}

func TestParseKeySet_InvalidJSON(t *testing.T) {
	_, err := ParseKeySet([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestVerify_ValidToken(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub":         "550e8400-e29b-41d4-a716-446655440000",
		"permissions": []string{"general:view", "general:manage"},
		"scopes":      "production:manage",
		"iat":         now.Unix(),
		"exp":         now.Add(5 * time.Minute).Unix(),
		"nbf":         now.Add(-1 * time.Minute).Unix(),
		"iss":         "https://oathkeeper",
	})

	claims, err := ks.Verify(token, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if claims.Subject != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("subject = %q, want UUID", claims.Subject)
	}
	if len(claims.Permissions) != 2 {
		t.Errorf("permissions count = %d, want 2", len(claims.Permissions))
	}
	if claims.Scopes != "production:manage" {
		t.Errorf("scopes = %q, want production:manage", claims.Scopes)
	}
}

func TestVerify_MissingExp(t *testing.T) {
	// Tokens without exp must be rejected by default — non-expiring
	// bearer tokens are indistinguishable from a stolen credential.
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
	})

	_, err := ks.Verify(token, now)
	if err == nil {
		t.Fatal("expected error for token missing exp claim")
	}
}

func TestVerify_FutureExp(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"exp": now.Add(1 * time.Hour).Unix(),
	})

	claims, err := ks.Verify(token, now)
	if err != nil {
		t.Fatalf("future-exp token must verify: %v", err)
	}
	if claims.ExpiresAt == 0 {
		t.Fatal("ExpiresAt must be populated for accepted token")
	}
}

func TestVerify_ExpiredToken(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"exp": now.Add(-5 * time.Minute).Unix(),
	})

	_, err := ks.Verify(token, now)
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestVerify_NotYetValid(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"nbf": now.Add(5 * time.Minute).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
	})

	_, err := ks.Verify(token, now)
	if err == nil {
		t.Fatal("expected error for not-yet-valid token")
	}
}

func TestVerify_ClockSkew(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()

	// Token expired 20 seconds ago — within 30s clock skew.
	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"exp": now.Add(-20 * time.Second).Unix(),
	})

	_, err := ks.Verify(token, now)
	if err != nil {
		t.Fatalf("expected clock skew tolerance, got: %v", err)
	}
}

func TestVerify_WrongKey(t *testing.T) {
	signingKey := testKey(t)
	otherKey := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, otherKey, "kid-1"))

	token := signJWT(t, signingKey, "kid-1", map[string]any{
		"sub": "user-1",
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})

	_, err := ks.Verify(token, time.Now())
	if err == nil {
		t.Fatal("expected error for wrong key")
	}
}

func TestVerify_UnknownKid(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))

	token := signJWT(t, key, "kid-unknown", map[string]any{
		"sub": "user-1",
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})

	_, err := ks.Verify(token, time.Now())
	if err == nil {
		t.Fatal("expected error for unknown kid")
	}
}

func TestVerify_MissingSub(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))

	token := signJWT(t, key, "kid-1", map[string]any{
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})

	_, err := ks.Verify(token, time.Now())
	if err == nil {
		t.Fatal("expected error for missing sub")
	}
}

func TestVerify_MalformedToken(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))

	_, err := ks.Verify("not.a.valid-token", time.Now())
	if err == nil {
		t.Fatal("expected error for malformed token")
	}

	_, err = ks.Verify("two.parts", time.Now())
	if err == nil {
		t.Fatal("expected error for 2-part token")
	}
}

func TestVerify_UnsupportedAlgorithm(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))

	// Manually construct a token with RS256 header.
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"kid-1","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user-1"}`))
	token := header + "." + payload + ".fakesig"

	_, err := ks.Verify(token, time.Now())
	if err == nil {
		t.Fatal("expected error for unsupported algorithm")
	}
}

func TestVerify_TamperedPayload(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})

	// Tamper with the payload.
	parts := strings.SplitN(token, ".", 3)
	tampered := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"admin","exp":` +
		strings.TrimRight(base64.RawURLEncoding.EncodeToString([]byte("9999999999")), "=") + `}`))
	parts[1] = tampered
	tamperedToken := parts[0] + "." + parts[1] + "." + parts[2]

	_, err := ks.Verify(tamperedToken, time.Now())
	if err == nil {
		t.Fatal("expected error for tampered payload")
	}
}

func TestParseKeySetFromPEM_Valid(t *testing.T) {
	key := testKey(t)
	pemData := ecdsaPublicKeyPEM(t, &key.PublicKey)

	ks, err := ParseKeySetFromPEM(pemData, "pem-kid")
	if err != nil {
		t.Fatalf("ParseKeySetFromPEM: %v", err)
	}

	now := time.Now()
	token := signJWT(t, key, "pem-kid", map[string]any{
		"sub": "user-1",
		"exp": now.Add(5 * time.Minute).Unix(),
	})

	claims, err := ks.Verify(token, now)
	if err != nil {
		t.Fatalf("verify with PEM keyset: %v", err)
	}
	if claims.Subject != "user-1" {
		t.Errorf("subject = %q, want user-1", claims.Subject)
	}
}

func TestParseKeySetFromPEM_InvalidPEM(t *testing.T) {
	_, err := ParseKeySetFromPEM([]byte("not-a-pem"), "kid")
	if err == nil {
		t.Fatal("expected error for invalid PEM")
	}
}

func TestVerify_ExpectedIssuer_Match(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	ks.ExpectedIssuer = "https://oathkeeper"
	now := time.Now()

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"iss": "https://oathkeeper",
		"exp": now.Add(5 * time.Minute).Unix(),
	})

	claims, err := ks.Verify(token, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims.Issuer != "https://oathkeeper" {
		t.Errorf("issuer = %q, want https://oathkeeper", claims.Issuer)
	}
}

func TestVerify_ExpectedIssuer_Mismatch(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	ks.ExpectedIssuer = "https://oathkeeper"
	now := time.Now()

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"iss": "https://evil-issuer",
		"exp": now.Add(5 * time.Minute).Unix(),
	})

	_, err := ks.Verify(token, now)
	if err == nil {
		t.Fatal("expected error for issuer mismatch")
	}
}

func TestToStringSlice_StringSlice(t *testing.T) {
	in := []string{"a", "b", "c"}
	out, err := toStringSlice(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 3 || out[0] != "a" || out[1] != "b" || out[2] != "c" {
		t.Errorf("toStringSlice([]string) = %v", out)
	}
}

func TestToStringSlice_AnySlice(t *testing.T) {
	in := []any{"x", "y", "z"}
	out, err := toStringSlice(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 3 || out[0] != "x" || out[1] != "y" || out[2] != "z" {
		t.Errorf("toStringSlice([]any) = %v", out)
	}
}

func TestToStringSlice_AnySliceWithNonString(t *testing.T) {
	// A misshaped element must surface as an error so the caller can
	// reject the token instead of silently truncating to []string{"x"}.
	in := []any{"x", 42}
	_, err := toStringSlice(in)
	if err == nil {
		t.Fatal("expected error for non-string element in []any")
	}
}

func TestToStringSlice_Other(t *testing.T) {
	_, err := toStringSlice("not-a-slice")
	if err == nil {
		t.Fatal("expected error for non-slice value")
	}
}

func TestDefaultHTTPClient_RetainsTransportDefaults(t *testing.T) {
	// Regression: defaultHTTPClient previously installed a bare
	// http.Transport that lost proxy handling, dialer timeouts, and the
	// idle-conn pool. It now clones http.DefaultTransport.
	c := defaultHTTPClient()
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", c.Transport)
	}
	if tr.Proxy == nil {
		t.Error("Proxy must be inherited from DefaultTransport (env-aware)")
	}
	if tr.DialContext == nil {
		t.Error("DialContext must be inherited from DefaultTransport")
	}
	if tr.TLSHandshakeTimeout == 0 {
		t.Error("TLSHandshakeTimeout must be inherited from DefaultTransport")
	}
	if tr.IdleConnTimeout == 0 {
		t.Error("IdleConnTimeout must be inherited from DefaultTransport")
	}
	if tr.MaxIdleConns == 0 {
		t.Error("MaxIdleConns must be inherited from DefaultTransport")
	}
	if tr.MaxResponseHeaderBytes != 64*1024 {
		t.Errorf("MaxResponseHeaderBytes = %d, want 65536 (kit override)", tr.MaxResponseHeaderBytes)
	}
	if c.Timeout != defaultHTTPTimeout {
		t.Errorf("client Timeout = %v, want %v", c.Timeout, defaultHTTPTimeout)
	}
}

func TestNewProvider(t *testing.T) {
	p := NewProvider("https://example.com/.well-known/jwks.json", nil, 5*time.Minute,
		WithExpectedIssuer("https://example.com"),
		WithExpectedAudience("svc"))
	if p.url != "https://example.com/.well-known/jwks.json" {
		t.Errorf("url = %q", p.url)
	}
	if p.KeySet() != nil {
		t.Error("initial keyset should be nil")
	}
}

// NewProvider must reject any configuration that leaves issuer or audience
// unspecified without an explicit opt-out — the kit-level confused-deputy
// guardrail (RFC 7519 §4.1.3). Standalone callers must pair WithExpectedIssuer
// or WithAllowAnyIssuer (and likewise for audience) just like the Builder.
func TestNewProvider_PanicsWithoutIssuerConfig(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when neither WithExpectedIssuer nor WithAllowAnyIssuer is supplied")
		}
		if !strings.Contains(fmt.Sprint(r), "WithExpectedIssuer") {
			t.Fatalf("panic must mention required option; got: %v", r)
		}
	}()
	_ = NewProvider("https://example.com/jwks", nil, time.Minute, WithAllowAnyAudience())
}

func TestNewProvider_PanicsWithoutAudienceConfig(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when neither WithExpectedAudience nor WithAllowAnyAudience is supplied")
		}
		if !strings.Contains(fmt.Sprint(r), "WithExpectedAudience") {
			t.Fatalf("panic must mention required option; got: %v", r)
		}
	}()
	_ = NewProvider("https://example.com/jwks", nil, time.Minute, WithAllowAnyIssuer())
}

func TestNewProvider_AcceptsExplicitOptOuts(t *testing.T) {
	p := NewProvider("https://example.com/jwks", nil, time.Minute,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience())
	if p == nil {
		t.Fatal("expected provider with explicit opt-outs")
	}
}

func TestNewProvider_AcceptsConfiguredIssuerAndAudience(t *testing.T) {
	p := NewProvider("https://example.com/jwks", nil, time.Minute,
		WithExpectedIssuer("https://issuer.example.com"),
		WithExpectedAudience("svc"))
	if p.expectedIssuer != "https://issuer.example.com" {
		t.Errorf("expectedIssuer = %q", p.expectedIssuer)
	}
	if p.expectedAudience != "svc" {
		t.Errorf("expectedAudience = %q", p.expectedAudience)
	}
}

func TestProvider_KeySetReturnsNilWhenStale(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	p := NewProvider("https://example.com/jwks", nil, time.Minute,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
		WithMaxStale(30*time.Minute),
		withClock(clock),
	)

	// Simulate a successful fetch 31 minutes ago.
	p.mu.Lock()
	p.keyset = &KeySet{}
	p.lastSuccessfulFetch = now.Add(-31 * time.Minute)
	p.mu.Unlock()

	if got := p.KeySet(); got != nil {
		t.Fatal("KeySet should return nil when last fetch is older than max-stale")
	}

	// And not nil when within the window.
	p.mu.Lock()
	p.lastSuccessfulFetch = now.Add(-15 * time.Minute)
	p.mu.Unlock()

	if got := p.KeySet(); got == nil {
		t.Fatal("KeySet should not be nil when last fetch is within max-stale")
	}
}

func TestProvider_MaxStaleZeroDisablesCheck(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	p := NewProvider("https://example.com/jwks", nil, time.Minute,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
		WithMaxStale(0), // disabled
		withClock(clock),
	)
	p.mu.Lock()
	p.keyset = &KeySet{}
	p.lastSuccessfulFetch = now.Add(-365 * 24 * time.Hour) // a year ago
	p.mu.Unlock()

	if got := p.KeySet(); got == nil {
		t.Fatal("KeySet must not return nil when max-stale is disabled")
	}
}

func TestProvider_StalenessAccessor(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	p := NewProvider("https://example.com/jwks", nil, time.Minute,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
		withClock(clock),
	)
	if got := p.Staleness(); got != 0 {
		t.Errorf("Staleness should be 0 before any fetch; got %v", got)
	}
	p.mu.Lock()
	p.lastSuccessfulFetch = now.Add(-10 * time.Minute)
	p.mu.Unlock()
	if got := p.Staleness(); got != 10*time.Minute {
		t.Errorf("Staleness = %v, want 10m", got)
	}
}

func TestNewProvider_AllowAnyIssuerOptOut(t *testing.T) {
	p := NewProvider("https://example.com/jwks", nil, time.Minute,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience())
	if p == nil {
		t.Fatal("expected provider, got nil")
	}
}

func TestNewProvider_WithExpectedIssuer(t *testing.T) {
	p := NewProvider("https://example.com/jwks", nil, time.Minute,
		WithExpectedIssuer("https://issuer"),
		WithAllowAnyAudience())
	if p.expectedIssuer != "https://issuer" {
		t.Errorf("expectedIssuer = %q", p.expectedIssuer)
	}
}

func TestNewProviderWithKeySet(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	p := NewProviderWithKeySet(ks, WithAllowAnyIssuer(), WithAllowAnyAudience())
	if p.KeySet() != ks {
		t.Error("expected keyset to be set")
	}
}

func TestNewProviderWithKeySet_PanicsWithoutIssuerOrAudienceOpts(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))

	cases := map[string][]ProviderOption{
		"missing both":     nil,
		"missing audience": {WithExpectedIssuer("https://issuer")},
		"missing issuer":   {WithExpectedAudience("svc")},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic on missing iss/aud opt")
				}
			}()
			_ = NewProviderWithKeySet(ks, opts...)
		})
	}
}

func TestNewProviderWithKeySet_AcceptsExplicitOptIns(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))

	p := NewProviderWithKeySet(ks,
		WithExpectedIssuer("https://issuer"),
		WithExpectedAudience("svc"),
	)
	if p.KeySet() != ks {
		t.Fatal("expected keyset to be set")
	}
	if got := ks.ExpectedIssuer; got != "https://issuer" {
		t.Errorf("ExpectedIssuer = %q, want https://issuer", got)
	}
	if got := ks.ExpectedAudience; got != "svc" {
		t.Errorf("ExpectedAudience = %q, want svc", got)
	}
}

func TestNewProviderWithKeySet_AcceptsExplicitOptOuts(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))

	p := NewProviderWithKeySet(ks,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
	)
	if p.KeySet() != ks {
		t.Fatal("expected keyset to be set")
	}
}

func TestProvider_Run_FetchFromTestServer(t *testing.T) {
	key := testKey(t)
	jwksData := testJWKS(t, key, "kid-1")

	srv := newTestJWKSServer(t, jwksData)
	defer srv.Close()

	p := NewProvider(srv.URL, srv.Client(), 100*time.Millisecond,
		WithExpectedIssuer("test"),
		WithAllowAnyAudience())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	// Wait for initial fetch.
	deadline := time.After(2 * time.Second)
	for p.KeySet() == nil {
		select {
		case <-deadline:
			t.Fatal("keyset not fetched within deadline")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	ks := p.KeySet()
	if ks == nil {
		t.Fatal("expected keyset after Run")
	}
	if ks.ExpectedIssuer != "test" {
		t.Errorf("expectedIssuer = %q, want test", ks.ExpectedIssuer)
	}

	cancel()
	<-done
}

func TestProvider_Run_RetryOnFailure(t *testing.T) {
	key := testKey(t)
	jwksData := testJWKS(t, key, "kid-1")

	callCount := 0
	srv := newTestJWKSServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksData)
	})
	defer srv.Close()

	p := NewProvider(srv.URL, srv.Client(), time.Hour,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience())

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	deadline := time.After(12 * time.Second)
	for p.KeySet() == nil {
		select {
		case <-deadline:
			t.Fatal("keyset not fetched after retry")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	cancel()
	<-done
}

func TestVerify_NoPermissions(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"exp": now.Add(5 * time.Minute).Unix(),
	})

	claims, err := ks.Verify(token, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if claims.Permissions != nil {
		t.Errorf("permissions = %v, want nil", claims.Permissions)
	}
}

// withCapturedSlog swaps the process default slog handler for a buffer-backed
// one for the duration of fn and returns the captured output. The handler is
// restored on exit so concurrent tests inherit a clean default.
func withCapturedSlog(t *testing.T, level slog.Level, fn func()) string {
	t.Helper()
	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(syncWriter{w: &buf, mu: &mu}, &slog.HandlerOptions{Level: level})))
	fn()
	mu.Lock()
	defer mu.Unlock()
	return buf.String()
}

type syncWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (s syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func TestPermissionsClaim_MalformedRejectsToken(t *testing.T) {
	// R2 fix: a malformed permissions claim (e.g. a string where an
	// array is expected, or an array element that is not a string) used
	// to silently downgrade to an empty permission set, hiding issuer
	// drift behind fail-closed RBAC. The verifier now rejects the token
	// outright with an authentication error.
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub":         "user-1",
		"permissions": "not-an-array",
		"exp":         now.Add(5 * time.Minute).Unix(),
	})

	captured := withCapturedSlog(t, slog.LevelWarn, func() {
		_, err := ks.Verify(token, now)
		if err == nil {
			t.Fatal("expected verification to fail on malformed permissions claim")
		}
		if !strings.Contains(err.Error(), "malformed permissions claim") {
			t.Errorf("error must identify the malformed claim; got: %v", err)
		}
	})

	if !strings.Contains(captured, "level=WARN") {
		t.Errorf("expected WARN-level log entry, got: %s", captured)
	}
	if !strings.Contains(captured, "permissions claim malformed") {
		t.Errorf("expected permissions-claim warning, got: %s", captured)
	}
}

func TestPermissionsClaim_NumericArrayElementRejectsToken(t *testing.T) {
	// `permissions: [123]` decodes to []any{int} — under the old
	// behaviour every element was filtered, leaving an empty []string.
	// The token is now rejected so downstream RBAC sees the failure
	// instead of silently dropping privileges.
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub":         "user-1",
		"permissions": []any{123},
		"exp":         now.Add(5 * time.Minute).Unix(),
	})

	_, err := ks.Verify(token, now)
	if err == nil {
		t.Fatal("expected verification to fail on numeric permissions element")
	}
}

func TestPermissionsClaim_WellFormedSucceeds(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub":         "user-1",
		"permissions": []string{"read"},
		"exp":         now.Add(5 * time.Minute).Unix(),
	})

	claims, err := ks.Verify(token, now)
	if err != nil {
		t.Fatalf("well-formed permissions must verify: %v", err)
	}
	if len(claims.Permissions) != 1 || claims.Permissions[0] != "read" {
		t.Errorf("permissions = %v, want [read]", claims.Permissions)
	}
}

func TestScopesClaim_MalformedRejectsToken(t *testing.T) {
	// Same downgrade hazard as permissions: a numeric scopes claim used
	// to log-and-continue with empty scopes. Now rejected.
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub":    "user-1",
		"scopes": 42,
		"exp":    now.Add(5 * time.Minute).Unix(),
	})

	_, err := ks.Verify(token, now)
	if err == nil {
		t.Fatal("expected verification to fail on numeric scopes claim")
	}
}

func TestVerify_ConfiguredAudienceMatch(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	ks.ExpectedAudience = "svc-A"
	now := time.Now()

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"aud": "svc-A",
		"exp": now.Add(5 * time.Minute).Unix(),
	})

	if _, err := ks.Verify(token, now); err != nil {
		t.Fatalf("matching audience must verify: %v", err)
	}
}

func TestVerify_ConfiguredAudienceMismatch(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	ks.ExpectedAudience = "svc-A"
	now := time.Now()

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"aud": "svc-B",
		"exp": now.Add(5 * time.Minute).Unix(),
	})

	if _, err := ks.Verify(token, now); err == nil {
		t.Fatal("audience mismatch must be rejected (RFC 7519 §4.1.3)")
	}
}

func TestDefaultHTTPClient_HandlesReplacedDefaultTransport(t *testing.T) {
	// R2 fix: defaultHTTPClient previously did
	//   transport, _ := http.DefaultTransport.(*http.Transport)
	//   clone := transport.Clone()
	// which panics with nil-pointer-deref when the process replaces
	// http.DefaultTransport with a custom RoundTripper (otelhttp,
	// test doubles, instrumentation packages). The fallback now
	// constructs a fresh *http.Transport with stdlib defaults.
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	http.DefaultTransport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("not used")
	})

	c := defaultHTTPClient()
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport (fresh fallback)", c.Transport)
	}
	if tr.Proxy == nil {
		t.Error("fallback Proxy must be set (env-aware)")
	}
	if tr.DialContext == nil {
		t.Error("fallback DialContext must be set")
	}
	if tr.TLSHandshakeTimeout == 0 {
		t.Error("fallback TLSHandshakeTimeout must be set")
	}
	if tr.MaxResponseHeaderBytes != 64*1024 {
		t.Errorf("MaxResponseHeaderBytes = %d, want 65536 (kit override)", tr.MaxResponseHeaderBytes)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
