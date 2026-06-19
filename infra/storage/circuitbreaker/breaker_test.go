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
	assert.PanicsWithValue(t, "storage/circuitbreaker: New backend must not be nil", func() {
		_ = New(nil)
	})
}

func TestCircuitBreaker_New_NilOptionPanics(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "storage/circuitbreaker: New option must not be nil", func() {
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
	assert.PanicsWithValue(t, "storage/circuitbreaker: WithResetTimeout reset timeout must be positive", func() {
		_ = WithResetTimeout(0)
	})
}

func TestCircuitBreaker_New_PanicsOnNegativeResetTimeout(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "storage/circuitbreaker: WithResetTimeout reset timeout must be positive", func() {
		_ = WithResetTimeout(-time.Second)
	})
}

func TestCircuitBreaker_New_PanicsOnZeroThreshold(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "storage/circuitbreaker: WithThreshold threshold must be >= 1", func() {
		_ = WithThreshold(0)
	})
}

// presignedListerCBBackend implements the four optional interfaces (Lister via
// the embedded membackend, plus Copier, PresignedStore, PublicURLer) with hooks
// so we can verify the circuit breaker forwards capabilities and that open
// circuits block them.
type presignedListerCBBackend struct {
	*membackend.Backend
	failPresign func() error
	failURL     func() error
	failCopy    func() error
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

func (b *presignedListerCBBackend) Copy(_ context.Context, _, _ string) error {
	if b.failCopy != nil {
		if err := b.failCopy(); err != nil {
			return err
		}
	}
	return nil
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
	// blocks List dispatch. MemBackend.Get returns ErrObjectNotFound, which
	// the default ShouldTrip filters out, so trip via a backend whose every
	// op fails with a trippable error.
	tripping := &alwaysFailListerBackend{err: errors.New("down")}
	cb2 := New(tripping, WithThreshold(1), WithResetTimeout(time.Hour))
	_, _, _ = cb2.Get(ctx, "key") // trips
	assert.Equal(t, StateOpen, cb2.State())

	lister, ok := storage.AsLister(cb2)
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

func TestCircuitBreaker_PresignGetBlockedByOpenCircuit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var callCount atomic.Int32
	backend := &presignedListerCBBackend{
		Backend: membackend.New(),
		failPresign: func() error {
			callCount.Add(1)
			return errors.New("down")
		},
	}
	cb := New(backend, WithThreshold(1), WithResetTimeout(time.Hour))

	ps, ok := storage.AsPresigned(cb)
	require.True(t, ok)

	// First call hits the backend, fails, and trips the breaker.
	_, err := ps.PresignGetURL(ctx, "key", time.Minute)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrCircuitOpen)
	require.Equal(t, StateOpen, cb.State())
	require.Equal(t, int32(1), callCount.Load())

	// Subsequent call must fast-fail with ErrCircuitOpen without touching
	// the backend.
	_, err = ps.PresignGetURL(ctx, "key", time.Minute)
	assert.ErrorIs(t, err, ErrCircuitOpen, "PresignGetURL must be blocked by open circuit")
	assert.Equal(t, int32(1), callCount.Load(), "open circuit must not reach the backend")
}

func TestCircuitBreaker_PresignPutBlockedByOpenCircuit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var callCount atomic.Int32
	backend := &presignedListerCBBackend{
		Backend: membackend.New(),
		failPresign: func() error {
			callCount.Add(1)
			return errors.New("down")
		},
	}
	cb := New(backend, WithThreshold(1), WithResetTimeout(time.Hour))

	ps, ok := storage.AsPresigned(cb)
	require.True(t, ok)

	// First call hits the backend, fails, and trips the breaker.
	_, err := ps.PresignPutURL(ctx, "key", time.Minute, storage.ObjectMeta{})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrCircuitOpen)
	require.Equal(t, StateOpen, cb.State())
	require.Equal(t, int32(1), callCount.Load())

	// Subsequent call must fast-fail with ErrCircuitOpen without touching
	// the backend.
	_, err = ps.PresignPutURL(ctx, "key", time.Minute, storage.ObjectMeta{})
	assert.ErrorIs(t, err, ErrCircuitOpen, "PresignPutURL must be blocked by open circuit")
	assert.Equal(t, int32(1), callCount.Load(), "open circuit must not reach the backend")
}

func TestCircuitBreaker_URLBlockedByOpenCircuit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var callCount atomic.Int32
	backend := &presignedListerCBBackend{
		Backend: membackend.New(),
		failURL: func() error {
			callCount.Add(1)
			return errors.New("down")
		},
	}
	cb := New(backend, WithThreshold(1), WithResetTimeout(time.Hour))

	urler, ok := storage.AsPublicURLer(cb)
	require.True(t, ok)

	// First call hits the backend, fails, and trips the breaker.
	_, err := urler.URL(ctx, "key")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrCircuitOpen)
	require.Equal(t, StateOpen, cb.State())
	require.Equal(t, int32(1), callCount.Load())

	// Subsequent call must fast-fail with ErrCircuitOpen without touching
	// the backend.
	_, err = urler.URL(ctx, "key")
	assert.ErrorIs(t, err, ErrCircuitOpen, "URL must be blocked by open circuit")
	assert.Equal(t, int32(1), callCount.Load(), "open circuit must not reach the backend")
}

