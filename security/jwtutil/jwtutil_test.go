package jwtutil

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
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

func TestParseKeySet_RejectsNonVerificationKeyOps(t *testing.T) {
	key := testKey(t)
	pubJWK, err := jwk.Import(key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	_ = pubJWK.Set(jwk.KeyIDKey, "enc-only")
	_ = pubJWK.Set(jwk.AlgorithmKey, jwa.ES256())
	_ = pubJWK.Set(jwk.KeyOpsKey, jwk.KeyOperationList{jwk.KeyOpEncrypt})

	set := jwk.NewSet()
	if err := set.AddKey(pubJWK); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ParseKeySet(data); err == nil {
		t.Fatal("expected ParseKeySet to reject a JWKS with no verification-capable keys")
	}
}

func TestParseKeySet_AcceptsVerificationKeyOps(t *testing.T) {
	key := testKey(t)
	pubJWK, err := jwk.Import(key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	_ = pubJWK.Set(jwk.KeyIDKey, "verify-key")
	_ = pubJWK.Set(jwk.AlgorithmKey, jwa.ES256())
	_ = pubJWK.Set(jwk.KeyOpsKey, jwk.KeyOperationList{jwk.KeyOpVerify})

	set := jwk.NewSet()
	if err := set.AddKey(pubJWK); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	ks, err := ParseKeySet(data)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	token := signJWT(t, key, "verify-key", map[string]any{
		"sub": "user-1",
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	if _, err := ks.Verify(token, now); err != nil {
		t.Fatalf("keyset should verify a token with key_ops verify: %v", err)
	}
}

func TestParseKeySet_StripsPrivateKeyMaterial(t *testing.T) {
	key := testKey(t)
	privateJWK, err := jwk.Import(key)
	if err != nil {
		t.Fatal(err)
	}
	_ = privateJWK.Set(jwk.KeyIDKey, "private-key")
	_ = privateJWK.Set(jwk.AlgorithmKey, jwa.ES256())
	_ = privateJWK.Set(jwk.KeyUsageKey, "sig")

	set := jwk.NewSet()
	if err := set.AddKey(privateJWK); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	ks, err := ParseKeySet(data)
	if err != nil {
		t.Fatal(err)
	}

	stored, ok := ks.set.Key(0)
	if !ok {
		t.Fatal("expected parsed key")
	}
	isPrivate, err := jwk.IsPrivateKey(stored)
	if err != nil {
		t.Fatal(err)
	}
	if isPrivate {
		t.Fatal("ParseKeySet retained private key material")
	}

	now := time.Now()
	token := signJWT(t, key, "private-key", map[string]any{
		"sub": "user-1",
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	if _, err := ks.Verify(token, now); err != nil {
		t.Fatalf("public-normalized keyset should still verify: %v", err)
	}
}

func TestKeySet_InvalidReceiverReturnsError(t *testing.T) {
	var nilKeySet *KeySet
	if _, err := nilKeySet.Verify("token", time.Now()); !errors.Is(err, ErrInvalidKeySet) {
		t.Fatalf("nil KeySet Verify error = %v, want ErrInvalidKeySet", err)
	}

	var zero KeySet
	if _, err := zero.Verify("token", time.Now()); !errors.Is(err, ErrInvalidKeySet) {
		t.Fatalf("zero KeySet Verify error = %v, want ErrInvalidKeySet", err)
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

func TestVerify_PopulatesJWTID(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()

	token := signJWT(t, key, "kid-1", map[string]any{
		"jti": "token-123",
		"sub": "user-1",
		"exp": now.Add(5 * time.Minute).Unix(),
	})

	claims, err := ks.Verify(token, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims.ID != "token-123" {
		t.Fatalf("claims.ID = %q, want token-123", claims.ID)
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

func TestVerify_ZeroTimeUsesWallClock(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))

	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"exp": time.Now().Add(-5 * time.Minute).Unix(),
	})

	_, err := ks.Verify(token, time.Time{})
	if err == nil {
		t.Fatal("expected error for expired token when now is zero")
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
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion != minimumTLSVersion {
		t.Errorf("TLS MinVersion = %v, want %v", tr.TLSClientConfig, minimumTLSVersion)
	}
	if c.Timeout != defaultHTTPTimeout {
		t.Errorf("client Timeout = %v, want %v", c.Timeout, defaultHTTPTimeout)
	}
}

func TestDefaultHTTPClient_BlocksRedirects(t *testing.T) {
	srv := newTestJWKSServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/jwks", http.StatusFound)
	})
	defer srv.Close()

	resp, err := defaultHTTPClient().Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrJWKSRedirectBlocked) {
		t.Fatalf("Get redirect error = %v, want ErrJWKSRedirectBlocked", err)
	}
}

func TestJWKSHTTPClient_CustomClientBlocksRedirectsByDefault(t *testing.T) {
	srv := newTestJWKSServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/jwks", http.StatusFound)
	})
	defer srv.Close()

	custom := srv.Client()
	if custom.CheckRedirect != nil {
		t.Fatal("test setup expected httptest client without redirect policy")
	}

	hardened := jwksHTTPClient(custom)
	if hardened == custom {
		t.Fatal("expected a cloned client when installing the JWKS redirect policy")
	}
	if custom.CheckRedirect != nil {
		t.Fatal("jwksHTTPClient must not mutate the caller's client")
	}

	resp, err := hardened.Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrJWKSRedirectBlocked) {
		t.Fatalf("Get redirect error = %v, want ErrJWKSRedirectBlocked", err)
	}
}

func TestJWKSHTTPClient_RespectsExplicitCustomRedirectPolicy(t *testing.T) {
	srv := newTestJWKSServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/jwks", http.StatusFound)
	})
	defer srv.Close()

	custom := srv.Client()
	custom.Timeout = time.Second
	custom.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	hardened := jwksHTTPClient(custom)
	// Wave 66: the kit now always clones the caller's client so the
	// TLS floor can be re-applied to the Transport. Pointer identity
	// is not preserved; verify the explicit CheckRedirect survived.
	if hardened.CheckRedirect == nil {
		t.Fatal("expected explicit CheckRedirect to be preserved on clone")
	}
	resp, err := hardened.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusFound)
	}
}

