package ratelimit

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
)

// HealthIndicator reports whether a dependency is healthy.
// This interface is satisfied by [redis.Connection] (via its Healthy method).
type HealthIndicator interface {
	Healthy() bool
}

// DegradationHandler decides what to do when the dependency is unavailable.
// Return nil to allow the request through (passthrough), or return an error
// to reject it (fail-fast). This interface is satisfied by
// [redis.DegradationPolicy] (via its OnUnavailable method).
type DegradationHandler interface {
	OnUnavailable(ctx context.Context) error
}

// WithDegradation configures the rate limiter to check a health indicator
// before enforcing rate limits. When the indicator reports unhealthy:
//
//   - If the handler returns nil (passthrough), requests are allowed through
//     without rate limiting.
//   - If the handler returns an error (fail-fast), a 503 Service Unavailable
//     response is returned.
//
// This enables graceful degradation when Redis (or another backing store)
// is unavailable. The health indicator is typically a [redis.Connection]
// and the handler a [redis.DegradationPolicy].
//
// When not configured, the rate limiter operates normally without health checks.
func WithDegradation(health HealthIndicator, handler DegradationHandler) LimiterOption {
	if health == nil {
		panic("middleware/ratelimit: health indicator must not be nil")
	}
	if handler == nil {
		panic("middleware/ratelimit: degradation handler must not be nil")
	}
	return func(rl *Limiter) {
		rl.health = health
		rl.degradation = handler
	}
}

// WithKeyedDegradation configures the keyed rate limiter to check a health
// indicator before enforcing rate limits. Behavior matches [WithDegradation].
func WithKeyedDegradation(health HealthIndicator, handler DegradationHandler) KeyedOption {
	if health == nil {
		panic("middleware/ratelimit: health indicator must not be nil")
	}
	if handler == nil {
		panic("middleware/ratelimit: degradation handler must not be nil")
	}
	return func(rl *KeyedLimiter) {
		rl.health = health
		rl.degradation = handler
	}
}

// handleDegradation checks health and applies degradation policy.
// Returns (shouldSkipRateLimit, handled, metricOutcome). If handled is true,
// the response has already been written and the caller should return
// immediately.
func handleDegradation(
	w http.ResponseWriter,
	r *http.Request,
	health HealthIndicator,
	handler DegradationHandler,
) (skip bool, handled bool, metricOutcome string) {
	if health == nil {
		return false, false, ""
	}
	healthy, ok := safeHealthy(health)
	if !ok {
		httpx.WriteError(w, http.StatusServiceUnavailable, "service unavailable")
		return false, true, rateLimitOutcomeDegradedRejected
	}
	if healthy {
		return false, false, ""
	}

	err, ok := safeOnUnavailable(handler, r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusServiceUnavailable, "service unavailable")
		return false, true, rateLimitOutcomeDegradedRejected
	}
	if err == nil {
		// Passthrough: skip rate limiting, allow request through.
		return true, false, rateLimitOutcomeDegradedPassthrough
	}

	// Fail-fast: return 503.
	httpx.WriteError(w, http.StatusServiceUnavailable, "service unavailable")
	return false, true, rateLimitOutcomeDegradedRejected
}

func safeHealthy(health HealthIndicator) (healthy bool, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Default().Error("ratelimit: health indicator panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			healthy, ok = false, false
		}
	}()
	return health.Healthy(), true
}

func safeOnUnavailable(handler DegradationHandler, ctx context.Context) (err error, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Default().Error("ratelimit: degradation handler panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			err, ok = nil, false
		}
	}()
	return handler.OnUnavailable(ctx), true
}
