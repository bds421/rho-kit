package circuitbreaker

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/redact"
)

// ErrCircuitOpen is returned when the circuit breaker is open and the call is
// rejected without attempting the underlying operation.
var ErrCircuitOpen = errors.New("circuitbreaker: circuit is open")

// State represents the circuit breaker state.
type State string

const (
	StateClosed   State = "closed"
	StateOpen     State = "open"
	StateHalfOpen State = "half-open"
	StateUnknown  State = "unknown"
)

// Option customizes the circuit breaker settings.
type Option func(*gobreaker.Settings)

// WithIsSuccessful overrides the success predicate used to decide whether
// an error should count as a failure. Returning true treats the call as success.
func WithIsSuccessful(fn func(err error) bool) Option {
	return func(s *gobreaker.Settings) { s.IsSuccessful = fn }
}

// WithPermanentSuccess treats apperror.Permanent as successful calls, preventing
// permanent errors from opening the circuit.
func WithPermanentSuccess() Option {
	return WithIsSuccessful(func(err error) bool {
		return err == nil || apperror.IsPermanent(err)
	})
}

// defaultIsSuccessful is the package-level default for the success
// predicate. It ignores caller-driven cancellation (context.Canceled)
// because a client aborting an in-flight request is not evidence the
// downstream is unhealthy.
func defaultIsSuccessful(err error) bool {
	if err == nil {
		return true
	}
	return errors.Is(err, context.Canceled)
}

// WithName sets the breaker name (useful for metrics).
func WithName(name string) Option {
	return func(s *gobreaker.Settings) { s.Name = name }
}

// WithInterval sets the rolling window for clearing counts.
func WithInterval(d time.Duration) Option {
	return func(s *gobreaker.Settings) { s.Interval = d }
}

// WithMaxRequests sets the number of allowed requests in half-open state.
func WithMaxRequests(n uint32) Option {
	return func(s *gobreaker.Settings) { s.MaxRequests = n }
}

// WithReadyToTrip replaces the trip predicate. The default predicate
// trips on consecutive failures; use this to install an error-rate
// window or any other custom signal.
//
// Example: trip when more than 50% of at least 20 requests in the
// current Interval failed.
//
//	WithReadyToTrip(func(c circuitbreaker.Counts) bool {
//	    if c.Requests < 20 { return false }
//	    return float64(c.TotalFailures)/float64(c.Requests) > 0.5
//	})
func WithReadyToTrip(fn func(Counts) bool) Option {
	if fn == nil {
		return func(*gobreaker.Settings) {}
	}
	return func(s *gobreaker.Settings) {
		s.ReadyToTrip = func(c gobreaker.Counts) bool {
			return fn(Counts{
				Requests:             c.Requests,
				TotalSuccesses:       c.TotalSuccesses,
				TotalFailures:        c.TotalFailures,
				ConsecutiveSuccesses: c.ConsecutiveSuccesses,
				ConsecutiveFailures:  c.ConsecutiveFailures,
			})
		}
	}
}

// WithErrorRateThreshold installs a [WithReadyToTrip] predicate that
// trips when the failure ratio over the current Interval exceeds rate
// AND the request count is at least minRequests. Pair with
// [WithInterval] to define the rolling window.
//
// rate must be in (0, 1]; minRequests prevents tripping on a single
// failed request out of one.
//
// FR-088 [LOW]: panics on rate outside (0, 1] or minRequests == 0.
// rate > 1 made the breaker un-trippable; minRequests == 0 made it
// trip on the very first failure.
func WithErrorRateThreshold(rate float64, minRequests uint32) Option {
	if rate <= 0 || rate > 1 {
		panic("circuitbreaker: WithErrorRateThreshold requires 0 < rate <= 1")
	}
	if minRequests == 0 {
		panic("circuitbreaker: WithErrorRateThreshold requires minRequests >= 1")
	}
	return WithReadyToTrip(func(c Counts) bool {
		if c.Requests < minRequests {
			return false
		}
		return float64(c.TotalFailures)/float64(c.Requests) > rate
	})
}

// Counts is the kit-stable mirror of gobreaker.Counts. Re-exposed so
// callers don't need to import gobreaker for [WithReadyToTrip].
type Counts struct {
	Requests             uint32
	TotalSuccesses       uint32
	TotalFailures        uint32
	ConsecutiveSuccesses uint32
	ConsecutiveFailures  uint32
}

