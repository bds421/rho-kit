// Package circuitbreaker provides HTTP middleware that wraps
// inbound request handling in a [circuitbreaker.CircuitBreaker].
//
// The default trip rule treats any response with status >= 500 (or
// a handler panic) as a failure; everything else is a success. When
// the breaker is open the middleware short-circuits the request
// with 503 Service Unavailable plus a Retry-After header — the
// wrapped handler is never invoked.
//
// # When to use
//
// Use this on inbound HTTP routes whose work fans out to a
// dependency that can itself fail catastrophically (database, LLM
// provider, payment gateway). Tripping the breaker sheds load
// from the failing dependency and lets the caller fail fast
// instead of paying the request-timeout cost for every request.
//
// For outbound HTTP calls use the resilient client built by
// httpx.NewResilientClient — it wires the same breaker into the
// http.RoundTripper chain.
//
// asvs: V11.1.1
package circuitbreaker

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/bds421/rho-kit/resilience/v2/circuitbreaker"
)

// Header set on the 503 response written when the circuit is open.
// Mirrors httpx/middleware/budget header naming.
const (
	HeaderRetry = "Retry-After"
	HeaderState = "X-Circuit-State"
)

// errFailedStatus is the sentinel returned to the breaker so a
// non-error handler invocation that produced a tripping status
// (default: 5xx) is still counted as a failure. It is never
// surfaced to the caller — the response body is already written
// by the handler at that point.
var errFailedStatus = errors.New("middleware/circuitbreaker: response indicates failure")

// BreakerFunc selects the breaker for a given request. Returning
// nil short-circuits the breaker logic for that request — the
// handler runs unwrapped.
//
// Use this to install per-tenant or per-route breakers. The
// returned breaker must be safe for concurrent use (the kit's
// CircuitBreaker is).
type BreakerFunc func(*http.Request) *circuitbreaker.CircuitBreaker

// ShouldTripFunc decides whether a completed handler invocation
// counted as a failure. `panicked` is true when the wrapped
// handler panicked (the panic is re-raised after the breaker
// records the failure, so an upstream recover middleware still
// writes the 500).
type ShouldTripFunc func(status int, panicked bool) bool

// Option configures the [Middleware].
type Option func(*config)

type config struct {
	breakerFor    BreakerFunc
	shouldTrip    ShouldTripFunc
	onOpenRespond func(w http.ResponseWriter, r *http.Request, retryAfter time.Duration)
	retryAfter    time.Duration
	logger        *slog.Logger
}

// WithBreaker installs a single breaker shared by every request.
//
// Panics if b is nil — pass the option directly only when you
// already constructed a breaker; for "no breaker on this route"
// just don't install the middleware.
func WithBreaker(b *circuitbreaker.CircuitBreaker) Option {
	if b == nil {
		panic("middleware/circuitbreaker: WithBreaker requires a non-nil breaker")
	}
	return WithBreakerFor(func(*http.Request) *circuitbreaker.CircuitBreaker { return b })
}

// WithBreakerFor selects the breaker per request. Use this to
// scope breakers per tenant, per upstream, or per route.
//
// Panics if fn is nil.
func WithBreakerFor(fn BreakerFunc) Option {
	if fn == nil {
		panic("middleware/circuitbreaker: WithBreakerFor requires a non-nil function")
	}
	return func(c *config) { c.breakerFor = fn }
}

// WithShouldTrip overrides the default trip predicate. The
// default trips on status >= 500 OR a handler panic.
//
// Panics if fn is nil.
func WithShouldTrip(fn ShouldTripFunc) Option {
	if fn == nil {
		panic("middleware/circuitbreaker: WithShouldTrip requires a non-nil predicate")
	}
	return func(c *config) { c.shouldTrip = fn }
}

// WithRetryAfter sets the Retry-After header value sent on the
// 503 response when the circuit is open. Default: 30s, matching
// httpx.WithCBResetTimeout's default. Pair this with the cooldown
// the upstream breaker was configured with.
//
// Panics if d <= 0.
func WithRetryAfter(d time.Duration) Option {
	if d <= 0 {
		panic("middleware/circuitbreaker: WithRetryAfter requires a positive duration")
	}
	return func(c *config) { c.retryAfter = d }
}

