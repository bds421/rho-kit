package circuitbreaker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"iter"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/infra/v2/storage/membackend"
)

func TestCircuitBreaker_PassesThrough(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := membackend.New()
	cb := New(backend)

	err := cb.Put(ctx, "file.txt", bytes.NewReader([]byte("hello")), storage.ObjectMeta{})
	require.NoError(t, err)

	rc, _, err := cb.Get(ctx, "file.txt")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	data, _ := io.ReadAll(rc)
	assert.Equal(t, []byte("hello"), data)
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	failing := &alwaysFailBackend{err: errors.New("down")}
	cb := New(failing, WithThreshold(3), WithResetTimeout(time.Hour))

	// First 3 calls fail normally.
	for range 3 {
		_, _, err := cb.Get(ctx, "key")
		assert.Error(t, err)
	}

	// 4th call should fail fast with ErrCircuitOpen.
	_, _, err := cb.Get(ctx, "key")
	assert.ErrorIs(t, err, ErrCircuitOpen)
	assert.Equal(t, StateOpen, cb.State())
}

func TestCircuitBreaker_TransitionsToHalfOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	failing := &alwaysFailBackend{err: errors.New("down")}
	cb := New(failing, WithThreshold(1), WithResetTimeout(time.Millisecond))

	// Trip the breaker.
	_, _, _ = cb.Get(ctx, "key")
	assert.Equal(t, StateOpen, cb.State())

	// Wait for reset timeout.
	time.Sleep(5 * time.Millisecond)

	assert.Equal(t, StateHalfOpen, cb.State())
}

func TestCircuitBreaker_ClosesOnSuccessInHalfOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	backend := membackend.New()
	err := backend.Put(ctx, "file.txt", bytes.NewReader([]byte("data")), storage.ObjectMeta{})
	require.NoError(t, err)

	// Create a backend that fails once then succeeds.
	callCount := 0
	switchable := &switchableBackend{
		failUntil: 1,
		err:       errors.New("down"),
		backend:   backend,
		counter:   &callCount,
	}

	cb := New(switchable, WithThreshold(1), WithResetTimeout(time.Millisecond))

	// Trip the breaker.
	_, err = cb.Exists(ctx, "file.txt")
	assert.Error(t, err)
	assert.Equal(t, StateOpen, cb.State())

	// Wait for reset timeout.
	time.Sleep(5 * time.Millisecond)

	// Probe succeeds → closes circuit.
	ok, err := cb.Exists(ctx, "file.txt")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, StateClosed, cb.State())
}

func TestCircuitBreaker_StateChangeCallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var transitions []string
	failing := &alwaysFailBackend{err: errors.New("down")}
	cb := New(failing,
		WithThreshold(1),
		WithOnStateChange(func(from, to State) {
			transitions = append(transitions, from.String()+"→"+to.String())
		}),
	)

	_, _, _ = cb.Get(ctx, "key")

	assert.Contains(t, transitions, "closed→open")
}

func TestCircuitBreaker_StateChangeCallbackPanicDoesNotEscape(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	failing := &alwaysFailBackend{err: errors.New("down")}
	cb := New(failing,
		WithThreshold(1),
		WithOnStateChange(func(State, State) {
			panic("metrics hook exploded")
		}),
	)

	assert.NotPanics(t, func() {
		_, _, err := cb.Get(ctx, "key")
		assert.Error(t, err)
	})
	assert.Equal(t, StateOpen, cb.State())
}

func TestCircuitBreaker_Delete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := membackend.New()
	cb := New(backend)

	err := cb.Delete(ctx, "nonexistent.txt")
	assert.NoError(t, err)
}

func TestCircuitBreaker_New_NilBackendPanics(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "storage/circuitbreaker: New: backend must not be nil", func() {
		_ = New(nil)
	})
}

func TestCircuitBreaker_New_NilOptionPanics(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "storage/circuitbreaker: New: option must not be nil", func() {
		_ = New(membackend.New(), nil)
	})
}

func TestCircuitBreaker_New_NilShouldTripPanics(t *testing.T) {
	t.Parallel()
	backend := membackend.New()
	// Direct config mutation via an Option that nilly assigns ShouldTrip
	// (rather than via WithShouldTrip(nil), which is a no-op).
	clear := func(c *Config) { c.ShouldTrip = nil }
	assert.PanicsWithValue(t, "storage/circuitbreaker: ShouldTrip must not be nil", func() {
		_ = New(backend, clear)
	})
}

func TestCircuitBreaker_WithShouldTripNil_PreservesDefault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := membackend.New()
	cb := New(backend, WithShouldTrip(nil))

	// The default predicate must still work — a missing key is not transient
	// and must not contribute to tripping.
	_, err := cb.Exists(ctx, "missing")
	assert.NoError(t, err)
	assert.Equal(t, StateClosed, cb.State())
}

