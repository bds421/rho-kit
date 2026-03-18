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