// WithOnOpenRespond overrides the response written when the
// breaker is open. The default writes 503 with Retry-After plus
// a small JSON body identifying the circuit-open condition; an
// override can swap in a problem+json body or redirect to a
// status page.
//
// Panics if fn is nil.
func WithOnOpenRespond(fn func(w http.ResponseWriter, r *http.Request, retryAfter time.Duration)) Option {
	if fn == nil {
		panic("middleware/circuitbreaker: WithOnOpenRespond requires a non-nil function")
	}
	return func(c *config) { c.onOpenRespond = fn }
}

// WithLogger installs a logger that records the open-state
// rejection at WARN. Without this option, rejections fall back
// to [slog.Default] so the event is never silently dropped.
//
// Panics if logger is nil.
func WithLogger(logger *slog.Logger) Option {
	if logger == nil {
		panic("middleware/circuitbreaker: WithLogger requires a non-nil logger")
	}
	return func(c *config) { c.logger = logger }
}

// Middleware returns the HTTP middleware. Either [WithBreaker]
// or [WithBreakerFor] MUST be supplied — otherwise the
// constructor panics rather than silently producing a no-op
// middleware that fails open.
func Middleware(opts ...Option) func(http.Handler) http.Handler {
	cfg := config{
		shouldTrip: defaultShouldTrip,
		retryAfter: 30 * time.Second,
	}
	for _, o := range opts {
		if o == nil {
			panic("middleware/circuitbreaker: Middleware option must not be nil")
		}
		o(&cfg)
	}
	if cfg.breakerFor == nil {
		panic("middleware/circuitbreaker: WithBreaker or WithBreakerFor is required")
	}
	if cfg.onOpenRespond == nil {
		cfg.onOpenRespond = defaultOnOpenRespond
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cb := cfg.breakerFor(r)
			if cb == nil {
				// Caller decided "no breaker for this request" —
				// bypass cleanly.
				next.ServeHTTP(w, r)
				return
			}

			rec := newStatusRecorder(w)

			// Probe the breaker BEFORE calling next so an open
			// state never reaches the handler. The breaker layer
			// already recovers panics, counts them as failures,
			// and re-raises — so an upstream recover middleware
			// writes the 500 while the breaker still sees the
			// panic as a failure.
			err := cb.Execute(func() error {
				next.ServeHTTP(rec, r)
				if cfg.shouldTrip(rec.status, false) {
					return errFailedStatus
				}
				return nil
			})

			if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
				logger := cfg.logger
				if logger == nil {
					logger = slog.Default()
				}
				logger.WarnContext(r.Context(), "middleware/circuitbreaker: rejecting request",
					"method", r.Method,
					"path", r.URL.Path,
					"retry_after_seconds", int64(math.Ceil(cfg.retryAfter.Seconds())),
				)
				cfg.onOpenRespond(w, r, cfg.retryAfter)
				return
			}
			// Other return values (nil, errFailedStatus) are
			// already reflected in the response the handler
			// wrote — nothing more to do.
		})
	}
}

func defaultShouldTrip(status int, panicked bool) bool {
	if panicked {
		return true
	}
	return status >= 500
}

func defaultOnOpenRespond(w http.ResponseWriter, _ *http.Request, retryAfter time.Duration) {
	secs := int64(math.Ceil(retryAfter.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set(HeaderState, string(circuitbreaker.StateOpen))
	w.Header().Set(HeaderRetry, strconv.FormatInt(secs, 10))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = fmt.Fprintf(w, `{"error":"circuit breaker is open","code":"CIRCUIT_OPEN","retry_after_seconds":%d}`, secs)
}

// statusRecorder is a minimal status-capturing wrapper kept local
// to the package so the middleware does not pick up the larger
// middleware.ResponseRecorder dependency (which lives in the
// parent package and would create an import cycle).
type statusRecorder struct {
	http.ResponseWriter
	status     int
	wroteHead  bool
}

func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	return &statusRecorder{ResponseWriter: w, status: http.StatusOK}
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.wroteHead {
		return
	}
	if code < 100 || code > 999 {
		code = http.StatusInternalServerError
	}
	s.status = code
	s.wroteHead = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHead {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
