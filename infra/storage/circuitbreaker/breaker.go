package circuitbreaker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"time"

	"github.com/bds421/rho-kit/infra/v2/storage"
	kitcb "github.com/bds421/rho-kit/resilience/v2/circuitbreaker"
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
// Panics if n < 1 — there is no sensible fallback for a zero/negative
// threshold, and silently ignoring masks misconfiguration.
func WithThreshold(n int) Option {
	if n < 1 {
		panic("storage/circuitbreaker: WithThreshold threshold must be >= 1")
	}
	return func(c *Config) { c.Threshold = n }
}

// WithResetTimeout sets the open-state duration before allowing a probe. Default is 30s.
// Panics if d is zero or negative — a zero/negative timeout would let the
// breaker probe immediately and never protect the dependency.
func WithResetTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("storage/circuitbreaker: WithResetTimeout reset timeout must be positive")
	}
	return func(c *Config) { c.ResetTimeout = d }
}

// WithShouldTrip sets a custom predicate for trippable errors.
// A nil fn is a no-op that preserves the default predicate.
func WithShouldTrip(fn func(error) bool) Option {
	return func(c *Config) {
		if fn == nil {
			return
		}
		c.ShouldTrip = fn
	}
}

// WithOnStateChange sets a callback for state transitions.
func WithOnStateChange(fn func(from, to State)) Option {
	return func(c *Config) { c.OnStateChange = fn }
}

// Stater exposes the breaker's current state in addition to [storage.Storage].
// New returns a value that always satisfies Stater.
type Stater interface {
	storage.Storage
	State() State
}

// CircuitBreaker wraps a [storage.Storage] with circuit breaker logic
// backed by [kitcb.CircuitBreaker] from kit/circuitbreaker (sony/gobreaker).
//
// Optional capabilities (Lister, Copier, PresignedStore, PublicURLer) are
// forwarded through the breaker when the underlying backend supports them,
// so an open circuit also blocks optional operations. The [New] factory
// returns a [Stater] whose dynamic type implements the same subset of
// optional interfaces as the underlying backend; callers should detect
// support via [storage.AsLister] etc.
type CircuitBreaker struct {
	backend storage.Storage
	cb      *kitcb.CircuitBreaker
}

// Unwrap returns the underlying storage backend.
func (cb *CircuitBreaker) Unwrap() storage.Storage { return cb.backend }

// OpaqueStorageDecorator marks CircuitBreaker as an [storage.OpaqueDecorator]
// so capability discovery via storage.As* cannot bypass the breaker by
// reaching the underlying backend's optional interfaces directly. An open
// circuit must block optional ops, not just the four core ones.
func (cb *CircuitBreaker) OpaqueStorageDecorator() {}

// New wraps backend with a circuit breaker.
//
// The returned value's dynamic type forwards the optional interfaces the
// underlying chain exposes (via [storage.AsLister] etc, which honor opaque
// decorators). Optional operations go through the breaker so an open
// circuit blocks them too.
//
// Panics if backend is nil. A nil backend would otherwise only surface as a
// confusing nil-pointer panic on the first storage operation.
func New(backend storage.Storage, opts ...Option) Stater {
	if backend == nil {
		panic("storage/circuitbreaker: New backend must not be nil")
	}
	cfg := Config{
		Threshold:    5,
		ResetTimeout: 30 * time.Second,
		ShouldTrip: func(err error) bool {
			return err != nil &&
				!errors.Is(err, storage.ErrObjectNotFound) &&
				!errors.Is(err, storage.ErrValidation)
		},
	}
	for _, o := range opts {
		if o == nil {
			panic("storage/circuitbreaker: New option must not be nil")
		}
		o(&cfg)
	}
	if cfg.ShouldTrip == nil {
		panic("storage/circuitbreaker: ShouldTrip must not be nil")
	}
	if cfg.Threshold < 1 {
		panic("storage/circuitbreaker: Threshold must be >= 1")
	}
	if cfg.ResetTimeout <= 0 {
		panic("storage/circuitbreaker: ResetTimeout must be positive")
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

	cb := &CircuitBreaker{
		backend: backend,
		cb:      kitcb.NewCircuitBreaker(cfg.Threshold, cfg.ResetTimeout, kitOpts...),
	}

	_, hasLister := storage.AsLister(backend)
	_, hasCopier := storage.AsCopier(backend)
	_, hasPresigned := storage.AsPresigned(backend)
	_, hasURLer := storage.AsPublicURLer(backend)

	return composeBreaker(cb, hasLister, hasCopier, hasPresigned, hasURLer)
}

// State returns the current circuit state.
func (cb *CircuitBreaker) State() State {
	return State(cb.cb.StateValue())
}

// Put delegates to the backend if the circuit allows.
func (cb *CircuitBreaker) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	return cb.cb.Execute(func() error {
		return cb.backend.Put(ctx, key, r, storage.CloneObjectMeta(meta))
	})
}