func TestCircuitBreaker_CopyBlockedByOpenCircuit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var callCount atomic.Int32
	backend := &presignedListerCBBackend{
		Backend: membackend.New(),
		failCopy: func() error {
			callCount.Add(1)
			return errors.New("down")
		},
	}
	cb := New(backend, WithThreshold(1), WithResetTimeout(time.Hour))

	copier, ok := storage.AsCopier(cb)
	require.True(t, ok)

	// First call hits the backend, fails, and trips the breaker.
	err := copier.Copy(ctx, "src", "dst")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrCircuitOpen)
	require.Equal(t, StateOpen, cb.State())
	require.Equal(t, int32(1), callCount.Load())

	// Subsequent call must fast-fail with ErrCircuitOpen without touching
	// the backend.
	err = copier.Copy(ctx, "src", "dst")
	assert.ErrorIs(t, err, ErrCircuitOpen, "Copy must be blocked by open circuit")
	assert.Equal(t, int32(1), callCount.Load(), "open circuit must not reach the backend")
}

func TestCircuitBreaker_ListIterationFailuresTripBreaker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// The backend yields an error during iteration of List (the common case
	// for a streaming List against a dead backend). These iteration errors
	// must be counted by the breaker, otherwise a dead backend never trips.
	tripping := &alwaysFailListerBackend{err: errors.New("down")}
	cb := New(tripping, WithThreshold(3), WithResetTimeout(time.Hour))
	lister, ok := storage.AsLister(cb)
	require.True(t, ok)

	drain := func() error {
		var seen error
		for _, err := range lister.List(ctx, "", storage.ListOptions{}) {
			if err != nil {
				seen = err
				break
			}
		}
		return seen
	}

	// First 3 List drains fail with the backend error.
	for range 3 {
		err := drain()
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrCircuitOpen)
	}

	// The breaker must now be open: iteration failures counted toward the
	// consecutive-failure threshold.
	assert.Equal(t, StateOpen, cb.State())

	// A subsequent drain fails fast with ErrCircuitOpen.
	err := drain()
	assert.ErrorIs(t, err, ErrCircuitOpen)
}

func TestCircuitBreaker_ListProbeDoesNotPhantomCloseHalfOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tripping := &alwaysFailListerBackend{err: errors.New("down")}
	cb := New(tripping, WithThreshold(1), WithResetTimeout(time.Millisecond))

	// Trip the breaker via a Get failure.
	_, _, _ = cb.Get(ctx, "key")
	require.Equal(t, StateOpen, cb.State())

	// Wait for the reset timeout so the breaker enters half-open.
	time.Sleep(5 * time.Millisecond)
	require.Equal(t, StateHalfOpen, cb.State())

	lister, ok := storage.AsLister(cb)
	require.True(t, ok)

	// A List probe whose stream errors during iteration must NOT close the
	// circuit. A bare lazy dispatch would otherwise be recorded as a phantom
	// success and re-close the breaker against a still-dead backend.
	var seen error
	for _, err := range lister.List(ctx, "", storage.ListOptions{}) {
		if err != nil {
			seen = err
			break
		}
	}
	require.Error(t, seen)
	assert.NotEqual(t, StateClosed, cb.State(), "failed List probe must not close the circuit")
}

func TestCircuitBreaker_ListSuccessfulIterationClosesHalfOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Seed a healthy backend so a Lister probe iterates successfully and
	// closes the circuit in half-open.
	healthy := membackend.New()
	require.NoError(t, healthy.Put(ctx, "file.txt", bytes.NewReader([]byte("x")), storage.ObjectMeta{}))

	switchable := &switchableListerBackend{
		failFirstGet: true,
		err:          errors.New("down"),
		backend:      healthy,
	}
	cb := New(switchable, WithThreshold(1), WithResetTimeout(time.Millisecond))

	// Trip the breaker.
	_, _, _ = cb.Get(ctx, "file.txt")
	require.Equal(t, StateOpen, cb.State())

	time.Sleep(5 * time.Millisecond)
	require.Equal(t, StateHalfOpen, cb.State())

	lister, ok := storage.AsLister(cb)
	require.True(t, ok)

	var count int
	var seenErr error
	for _, err := range lister.List(ctx, "", storage.ListOptions{}) {
		if err != nil {
			seenErr = err
			break
		}
		count++
	}
	require.NoError(t, seenErr)
	assert.Equal(t, 1, count)
	assert.Equal(t, StateClosed, cb.State(), "successful List probe must close the circuit")
}

func TestCircuitBreaker_DefaultDoesNotTripOnContextCanceled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	failing := &alwaysFailBackend{err: context.Canceled}
	cb := New(failing, WithThreshold(1), WithResetTimeout(time.Hour))

	// Many consecutive client cancellations must not trip the circuit: a
	// caller aborting is not evidence the backend is unhealthy.
	for range 5 {
		_, _, err := cb.Get(ctx, "key")
		require.ErrorIs(t, err, context.Canceled)
		assert.NotErrorIs(t, err, ErrCircuitOpen)
	}
	assert.Equal(t, StateClosed, cb.State())
}

// switchableListerBackend fails the first Get, then delegates to a real
// backend (including List) so half-open probes can succeed.
type switchableListerBackend struct {
	failFirstGet bool
	err          error
	backend      storage.Storage
	gotFirst     bool
}

func (b *switchableListerBackend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	return b.backend.Put(ctx, key, r, meta)
}

func (b *switchableListerBackend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	if b.failFirstGet && !b.gotFirst {
		b.gotFirst = true
		return nil, storage.ObjectMeta{}, b.err
	}
	return b.backend.Get(ctx, key)
}

func (b *switchableListerBackend) Delete(ctx context.Context, key string) error {
	return b.backend.Delete(ctx, key)
}

func (b *switchableListerBackend) Exists(ctx context.Context, key string) (bool, error) {
	return b.backend.Exists(ctx, key)
}

func (b *switchableListerBackend) List(ctx context.Context, prefix string, opts storage.ListOptions) iter.Seq2[storage.ObjectInfo, error] {
	lister, _ := storage.AsLister(b.backend)
	return lister.List(ctx, prefix, opts)
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
