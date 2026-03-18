package jwtutil

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
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
	out := toStringSlice(in)
	if len(out) != 3 || out[0] != "a" || out[1] != "b" || out[2] != "c" {
		t.Errorf("toStringSlice([]string) = %v", out)
	}
}

func TestToStringSlice_AnySlice(t *testing.T) {
	in := []any{"x", "y", 42}
	out := toStringSlice(in)
	if len(out) != 2 || out[0] != "x" || out[1] != "y" {
		t.Errorf("toStringSlice([]any) = %v, want [x y]", out)
	}
}

func TestToStringSlice_Other(t *testing.T) {
	out := toStringSlice("not-a-slice")
	if out != nil {
		t.Errorf("toStringSlice(string) = %v, want nil", out)
	}
}

func TestNewProvider(t *testing.T) {
	p := NewProvider("https://example.com/.well-known/jwks.json", nil, 5*time.Minute)
	if p.url != "https://example.com/.well-known/jwks.json" {
		t.Errorf("url = %q", p.url)
	}
	if p.KeySet() != nil {
		t.Error("initial keyset should be nil")
	}
}

func TestNewProvider_WithExpectedIssuer(t *testing.T) {
	p := NewProvider("https://example.com/jwks", nil, time.Minute, WithExpectedIssuer("https://issuer"))
	if p.expectedIssuer != "https://issuer" {
		t.Errorf("expectedIssuer = %q", p.expectedIssuer)
	}
}

func TestNewProviderWithKeySet(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	p := NewProviderWithKeySet(ks)
	if p.KeySet() != ks {
		t.Error("expected keyset to be set")
	}
}

func TestProvider_Run_FetchFromTestServer(t *testing.T) {
	key := testKey(t)
	jwksData := testJWKS(t, key, "kid-1")

	srv := newTestJWKSServer(t, jwksData)
	defer srv.Close()

	p := NewProvider(srv.URL, srv.Client(), 100*time.Millisecond, WithExpectedIssuer("test"))

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

	p := NewProvider(srv.URL, srv.Client(), time.Hour)

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