func TestJWKSHTTPClient_CustomClientFillsMissingTimeout(t *testing.T) {
	transport := &staticJWKSTransport{}
	custom := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	hardened := jwksHTTPClient(custom)
	if hardened == custom {
		t.Fatal("expected custom client with zero timeout to be cloned")
	}
	if custom.Timeout != 0 {
		t.Fatal("jwksHTTPClient must not mutate caller timeout")
	}
	if hardened.Timeout != defaultHTTPTimeout {
		t.Fatalf("timeout = %s, want %s", hardened.Timeout, defaultHTTPTimeout)
	}
	if hardened.Transport != transport {
		t.Fatal("expected custom transport to be preserved")
	}
	if hardened.CheckRedirect == nil {
		t.Fatal("expected custom redirect policy to be preserved")
	}
}

func TestJWKSHTTPClient_CustomClientFillsMissingTransport(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	http.DefaultTransport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("global default transport used")
	})

	custom := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	hardened := jwksHTTPClient(custom)
	if hardened == custom {
		t.Fatal("expected custom client with nil transport to be cloned")
	}
	if custom.Transport != nil {
		t.Fatal("jwksHTTPClient must not mutate caller transport")
	}
	tr, ok := hardened.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport fallback", hardened.Transport)
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion != minimumTLSVersion {
		t.Fatalf("TLS MinVersion = %v, want %x", tr.TLSClientConfig, minimumTLSVersion)
	}
}

func TestDefaultHTTPClient_ClonesTLSConfigAndEnforcesFloor(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })

	base := http.DefaultTransport.(*http.Transport).Clone()
	cfg := &tls.Config{ServerName: "jwks.internal.test"}
	cfg.MinVersion = minimumTLSVersion - 1
	base.TLSClientConfig = cfg
	http.DefaultTransport = base

	c := defaultHTTPClient()
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", c.Transport)
	}
	if tr.TLSClientConfig == cfg {
		t.Fatal("JWKS client transport must own a cloned TLS config")
	}
	if cfg.MinVersion != minimumTLSVersion-1 {
		t.Fatalf("caller TLS config was mutated: got MinVersion %x", cfg.MinVersion)
	}
	if tr.TLSClientConfig.MinVersion != minimumTLSVersion {
		t.Fatalf("expected TLS floor %x, got %x", minimumTLSVersion, tr.TLSClientConfig.MinVersion)
	}
	if tr.TLSClientConfig.ServerName != "jwks.internal.test" {
		t.Fatalf("expected ServerName to be preserved, got %q", tr.TLSClientConfig.ServerName)
	}
}

func TestDefaultHTTPClient_PanicsWhenTLSMaxVersionBelowFloor(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })

	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{MaxVersion: minimumTLSVersion - 1}
	http.DefaultTransport = base

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for impossible TLS version range")
		}
	}()
	_ = defaultHTTPClient()
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

func TestProvider_InvalidReceiverReturnsUnavailable(t *testing.T) {
	var nilProvider *Provider
	if got := nilProvider.KeySet(); got != nil {
		t.Fatalf("nil KeySet = %v, want nil", got)
	}
	if got := nilProvider.LastSuccessfulFetch(); !got.IsZero() {
		t.Fatalf("nil LastSuccessfulFetch = %v, want zero", got)
	}
	if got := nilProvider.Staleness(); got != 0 {
		t.Fatalf("nil Staleness = %v, want 0", got)
	}
	if _, err := nilProvider.Verify("token", time.Now()); !errors.Is(err, ErrKeySetUnavailable) {
		t.Fatalf("nil Verify error = %v, want ErrKeySetUnavailable", err)
	}
	if err := nilProvider.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "non-nil provider") {
		t.Fatalf("nil Run error = %v, want non-nil provider error", err)
	}

	var zero Provider
	if _, err := zero.Verify("token", time.Now()); !errors.Is(err, ErrKeySetUnavailable) {
		t.Fatalf("zero Verify error = %v, want ErrKeySetUnavailable", err)
	}
	if err := zero.Run(nilContextForTest()); err == nil || !strings.Contains(err.Error(), "non-nil context") {
		t.Fatalf("zero Run nil context error = %v, want non-nil context error", err)
	}
}

func TestNewProvider_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil provider option")
		}
	}()
	func() {
		NewProvider("https://example.com/.well-known/jwks.json", nil, 5*time.Minute, nil)
	}()
}