// WithOnStateChange registers a callback invoked when the breaker transitions
// between states. The name is empty unless WithName is used.
func WithOnStateChange(fn func(name string, from, to State)) Option {
	if fn == nil {
		return func(*gobreaker.Settings) {}
	}
	return func(s *gobreaker.Settings) {
		s.OnStateChange = func(name string, from gobreaker.State, to gobreaker.State) {
			callOnStateChange(fn, name, mapState(from), mapState(to))
		}
	}
}

func callOnStateChange(fn func(name string, from, to State), name string, from, to State) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Default().Error("circuitbreaker: OnStateChange callback panicked",
				"name", name,
				"from", from,
				"to", to,
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
		}
	}()
	fn(name, from, to)
}

// CircuitBreaker wraps a gobreaker instance with defaults.
// Safe for concurrent use — the embedded gobreaker.CircuitBreaker
// serialises state transitions and per-call counter updates internally.
type CircuitBreaker struct {
	cb *gobreaker.CircuitBreaker[any]
}

// NewCircuitBreaker creates a circuit breaker that opens after threshold
// consecutive failures and stays open for cooldownPeriod before probing.
func NewCircuitBreaker(threshold int, cooldownPeriod time.Duration, opts ...Option) *CircuitBreaker {
	if threshold < 1 {
		threshold = 1
	}

	settings := gobreaker.Settings{
		MaxRequests: 1,
		Timeout:     cooldownPeriod,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= uint32(threshold)
		},
		// Default success predicate: treat caller-driven cancellation
		// (context.Canceled) as success so a flood of cancelled requests
		// from a flaky client cannot trip the circuit and harm
		// unrelated callers. Server-side timeouts (DeadlineExceeded)
		// remain failures.
		IsSuccessful: defaultIsSuccessful,
	}
	for _, opt := range opts {
		if opt == nil {
			panic("circuitbreaker: NewCircuitBreaker option must not be nil")
		}
		opt(&settings)
	}

	return &CircuitBreaker{
		cb: gobreaker.NewCircuitBreaker[any](settings),
	}
}

// Execute runs fn through the circuit breaker. If the circuit is open,
// it returns ErrCircuitOpen without calling fn.
//
// A nil receiver is treated as a no-op: fn is invoked directly with no
// breaker semantics. This makes it safe to compose with optional
// dependencies (e.g. wrapping an outbound call when a breaker may or may
// not have been wired up).
func (cb *CircuitBreaker) Execute(fn func() error) error {
	if cb == nil {
		return fn()
	}
	_, err := cb.cb.Execute(func() (any, error) {
		return nil, fn()
	})
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		return ErrCircuitOpen
	}
	return err
}

// ExecuteCtx runs fn through the circuit breaker, observing ctx for early
// cancellation. If ctx is already cancelled when ExecuteCtx is called, fn
// is not invoked and ctx.Err() is returned. If the circuit is open,
// ErrCircuitOpen is returned without calling fn.
//
// fn receives ctx so it can stop work on cancellation. The breaker's
// failure-counting predicate (see [WithIsSuccessful]) decides whether
// ctx.Err() returned by fn counts as a failure — by default ctx.Canceled
// and ctx.DeadlineExceeded count as failures. Use [WithIsSuccessful] to
// exclude them when callers may cancel for reasons unrelated to the
// downstream's health (e.g. shedding load on a slow client).
//
// A nil receiver is treated as a no-op: fn is invoked directly with no
// breaker semantics, after the ctx pre-check.
func (cb *CircuitBreaker) ExecuteCtx(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cb == nil {
		return fn(ctx)
	}
	_, err := cb.cb.Execute(func() (any, error) {
		return nil, fn(ctx)
	})
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		return ErrCircuitOpen
	}
	return err
}

// State returns the current circuit state as a string (for observability).
func (cb *CircuitBreaker) State() string {
	if cb == nil {
		return "unknown"
	}
	return cb.cb.State().String()
}

// StateValue returns the current circuit state as a typed value.
func (cb *CircuitBreaker) StateValue() State {
	if cb == nil {
		return StateUnknown
	}
	return mapState(cb.cb.State())
}

func mapState(state gobreaker.State) State {
	switch state {
	case gobreaker.StateClosed:
		return StateClosed
	case gobreaker.StateOpen:
		return StateOpen
	case gobreaker.StateHalfOpen:
		return StateHalfOpen
	default:
		return StateUnknown
	}
}
