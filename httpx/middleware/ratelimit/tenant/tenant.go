// Package tenant provides per-tenant HTTP rate-limit middleware that
// gates requests on a [ratelimit.Limiter] keyed by the tenant ID
// resolved by the upstream tenant middleware.
//
// The middleware is intended to layer on TOP of an existing IP-based
// limit: both must pass before the handler runs. When the tenant cap
// fires, the response carries `X-RateLimit-Scope: tenant` so the
// caller (and on-call observability) can tell *which* limit triggered
// the 429 — useful when an IP and a tenant both exceed in the same
// request and the operator needs to know which budget to widen.
//
// Missing tenant on the context returns 400 — the middleware MUST be
// wired downstream of a tenant middleware that resolves the tenant ID
// onto the request context.
//
// The package depends only on [ratelimit.Limiter], not on a concrete
// algorithm; swap in token-bucket, GCRA, or the upcoming Redis-backed
// limiter depending on whether limits must hold across replicas.
package tenant

import (
	"math"
	"net/http"
	"strconv"

	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
	"github.com/bds421/rho-kit/data/v2/ratelimit"
)

// scopeHeader names the response header that disambiguates *which*
// scope triggered the 429. The IP-based middleware does not set this
// header — its absence implies "IP scope" (or any other limit
// upstream of this one).
const scopeHeader = "X-RateLimit-Scope"

// scopeValue is what we set scopeHeader to when our cap fires.
const scopeValue = "tenant"

// New returns an HTTP middleware that gates requests on lim, keyed
// by the tenant ID on the request context.
//
// Behaviour:
//
//   - Missing tenant ID on ctx ⇒ 400 ("tenant: required tenant ID is
//     missing"). Wire a tenant middleware upstream.
//   - lim.Allow returns false ⇒ 429 with X-RateLimit-Scope: tenant.
//     If the limiter supplies a retryAfter, it is rendered as a
//     Retry-After header (seconds, ceiling, minimum 1).
//   - lim.Allow returns an error ⇒ 500 ("rate limit check failed").
//     Limiter back-end errors must surface so a degraded backend
//     doesn't silently fail-open.
//   - lim.Allow returns true ⇒ next handler runs.
//
// New panics on a nil lim — a nil limiter would be a fail-open silent
// bypass, almost certainly a wiring bug.
func New(lim ratelimit.Limiter) func(http.Handler) http.Handler {
	if lim == nil {
		panic("ratelimit/tenant: limiter must not be nil")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := coretenant.Required(r.Context())
			if err != nil {
				writePlainError(w, http.StatusBadRequest, err.Error())
				return
			}

			allowed, retryAfter, err := lim.Allow(r.Context(), string(id))
			if err != nil {
				writePlainError(w, http.StatusInternalServerError, "rate limit check failed")
				return
			}
			if !allowed {
				w.Header().Set(scopeHeader, scopeValue)
				if retryAfter > 0 {
					seconds := int(math.Ceil(retryAfter.Seconds()))
					if seconds < 1 {
						seconds = 1
					}
					w.Header().Set("Retry-After", strconv.Itoa(seconds))
				}
				writePlainError(w, http.StatusTooManyRequests, "tenant rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writePlainError writes a minimal JSON error so this package stays
// out of httpx's import graph. We mirror httpx.WriteError's body
// shape ({"error": "..."}) closely enough for clients to parse, but
// keep the implementation hermetic — the full kit-wide error envelope
// belongs in httpx. strconv.Quote handles JSON-safe escaping of msg.
func writePlainError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":` + strconv.Quote(msg) + `}`))
}
