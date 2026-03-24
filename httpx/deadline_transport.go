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
