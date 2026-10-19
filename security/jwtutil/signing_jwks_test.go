package jwtutil

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
	"time"
)

func TestPublicJWKSVerifiesSignedTokens(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewSigningProvider(context.Background(),
		func(context.Context) (crypto.PrivateKey, error) { return priv, nil },
		WithSigningRotationInterval(time.Hour),
		WithSigningExpectedIssuer("issuer"),
		WithSigningExpectedAudience("aud"),
		WithSigningDefaultLifetime(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer signer.Close()

	jwks, err := signer.PublicJWKS()
	if err != nil {
		t.Fatal(err)
	}
	ks, err := ParseKeySet(jwks)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.Sign(Claims{Subject: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := NewProviderWithKeySet(ks, WithExpectedIssuer("issuer"), WithExpectedAudience("aud")).
		VerifyContext(context.Background(), token, time.Now())
	if err != nil {
		t.Fatalf("verify with exported JWKS: %v", err)
	}
	if claims.Subject != "user-1" {
		t.Fatalf("subject %q", claims.Subject)
	}
}
