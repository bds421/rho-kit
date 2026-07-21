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
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
	"github.com/bds421/rho-kit/data/v2/ratelimit"
	"github.com/bds421/rho-kit/httpx/v2"
)

// scopeHeader names the response header that disambiguates *which*
// scope triggered the 429. The IP-based middleware does not set this
// header — its absence implies "IP scope" (or any other limit
// upstream of this one).
const scopeHeader = "X-RateLimit-Scope"

// scopeValue is what we set scopeHeader to when our cap fires.
const scopeValue = "tenant"

// errLimiterPanic is the sentinel returned by safeAllow when the
// caller-supplied limiter panics, routed through the same 500 path as a
// limiter back-end error so a buggy limiter fails closed.
var errLimiterPanic = errors.New("ratelimit/tenant: limiter panicked")

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
//   - lim.Allow panics ⇒ 500 ("rate limit check failed"). A buggy
//     caller-supplied limiter is contained here (mirroring the
//     KeyedMiddleware keyFunc guard) rather than propagating and
//     relying on an outer recover middleware being installed.
//   - lim.Allow returns true ⇒ next handler runs.
//
// New panics on a nil lim — a nil limiter would be a fail-open silent
// bypass, almost certainly a wiring bug.
func New(lim ratelimit.Limiter) func(http.Handler) http.Handler {
	if lim == nil {
		panic("ratelimit/tenant: New limiter must not be nil")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := coretenant.Required(r.Context())
			if err != nil {
				httpx.WriteError(w, http.StatusBadRequest, "tenant: required tenant ID is missing")
				return
			}

			allowed, retryAfter, err := safeAllow(r.Context(), lim, string(id))
			if err != nil {
				httpx.Logger(r.Context(), slog.Default()).Error(
					"ratelimit/tenant: limiter Allow failed",
					redact.Error(err),
				)
				httpx.WriteError(w, http.StatusInternalServerError, "rate limit check failed")
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
				httpx.WriteError(w, http.StatusTooManyRequests, "tenant rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// safeAllow invokes the caller-supplied limiter and converts a panic
// into a synthetic error so the middleware can render a contained 500
// instead of unwinding the request. This mirrors the safeRateLimitKey
// guard around the caller-supplied keyFunc in the sibling
// KeyedMiddleware: both treat a panicking caller-supplied extension
// point as a degraded backend (fail-closed) rather than fail-open.
func safeAllow(ctx context.Context, lim ratelimit.Limiter, key string) (allowed bool, retryAfter time.Duration, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Default().Error("ratelimit/tenant: limiter panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			allowed, retryAfter, err = false, 0, errLimiterPanic
		}
	}()
	return lim.Allow(ctx, key)
}
