package redislock_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/lock"
	redislock "github.com/bds421/rho-kit/data/lock/redislock"
)

func setupRedis(t *testing.T) (*miniredis.Miniredis, redis.UniversalClient) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

func TestAcquireAndRelease(t *testing.T) {
	_, client := setupRedis(t)
	ctx := context.Background()

	l := redislock.New(client, "test:lock")

	acquired, err := l.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)

	err = l.Release(ctx)
	require.NoError(t, err)

	// After release, the key should be gone and re-acquirable.
	acquired, err = l.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)
}

func TestAcquireFailsWhenAlreadyHeld(t *testing.T) {
	_, client := setupRedis(t)
	ctx := context.Background()

	l1 := redislock.New(client, "test:lock")
	l2 := redislock.New(client, "test:lock")

	acquired, err := l1.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)

	// Second lock with different token should fail.
	acquired, err = l2.Acquire(ctx)
	require.NoError(t, err)
	assert.False(t, acquired)
}

func TestReleaseOnlyByOwner(t *testing.T) {
	_, client := setupRedis(t)
	ctx := context.Background()

	l1 := redislock.New(client, "test:lock")
	l2 := redislock.New(client, "test:lock")

	acquired, err := l1.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)

	// l2 (different token) tries to release l1's lock - should not release it.
	err = l2.Release(ctx)
	require.NoError(t, err)

	// Lock should still be held by l1, so l2 cannot acquire.
	acquired, err = l2.Acquire(ctx)
	require.NoError(t, err)
	assert.False(t, acquired)

	// l1 can still release its own redislock.
	err = l1.Release(ctx)
	require.NoError(t, err)

	// Now l2 should be able to acquire.
	acquired, err = l2.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)
}

func TestWithLockSuccess(t *testing.T) {
	_, client := setupRedis(t)
	ctx := context.Background()

	l := redislock.New(client, "test:lock")
	called := false

	err := l.WithLock(ctx, func(_ context.Context) error {
		called = true
		return nil
	})

	require.NoError(t, err)
	assert.True(t, called)

	// Lock should be released after WithLock returns.
	l2 := redislock.New(client, "test:lock")
	acquired, err := l2.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)
}

func TestWithLockError(t *testing.T) {
	_, client := setupRedis(t)
	ctx := context.Background()

	l := redislock.New(client, "test:lock")
	expectedErr := errors.New("operation failed")

	err := l.WithLock(ctx, func(_ context.Context) error {
		return expectedErr
	})

	assert.ErrorIs(t, err, expectedErr)

	// Lock should still be released even on error.
	l2 := redislock.New(client, "test:lock")
	acquired, err := l2.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)
}

func TestWithLockCannotAcquire(t *testing.T) {
	_, client := setupRedis(t)
	ctx := context.Background()

	// Hold the lock with l1.
	l1 := redislock.New(client, "test:lock")
	acquired, err := l1.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)

	// WithLock on l2 should fail to acquire.
	l2 := redislock.New(client, "test:lock")
	err = l2.WithLock(ctx, func(_ context.Context) error {
		t.Fatal("should not be called")
		return nil
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "could not acquire lock")
}

func TestRetryOption(t *testing.T) {
	mr, client := setupRedis(t)
	ctx := context.Background()

	l1 := redislock.New(client, "test:lock", redislock.WithTTL(2*time.Second))
	acquired, err := l1.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)

	// Release the lock from miniredis side to simulate TTL expiration
	// during a retry window.
	go func() {
		time.Sleep(150 * time.Millisecond)
		mr.FastForward(3 * time.Second)
	}()

	// l2 with retry should eventually acquire after l1's TTL expires.
	l2 := redislock.New(client, "test:lock",
		redislock.WithRetry(100*time.Millisecond, 5),
	)

	acquired, err = l2.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)
}

func TestRetryExhausted(t *testing.T) {
	_, client := setupRedis(t)
	ctx := context.Background()

	l1 := redislock.New(client, "test:lock", redislock.WithTTL(10*time.Second))
	acquired, err := l1.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)

	// l2 with limited retries should give up.
	l2 := redislock.New(client, "test:lock",
		redislock.WithRetry(10*time.Millisecond, 3),
	)

	acquired, err = l2.Acquire(ctx)
	require.NoError(t, err)
	assert.False(t, acquired)
}

func TestTTLExpiration(t *testing.T) {
	mr, client := setupRedis(t)
	ctx := context.Background()

	l1 := redislock.New(client, "test:lock", redislock.WithTTL(5*time.Second))
	acquired, err := l1.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)

	// Fast-forward past the TTL.
	mr.FastForward(6 * time.Second)

	// Lock should have expired; a new lock can acquire.
	l2 := redislock.New(client, "test:lock")
	acquired, err = l2.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)
}

func TestRetryRespectsContextCancellation(t *testing.T) {
	_, client := setupRedis(t)

	l1 := redislock.New(client, "test:lock", redislock.WithTTL(10*time.Second))
	ctx := context.Background()
	acquired, err := l1.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)

	// Cancel context before retries can complete.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	l2 := redislock.New(client, "test:lock",
		redislock.WithRetry(100*time.Millisecond, 10),
	)

	acquired, err = l2.Acquire(ctx)
	assert.False(t, acquired)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestWithLock_ReleasesOnPanic(t *testing.T) {
	_, client := setupRedis(t)
	ctx := context.Background()

	l := redislock.New(client, "test:lock")

	// Run WithLock with a function that panics and capture the panic value.
	var panicVal any
	func() {
		defer func() {
			panicVal = recover()
		}()
		_ = l.WithLock(ctx, func(_ context.Context) error {
			panic("intentional panic for testing")
		})
	}()

	// The panic must have propagated and been captured.
	if panicVal == nil {
		t.Fatal("expected panic to propagate, but recover() returned nil")
	}
	if panicVal != "intentional panic for testing" {
		t.Errorf("unexpected panic value: got %v", panicVal)
	}

	// The lock must have been released despite the panic.
	// A fresh lock instance using a different token should be able to acquire.
	l2 := redislock.New(client, "test:lock")
	acquired, err := l2.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire after panic returned unexpected error: %v", err)
	}
	if !acquired {
		t.Error("expected second Acquire to succeed after panic released the lock, but it failed")
	}
}