func TestCircuitBreaker_DefaultDoesNotTripOnValidationErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := membackend.New()
	cb := New(backend, WithThreshold(1), WithResetTimeout(time.Hour))

	err := cb.Put(ctx, "file.txt", nil, storage.ObjectMeta{})
	require.ErrorIs(t, err, storage.ErrValidation)
	assert.Equal(t, StateClosed, cb.State())

	_, _, err = cb.Get(ctx, "")
	require.ErrorIs(t, err, storage.ErrValidation)
	assert.Equal(t, StateClosed, cb.State())
}

func TestCircuitBreaker_ValidatesBeforeBackendAndBreakerState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	backend := &validationProbeBackend{}
	cb := New(backend, WithThreshold(1), WithResetTimeout(time.Hour))

	_, _, err := cb.Get(ctx, "bad key")
	require.ErrorIs(t, err, storage.ErrValidation)
	assert.Equal(t, int32(0), backend.calls.Load())
	assert.Equal(t, StateClosed, cb.State())
}

func TestCircuitBreaker_ListValidatesBeforeBackendAndBreakerState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	backend := &validationProbeBackend{}
	cb := New(backend, WithThreshold(1), WithResetTimeout(time.Hour))
	lister, ok := storage.AsLister(cb)
	require.True(t, ok)

	var seenErr error
	for _, err := range lister.List(ctx, "", storage.ListOptions{MaxKeys: -1}) {
		seenErr = err
		break
	}

	require.ErrorIs(t, seenErr, storage.ErrValidation)
	assert.Equal(t, int32(0), backend.calls.Load())
	assert.Equal(t, StateClosed, cb.State())
}

func TestCircuitBreaker_New_PanicsOnZeroResetTimeout(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "storage/circuitbreaker: WithResetTimeout: reset timeout must be positive", func() {
		_ = WithResetTimeout(0)
	})
}

func TestCircuitBreaker_New_PanicsOnNegativeResetTimeout(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "storage/circuitbreaker: WithResetTimeout: reset timeout must be positive", func() {
		_ = WithResetTimeout(-time.Second)
	})
}

func TestCircuitBreaker_New_PanicsOnZeroThreshold(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "storage/circuitbreaker: WithThreshold: threshold must be >= 1", func() {
		_ = WithThreshold(0)
	})
}

// presignedListerCBBackend implements the four optional interfaces with
// hooks so we can verify circuit breaker forwards capabilities and that
// open circuits block them.
type presignedListerCBBackend struct {
	*membackend.Backend
	failPresign func() error
	failURL     func() error
}

func (b *presignedListerCBBackend) PresignGetURL(_ context.Context, key string, _ time.Duration) (string, error) {
	if b.failPresign != nil {
		if err := b.failPresign(); err != nil {
			return "", err
		}
	}
	return "https://signed/" + key, nil
}

func (b *presignedListerCBBackend) PresignPutURL(_ context.Context, key string, _ time.Duration, _ storage.ObjectMeta) (string, error) {
	if b.failPresign != nil {
		if err := b.failPresign(); err != nil {
			return "", err
		}
	}
	return "https://signed-put/" + key, nil
}

func (b *presignedListerCBBackend) URL(_ context.Context, key string) (string, error) {
	if b.failURL != nil {
		if err := b.failURL(); err != nil {
			return "", err
		}
	}
	return "https://public/" + key, nil
}

func TestAsPresigned_ReachesUnderlyingThroughCircuitBreaker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	backend := &presignedListerCBBackend{Backend: membackend.New()}
	cb := New(backend)

	ps, ok := storage.AsPresigned(cb)
	require.True(t, ok, "circuit breaker must expose Presigned when underlying has it")

	url, err := ps.PresignGetURL(ctx, "key", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "https://signed/key", url)
}

func TestAsPresigned_CircuitBreakerDoesNotClaimWhenBackendLacks(t *testing.T) {
	t.Parallel()
	cb := New(membackend.New())
	_, ok := storage.AsPresigned(cb)
	assert.False(t, ok, "circuit breaker must not expose Presigned when underlying lacks it")
}

