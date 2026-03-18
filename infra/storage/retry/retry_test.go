package retry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/storage"
	"github.com/bds421/rho-kit/infra/storage/membackend"
)

func TestRetryStorage_SucceedsImmediately(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := membackend.New()
	r := New(backend)

	err := r.Put(ctx, "file.txt", bytes.NewReader([]byte("hello")), storage.ObjectMeta{})
	require.NoError(t, err)

	rc, _, err := r.Get(ctx, "file.txt")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	data, _ := io.ReadAll(rc)
	assert.Equal(t, []byte("hello"), data)
}

func TestRetryStorage_RetriesTransient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var callCount atomic.Int32
	transientErr := storage.NewTransientError("get", "key", errors.New("timeout"))

	backend := &failingBackend{
		underlying: membackend.New(),
		getFn: func() error {
			if callCount.Add(1) <= 2 {
				return transientErr
			}
			return nil
		},
	}

	// Seed data directly on the underlying backend.
	err := backend.underlying.Put(ctx, "key", bytes.NewReader([]byte("data")), storage.ObjectMeta{})
	require.NoError(t, err)

	r := New(backend, WithMaxAttempts(4), WithBaseDelay(time.Millisecond))

	rc, _, err := r.Get(ctx, "key")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	assert.Equal(t, int32(3), callCount.Load()) // 2 failures + 1 success
}

func TestRetryStorage_StopsOnPermanent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var callCount atomic.Int32

	backend := &failingBackend{
		underlying: membackend.New(),
		getFn: func() error {
			callCount.Add(1)
			return storage.NewPermanentError("get", "key", errors.New("not found"))
		},
	}

	r := New(backend, WithMaxAttempts(5), WithBaseDelay(time.Millisecond))

	_, _, err := r.Get(ctx, "key")
	assert.Error(t, err)
	assert.Equal(t, int32(1), callCount.Load()) // no retries for permanent errors
}

func TestRetryStorage_RespectsContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	transientErr := storage.NewTransientError("delete", "key", errors.New("timeout"))
	backend := &failingBackend{
		underlying: membackend.New(),
		deleteFn: func() error {
			return transientErr
		},
	}

	r := New(backend, WithMaxAttempts(5), WithBaseDelay(time.Millisecond))
	err := r.Delete(ctx, "key")
	assert.Error(t, err)
}

func TestRetryStorage_Exists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := membackend.New()
	r := New(backend)

	ok, err := r.Exists(ctx, "missing.txt")
	require.NoError(t, err)
	assert.False(t, ok)
}

// failingBackend wraps MemBackend but can inject errors per-operation.
type failingBackend struct {
	underlying *membackend.MemBackend
	getFn      func() error
	deleteFn   func() error
}

func (f *failingBackend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	return f.underlying.Put(ctx, key, r, meta)
}

func (f *failingBackend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	if f.getFn != nil {
		if err := f.getFn(); err != nil {
			return nil, storage.ObjectMeta{}, err
		}
	}
	return f.underlying.Get(ctx, key)
}

func (f *failingBackend) Delete(ctx context.Context, key string) error {
	if f.deleteFn != nil {
		if err := f.deleteFn(); err != nil {
			return err
		}
	}
	return f.underlying.Delete(ctx, key)
}

func (f *failingBackend) Exists(ctx context.Context, key string) (bool, error) {
	return f.underlying.Exists(ctx, key)
}
