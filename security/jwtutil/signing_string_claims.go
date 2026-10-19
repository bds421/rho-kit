package jwtutil

import (
	"context"
	"errors"
	"maps"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrSigningStringClaims reports unconfigured or malformed additional claims.
// Errors never contain claim values or caller-selected names.
var ErrSigningStringClaims = errors.New("jwtutil: invalid signing string claims")

const (
	maxSigningStringClaims = 16
	maxSigningClaimName    = 64
	maxSigningClaimValue   = 2048
	maxSigningClaimBytes   = 8192
)

func validSigningClaimName(name string) bool {
	if len(name) == 0 || len(name) > maxSigningClaimName {
		return false
	}
	for i := range len(name) {
		b := name[i]
		if b != '_' && !(b >= 'a' && b <= 'z') && !(b >= 'A' && b <= 'Z') && !(i > 0 && b >= '0' && b <= '9') {
			return false
		}
	}
	// Keep registered claims, kit-owned permissions/scopes and JWS header names
	// out of the extra-claim API, including case variants.
	switch strings.ToLower(name) {
	case "iss", "aud", "sub", "jti", "iat", "exp", "nbf", "permissions", "scopes", "scope",
		"alg", "kid", "typ", "cty", "jku", "jwk", "x5u", "x5c", "x5t", "crit", "b64":
		return false
	}
	return true
}

// WithSigningStringClaims replaces the immutable additional-string allowlist.
// The default is empty. Names must be unique ASCII identifiers of 1..64 bytes;
// at most 16 are allowed. Reserved JWT, kit and JWS names are forbidden. Invalid
// wiring panics before key loading. Each provider receives its own copied map.
func WithSigningStringClaims(names ...string) SigningOption {
	if len(names) > maxSigningStringClaims {
		panic("jwtutil: signing string claim allowlist exceeds limit")
	}
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !validSigningClaimName(name) {
			panic("jwtutil: invalid signing string claim allowlist")
		}
		if _, exists := allowed[name]; exists {
			panic("jwtutil: duplicate signing string claim name")
		}
		allowed[name] = struct{}{}
	}
	return func(p *SigningProvider) { p.stringClaimNames = maps.Clone(allowed) }
}

// SignWithStringClaims issues bounded additional strings through the same key,
// pinned issuer/audience, JTI/expiry, recorder and lifecycle as SignContext.
// Fields must be a subset of WithSigningStringClaims; values are nonempty valid
// UTF-8, control-free and at most 2048 bytes. Total name/value bytes are at most
// 8192. The per-call map is copied before signing; callers must not mutate a map
// concurrently with its initial read. No verified Claims.stringClaims are
// implicitly reissued. Existing Sign/SignContext always issue no extras.
//
// As with SignContext, synchronous in-memory signing does not establish current
// caller authority. Consumers must check cancellation/authority before and after
// issuance before releasing a credential, including when no recorder is wired.
func (p *SigningProvider) SignWithStringClaims(ctx context.Context, claims Claims, fields map[string]string) (string, error) {
	return p.signContext(ctx, claims, fields)
}

func (p *SigningProvider) copySigningStringClaims(fields map[string]string) (map[string]string, error) {
	if len(fields) > maxSigningStringClaims {
		return nil, ErrSigningStringClaims
	}
	if len(fields) == 0 {
		return nil, nil
	}
	issued := maps.Clone(fields)
	total := 0
	for name, value := range issued {
		if _, allowed := p.stringClaimNames[name]; !allowed || !validSigningClaimName(name) ||
			value == "" || len(value) > maxSigningClaimValue || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
			return nil, ErrSigningStringClaims
		}
		total += len(name) + len(value)
		if total > maxSigningClaimBytes {
			return nil, ErrSigningStringClaims
		}
	}
	return issued, nil
}
