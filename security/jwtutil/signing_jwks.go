package jwtutil

import (
	"encoding/json"
	"fmt"

	"github.com/lestrrat-go/jwx/v3/jwk"
)

// PublicJWKS returns the current signing key's public half as a JWKS document
// with the same kid that Sign stamps, for in-process verifiers
// ([ParseKeySet]) or a /.well-known/jwks.json endpoint.
func (p *SigningProvider) PublicJWKS() ([]byte, error) {
	if p == nil || p.closed.Load() {
		return nil, ErrSigningProviderClosed
	}
	current := p.current.Load()
	if current == nil {
		return nil, ErrSigningKeyUnavailable
	}
	pub, err := (*current).PublicKey()
	if err != nil {
		return nil, fmt.Errorf("jwtutil: derive public key: %w", err)
	}
	set := jwk.NewSet()
	if err := set.AddKey(pub); err != nil {
		return nil, fmt.Errorf("jwtutil: build jwks: %w", err)
	}
	return json.Marshal(set)
}
