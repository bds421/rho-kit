package apikey

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/v2/contextutil"
	"github.com/bds421/rho-kit/httpx/v2"
	apikeycore "github.com/bds421/rho-kit/security/v2/apikey"
)

// Named types for type-safe, collision-free context keys.
type (
	keyID  string
	owner  string
	scopes []string
)

var (
	keyIDKey  = contextutil.NewKey[keyID]("httpx.apikey.id")
	ownerKey  = contextutil.NewKey[owner]("httpx.apikey.owner")
	scopesKey = contextutil.NewKey[scopes]("httpx.apikey.scopes")
)

// apiKeyHeader is the fallback header checked when no Bearer token is present.
const apiKeyHeader = "X-API-Key"

// Config configures [Middleware].
type Config struct {
	// Repository looks keys up by id. Required.
	Repository apikeycore.Repository
	// Prefix is the expected token prefix; defaults to [apikeycore.DefaultPrefix].
	Prefix string
	// Now supplies the verification clock; defaults to [time.Now].
	Now func() time.Time
	// Logger records authentication failures at debug level. Optional.
	Logger *slog.Logger
}

// Middleware returns chain-shape middleware that authenticates API-key
// requests. It panics if cfg.Repository is nil — a missing repository is a
// fail-fast misconfiguration that would reject every request at runtime.
func Middleware(cfg Config) func(http.Handler) http.Handler {
	if cfg.Repository == nil {
		panic("middleware/apikey: Middleware requires a non-nil Repository")
	}
	prefix := cfg.Prefix
	if prefix == "" {
		prefix = apikeycore.DefaultPrefix
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	// dummyKey is verified against the presented secret on a repository miss
	// so the unknown-id path performs the same constant-time hash comparison
	// as a known-id/bad-secret path. This keeps the two indistinguishable by
	// response timing, not just by response body — see the miss branch below.
	dummyKey := apikeycore.Key{Hash: apikeycore.Hash("")}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := extractToken(r)
			if !ok {
				unauthorized(w, cfg.Logger, "missing api key", nil)
				return
			}
			id, secret, err := apikeycore.Parse(token, prefix)
			if err != nil {
				unauthorized(w, cfg.Logger, "malformed api key", err)
				return
			}
			key, err := cfg.Repository.FindByID(r.Context(), id)
			if err != nil {
				// Do not distinguish "no such key" from a bad secret —
				// both are 401 so the endpoint does not leak which key
				// ids exist. Run a dummy Verify so the miss path does the
				// same hash comparison work as a bad-secret hit, equalising
				// response timing as well as the response body.
				_ = dummyKey.Verify(secret, now())
				unauthorized(w, cfg.Logger, "unknown api key", err)
				return
			}
			if err := key.Verify(secret, now()); err != nil {
				unauthorized(w, cfg.Logger, "invalid api key", err)
				return
			}

			ctx := keyIDKey.Set(r.Context(), keyID(key.ID))
			ctx = ownerKey.Set(ctx, owner(key.Owner))
			ctx = scopesKey.Set(ctx, scopes(key.Scopes))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractToken reads the API key from the Authorization: Bearer header, then
// falls back to the X-API-Key header. Returns ("", false) when neither holds
// a non-empty value.
func extractToken(r *http.Request) (string, bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		const bearer = "bearer "
		if len(h) > len(bearer) && strings.EqualFold(h[:len(bearer)], bearer) {
			if token := strings.TrimSpace(h[len(bearer):]); token != "" {
				return token, true
			}
		}
	}
	if token := strings.TrimSpace(r.Header.Get(apiKeyHeader)); token != "" {
		return token, true
	}
	return "", false
}

func unauthorized(w http.ResponseWriter, logger *slog.Logger, reason string, cause error) {
	if logger != nil {
		logger.Debug("apikey auth failed", "reason", reason, "error", cause)
	}
	httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
}

// OwnerFromContext returns the authenticated key's owner, or ("", false) when
// the request was not authenticated by this middleware.
func OwnerFromContext(r *http.Request) (string, bool) {
	v, ok := ownerKey.Get(r.Context())
	return string(v), ok
}

// KeyIDFromContext returns the authenticated key's public id.
func KeyIDFromContext(r *http.Request) (string, bool) {
	v, ok := keyIDKey.Get(r.Context())
	return string(v), ok
}

// ScopesFromContext returns the authenticated key's scopes. The returned
// slice is a copy and safe to retain.
func ScopesFromContext(r *http.Request) ([]string, bool) {
	v, ok := scopesKey.Get(r.Context())
	if !ok {
		return nil, false
	}
	out := make([]string, len(v))
	copy(out, v)
	return out, true
}
