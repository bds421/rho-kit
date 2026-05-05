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
	maxBuffer int
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
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
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
				// Wait for the handler goroutine to finish. The context is
				// already cancelled so well-behaved handlers will exit promptly.
				// This prevents the goroutine from outliving the ResponseWriter.
				<-done
			}
		})
	}
}