func TestAsLister_CircuitBreakerBlocksOpenCircuit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// MemBackend implements Lister. Trip the circuit using Get failures,
	// then assert that List operations are blocked by the open circuit.
	failing := &alwaysFailBackend{err: errors.New("down")}
	cb := New(failing, WithThreshold(1), WithResetTimeout(time.Hour))

	// Trip the breaker via a Get failure.
	_, _, _ = cb.Get(ctx, "key")
	assert.Equal(t, StateOpen, cb.State())

	// failing does not implement Lister, so AsLister returns false (no
	// underlying capability) — verify that path:
	_, ok := storage.AsLister(cb)
	assert.False(t, ok, "underlying lacks Lister so CB should not claim it")

	// Now wrap a Lister-capable backend and re-test that an open circuit
	// blocks List dispatch.
	backend := &presignedListerCBBackend{Backend: membackend.New()}
	cb2 := New(backend, WithThreshold(1), WithResetTimeout(time.Hour))

	// Trip the breaker on cb2.
	_, _, _ = cb2.Get(ctx, "missing")
	// MemBackend.Get returns ErrObjectNotFound which the default
	// ShouldTrip filters out — so we must use a different trip path.
	// Force a trip with a custom backend instead.
	tripping := &alwaysFailListerBackend{err: errors.New("down")}
	cb3 := New(tripping, WithThreshold(1), WithResetTimeout(time.Hour))
	_, _, _ = cb3.Get(ctx, "key") // trips
	assert.Equal(t, StateOpen, cb3.State())

	lister, ok := storage.AsLister(cb3)
	require.True(t, ok)

	// Iterating the list should yield ErrCircuitOpen.
	var seenErr error
	for _, err := range lister.List(ctx, "", storage.ListOptions{}) {
		if err != nil {
			seenErr = err
			break
		}
	}
	require.Error(t, seenErr)
	assert.ErrorIs(t, seenErr, ErrCircuitOpen, "list must be blocked by open circuit")
}

// alwaysFailListerBackend is alwaysFailBackend that ALSO implements Lister.
type alwaysFailListerBackend struct {
	err error
}

func (b *alwaysFailListerBackend) Put(context.Context, string, io.Reader, storage.ObjectMeta) error {
	return b.err
}
func (b *alwaysFailListerBackend) Get(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
	return nil, storage.ObjectMeta{}, b.err
}
func (b *alwaysFailListerBackend) Delete(context.Context, string) error         { return b.err }
func (b *alwaysFailListerBackend) Exists(context.Context, string) (bool, error) { return false, b.err }

func (b *alwaysFailListerBackend) List(_ context.Context, _ string, _ storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return func(yield func(storage.ObjectInfo, error) bool) {
		yield(storage.ObjectInfo{}, b.err)
	}
}

// alwaysFailBackend returns the same error for all operations.
type alwaysFailBackend struct {
	err error
}

func (b *alwaysFailBackend) Put(context.Context, string, io.Reader, storage.ObjectMeta) error {
	return b.err
}
func (b *alwaysFailBackend) Get(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
	return nil, storage.ObjectMeta{}, b.err
}
func (b *alwaysFailBackend) Delete(context.Context, string) error         { return b.err }
func (b *alwaysFailBackend) Exists(context.Context, string) (bool, error) { return false, b.err }

// switchableBackend fails for the first N calls then delegates to the real backend.
type switchableBackend struct {
	failUntil int
	err       error
	backend   storage.Storage
	counter   *int
}

func (b *switchableBackend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	*b.counter++
	if *b.counter <= b.failUntil {
		return b.err
	}
	return b.backend.Put(ctx, key, r, meta)
}

func (b *switchableBackend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	*b.counter++
	if *b.counter <= b.failUntil {
		return nil, storage.ObjectMeta{}, b.err
	}
	return b.backend.Get(ctx, key)
}

func (b *switchableBackend) Delete(ctx context.Context, key string) error {
	*b.counter++
	if *b.counter <= b.failUntil {
		return b.err
	}
	return b.backend.Delete(ctx, key)
}

func (b *switchableBackend) Exists(ctx context.Context, key string) (bool, error) {
	*b.counter++
	if *b.counter <= b.failUntil {
		return false, b.err
	}
	return b.backend.Exists(ctx, key)
}

type validationProbeBackend struct {
	calls atomic.Int32
}

func (b *validationProbeBackend) Put(context.Context, string, io.Reader, storage.ObjectMeta) error {
	b.calls.Add(1)
	return nil
}

func (b *validationProbeBackend) Get(context.Context, string) (io.ReadCloser, storage.ObjectMeta, error) {
	b.calls.Add(1)
	return nil, storage.ObjectMeta{}, errors.New("backend should not be called")
}

func (b *validationProbeBackend) Delete(context.Context, string) error {
	b.calls.Add(1)
	return nil
}

func (b *validationProbeBackend) Exists(context.Context, string) (bool, error) {
	b.calls.Add(1)
	return false, nil
}

func (b *validationProbeBackend) List(context.Context, string, storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	return func(yield func(storage.ObjectInfo, error) bool) {
		b.calls.Add(1)
		yield(storage.ObjectInfo{}, errors.New("backend should not be called"))
	}
}
