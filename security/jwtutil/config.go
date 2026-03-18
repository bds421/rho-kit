package jwtutil

import (
	"time"

	"github.com/bds421/rho-kit/core/config"
)

// JWTFields holds the JWKS URL for JWT verification via Oathkeeper.
// Embed this in service configs that verify JWTs.
type JWTFields struct {
	JWKSURL string
}

// LoadJWTFields reads the JWKS URL from environment variables.
func LoadJWTFields() JWTFields {
	return JWTFields{
		JWKSURL: config.Get("JWKS_URL", "https://oathkeeper:4456/.well-known/jwks.json"),
	}
}

// CacheTTL reads the JWT_CACHE_TTL_MINUTES environment variable and returns
// the cache duration for JWKS key sets. Defaults to 5 minutes if unset.
func CacheTTL() time.Duration {
	p := &config.Parser{}
	minutes := p.Int("JWT_CACHE_TTL_MINUTES", 5)
	if p.Err() != nil || minutes < 1 {
		return 5 * time.Minute
	}
	return time.Duration(minutes) * time.Minute
}