func TestNewProvider_RejectsInvalidJWKSURLs(t *testing.T) {
	baseOpts := []ProviderOption{
		WithExpectedIssuer("https://issuer.example.com"),
		WithExpectedAudience("svc"),
	}
	tests := []struct {
		name string
		url  string
		opts []ProviderOption
		leak string
	}{
		{
			name: "http without opt-in",
			url:  "http://example.com/jwks",
		},
		{
			name: "unsupported scheme with opt-in",
			url:  "secret-token://example.com/jwks",
			opts: []ProviderOption{WithAllowInsecureURL()},
			leak: "secret-token",
		},
		{
			name: "relative URL",
			url:  "/jwks",
		},
		{
			name: "userinfo credentials",
			url:  "https://user:secret@example.com/jwks",
		},
		{
			name: "query string",
			url:  "https://example.com/jwks?token=secret",
		},
		{
			name: "fragment",
			url:  "https://example.com/jwks#kid",
		},
		{
			name: "invalid port",
			url:  "https://example.com:0/jwks",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := append([]ProviderOption{}, baseOpts...)
			opts = append(opts, tt.opts...)
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic")
				}
				msg := fmt.Sprint(r)
				if !strings.HasPrefix(msg, "jwtutil: NewProvider JWKS URL is invalid") {
					t.Fatalf("panic = %v, want stable JWKS URL marker prefix", r)
				}
				if tt.leak != "" && strings.Contains(msg, tt.leak) {
					t.Fatalf("panic leaked URL component %q: %v", tt.leak, r)
				}
			}()
			_ = NewProvider(tt.url, nil, time.Minute, opts...)
		})
	}
}

func TestNewProvider_InvalidURLParseErrorDoesNotEchoValue(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		msg := fmt.Sprint(r)
		if !strings.Contains(msg, "JWKS URL is invalid") {
			t.Fatalf("panic = %v, want invalid URL marker", r)
		}
		if strings.Contains(msg, "secret-token") || strings.Contains(msg, "token=") || strings.Contains(msg, "%zz") {
			t.Fatalf("panic leaked JWKS URL value: %q", msg)
		}
	}()

	_ = NewProvider("https://example.com/%zz?token=secret-token", nil, time.Minute,
		WithExpectedIssuer("https://issuer.example.com"),
		WithExpectedAudience("api"),
	)
}

func TestProviderRefreshLogDoesNotExposeURLOrRawError(t *testing.T) {
	const jwksURL = "https://identity.example.com/realms/acme/.well-known/jwks.json"
	p := &Provider{url: jwksURL}
	err := fmt.Errorf("Get %q: dial tcp identity.example.com:443: token=secret", jwksURL)

	rendered := withCapturedSlog(t, slog.LevelWarn, func() {
		p.logRefreshFailure(err)
	})

	for _, forbidden := range []string{
		jwksURL,
		"identity.example.com",
		"realms/acme",
		".well-known/jwks.json",
		"token=secret",
		"dial tcp",
		"Get",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("refresh log leaked %q in %s", forbidden, rendered)
		}
	}
	if !strings.Contains(rendered, "jwks_configured=true") {
		t.Fatalf("refresh log missing configured marker: %s", rendered)
	}
	if !strings.Contains(rendered, "error_kind=fetch_failed") {
		t.Fatalf("refresh log missing stable failure kind: %s", rendered)
	}
}

func TestJWKSFetchErrorKind(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", want: "unknown"},
		{name: "context canceled", err: context.Canceled, want: "context_canceled"},
		{name: "deadline", err: context.DeadlineExceeded, want: "timeout"},
		{name: "redirect", err: ErrJWKSRedirectBlocked, want: "redirect_blocked"},
		{name: "bad status", err: fmt.Errorf("%w: 503", errJWKSBadStatus), want: "bad_status"},
		{name: "content type", err: errJWKSUnexpectedContentType, want: "unexpected_content_type"},
		{name: "fallback", err: errors.New("connection refused"), want: "fetch_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jwksFetchErrorKind(tt.err); got != tt.want {
				t.Fatalf("jwksFetchErrorKind() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewProvider_AllowsHTTPWithExplicitOptIn(t *testing.T) {
	p := NewProvider("http://127.0.0.1/jwks", nil, time.Minute,
		WithExpectedIssuer("https://issuer.example.com"),
		WithExpectedAudience("svc"),
		WithAllowInsecureURL(),
	)
	if p == nil {
		t.Fatal("expected provider")
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

func TestProvider_WithoutMaxStaleLimitDisablesCheck(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	p := NewProvider("https://example.com/jwks", nil, time.Minute,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
		WithoutMaxStaleLimit(),
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

func TestProvider_WithMaxStalePanicsOnNonPositive(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected WithMaxStale to panic")
				}
			}()
			WithMaxStale(d)
		})
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
	got := p.KeySet()
	if got == nil || got.set != ks.set {
		t.Error("expected keyset to be set")
	}
}

func TestNewProviderWithKeySet_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil provider option")
		}
	}()
	func() {
		NewProviderWithKeySet(&KeySet{}, nil)
	}()
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
	got := p.KeySet()
	if got == nil || got.set != ks.set {
		t.Fatal("expected keyset to be set")
	}
	// R4: provider must NOT mutate the caller's KeySet — provider policy is
	// stored on the Provider and consulted by Provider.Verify directly.
	if got := ks.ExpectedIssuer; got != "" {
		t.Errorf("ks.ExpectedIssuer = %q, must remain unchanged by provider construction", got)
	}
	if got := ks.ExpectedAudience; got != "" {
		t.Errorf("ks.ExpectedAudience = %q, must remain unchanged by provider construction", got)
	}
	if p.expectedIssuer != "https://issuer" {
		t.Errorf("p.expectedIssuer = %q, want https://issuer", p.expectedIssuer)
	}
	if p.expectedAudience != "svc" {
		t.Errorf("p.expectedAudience = %q, want svc", p.expectedAudience)
	}
}

