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
	"github.com/bds421/rho-kit/observability/v2/promutil"
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

// breakerConfig carries everything an Option might want to set:
// the gobreaker.Settings the breaker is constructed with, plus the
// kit-side metrics handle (if WithMetrics is used).
type breakerConfig struct {
	settings *gobreaker.Settings
	metrics  *Metrics
}

// Option customizes the circuit breaker settings.
type Option func(*breakerConfig)

// WithIsSuccessful overrides the success predicate used to decide whether
// an error should count as a failure. Returning true treats the call as success.
func WithIsSuccessful(fn func(err error) bool) Option {
	return func(c *breakerConfig) { c.settings.IsSuccessful = fn }
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

// WithName sets the breaker name. The name flows into the
// `kit.breaker.name` OTel attribute on every span the breaker
// emits, so it must be a bounded, developer-defined identifier —
// never a tenant id, customer id, or any caller-controlled value
// that could inflate trace-backend cardinality.
//
// Panics on a value that fails
// [promutil.ValidateStaticLabelValue] (empty, too long, or
// containing whitespace/control runes) so misuse surfaces at
// startup rather than as a silent observability budget burn.
// Matches the validation siblings [bulkhead.New] and
// [ratelimit.WithLimiterName] apply to their name fields.
func WithName(name string) Option {
	if err := promutil.ValidateStaticLabelValue("name", name); err != nil {
		panic("circuitbreaker: " + err.Error())
	}
	return func(c *breakerConfig) { c.settings.Name = name }
}

// WithInterval sets the rolling window for clearing counts.
func WithInterval(d time.Duration) Option {
	return func(c *breakerConfig) { c.settings.Interval = d }
}

// WithMaxRequests sets the number of allowed requests in half-open state.
func WithMaxRequests(n uint32) Option {
	return func(c *breakerConfig) { c.settings.MaxRequests = n }
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
		return func(*breakerConfig) {}
	}
	return func(c *breakerConfig) {
		c.settings.ReadyToTrip = func(gc gobreaker.Counts) bool {
			return fn(Counts{
				Requests:             gc.Requests,
				TotalSuccesses:       gc.TotalSuccesses,
				TotalFailures:        gc.TotalFailures,
				ConsecutiveSuccesses: gc.ConsecutiveSuccesses,
				ConsecutiveFailures:  gc.ConsecutiveFailures,
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
		return func(*breakerConfig) {}
	}
	return func(c *breakerConfig) {
		c.settings.OnStateChange = func(name string, from gobreaker.State, to gobreaker.State) {
			callOnStateChange(fn, name, mapState(from), mapState(to))
		}
	}
}

// WithMetrics wires a constructed [*Metrics] so the breaker records
// state transitions and per-call outcomes without the consumer having
// to hand-wire counters through WithOnStateChange. Pass nil to
// disable metrics (the default).
//
// The kit's wave-167 OTel tracing remains unchanged; metrics are
// additive. When both WithMetrics and WithOnStateChange are set,
// the metric record runs FIRST and the caller's callback runs after
// — so a caller's panic in OnStateChange does not prevent the
// metric from being recorded.
func WithMetrics(m *Metrics) Option {
	return func(c *breakerConfig) { c.metrics = m }
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
	cb      *gobreaker.CircuitBreaker[any]
	metrics *Metrics
	name    string
	// isSuccessful mirrors the predicate the embedded gobreaker uses to
	// decide whether a call counts as a success or a failure. Captured
	// here so per-call metric outcome labels match the breaker's own
	// accounting under custom predicates (WithIsSuccessful /
	// WithPermanentSuccess), not just the package default.
	isSuccessful func(err error) bool
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
	cfg := &breakerConfig{settings: &settings}
	for _, opt := range opts {
		if opt == nil {
			panic("circuitbreaker: NewCircuitBreaker option must not be nil")
		}
		opt(cfg)
	}

	// When metrics are wired, install a state-change hook that records
	// the transition counter BEFORE invoking the caller's OnStateChange
	// (if any). Recording first guarantees a panicking caller callback
	// cannot suppress the metric.
	if cfg.metrics != nil {
		userOnStateChange := settings.OnStateChange
		settings.OnStateChange = func(name string, from, to gobreaker.State) {
			cfg.metrics.recordStateChange(name, mapState(from), mapState(to))
			if userOnStateChange != nil {
				userOnStateChange(name, from, to)
			}
		}
	}

	return &CircuitBreaker{
		cb:           gobreaker.NewCircuitBreaker[any](settings),
		metrics:      cfg.metrics,
		name:         settings.Name,
		isSuccessful: settings.IsSuccessful,
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
	_, span := cb.startSpan(context.Background(), "breaker.Execute")
	defer span.End()
	_, err := cb.cb.Execute(func() (any, error) {
		return nil, fn()
	})
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		err = ErrCircuitOpen
	}
	recordResult(span, err)
	cb.metrics.recordCall(cb.name, callOutcome(err, cb.isSuccessful))
	return err
}

// ExecuteCtx runs fn through the circuit breaker, observing ctx for early
// cancellation. If ctx is already cancelled when ExecuteCtx is called, fn
// is not invoked and ctx.Err() is returned. If the circuit is open,
// ErrCircuitOpen is returned without calling fn.
//
// fn receives ctx so it can stop work on cancellation. The breaker's
// failure-counting predicate (see [WithIsSuccessful]) decides whether
// ctx.Err() returned by fn counts as a failure — by default
// context.Canceled is treated as success (a caller aborting an in-flight
// request is not evidence the downstream is unhealthy) while
// context.DeadlineExceeded counts as a failure. Use [WithIsSuccessful]
// to change this when callers may cancel for reasons unrelated to the
// downstream's health (e.g. shedding load on a slow client).
//
// A nil ctx is rejected with an error rather than panicking, matching
// the sibling [bulkhead.Bulkhead.ExecuteCtx] contract.
//
// A nil receiver is treated as a no-op: fn is invoked directly with no
// breaker semantics, after the ctx pre-check.
func (cb *CircuitBreaker) ExecuteCtx(ctx context.Context, fn func(ctx context.Context) error) error {
	if ctx == nil {
		return errors.New("circuitbreaker: ExecuteCtx requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if cb == nil {
		return fn(ctx)
	}
	_, span := cb.startSpan(ctx, "breaker.ExecuteCtx")
	defer span.End()
	_, err := cb.cb.Execute(func() (any, error) {
		return nil, fn(ctx)
	})
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		err = ErrCircuitOpen
	}
	recordResult(span, err)
	cb.metrics.recordCall(cb.name, callOutcome(err, cb.isSuccessful))
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
