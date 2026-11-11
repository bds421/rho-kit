package centrifuge

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"time"

	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

// ErrSigningKeyNotPEM is returned by [NewConnectionTokens] for a key that is
// not a PEM block.
var ErrSigningKeyNotPEM = errors.New("centrifuge: signing key is not PEM")

// TokenConfig describes the tokens a service issues to its connections and
// the node verifies ([WithJWTAuth]).
type TokenConfig struct {
	// SigningKeyPEM is the PKCS #8 private key that signs the tokens.
	SigningKeyPEM []byte
	// Issuer and Audience are expected on every token.
	Issuer, Audience string
	// Lifetime is a token's default validity.
	Lifetime time.Duration
	// StringClaims are the custom string claims a token may carry.
	StringClaims []string
}

// ConnectionTokens signs a service's connection tokens and verifies them.
type ConnectionTokens struct {
	Signer   *jwtutil.SigningProvider
	Verifier *jwtutil.Provider
}

// NewConnectionTokens makes the signer and, from its public keys, the
// verifier for the same issuer, audience and claims.
func NewConnectionTokens(ctx context.Context, c TokenConfig) (ConnectionTokens, error) {
	block, _ := pem.Decode(c.SigningKeyPEM)
	if block == nil {
		return ConnectionTokens{}, ErrSigningKeyNotPEM
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return ConnectionTokens{}, err
	}
	signer, err := jwtutil.NewSigningProvider(ctx,
		func(context.Context) (crypto.PrivateKey, error) { return key, nil },
		jwtutil.WithSigningRotationInterval(time.Hour),
		jwtutil.WithSigningExpectedIssuer(c.Issuer),
		jwtutil.WithSigningExpectedAudience(c.Audience),
		jwtutil.WithSigningDefaultLifetime(c.Lifetime),
		jwtutil.WithSigningStringClaims(c.StringClaims...),
	)
	if err != nil {
		return ConnectionTokens{}, err
	}
	jwks, err := signer.PublicJWKS()
	if err != nil {
		return ConnectionTokens{}, err
	}
	ks, err := jwtutil.ParseKeySet(jwks)
	if err != nil {
		return ConnectionTokens{}, err
	}
	verifier := jwtutil.NewProviderWithKeySet(ks,
		jwtutil.WithExpectedIssuer(c.Issuer),
		jwtutil.WithExpectedAudience(c.Audience),
		jwtutil.WithStringClaims(c.StringClaims...),
	)
	return ConnectionTokens{Signer: signer, Verifier: verifier}, nil
}
