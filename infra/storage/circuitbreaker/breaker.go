package circuitbreaker

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/bds421/rho-kit/infra/storage"
	kitcb "github.com/bds421/rho-kit/resilience/circuitbreaker"
)

// ErrCircuitOpen is returned when the circuit breaker is in the open state.
// This is the same sentinel from kit/circuitbreaker, re-exported for convenience.
var ErrCircuitOpen = kitcb.ErrCircuitOpen

// State represents the circuit breaker state.
type State string

const (
	StateClosed   State = State(kitcb.StateClosed)
	StateOpen     State = State(kitcb.StateOpen)
	StateHalfOpen State = State(kitcb.StateHalfOpen)
)

// String returns the state name.
func (s State) String() string { return string(s) }

// Compile-time interface compliance check.
var _ storage.Storage = (*CircuitBreaker)(nil)

// Config controls circuit breaker behavior.
type Config struct {
	// Threshold is the number of consecutive failures to trip the breaker.
	Threshold int

	// ResetTimeout is how long the breaker stays open before allowing a probe.
	ResetTimeout time.Duration

	// ShouldTrip decides whether an error counts toward the failure threshold.
	// Defaults to all non-nil errors.
	ShouldTrip func(err error) bool

	// OnStateChange is called when the circuit transitions between states.
	OnStateChange func(from, to State)
}

// Option configures the circuit breaker.
type Option func(*Config)

// WithThreshold sets the consecutive failure count to trip the breaker. Default is 5.
func WithThreshold(n int) Option {
	return func(c *Config) {
		if n > 0 {
			c.Threshold = n
		}
	}
}

// WithResetTimeout sets the open-state duration before allowing a probe. Default is 30s.
func WithResetTimeout(d time.Duration) Option {
	return func(c *Config) { c.ResetTimeout = d }
}

// WithShouldTrip sets a custom predicate for trippable errors.
func WithShouldTrip(fn func(error) bool) Option {
	return func(c *Config) { c.ShouldTrip = fn }
}

// WithOnStateChange sets a callback for state transitions.
func WithOnStateChange(fn func(from, to State)) Option {
	return func(c *Config) { c.OnStateChange = fn }
}

// CircuitBreaker wraps a [storage.Storage] with circuit breaker logic
// backed by [kitcb.CircuitBreaker] from kit/circuitbreaker (sony/gobreaker).
type CircuitBreaker struct {
	backend storage.Storage
	cb      *kitcb.CircuitBreaker
}

// Unwrap returns the underlying storage backend.
func (cb *CircuitBreaker) Unwrap() storage.Storage { return cb.backend }

// New wraps backend with a circuit breaker.
func New(backend storage.Storage, opts ...Option) *CircuitBreaker {
	cfg := Config{
		Threshold:    5,
		ResetTimeout: 30 * time.Second,
		ShouldTrip: func(err error) bool {
			return err != nil && !errors.Is(err, storage.ErrObjectNotFound)
		},
	}
	for _, o := range opts {
		o(&cfg)
	}

	// Map storage ShouldTrip (failure predicate) to kit's IsSuccessful (success predicate).
	kitOpts := []kitcb.Option{
		kitcb.WithIsSuccessful(func(err error) bool {
			return err == nil || !cfg.ShouldTrip(err)
		}),
	}
	if cfg.OnStateChange != nil {
		kitOpts = append(kitOpts, kitcb.WithOnStateChange(func(_ string, from, to kitcb.State) {
			cfg.OnStateChange(State(from), State(to))
		}))
	}

	return &CircuitBreaker{
		backend: backend,
		cb:      kitcb.NewCircuitBreaker(cfg.Threshold, cfg.ResetTimeout, kitOpts...),
	}
}

// State returns the current circuit state.
func (cb *CircuitBreaker) State() State {
	return State(cb.cb.StateValue())
}

// Put delegates to the backend if the circuit allows.
func (cb *CircuitBreaker) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	return cb.cb.Execute(func() error {
		return cb.backend.Put(ctx, key, r, meta)
	})
}

// Get delegates to the backend if the circuit allows.
func (cb *CircuitBreaker) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	var (
		rc   io.ReadCloser
		meta storage.ObjectMeta
	)
	err := cb.cb.Execute(func() error {
		var getErr error
		rc, meta, getErr = cb.backend.Get(ctx, key)
		return getErr
	})
	if err != nil {
		// Close the reader if the backend returned one before the circuit
		// breaker decided to report an error (e.g., state transition).
		if rc != nil {
			_ = rc.Close()
		}
		return nil, storage.ObjectMeta{}, err
	}
	return rc, meta, nil
}

// Delete delegates to the backend if the circuit allows.
func (cb *CircuitBreaker) Delete(ctx context.Context, key string) error {
	return cb.cb.Execute(func() error {
		return cb.backend.Delete(ctx, key)
	})
}

// Exists delegates to the backend if the circuit allows.
func (cb *CircuitBreaker) Exists(ctx context.Context, key string) (bool, error) {
	var ok bool
	err := cb.cb.Execute(func() error {
		var existsErr error
		ok, existsErr = cb.backend.Exists(ctx, key)
		return existsErr
	})
	if err != nil {
		return false, err
	}
	return ok, nil
}

// Note: Optional storage interfaces (Lister, Copier, PresignedStore, PublicURLer)
// are intentionally NOT implemented on CircuitBreaker. When callers use
// storage.AsLister(cb), the Unwrap chain resolves to the underlying backend,
// bypassing circuit breaker protection for List/Copy/Presign/URL operations.
//
// This is a deliberate trade-off to avoid the 2^4 = 16 combinatorial wrapper
// types needed for all interface combinations. The core Storage operations
// (Put, Get, Delete, Exists) are the most likely to encounter transient
// failures that benefit from circuit breaker protection. If circuit-breaking
// List/Copy is needed, use storage.WithHooks instead.
