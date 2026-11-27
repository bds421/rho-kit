package timeout

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

const defaultPostTimeoutWait = 100 * time.Millisecond

// Option configures the Timeout middleware.
type Option func(*timeoutOptions)

type timeoutOptions struct {
	maxBuffer             int
	postTimeoutWait       time.Duration
	allowWebSocketUpgrade bool
	logger                *slog.Logger
}

type handlerResult struct {
	panicValue any
	panicStack []byte
}

// panicWithStack re-raises a handler panic with the original stack so the
// outer recover middleware's debug.Stack() is not limited to the timeout
// re-panic site (mirrors net/http.TimeoutHandler).
type panicWithStack struct {
	value any
	stack []byte
}

func (p panicWithStack) Error() string {
	return fmt.Sprintf("timeout: handler panic: %v", p.value)
}

// StackTrace returns the original handler stack captured at recover.
// The outer recover middleware prefers this over debug.Stack() at the
// re-panic site so logs point at the real handler frame.
func (p panicWithStack) StackTrace() []byte { return p.stack }

// UnwrapPanic returns the original panic value for redaction/logging.
func (p panicWithStack) UnwrapPanic() any { return p.value }

func (p panicWithStack) Unwrap() error {
	if err, ok := p.value.(error); ok {
		return err
	}
	return nil
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
// 1 MiB). Responses exceeding the cap are discarded and replaced by a 500 JSON
// error, even if the handler ignores [ErrResponseTooLarge] from Write. Useful
// for endpoints that legitimately return multi-megabyte JSON; pair with a
// body-size limit on the request path so the same memory budget applies in both
// directions.
//
// Panics if size <= 0.
func WithMaxBufferSize(size int) Option {
	if size <= 0 {
		panic("middleware/timeout: WithMaxBufferSize requires a positive size")
	}
	return func(o *timeoutOptions) { o.maxBuffer = size }
}

// WithPostTimeoutWait sets how long the middleware waits for the handler
// to observe cancellation after writing the timeout response. The default
// is 100ms. Set d to 0 to return immediately after the 503 is written.
//
// Panics if d is negative.
func WithPostTimeoutWait(d time.Duration) Option {
	if d < 0 {
		panic("middleware/timeout: WithPostTimeoutWait requires d >= 0")
	}
	return func(o *timeoutOptions) { o.postTimeoutWait = d }
}

// WithHard enables immediate-return mode: when the deadline fires, the
// middleware writes the 503 response and returns without waiting for the
// handler goroutine to exit. Later writes flow into the buffered
// timeoutWriter, which returns http.ErrHandlerTimeout, so no double-write
// reaches the real ResponseWriter.
func WithHard() Option {
	return WithPostTimeoutWait(0)
}

// WithLogger installs a logger that records late panics from handler
// goroutines that completed AFTER the middleware already returned a
// 503. Without this option, late panics fall back to [slog.Default]
// so the regression is never silently dropped — the option is the
// preferred path because it lets operators route late-panic events
// to the structured logger their service already wires (and to
// downstream metric/alert pipelines that read those records).
//
// Panics if logger is nil — omit the option entirely to opt out.
func WithLogger(logger *slog.Logger) Option {
	if logger == nil {
		panic("middleware/timeout: WithLogger requires a non-nil logger")
	}
	return func(o *timeoutOptions) { o.logger = logger }
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
// Handlers must still respect context cancellation. After the timeout fires,
// the context is cancelled and the middleware waits briefly for the handler
// goroutine to exit before returning. If your handler delegates to slow I/O,
// ensure it selects on ctx.Done() or uses context-aware clients such as
// database drivers or HTTP clients.
//
// Like http.TimeoutHandler, this middleware abandons a handler that ignores
// cancellation: once the 503 is written (immediately with [WithHard], or after
// the post-timeout grace otherwise) ServeHTTP returns while the handler
// goroutine may still be running. A handler that keeps reading r.Body or r
// after ctx.Done() races with net/http tearing down the request and reusing
// the connection. Stop touching r and r.Body once ctx.Done() is observed.
func Timeout(d time.Duration, opts ...Option) func(http.Handler) http.Handler {
	if d <= 0 {
		panic("middleware/timeout: Timeout duration must be positive")
	}
	cfg := timeoutOptions{postTimeoutWait: defaultPostTimeoutWait}
	for _, opt := range opts {
		if opt == nil {
			panic("middleware/timeout: Timeout option must not be nil")
		}
		opt(&cfg)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cfg.serve(w, r, next, d)
		})
	}
}

