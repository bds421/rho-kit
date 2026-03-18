package jwtutil

import (
	"testing"
	"time"
)

func TestLoadJWTFields(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		f := LoadJWTFields()
		if f.JWKSURL != "https://oathkeeper:4456/.well-known/jwks.json" {
			t.Errorf("jwks_url = %q", f.JWKSURL)
		}
	})

	t.Run("env override", func(t *testing.T) {
		t.Setenv("JWKS_URL", "https://custom:1234/jwks")
		f := LoadJWTFields()
		if f.JWKSURL != "https://custom:1234/jwks" {
			t.Errorf("jwks_url = %q, want custom URL", f.JWKSURL)
		}
	})
}

func TestCacheTTL_Default(t *testing.T) {
	ttl := CacheTTL()
	if ttl != 5*time.Minute {
		t.Errorf("CacheTTL() = %v, want 5m", ttl)
	}
}

func TestCacheTTL_EnvOverride(t *testing.T) {
	t.Setenv("JWT_CACHE_TTL_MINUTES", "10")
	ttl := CacheTTL()
	if ttl != 10*time.Minute {
		t.Errorf("CacheTTL() = %v, want 10m", ttl)
	}
}

func TestCacheTTL_InvalidFallsBackToDefault(t *testing.T) {
	t.Setenv("JWT_CACHE_TTL_MINUTES", "not-a-number")
	ttl := CacheTTL()
	if ttl != 5*time.Minute {
		t.Errorf("CacheTTL() = %v, want 5m (fallback)", ttl)
	}
}

func TestCacheTTL_ZeroFallsBackToDefault(t *testing.T) {
	t.Setenv("JWT_CACHE_TTL_MINUTES", "0")
	ttl := CacheTTL()
	if ttl != 5*time.Minute {
		t.Errorf("CacheTTL() = %v, want 5m (fallback for zero)", ttl)
	}
}