// --- New Locker (per-call returned handle) ---

func TestLocker_AcquireReleaseRoundTrip(t *testing.T) {
	_, client := setupRedis(t)
	ctx := context.Background()

	lc := redislock.NewLocker(client, redislock.WithTTL(2*time.Second))

	l, ok, err := lc.Acquire(ctx, "test:lock")
	require.NoError(t, err)
	require.True(t, ok)

	// Second Acquire on the same key from the same Locker returns (nil, false, nil)
	// — different fresh handle internally, but the SETNX still fails.
	l2, ok2, err := lc.Acquire(ctx, "test:lock")
	require.NoError(t, err)
	assert.False(t, ok2)
	assert.Nil(t, l2)

	// Release the first handle. After release, the key is reacquirable.
	require.NoError(t, l.Release(ctx))

	l3, ok3, err := lc.Acquire(ctx, "test:lock")
	require.NoError(t, err)
	require.True(t, ok3)
	require.NoError(t, l3.Release(ctx))
}

func TestLocker_ReleaseSurfacesErrLockLost(t *testing.T) {
	mr, client := setupRedis(t)
	ctx := context.Background()

	lc := redislock.NewLocker(client, redislock.WithTTL(1*time.Second))
	l, ok, err := lc.Acquire(ctx, "test:lock")
	require.NoError(t, err)
	require.True(t, ok)

	// TTL expires, someone else takes the key.
	mr.FastForward(2 * time.Second)
	require.NoError(t, mr.Set("test:lock", "stolen-by-someone-else"))

	relErr := l.Release(ctx)
	assert.ErrorIs(t, relErr, lock.ErrLockLost)
}

func TestLocker_WithLockSurfacesErrLockLost(t *testing.T) {
	mr, client := setupRedis(t)
	ctx := context.Background()

	lc := redislock.NewLocker(client, redislock.WithTTL(1*time.Second))

	err := lc.WithLock(ctx, "test:lock", func(_ context.Context) error {
		mr.FastForward(2 * time.Second)
		if setErr := mr.Set("test:lock", "stolen-by-someone-else"); setErr != nil {
			return setErr
		}
		return nil
	})
	assert.ErrorIs(t, err, lock.ErrLockLost)
}

func TestLocker_WithLockJoinsFnErrAndLockLost(t *testing.T) {
	mr, client := setupRedis(t)
	ctx := context.Background()

	lc := redislock.NewLocker(client, redislock.WithTTL(1*time.Second))
	fnErr := errors.New("downstream blew up")

	err := lc.WithLock(ctx, "test:lock", func(_ context.Context) error {
		mr.FastForward(2 * time.Second)
		if setErr := mr.Set("test:lock", "stolen-by-someone-else"); setErr != nil {
			return setErr
		}
		return fnErr
	})
	assert.ErrorIs(t, err, fnErr)
	assert.ErrorIs(t, err, lock.ErrLockLost)
}

func TestLocker_WithLockReleasesOnPanic(t *testing.T) {
	_, client := setupRedis(t)
	ctx := context.Background()

	lc := redislock.NewLocker(client)

	var panicVal any
	func() {
		defer func() { panicVal = recover() }()
		_ = lc.WithLock(ctx, "test:lock", func(_ context.Context) error {
			panic("intentional")
		})
	}()
	assert.Equal(t, "intentional", panicVal)

	// Re-acquire must succeed — defer ran the Release.
	l, ok, err := lc.Acquire(ctx, "test:lock")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, l.Release(ctx))
}

// LockerWithValue smoke test — generic alternative for return values.
func TestLockerWithValue(t *testing.T) {
	_, client := setupRedis(t)
	ctx := context.Background()
	lc := redislock.NewLocker(client)

	got, err := redislock.LockerWithValue(ctx, lc, "test:lock", func(_ context.Context) (int, error) {
		return 42, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 42, got)
}

// --- Stateful Lock.Release semantic guarantee ---

func TestLock_ReleaseAfterTTLExpiryReturnsErrLockLost(t *testing.T) {
	mr, client := setupRedis(t)
	ctx := context.Background()

	l := redislock.New(client, "test:lock", redislock.WithTTL(1*time.Second))
	ok, err := l.Acquire(ctx)
	require.NoError(t, err)
	require.True(t, ok)

	mr.FastForward(2 * time.Second)
	require.NoError(t, mr.Set("test:lock", "someone-else"))

	relErr := l.Release(ctx)
	assert.ErrorIs(t, relErr, lock.ErrLockLost)
}

func TestNew_NilClientPanics(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "redislock: New requires a non-nil Redis client", func() {
		_ = redislock.New(nil, "test:lock")
	})
}

func TestNewLocker_NilClientPanics(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "redislock: NewLocker requires a non-nil Redis client", func() {
		_ = redislock.NewLocker(nil)
	})
}