func TestNewProviderWithKeySet_AcceptsExplicitOptOuts(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))

	p := NewProviderWithKeySet(ks,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
	)
	got := p.KeySet()
	if got == nil {
		t.Fatal("expected keyset to be set")
	}
	// KeySet() returns a defensive snapshot — pointer identity differs but
	// content matches.
	if got.set != ks.set || got.ExpectedIssuer != ks.ExpectedIssuer || got.ExpectedAudience != ks.ExpectedAudience {
		t.Fatalf("snapshot does not match source keyset: got %+v want %+v", got, ks)
	}
	// Mutating the snapshot must NOT affect the provider's live keyset.
	got.ExpectedAudience = "tampered"
	if again := p.KeySet(); again.ExpectedAudience == "tampered" {
		t.Fatal("KeySet() returned live struct; mutation leaked back into provider")
	}
}

type revocationCheckerFunc func(context.Context, *Claims) (bool, error)

func (fn revocationCheckerFunc) IsRevoked(ctx context.Context, claims *Claims) (bool, error) {
	return fn(ctx, claims)
}

type revocationContextKey struct{}

func TestNewProvider_WithRevocationCheckerPanicsOnNil(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	_ = WithRevocationChecker(nil)
}

func TestProviderVerifyContext_RejectsRevokedToken(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()
	token := signJWT(t, key, "kid-1", map[string]any{
		"jti": "revoked-token",
		"sub": "user-1",
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	provider := NewProviderWithKeySet(ks,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
		WithRevocationChecker(revocationCheckerFunc(func(ctx context.Context, claims *Claims) (bool, error) {
			if ctx.Value(revocationContextKey{}) != "ctx" {
				t.Fatalf("revocation checker did not receive request context")
			}
			if claims.ID != "revoked-token" {
				t.Fatalf("claims.ID = %q, want revoked-token", claims.ID)
			}
			return true, nil
		})),
	)

	_, err := provider.VerifyContext(context.WithValue(context.Background(), revocationContextKey{}, "ctx"), token, now)
	if !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("VerifyContext error = %v, want ErrTokenRevoked", err)
	}
}

func TestProviderVerifyContext_RejectsMissingJTIWhenRevocationEnabled(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()
	token := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	checkerCalled := false
	provider := NewProviderWithKeySet(ks,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
		WithRevocationChecker(revocationCheckerFunc(func(context.Context, *Claims) (bool, error) {
			checkerCalled = true
			return false, nil
		})),
	)

	_, err := provider.VerifyContext(context.Background(), token, now)
	if !errors.Is(err, ErrMissingTokenID) {
		t.Fatalf("VerifyContext error = %v, want ErrMissingTokenID", err)
	}
	if checkerCalled {
		t.Fatal("revocation checker must not be called for missing jti")
	}
}

func TestProviderVerifyContext_PropagatesRevocationError(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()
	token := signJWT(t, key, "kid-1", map[string]any{
		"jti": "token-1",
		"sub": "user-1",
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	want := errors.New("cache unavailable")
	provider := NewProviderWithKeySet(ks,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
		WithRevocationChecker(revocationCheckerFunc(func(context.Context, *Claims) (bool, error) {
			return false, want
		})),
	)

	_, err := provider.VerifyContext(context.Background(), token, now)
	if !errors.Is(err, want) {
		t.Fatalf("VerifyContext error = %v, want %v", err, want)
	}
}

func TestProvider_Run_FetchFromTestServer(t *testing.T) {
	key := testKey(t)
	jwksData := testJWKS(t, key, "kid-1")

	srv := newTestJWKSServer(t, jwksData)
	defer srv.Close()

	p := NewProvider(srv.URL, srv.Client(), 100*time.Millisecond,
		WithExpectedIssuer("test"),
		WithAllowAnyAudience(),
		WithAllowInsecureURL())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		if err := p.Run(ctx); err != nil {
			t.Errorf("Run returned %v", err)
		}
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
	// R4: fetch() must not mutate the live stored keyset's policy fields
	// (two providers may share one parsed *KeySet). The defensive
	// snapshot returned by KeySet() does copy the Provider's
	// issuer/audience so callers that verify via the snapshot inherit
	// the same guardrails as Provider.Verify.
	if got := ks.ExpectedIssuer; got != "test" {
		t.Errorf("KeySet() snapshot ExpectedIssuer = %q, want test (provider policy)", got)
	}
	if p.expectedIssuer != "test" {
		t.Errorf("provider expectedIssuer = %q, want test", p.expectedIssuer)
	}
	// Live store remains free of provider policy (snapshot is a copy).
	live, _ := p.keySetWithReason()
	if live != nil && live.ExpectedIssuer != "" {
		t.Errorf("live keyset ExpectedIssuer = %q, want empty (policy lives on Provider)", live.ExpectedIssuer)
	}

	cancel()
	<-done
}

func TestProvider_RunRejectsSecondStart(t *testing.T) {
	key := testKey(t)
	ks, err := ParseKeySet(testJWKS(t, key, "kid-1"))
	if err != nil {
		t.Fatal(err)
	}
	p := NewProviderWithKeySet(ks,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	waitForProviderRunStarted(t, p)

	err = p.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("expected already started error, got %v", err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

func TestProvider_RunRejectsRestartAfterCancel(t *testing.T) {
	key := testKey(t)
	ks, err := ParseKeySet(testJWKS(t, key, "kid-1"))
	if err != nil {
		t.Fatal(err)
	}
	p := NewProviderWithKeySet(ks,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	waitForProviderRunStarted(t, p)

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}

	err = p.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("expected already started error, got %v", err)
	}
}

func waitForProviderRunStarted(t *testing.T, p *Provider) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.runMu.Lock()
		started := p.started
		p.runMu.Unlock()
		if started {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Provider.Run did not start")
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
		WithAllowAnyAudience(),
		WithAllowInsecureURL())

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		if err := p.Run(ctx); err != nil {
			t.Errorf("Run returned %v", err)
		}
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

func TestProviderFetch_RejectsAmbiguousContentType(t *testing.T) {
	key := testKey(t)
	jwksData := testJWKS(t, key, "kid-1")
	srv := newTestJWKSServerFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Content-Type", "application/json")
		w.Header().Add("Content-Type", "text/html")
		_, _ = w.Write(jwksData)
	})
	defer srv.Close()

	p := NewProvider(srv.URL, srv.Client(), time.Hour,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
		WithAllowInsecureURL())

	err := p.fetch(context.Background())
	if err == nil {
		t.Fatal("expected ambiguous content-type error")
	}
	if !strings.Contains(err.Error(), "multiple content-type") {
		t.Fatalf("expected multiple content-type error, got %v", err)
	}
	if p.KeySet() != nil {
		t.Fatal("expected keyset to remain unset")
	}
}

func TestProviderFetch_UnexpectedContentTypeDoesNotEchoHeader(t *testing.T) {
	key := testKey(t)
	jwksData := testJWKS(t, key, "kid-1")
	srv := newTestJWKSServerFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; token=secret-token")
		_, _ = w.Write(jwksData)
	})
	defer srv.Close()

	p := NewProvider(srv.URL, srv.Client(), time.Hour,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
		WithAllowInsecureURL())

	err := p.fetch(context.Background())
	if err == nil {
		t.Fatal("expected unexpected content-type error")
	}
	if !strings.Contains(err.Error(), "unexpected content-type") {
		t.Fatalf("expected unexpected content-type error, got %v", err)
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "text/html") {
		t.Fatalf("fetch error leaked content-type header: %v", err)
	}
	if p.KeySet() != nil {
		t.Fatal("expected keyset to remain unset")
	}
}

func TestProviderFetch_OversizedJWKSBodyUsesStableError(t *testing.T) {
	srv := newTestJWKSServerFunc(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bytes.Repeat([]byte("x"), (64<<10)+1))
	})
	defer srv.Close()

	p := NewProvider(srv.URL, srv.Client(), time.Hour,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
		WithAllowInsecureURL())

	err := p.fetch(context.Background())
	if err == nil {
		t.Fatal("expected oversized JWKS body error")
	}
	if !strings.Contains(err.Error(), "jwks body exceeds maximum size") {
		t.Fatalf("expected stable oversized JWKS error, got %v", err)
	}
	if strings.Contains(err.Error(), "65536") || strings.Contains(err.Error(), "65537") {
		t.Fatalf("JWKS body error leaked size limits: %v", err)
	}
}

