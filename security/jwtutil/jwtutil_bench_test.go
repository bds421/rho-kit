package jwtutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

// BenchmarkKeySet_Verify_ValidToken measures the hot path that every
// authenticated request takes: validate a structurally valid token
// against a fixed JWKS. The kit aims to keep this well under 100
// microseconds on a typical CI VM so that auth middleware does not
// become the dominant request latency. The benchmark exists so a
// future refactor (different JOSE library, additional claim
// validation) can be evaluated against a concrete baseline (L167).
func BenchmarkKeySet_Verify_ValidToken(b *testing.B) {
	key := benchKey(b)
	ks, err := ParseKeySet(benchJWKS(b, key, "kid-1"))
	if err != nil {
		b.Fatalf("ParseKeySet: %v", err)
	}
	now := time.Now()
	token := benchSignJWT(b, key, "kid-1", map[string]any{
		"sub":         "user-bench",
		"permissions": []string{"general:view", "general:manage"},
		"scopes":      "production:view",
		"iat":         now.Unix(),
		"exp":         now.Add(5 * time.Minute).Unix(),
		"nbf":         now.Add(-1 * time.Minute).Unix(),
		"iss":         "https://oathkeeper",
	})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ks.Verify(token, now); err != nil {
			b.Fatalf("Verify: %v", err)
		}
	}
}

// BenchmarkKeySet_Verify_WrongKid measures the rejection path where
// a token carries a key id the JWKS does not recognise. Tracks the
// invariant that rejection is not faster than acceptance (which
// would create a kid-existence side channel).
func BenchmarkKeySet_Verify_WrongKid(b *testing.B) {
	key := benchKey(b)
	ks, err := ParseKeySet(benchJWKS(b, key, "kid-1"))
	if err != nil {
		b.Fatalf("ParseKeySet: %v", err)
	}
	now := time.Now()
	token := benchSignJWT(b, key, "kid-unknown", map[string]any{
		"sub": "user-bench",
		"exp": now.Add(5 * time.Minute).Unix(),
	})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ks.Verify(token, now)
	}
}

func benchKey(b *testing.B) *ecdsa.PrivateKey {
	b.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	return key
}

func benchJWKS(b *testing.B, key *ecdsa.PrivateKey, kid string) []byte {
	b.Helper()
	pubJWK, err := jwk.Import(key.PublicKey)
	if err != nil {
		b.Fatal(err)
	}
	_ = pubJWK.Set(jwk.KeyIDKey, kid)
	_ = pubJWK.Set(jwk.AlgorithmKey, jwa.ES256())
	_ = pubJWK.Set(jwk.KeyUsageKey, "sig")

	set := jwk.NewSet()
	_ = set.AddKey(pubJWK)

	data, err := json.Marshal(set)
	if err != nil {
		b.Fatal(err)
	}
	return data
}

func benchSignJWT(b *testing.B, key *ecdsa.PrivateKey, kid string, claims map[string]any) string {
	b.Helper()
	tok, err := jwt.NewBuilder().Build()
	if err != nil {
		b.Fatal(err)
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
		b.Fatal(err)
	}
	_ = jwkKey.Set(jwk.KeyIDKey, kid)

	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.ES256(), jwkKey))
	if err != nil {
		b.Fatal(err)
	}
	return string(signed)
}
