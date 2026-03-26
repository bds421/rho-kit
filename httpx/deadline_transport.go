package httpx

import (
	"context"
	"net/http"
	"time"
)

const (
	defaultSafetyMargin = 500 * time.Millisecond
	defaultMinTimeout   = 1 * time.Second
)

// DeadlineBudgetOption configures deadline budget propagation.
type DeadlineBudgetOption func(*deadlineBudgetConfig)

type deadlineBudgetConfig struct {
	safetyMargin time.Duration // subtracted from remaining budget, default 500ms
	minTimeout   time.Duration // floor timeout, default 1s
}

// WithSafetyMargin sets the duration subtracted from the caller's remaining
// deadline to account for network overhead. Negative values are ignored.
// Default: 500ms.
//
// Setting the safety margin to 0 disables the margin entirely, giving the
// outbound request the full remaining budget. This is useful when the caller
// already accounts for overhead or in testing scenarios.
func WithSafetyMargin(d time.Duration) DeadlineBudgetOption {
	return func(c *deadlineBudgetConfig) {
		if d >= 0 {
			c.safetyMargin = d
		}
	}
}

// WithMinTimeout sets the minimum timeout for outbound requests. Even if the
// caller's remaining budget minus safety margin is lower, this floor is used.
// Zero and negative values are ignored. Default: 1s.
//
// Note: the parent context's deadline is always an upper bound. If the parent
// has less time remaining than minTimeout, the parent deadline still applies.
func WithMinTimeout(d time.Duration) DeadlineBudgetOption {
	return func(c *deadlineBudgetConfig) {
		if d > 0 {
			c.minTimeout = d
		}
	}
}

// deadlineBudgetTransport wraps an http.RoundTripper to propagate the caller's
// context deadline to outbound requests. When the caller's context has a
// deadline, the outbound request timeout is derived from the remaining budget
// minus a safety margin, clamped to a minimum timeout floor.
//
// The base transport chain MUST terminate at a standard *http.Transport (or
// equivalent) that detaches the connection from the request context after
// headers arrive. This is required because the derived context is cancelled
// after RoundTrip returns; if the base transport does not detach, reading the
// response body will fail with a context cancellation error.
type deadlineBudgetTransport struct {
	base         http.RoundTripper
	safetyMargin time.Duration
	minTimeout   time.Duration
}

// RoundTrip adjusts the request context timeout based on the caller's remaining
// deadline budget, then delegates to the base transport.
func (t *deadlineBudgetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	deadline, ok := req.Context().Deadline()
	if !ok {
		return t.base.RoundTrip(req)
	}

	remaining := time.Until(deadline) - t.safetyMargin
	if remaining < t.minTimeout {
		remaining = t.minTimeout
	}

	// cancel is deferred here and runs after RoundTrip returns (after response
	// headers are received). This is safe: http.Transport detaches the context
	// from the connection once headers arrive, so the body remains readable.
	ctx, cancel := context.WithTimeout(req.Context(), remaining)
	defer cancel()

	return t.base.RoundTrip(req.WithContext(ctx))
}