func TestProviderFetch_DefaultClientBlocksRedirects(t *testing.T) {
	srv := newTestJWKSServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/jwks", http.StatusFound)
	})
	defer srv.Close()

	p := NewProvider(srv.URL, nil, time.Hour,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
		WithAllowInsecureURL())

	err := p.fetch(context.Background())
	if !errors.Is(err, ErrJWKSRedirectBlocked) {
		t.Fatalf("fetch error = %v, want ErrJWKSRedirectBlocked", err)
	}
	if p.KeySet() != nil {
		t.Fatal("expected keyset to remain unset")
	}
}

func TestProviderFetch_CustomClientBlocksRedirectsByDefault(t *testing.T) {
	srv := newTestJWKSServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/jwks", http.StatusFound)
	})
	defer srv.Close()

	custom := srv.Client()
	if custom.CheckRedirect != nil {
		t.Fatal("test setup expected httptest client without redirect policy")
	}
	p := NewProvider(srv.URL, custom, time.Hour,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
		WithAllowInsecureURL())

	err := p.fetch(context.Background())
	if !errors.Is(err, ErrJWKSRedirectBlocked) {
		t.Fatalf("fetch error = %v, want ErrJWKSRedirectBlocked", err)
	}
	if p.KeySet() != nil {
		t.Fatal("expected keyset to remain unset")
	}
	if custom.CheckRedirect != nil {
		t.Fatal("NewProvider must not mutate the caller's client")
	}
	if p.httpClient == custom {
		t.Fatal("expected provider to use a cloned client with JWKS redirect policy")
	}
}