func (cfg *timeoutOptions) serve(w http.ResponseWriter, r *http.Request, next http.Handler, d time.Duration) {
	// Bypass the timeout only when the route opted into
	// WebSocket via cfg.allowWebSocketUpgrade. Honoring the
	// header alone is a generic timeout bypass: any client
	// could send `Upgrade: websocket` against a non-WS route
	// to run unbounded. Routes that genuinely upgrade should
	// be mounted via a builder that sets the option.
	if cfg.allowWebSocketUpgrade && isWebSocketUpgrade(r) {
		next.ServeHTTP(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), d)
	defer cancel()

	done := make(chan handlerResult, 1)
	tw := &timeoutWriter{
		w:         w,
		h:         make(http.Header),
		maxBuffer: cfg.maxBuffer,
		logger:    cfg.logger,
		head:      r.Method == http.MethodHead,
	}
	go runHandler(next, tw, r.WithContext(ctx), done)

	select {
	case result := <-done:
		result.repanic()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			tw.writeTimeout()
		} else {
			tw.writeToReal()
		}
	case <-ctx.Done():
		// Only emit the TIMEOUT 503 when our deadline fired.
		// Parent cancellation (client disconnect, server shutdown)
		// must not be counted/logged as a server timeout.
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			cfg.finishCancelled(tw, done)
			return
		}
		tw.writeTimeout()
		cfg.awaitLateHandler(done)
	}
}

// runHandler serves the request and reports its result, a panic included.
func runHandler(next http.Handler, tw *timeoutWriter, r *http.Request, done chan<- handlerResult) {
	var result handlerResult
	defer func() {
		if rv := recover(); rv != nil {
			result.panicValue = rv
			result.panicStack = debug.Stack()
		}
		done <- result
	}()
	next.ServeHTTP(tw, r)
}

// repanic re-raises the handler's panic on the serving goroutine.
func (result handlerResult) repanic() {
	if result.panicValue != nil {
		panic(panicWithStack{value: result.panicValue, stack: result.panicStack})
	}
}

// finishCancelled ends a request whose parent context was cancelled. If the
// handler already finished, its response is still flushed (best effort);
// otherwise the connection is left without a forged timeout body and the
// handler drained so its panics still surface.
func (cfg *timeoutOptions) finishCancelled(tw *timeoutWriter, done <-chan handlerResult) {
	select {
	case result := <-done:
		result.repanic()
		tw.writeToReal()
	default:
		go drainLateHandler(done, cfg.lateLogger())
	}
}

// awaitLateHandler waits up to the post-timeout grace for the handler after
// the 503 was written, then drains it in the background. Always draining
// keeps a post-timeout panic visible in the logs although the request has
// already returned; without it the buffered channel would also pin the
// handler goroutine's deferred sender at process exit.
func (cfg *timeoutOptions) awaitLateHandler(done <-chan handlerResult) {
	if cfg.postTimeoutWait == 0 {
		go drainLateHandler(done, cfg.lateLogger())
		return
	}
	timer := time.NewTimer(cfg.postTimeoutWait)
	defer timer.Stop()
	select {
	case result := <-done:
		result.repanic()
	case <-timer.C:
		go drainLateHandler(done, cfg.lateLogger())
	}
}

// lateLogger is the configured logger, or slog.Default() rather than
// silently dropping a late panic value (L-073).
func (cfg *timeoutOptions) lateLogger() *slog.Logger {
	if cfg.logger == nil {
		return slog.Default()
	}
	return cfg.logger
}

// drainLateHandler consumes the abandoned handler-goroutine result so
// a late panic still reaches the operator via the configured logger.
// The done channel is buffered with capacity 1, so the deferred sender
// in the handler goroutine never blocks even after this drain runs.
func drainLateHandler(done <-chan handlerResult, logger *slog.Logger) {
	result := <-done
	if result.panicValue != nil {
		logger.Error("timeout: handler panicked after request returned",
			slog.String("panic", redact.PanicValue(result.panicValue)),
		)
	}
}

func isWebSocketUpgrade(r *http.Request) bool {
	upgrade := r.Header.Values("Upgrade")
	if len(upgrade) != 1 || !strings.EqualFold(strings.TrimSpace(upgrade[0]), "websocket") {
		return false
	}
	for _, value := range r.Header.Values("Connection") {
		for token := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}
