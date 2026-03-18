package circuitbreaker

import (
	"errors"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/bds421/rho-kit/core/apperror"
)

// ErrCircuitOpen is returned when the circuit breaker is open and the call is
// rejected without attempting the underlying operation.
var ErrCircuitOpen = errors.New("circuit breaker is open")

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

// WithOnStateChange registers a callback invoked when the breaker transitions
// between states. The name is empty unless WithName is used.
func WithOnStateChange(fn func(name string, from, to State)) Option {
	if fn == nil {
		return func(*gobreaker.Settings) {}
	}
	return func(s *gobreaker.Settings) {
		s.OnStateChange = func(name string, from gobreaker.State, to gobreaker.State) {
			fn(name, mapState(from), mapState(to))
		}
	}
}

// CircuitBreaker wraps a gobreaker instance with defaults.
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
	}
	for _, opt := range opts {
		opt(&settings)
	}

	return &CircuitBreaker{
		cb: gobreaker.NewCircuitBreaker[any](settings),
	}
}

// Execute runs fn through the circuit breaker. If the circuit is open,
// it returns ErrCircuitOpen without calling fn.
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