func TestProviderFetch_RespectsExplicitCustomRedirectPolicy(t *testing.T) {
	srv := newTestJWKSServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/jwks", http.StatusFound)
	})
	defer srv.Close()

	custom := srv.Client()
	custom.Timeout = time.Second
	custom.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	p := NewProvider(srv.URL, custom, time.Hour,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
		WithAllowInsecureURL())

	err := p.fetch(context.Background())
	if err == nil {
		t.Fatal("expected non-200 redirect response error")
	}
	if errors.Is(err, ErrJWKSRedirectBlocked) {
		t.Fatalf("fetch error = %v, did not expect ErrJWKSRedirectBlocked", err)
	}
	if !errors.Is(err, errJWKSBadStatus) && !strings.Contains(err.Error(), "302") {
		t.Fatalf("fetch error = %v, want 302 response error", err)
	}
	if p.KeySet() != nil {
		t.Fatal("expected keyset to remain unset")
	}
	// Wave 66: the provider clones the caller's client to re-apply
	// the TLS floor; pointer identity is no longer preserved. Verify
	// the explicit CheckRedirect survived the clone instead.
	if p.httpClient.CheckRedirect == nil {
		t.Fatal("expected provider to preserve explicit custom CheckRedirect")
	}
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
		if strings.Contains(err.Error(), "not-an-array") {
			t.Errorf("error leaked malformed claim value: %v", err)
		}
	})

	if !strings.Contains(captured, "level=WARN") {
		t.Errorf("expected WARN-level log entry, got: %s", captured)
	}
	if !strings.Contains(captured, "permissions claim malformed") {
		t.Errorf("expected permissions-claim warning, got: %s", captured)
	}
	if strings.Contains(captured, "not-an-array") {
		t.Errorf("warning leaked malformed claim value: %s", captured)
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
	if strings.Contains(err.Error(), "element") || strings.Contains(err.Error(), "int") {
		t.Fatalf("error leaked malformed permissions detail: %v", err)
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
	if strings.Contains(err.Error(), "42") || strings.Contains(err.Error(), "int") {
		t.Fatalf("error leaked malformed scopes detail: %v", err)
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

type staticJWKSTransport struct{}

func (*staticJWKSTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

// TestNewProviderWithKeySet_DoesNotMutateSharedKeySet exercises the R4 bug:
// when two providers share one parsed *KeySet, constructing the second
// must not stomp the first provider's iss/aud policy. Earlier revisions
// stored Provider options as fields on the shared *KeySet, so building p2
// silently changed how p1 verified tokens.
func TestNewProviderWithKeySet_DoesNotMutateSharedKeySet(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()

	p1 := NewProviderWithKeySet(ks,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("aud-A"),
	)
	// Constructing p2 against the same *ks must not bleed into p1's policy.
	p2 := NewProviderWithKeySet(ks,
		WithExpectedIssuer("svc-B"),
		WithExpectedAudience("aud-B"),
	)

	if got := ks.ExpectedIssuer; got != "" {
		t.Fatalf("provider construction must not mutate ks.ExpectedIssuer; got %q", got)
	}
	if got := ks.ExpectedAudience; got != "" {
		t.Fatalf("provider construction must not mutate ks.ExpectedAudience; got %q", got)
	}

	tokenA := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"iss": "svc-A",
		"aud": "aud-A",
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	tokenB := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"iss": "svc-B",
		"aud": "aud-B",
		"exp": now.Add(5 * time.Minute).Unix(),
	})

	if _, err := p1.Verify(tokenA, now); err != nil {
		t.Fatalf("p1 must verify svc-A/aud-A token after p2 was constructed: %v", err)
	}
	if _, err := p2.Verify(tokenB, now); err != nil {
		t.Fatalf("p2 must verify svc-B/aud-B token: %v", err)
	}
	if _, err := p1.Verify(tokenB, now); err == nil {
		t.Fatal("p1 must reject svc-B/aud-B token (cross-provider policy bleed)")
	}
	if _, err := p2.Verify(tokenA, now); err == nil {
		t.Fatal("p2 must reject svc-A/aud-A token (cross-provider policy bleed)")
	}
}

// TestProvider_Verify_ConcurrentSharedKeySet runs concurrent verifications
// across two providers that share one *KeySet. With -race, this fails if
// either Provider.Verify or any path it touches writes to a shared mutable
// field. It is the regression guard for the R4 race-on-shared-policy bug.
func TestProvider_Verify_ConcurrentSharedKeySet(t *testing.T) {
	key := testKey(t)
	ks, _ := ParseKeySet(testJWKS(t, key, "kid-1"))
	now := time.Now()

	p1 := NewProviderWithKeySet(ks,
		WithExpectedIssuer("svc-A"),
		WithExpectedAudience("aud-A"),
	)
	p2 := NewProviderWithKeySet(ks,
		WithExpectedIssuer("svc-B"),
		WithExpectedAudience("aud-B"),
	)

	tokenA := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"iss": "svc-A",
		"aud": "aud-A",
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	tokenB := signJWT(t, key, "kid-1", map[string]any{
		"sub": "user-1",
		"iss": "svc-B",
		"aud": "aud-B",
		"exp": now.Add(5 * time.Minute).Unix(),
	})

	const workers = 16
	const iters = 200
	var wg sync.WaitGroup
	wg.Add(workers * 2)
	errs := make(chan error, workers*2*iters)

	verifyLoop := func(p *Provider, token string) {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			if _, err := p.Verify(token, now); err != nil {
				errs <- fmt.Errorf("verify failed mid-loop: %w", err)
				return
			}
		}
	}

	for i := 0; i < workers; i++ {
		go verifyLoop(p1, tokenA)
		go verifyLoop(p2, tokenB)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// TestProvider_Verify_KeySetUnavailable confirms Provider.Verify surfaces
// the unavailable-keyset case as a distinguishable sentinel error so HTTP
// and gRPC middleware can keep separating "JWKS not loaded" from "bad token"
// after the R4 refactor moved verification onto the Provider.
func TestProvider_Verify_KeySetUnavailable(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	p := NewProvider("https://example.com/jwks", nil, time.Minute,
		WithAllowAnyIssuer(),
		WithAllowAnyAudience(),
		WithMaxStale(30*time.Minute),
		withClock(clock),
	)
	// No fetch yet — keyset is nil.
	_, err := p.Verify("any.token.here", now)
	if !errors.Is(err, ErrKeySetUnavailable) {
		t.Fatalf("expected ErrKeySetUnavailable when no fetch happened; got %v", err)
	}

	// Stale fetch: still unavailable.
	p.mu.Lock()
	p.keyset = &KeySet{}
	p.lastSuccessfulFetch = now.Add(-2 * time.Hour)
	p.mu.Unlock()
	_, err = p.Verify("any.token.here", now)
	if !errors.Is(err, ErrKeySetUnavailable) {
		t.Fatalf("expected ErrKeySetUnavailable when keyset is stale; got %v", err)
	}
}

// TestVerify_RejectsHS256SignedWithECPublicKeyBytes is the adversarial
// alg-confusion test for the classic HS-vs-asymmetric attack:
//
//  1. A trusted JWKS publishes an EC P-256 public key with alg=ES256.
//  2. An attacker who reads that JWKS (or any public consumer of the
//     verifier's signer endpoint) crafts a JWT with header
//     `{"alg":"HS256","kid":"<the kid>"}` and signs the
//     `header.payload` half with HMAC-SHA256, using the public key
//     bytes as the HMAC secret. If the verifier blindly hands its
//     stored key bytes to whatever algorithm the token header asks
//     for, the forgery verifies.
//
// The kit's mitigation is layered:
//
//   - ParseKeySet filters out symmetric (oct) JWKS entries up front
//     and calls k.PublicKey() so only public material is retained.
//   - The verifier uses jws.WithInferAlgorithmFromKey(true), so the
//     algorithm is derived from the *stored key*, not from the token
//     header. An EC public key infers ES256/ES384/ES512 and never
//     HS*.
//
// This test forges three plausible HS256-signed-with-pubkey shapes
// (raw uncompressed EC point, DER-encoded SubjectPublicKeyInfo, and
// the marshalled JWK JSON) and asserts Provider.Verify rejects all of
// them. Any non-nil error is acceptable — the security property is
// "did not succeed". We additionally assert no Claims object is
// returned, since a non-nil claims with an error would be a
// fail-open bug.
func TestVerify_RejectsHS256SignedWithECPublicKeyBytes(t *testing.T) {
	const kid = "alg-confusion-victim"
	const issuer = "https://issuer.example.com"
	const audience = "alg-confusion-svc"

	key := testKey(t)
	jwksData := testJWKS(t, key, kid)

	ks, err := ParseKeySet(jwksData)
	if err != nil {
		t.Fatalf("ParseKeySet: %v", err)
	}
	provider := NewProviderWithKeySet(ks,
		WithExpectedIssuer(issuer),
		WithExpectedAudience(audience),
	)

	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	claims := map[string]any{
		"sub": "550e8400-e29b-41d4-a716-446655440000",
		"iss": issuer,
		"aud": audience,
		"exp": now.Add(5 * time.Minute).Unix(),
		"iat": now.Unix(),
	}

	// Three attacker-controlled shapes for "the public key as a
	// symmetric HMAC secret". A verifier with even one path of
	// alg-confusion exposure would accept exactly one of these; the
	// kit must reject all three.
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}

	// SEC1 uncompressed point: 0x04 || X || Y. This is the shape jwx
	// would surface as "the raw key material" for an EC public key
	// and the one most likely to be re-borrowed as an HMAC secret.
	// ecdsa.PublicKey.Bytes (Go 1.25+) returns the SEC1 uncompressed
	// encoding directly — the prior raw-coordinate route via
	// PublicKey.X/Y was deprecated in Go 1.26.
	pubPoint, err := key.PublicKey.Bytes()
	if err != nil {
		t.Fatalf("PublicKey.Bytes: %v", err)
	}

	pubJWK, err := jwk.Import(key.PublicKey)
	if err != nil {
		t.Fatalf("jwk.Import: %v", err)
	}
	_ = pubJWK.Set(jwk.KeyIDKey, kid)
	pubJWKJSON, err := json.Marshal(pubJWK)
	if err != nil {
		t.Fatalf("marshal pub JWK: %v", err)
	}

	shapes := map[string][]byte{
		"DER-SubjectPublicKeyInfo":  pubDER,
		"SEC1-uncompressed-point":   pubPoint,
		"JWK-JSON":                  pubJWKJSON,
		"PEM":                       ecdsaPublicKeyPEM(t, &key.PublicKey),
		"base64url-of-uncompressed": []byte(base64.RawURLEncoding.EncodeToString(pubPoint)),
	}

	for shapeName, hmacSecret := range shapes {
		t.Run(shapeName, func(t *testing.T) {
			forged := forgeHS256Token(t, kid, hmacSecret, claims)

			// Sanity: the forged token actually verifies under
			// HMAC-with-pubkey-bytes when treated naively. Without this
			// check, a passing test could mean "we forged garbage that
			// nobody would accept" rather than "the kit's defence held".
			//
			// We verify this by parsing+verifying with jws directly,
			// using the same hmacSecret as the symmetric key. If THIS
			// fails, the forge helper is broken and the test below
			// would pass trivially.
			if _, verr := jws.Verify([]byte(forged), jws.WithKey(jwa.HS256(), hmacSecret)); verr != nil {
				t.Fatalf("self-check failed: forged HS256 token does not verify under its own HMAC secret: %v", verr)
			}

			gotClaims, err := provider.Verify(forged, now)
			if err == nil {
				t.Fatalf("alg-confusion attack succeeded: Provider.Verify returned nil error for forged HS256 token (shape=%s)", shapeName)
			}
			if gotClaims != nil {
				t.Fatalf("alg-confusion attack: Provider.Verify returned non-nil claims alongside error %v (shape=%s)", err, shapeName)
			}
			// Sanity-check the error chain: it should not be the
			// "key set unavailable" sentinel (which would mean we
			// never reached the verifier at all).
			if errors.Is(err, ErrKeySetUnavailable) {
				t.Fatalf("alg-confusion test never reached verifier: ErrKeySetUnavailable (shape=%s)", shapeName)
			}
		})
	}
}

// forgeHS256Token builds a compact-serialized JWT with header
// `{"alg":"HS256","kid":kid,"typ":"JWT"}`, a JSON body of claims, and
// an HMAC-SHA256 signature over the `header.payload` half computed
// under hmacSecret. The output is the shape a real attacker mounting
// an alg-confusion attack would put on the wire.
//
// Implemented by hand rather than through jws.Sign so the test does
// not depend on the high-level library agreeing that HMAC-with-arbitrary-
// bytes is a sensible thing to do — that is precisely the agreement the
// kit refuses to honour.
func forgeHS256Token(t *testing.T, kid string, hmacSecret []byte, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "HS256", "kid": kid, "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(payloadJSON)
	mac := hmac.New(sha256.New, hmacSecret)
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig
}

func nilContextForTest() context.Context { return nil }

// TestVerify_TimingFloorClosesKidExistenceSideChannel proves the
// kid-existence side channel is closed: a wrong-kid rejection must
// take at least verifyTimingFloor, removing the previous ~4 µs vs
// ~56 µs gap a hostile probe could use to enumerate valid kids.
//
// We compare the median of a small sample so a single slow GC pause
// doesn't dominate. The floor is enforced via a sleep so the wall-
// clock measurement is the right primitive.
func TestVerify_TimingFloorClosesKidExistenceSideChannel(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test under -short")
	}
	key := testKey(t)
	ks, parseErr := ParseKeySet(testJWKS(t, key, "kid-1"))
	if parseErr != nil {
		t.Fatalf("ParseKeySet: %v", parseErr)
	}

	now := time.Now()
	// Token signed with a kid the JWKS does not recognise.
	tokenWrongKid := signJWT(t, key, "kid-unknown", map[string]any{
		"sub": "u",
		"exp": now.Add(5 * time.Minute).Unix(),
	})

	const samples = 9
	durations := make([]time.Duration, samples)
	for i := 0; i < samples; i++ {
		start := time.Now()
		_, _ = ks.Verify(tokenWrongKid, now)
		durations[i] = time.Since(start)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	median := durations[samples/2]

	// The floor is 50 µs. Allow some slack for sleep wakeup
	// granularity — we require the median wrong-kid rejection to
	// take at least 30 µs, which is comfortably above the pre-floor
	// 4 µs measurement and well below the 50 µs nominal floor.
	if median < 30*time.Microsecond {
		t.Fatalf("wrong-kid rejection median (%s) must be ≥ 30µs; the timing floor closes the kid-existence side channel", median)
	}
}

// baseFieldRoundTripper mimics otelhttp.Transport: a wrapper with a public
// settable Base field holding the real *http.Transport.
type baseFieldRoundTripper struct {
	Base http.RoundTripper
}

func (b *baseFieldRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return b.Base.RoundTrip(req)
}

func TestJWKSHTTPClient_HardensWrappedTransportBase(t *testing.T) {
	inner := http.DefaultTransport.(*http.Transport).Clone()
	weak := &tls.Config{MinVersion: tls.VersionTLS10, ServerName: "jwks.wrap.test"}
	inner.TLSClientConfig = weak

	wrapper := &baseFieldRoundTripper{Base: inner}
	custom := &http.Client{Transport: wrapper, Timeout: time.Second}

	hardened := jwksHTTPClient(custom)
	// Caller wrapper must not be mutated.
	if wrapper.Base != inner {
		t.Fatal("jwksHTTPClient must not mutate caller wrapper Base")
	}
	if weak.MinVersion != tls.VersionTLS10 {
		t.Fatal("jwksHTTPClient must not mutate caller TLS config")
	}

	outWrap, ok := hardened.Transport.(*baseFieldRoundTripper)
	if !ok {
		t.Fatalf("transport type = %T, want *baseFieldRoundTripper clone", hardened.Transport)
	}
	tr, ok := outWrap.Base.(*http.Transport)
	if !ok {
		t.Fatalf("Base type = %T, want hardened *http.Transport", outWrap.Base)
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion != minimumTLSVersion {
		t.Fatalf("wrapped Base MinVersion = %v, want floor %x", tr.TLSClientConfig, minimumTLSVersion)
	}
	if tr.TLSClientConfig.ServerName != "jwks.wrap.test" {
		t.Fatalf("ServerName not preserved: %q", tr.TLSClientConfig.ServerName)
	}
}