// Get delegates to the backend if the circuit allows.
func (cb *CircuitBreaker) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}
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
	if err := storage.ValidateKey(key); err != nil {
		return err
	}
	return cb.cb.Execute(func() error {
		return cb.backend.Delete(ctx, key)
	})
}

// Close delegates to the wrapped backend so a circuit-breaker-wrapped
// Storage forwards [storage.Close] correctly. Uses [storage.Close] so
// a backend without Close (io.Closer) is a no-op.
func (cb *CircuitBreaker) Close() error {
	if cb == nil || cb.backend == nil {
		return nil
	}
	return storage.Close(cb.backend)
}

// Exists delegates to the backend if the circuit allows.
func (cb *CircuitBreaker) Exists(ctx context.Context, key string) (bool, error) {
	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}
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

// --- Optional capability forwarding helpers ---
//
// Each helper resolves the capability through the underlying chain (via
// storage.As*, which honors opaque-decorator markers) and runs the call
// inside cb.Execute so the circuit state gates optional ops too.

func (cb *CircuitBreaker) listImpl(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	if err := storage.ValidatePrefix(prefix); err != nil {
		return cbErrorSeq(err)
	}
	if err := storage.ValidateListOptions(opts); err != nil {
		return cbErrorSeq(err)
	}
	lister, ok := storage.AsLister(cb.backend)
	if !ok {
		return func(yield func(storage.ObjectInfo, error) bool) {
			yield(storage.ObjectInfo{}, fmt.Errorf("storage/circuitbreaker: underlying backend does not implement storage.Lister"))
		}
	}
	// Gate the initial dispatch on the breaker. Mid-iteration breaker
	// state is not re-checked because a partial stream cannot be
	// resumed; the caller observing an error during iteration will
	// surface that to its own retry/backoff.
	return func(yield func(storage.ObjectInfo, error) bool) {
		var seq iter.Seq2[storage.ObjectInfo, error]
		execErr := cb.cb.Execute(func() error {
			seq = lister.List(ctx, prefix, opts)
			return nil
		})
		if execErr != nil {
			yield(storage.ObjectInfo{}, execErr)
			return
		}
		for info, err := range seq {
			if !yield(info, err) {
				return
			}
		}
	}
}

func (cb *CircuitBreaker) copyImpl(ctx context.Context, srcKey, dstKey string) error {
	if err := storage.ValidateKey(srcKey); err != nil {
		return err
	}
	if err := storage.ValidateKey(dstKey); err != nil {
		return err
	}
	copier, ok := storage.AsCopier(cb.backend)
	if !ok {
		return fmt.Errorf("storage/circuitbreaker: underlying backend does not implement storage.Copier")
	}
	return cb.cb.Execute(func() error {
		return copier.Copy(ctx, srcKey, dstKey)
	})
}

func (cb *CircuitBreaker) presignGetImpl(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}
	ps, ok := storage.AsPresigned(cb.backend)
	if !ok {
		return "", fmt.Errorf("storage/circuitbreaker: underlying backend does not implement storage.PresignedStore")
	}
	var url string
	err := cb.cb.Execute(func() error {
		var perr error
		url, perr = ps.PresignGetURL(ctx, key, ttl)
		return perr
	})
	return url, err
}

func (cb *CircuitBreaker) presignPutImpl(ctx context.Context, key string, ttl time.Duration, meta storage.ObjectMeta) (string, error) {
	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}
	if err := storage.ValidateObjectMeta(meta); err != nil {
		return "", err
	}
	ps, ok := storage.AsPresigned(cb.backend)
	if !ok {
		return "", fmt.Errorf("storage/circuitbreaker: underlying backend does not implement storage.PresignedStore")
	}
	var url string
	err := cb.cb.Execute(func() error {
		var perr error
		url, perr = ps.PresignPutURL(ctx, key, ttl, storage.CloneObjectMeta(meta))
		return perr
	})
	return url, err
}

func (cb *CircuitBreaker) urlImpl(ctx context.Context, key string) (string, error) {
	if err := storage.ValidateKey(key); err != nil {
		return "", err
	}
	urler, ok := storage.AsPublicURLer(cb.backend)
	if !ok {
		return "", fmt.Errorf("storage/circuitbreaker: underlying backend does not implement storage.PublicURLer")
	}
	var url string
	err := cb.cb.Execute(func() error {
		var uerr error
		url, uerr = urler.URL(ctx, key)
		return uerr
	})
	return url, err
}

func cbErrorSeq(err error) iter.Seq2[storage.ObjectInfo, error] {
	return func(yield func(storage.ObjectInfo, error) bool) {
		yield(storage.ObjectInfo{}, err)
	}
}

// Compile-time interface compliance check.
var (
	_ storage.Storage         = (*CircuitBreaker)(nil)
	_ storage.OpaqueDecorator = (*CircuitBreaker)(nil)
)
