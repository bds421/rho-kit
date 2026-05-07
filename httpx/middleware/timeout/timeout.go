package timeout

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// Option configures the Timeout middleware.
type Option func(*timeoutOptions)

type timeoutOptions struct {
	maxBuffer             int
	hard                  bool
	allowWebSocketUpgrade bool
}

// WithWebSocketUpgradeBypass opts the route into bypassing the timeout
// when the request carries `Upgrade: websocket`. Only call this for
// routes that legitimately serve WebSocket connections — without the
// opt-in, any client could send the header against any route to run
// unbounded.
func WithWebSocketUpgradeBypass() Option {
	return func(o *timeoutOptions) { o.allowWebSocketUpgrade = true }
}

// WithMaxBufferSize overrides the per-request response buffer cap (default
// 1 MiB). Responses exceeding the cap are truncated and the handler sees
// [ErrResponseTooLarge] from Write. Useful for endpoints that legitimately
// stream multi-megabyte JSON; pair with a body-size limit on the request
// path so the same memory budget applies in both directions.
//
// Panics if size <= 0.
func WithMaxBufferSize(size int) Option {
	if size <= 0 {
		panic("timeout: WithMaxBufferSize requires a positive size")
	}
	return func(o *timeoutOptions) { o.maxBuffer = size }
}

// WithHard enables hard-timeout mode: when the deadline fires, the
// middleware writes the 503 response and returns IMMEDIATELY, without
// waiting for the handler goroutine to exit. The handler's later writes
// flow into the buffered timeoutWriter — which already returns
// http.ErrHandlerTimeout — so no double-write reaches the real
// ResponseWriter, but the goroutine continues running until it returns
// of its own accord.
//
// Use ONLY when you've measured handlers that can ignore ctx.Done() (legacy
// code, third-party libraries) and accept the goroutine-leak cost. Holding
// the HTTP goroutine alive is preferable to leaking it indefinitely; the
// hard mode trades determinism (the connection is freed on deadline) for
// resource accounting (the handler goroutine outlives the request).
//
// Default mode is cooperative: the middleware writes 503 then waits for
// the handler to exit. Cooperative mode is safer when handlers honor
// ctx.Done() — which is what the kit assumes by default.
func WithHard() Option {
	return func(o *timeoutOptions) { o.hard = true }
}

// Timeout wraps an http.Handler with a write deadline.
// Requests exceeding the duration receive a 503 JSON response with the correct
// Content-Type. WebSocket upgrade requests bypass the timeout since they need
// long-lived connections.
//
// This uses a custom timeout wrapper instead of http.TimeoutHandler because the
// stdlib always sets Content-Type: text/plain on timeout, which is incorrect for
// JSON API responses.
//
// IMPORTANT: Handlers MUST respect context cancellation. After the timeout fires,
// the context is cancelled and the middleware waits for the handler goroutine to
// exit before returning. A handler that ignores ctx.Done() will block the HTTP
// response indefinitely, effectively leaking the goroutine and the connection.
// If your handler delegates to slow I/O, ensure it selects on ctx.Done() or uses
// context-aware clients (e.g., database drivers, HTTP clients).
func Timeout(d time.Duration, opts ...Option) func(http.Handler) http.Handler {
	if d <= 0 {
		panic("timeout: duration must be positive")
	}
	cfg := timeoutOptions{}
	for _, opt := range opts {
		opt(&cfg)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Bypass the timeout only when the route opted into
			// WebSocket via cfg.allowWebSocketUpgrade. Honoring the
			// header alone is a generic timeout bypass: any client
			// could send `Upgrade: websocket` against a non-WS route
			// to run unbounded. Routes that genuinely upgrade should
			// be mounted via a builder that sets the option.
			if cfg.allowWebSocketUpgrade && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				next.ServeHTTP(w, r)
				return
			}

			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()

			done := make(chan struct{})
			tw := &timeoutWriter{
				w:         w,
				h:         make(http.Header),
				maxBuffer: cfg.maxBuffer,
			}

			go func() {
				next.ServeHTTP(tw, r.WithContext(ctx))
				close(done)
			}()

			select {
			case <-done:
				tw.writeToReal()
			case <-ctx.Done():
				tw.writeTimeout()
				if cfg.hard {
					// Hard mode: return immediately. The handler goroutine
					// keeps running until it exits on its own; later writes
					// go to the buffered timeoutWriter which already returns
					// ErrHandlerTimeout, so no double-write hits the real
					// ResponseWriter. Acceptable when handlers can ignore
					// ctx.Done() and the leak is the lesser evil.
					return
				}
				// Cooperative mode (default): wait for the handler goroutine
				// to finish. The context is already cancelled so well-behaved
				// handlers will exit promptly. This prevents the goroutine
				// from outliving the ResponseWriter.
				<-done
			}
		})
	}
}
