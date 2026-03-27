package ratelimit

import (
	"context"
	"net/http"

	"github.com/bds421/rho-kit/httpx"
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
func WithDegradation(health HealthIndicator, handler DegradationHandler) RateLimiterOption {
	if health == nil {
		panic("ratelimit: health indicator must not be nil")
	}
	if handler == nil {
		panic("ratelimit: degradation handler must not be nil")
	}
	return func(rl *RateLimiter) {
		rl.health = health
		rl.degradation = handler
	}
}

// WithKeyedDegradation configures the keyed rate limiter to check a health
// indicator before enforcing rate limits. Behavior matches [WithDegradation].
func WithKeyedDegradation(health HealthIndicator, handler DegradationHandler) KeyedOption {
	if health == nil {
		panic("ratelimit: health indicator must not be nil")
	}
	if handler == nil {
		panic("ratelimit: degradation handler must not be nil")
	}
	return func(rl *KeyedRateLimiter) {
		rl.health = health
		rl.degradation = handler
	}
}

// handleDegradation checks health and applies degradation policy.
// Returns (shouldSkipRateLimit, handled). If handled is true, the response
// has already been written and the caller should return immediately.
func handleDegradation(
	w http.ResponseWriter,
	r *http.Request,
	health HealthIndicator,
	handler DegradationHandler,
) (skip bool, handled bool) {
	if health == nil {
		return false, false
	}
	if health.Healthy() {
		return false, false
	}

	err := handler.OnUnavailable(r.Context())
	if err == nil {
		// Passthrough: skip rate limiting, allow request through.
		return true, false
	}

	// Fail-fast: return 503.
	httpx.WriteError(w, http.StatusServiceUnavailable, "service unavailable")
	return false, true
}
