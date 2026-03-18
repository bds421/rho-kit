package circuitbreaker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage"
	"github.com/bds421/rho-kit/infra/storage/membackend"
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

func TestCircuitBreaker_Delete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := membackend.New()
	cb := New(backend)

	err := cb.Delete(ctx, "nonexistent.txt")
	assert.NoError(t, err)
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
func (b *alwaysFailBackend) Delete(context.Context, string) error        { return b.err }
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
