package centrifuge_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/bds421/rho-kit/realtime/centrifuge/v2"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

func signingKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// A token the signer issues, with its custom claims, is what the verifier
// reads back; a key that is not PEM is refused.
func TestConnectionTokensRoundTrip(t *testing.T) {
	tokens, err := centrifuge.NewConnectionTokens(t.Context(), centrifuge.TokenConfig{
		SigningKeyPEM: signingKeyPEM(t), Issuer: "platform", Audience: "realtime", Lifetime: time.Minute, StringClaims: []string{"org"},
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := tokens.Signer.SignWithStringClaims(t.Context(), jwtutil.Claims{Subject: "u1", ExpiresAt: time.Now().Add(time.Minute).Unix()}, map[string]string{"org": "o1"})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := tokens.Verifier.Verify(token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if org, _ := claims.StringClaim("org"); claims.Subject != "u1" || org != "o1" {
		t.Errorf("read back %+v", claims)
	}
	if _, err := centrifuge.NewConnectionTokens(t.Context(), centrifuge.TokenConfig{SigningKeyPEM: []byte("x")}); !errors.Is(err, centrifuge.ErrSigningKeyNotPEM) {
		t.Errorf("a key that is not PEM: %v", err)
	}
}
