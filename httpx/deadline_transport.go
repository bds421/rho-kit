package httpx

import (
	"context"
	"io"
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
// deadline to account for network overhead. Negative values panic.
// Default: 500ms.
//
// Setting the safety margin to 0 disables the margin entirely, giving the
// outbound request the full remaining budget. This is useful when the caller
// already accounts for overhead or in testing scenarios.
func WithSafetyMargin(d time.Duration) DeadlineBudgetOption {
	if d < 0 {
		panic("httpx: WithSafetyMargin requires d >= 0")
	}
	return func(c *deadlineBudgetConfig) {
		c.safetyMargin = d
	}
}

// WithMinTimeout sets the minimum timeout for outbound requests. Even if the
// caller's remaining budget minus safety margin is lower, this floor is used.
// The duration must be positive. Default: 1s.
//
// Note: the parent context's deadline is always an upper bound. If the parent
// has less time remaining than minTimeout, the parent deadline still applies.
func WithMinTimeout(d time.Duration) DeadlineBudgetOption {
	if d <= 0 {
		panic("httpx: WithMinTimeout requires a positive duration")
	}
	return func(c *deadlineBudgetConfig) {
		c.minTimeout = d
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
//
// The derived ctx must remain alive until the response body is closed —
// HTTP/2 (and some HTTP/1.1 wrapping transports) tie body Read errors to the
// request context, so cancelling on RoundTrip return would surface as
// context.Canceled mid-body. We therefore wrap resp.Body in a closer that
// cancels only when the caller closes the body. On error paths (no body
// returned) the cancel runs immediately.
func (t *deadlineBudgetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	deadline, ok := req.Context().Deadline()
	if !ok {
		return t.base.RoundTrip(req)
	}

	remaining := time.Until(deadline) - t.safetyMargin
	if remaining < t.minTimeout {
		remaining = t.minTimeout
	}

	ctx, cancel := context.WithTimeout(req.Context(), remaining)
	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.Body == nil {
		cancel()
		return resp, nil
	}
	resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelOnCloseBody runs cancel exactly once when the body is closed,
// keeping the request context alive until the caller is done reading.
type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	return err
}
