package retry

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

func TestRetryStorage_ValidatesBeforeBackendAndRetryPolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var retryPredicateCalled atomic.Bool
	backend := &validationProbeBackend{}
	r := New(backend, WithShouldRetry(func(error) bool {
		retryPredicateCalled.Store(true)
		return true
	}))

	_, _, err := r.Get(ctx, "bad key")
	require.ErrorIs(t, err, storage.ErrValidation)
	assert.Equal(t, int32(0), backend.calls.Load())
	assert.False(t, retryPredicateCalled.Load())
}

func TestRetryStorage_ListValidatesBeforeBackendAndRetryPolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var retryPredicateCalled atomic.Bool
	backend := &validationProbeBackend{}
	r := New(backend, WithShouldRetry(func(error) bool {
		retryPredicateCalled.Store(true)
		return true
	}))
	lister, ok := storage.AsLister(r)
	require.True(t, ok)

	var seenErr error
	for _, err := range lister.List(ctx, "", storage.ListOptions{StartAfter: "bad key"}) {
		seenErr = err
		break
	}

	require.ErrorIs(t, seenErr, storage.ErrValidation)
	assert.Equal(t, int32(0), backend.calls.Load())
	assert.False(t, retryPredicateCalled.Load())
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

func TestRetryStorage_New_NilBackendPanics(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "storage/retry: backend must not be nil", func() {
		_ = New(nil)
	})
}

func TestRetryStorage_New_NilOptionPanics(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "storage/retry: option must not be nil", func() {
		_ = New(membackend.New(), nil)
	})
}

func TestRetryStorage_New_NilShouldRetryPanics(t *testing.T) {
	t.Parallel()
	backend := membackend.New()
	clear := func(c *Config) { c.ShouldRetry = nil }
	assert.PanicsWithValue(t, "storage/retry: ShouldRetry must not be nil", func() {
		_ = New(backend, clear)
	})
}

func TestRetryStorage_WithShouldRetryNil_PreservesDefault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := membackend.New()
	r := New(backend, WithShouldRetry(nil))

	ok, err := r.Exists(ctx, "missing.txt")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestRetryStorage_New_PanicsOnZeroMaxAttempts(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "storage/retry: max attempts must be >= 1", func() {
		_ = WithMaxAttempts(0)
	})
}

func TestRetryStorage_New_PanicsOnNegativeMaxAttempts(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "storage/retry: max attempts must be >= 1", func() {
		_ = WithMaxAttempts(-1)
	})
}

func TestRetryStorage_New_PanicsOnZeroMaxDelay(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "storage/retry: max delay must be positive", func() {
		_ = WithMaxDelay(0)
	})
}

func TestRetryStorage_New_PanicsOnNegativeMaxDelay(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "storage/retry: max delay must be positive", func() {
		_ = WithMaxDelay(-time.Second)
	})
}

func TestRetryStorage_New_PanicsOnZeroBaseDelay(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "storage/retry: base delay must be positive", func() {
		_ = WithBaseDelay(0)
	})
}

// presignedListerBackend implements the four optional interfaces with
// hooks so we can verify retry forwards capabilities and applies retry
// policy.
type presignedListerBackend struct {
	*membackend.Backend
	presignCalls atomic.Int32
	urlCalls     atomic.Int32
	failPresign  func() error
	failURL      func() error
}

func (b *presignedListerBackend) PresignGetURL(_ context.Context, key string, _ time.Duration) (string, error) {
	b.presignCalls.Add(1)
	if b.failPresign != nil {
		if err := b.failPresign(); err != nil {
			return "", err
		}
	}
	return "https://signed/" + key, nil
}

func (b *presignedListerBackend) PresignPutURL(_ context.Context, key string, _ time.Duration, _ storage.ObjectMeta) (string, error) {
	b.presignCalls.Add(1)
	if b.failPresign != nil {
		if err := b.failPresign(); err != nil {
			return "", err
		}
	}
	return "https://signed-put/" + key, nil
}

func (b *presignedListerBackend) URL(_ context.Context, key string) (string, error) {
	b.urlCalls.Add(1)
	if b.failURL != nil {
		if err := b.failURL(); err != nil {
			return "", err
		}
	}
	return "https://public/" + key, nil
}

func TestAsPresigned_ReachesUnderlyingThroughRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	backend := &presignedListerBackend{Backend: membackend.New()}
	r := New(backend, WithMaxAttempts(2), WithBaseDelay(time.Millisecond))

	ps, ok := storage.AsPresigned(r)
	require.True(t, ok, "retry must expose Presigned when underlying has it")

	url, err := ps.PresignGetURL(ctx, "key", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "https://signed/key", url)
}

func TestAsPresigned_RetryDoesNotClaimWhenBackendLacks(t *testing.T) {
	t.Parallel()
	r := New(membackend.New())
	_, ok := storage.AsPresigned(r)
	assert.False(t, ok, "retry must not expose Presigned when underlying lacks it")
}

func TestAsLister_RetryRetriesUnderlyingErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Inject a transient failure on the FIRST presign attempt; retry
	// must reissue and succeed.
	var attempts atomic.Int32
	backend := &presignedListerBackend{
		Backend: membackend.New(),
		failPresign: func() error {
			if attempts.Add(1) == 1 {
				return storage.NewTransientError("presign", "key", errors.New("timeout"))
			}
			return nil
		},
	}

	r := New(backend, WithMaxAttempts(3), WithBaseDelay(time.Millisecond))
	ps, ok := storage.AsPresigned(r)
	require.True(t, ok)

	url, err := ps.PresignGetURL(ctx, "key", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "https://signed/key", url)
	assert.Equal(t, int32(2), backend.presignCalls.Load(), "retry should have made 2 calls")
}

func TestAsPublicURLer_ReachesUnderlyingThroughRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	backend := &presignedListerBackend{Backend: membackend.New()}
	r := New(backend, WithMaxAttempts(2), WithBaseDelay(time.Millisecond))

	urler, ok := storage.AsPublicURLer(r)
	require.True(t, ok)

	url, err := urler.URL(ctx, "key")
	require.NoError(t, err)
	assert.Equal(t, "https://public/key", url)
}

// failingBackend wraps MemBackend but can inject errors per-operation.
type failingBackend struct {
	underlying *membackend.Backend
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
