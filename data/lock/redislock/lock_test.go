package redislock_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	redislock "github.com/bds421/rho-kit/data/lock/redislock/v2"
	"github.com/bds421/rho-kit/data/v2/lock"
)

func setupRedis(t *testing.T) (*miniredis.Miniredis, redis.UniversalClient) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

func TestOptions_PanicOnInvalidValues(t *testing.T) {
	for name, fn := range map[string]func(){
		"WithTTL zero":       func() { redislock.WithTTL(0) },
		"WithTTL negative":   func() { redislock.WithTTL(-time.Second) },
		"WithRetry zero":     func() { redislock.WithRetry(0, 1) },
		"WithRetry negative": func() { redislock.WithRetry(-time.Second, 1) },
		"WithRetry attempts": func() { redislock.WithRetry(time.Millisecond, -1) },
		"WithMaxWait zero":   func() { redislock.WithMaxWait(0) },
		"WithMaxWait negative": func() {
			redislock.WithMaxWait(-time.Second)
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.Panics(t, fn)
		})
	}
}

func TestNewLocker_PanicsOnNilOption(t *testing.T) {
	_, client := setupRedis(t)

	assert.Panics(t, func() {
		redislock.NewLocker(client, nil)
	})
}

func TestLocker_AcquireReleaseRoundTrip(t *testing.T) {
	_, client := setupRedis(t)
	ctx := context.Background()

	lc := redislock.NewLocker(client, redislock.WithTTL(2*time.Second))

	l, ok, err := lc.Acquire(ctx, "test:lock")
	require.NoError(t, err)
	require.True(t, ok)

	// Second Acquire on the same key returns (nil, false, nil) — fresh
	// handle internally, but the SETNX still fails because the key
	// exists.
	l2, ok2, err := lc.Acquire(ctx, "test:lock")
	require.NoError(t, err)
	assert.False(t, ok2)
	assert.Nil(t, l2)

	require.NoError(t, l.Release(ctx))

	l3, ok3, err := lc.Acquire(ctx, "test:lock")
	require.NoError(t, err)
	require.True(t, ok3)
	require.NoError(t, l3.Release(ctx))
}

func TestLocker_RetryOption(t *testing.T) {
	_, client := setupRedis(t)
	ctx := context.Background()

	lc := redislock.NewLocker(client,
		redislock.WithTTL(50*time.Millisecond),
		redislock.WithRetry(20*time.Millisecond, 5),
	)

	// First Acquire succeeds.
	l1, ok, err := lc.Acquire(ctx, "test:lock")
	require.NoError(t, err)
	require.True(t, ok)

	// Releasing the first handle on a goroutine; second Acquire retries
	// until release.
	released := make(chan struct{})
	go func() {
		time.Sleep(40 * time.Millisecond)
		_ = l1.Release(ctx)
		close(released)
	}()

	l2, ok, err := lc.Acquire(ctx, "test:lock")
	require.NoError(t, err)
	require.True(t, ok)
	<-released
	require.NoError(t, l2.Release(ctx))
}

func TestLocker_RetryRespectsContextCancellation(t *testing.T) {
	_, client := setupRedis(t)

	lc := redislock.NewLocker(client,
		redislock.WithTTL(10*time.Second),
		redislock.WithRetry(50*time.Millisecond, 100),
	)
	ctx0 := context.Background()
	l, ok, err := lc.Acquire(ctx0, "test:lock")
	require.NoError(t, err)
	require.True(t, ok)
	defer func() { _ = l.Release(ctx0) }()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	_, ok2, err := lc.Acquire(ctx, "test:lock")
	assert.False(t, ok2)
	assert.Error(t, err)
}

func TestLocker_ReleaseOnlyByOwner(t *testing.T) {
	mr, client := setupRedis(t)
	ctx := context.Background()

	lc := redislock.NewLocker(client, redislock.WithTTL(10*time.Second))

	// Acquire the lock; the handle owns the key under its own token.
	l1, ok, err := lc.Acquire(ctx, "test:lock")
	require.NoError(t, err)
	require.True(t, ok)

	// Simulate another process reclaiming the key under a different
	// token (e.g. after a TTL race). The handle no longer owns the key.
	const foreignToken = "owned-by-someone-else"
	require.NoError(t, mr.Set("test:lock", foreignToken))

	// The original handle's Release must be token-fenced: it surfaces
	// ErrLockLost rather than silently succeeding...
	relErr := l1.Release(ctx)
	assert.ErrorIs(t, relErr, lock.ErrLockLost,
		"Release by a non-owner must surface ErrLockLost, not succeed")

	// ...and it must NOT delete the foreign holder's key. An unfenced
	// (unconditional DEL) release would clobber the other owner here.
	got, getErr := mr.Get("test:lock")
	require.NoError(t, getErr)
	assert.Equal(t, foreignToken, got,
		"Release must not delete a key owned by a different token")
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

func TestLocker_WithLockSuccess(t *testing.T) {
	_, client := setupRedis(t)
	ctx := context.Background()

	lc := redislock.NewLocker(client)

	called := false
	err := lc.WithLock(ctx, "test:lock", func(_ context.Context) error {
		called = true
		return nil
	})
	require.NoError(t, err)
	assert.True(t, called)

	// Lock should be released after WithLock returns.
	l, ok, err := lc.Acquire(ctx, "test:lock")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, l.Release(ctx))
}

func TestLocker_ContentionErrorsDoNotReflectLockKey(t *testing.T) {
	_, client := setupRedis(t)
	ctx := context.Background()
	lc := redislock.NewLocker(client)

	held, ok, err := lc.Acquire(ctx, "tenant:secret-token")
	require.NoError(t, err)
	require.True(t, ok)
	defer func() { _ = held.Release(ctx) }()

	err = lc.WithLock(ctx, "tenant:secret-token", func(context.Context) error {
		t.Fatal("contended WithLock must not run callback")
		return nil
	})
	require.Error(t, err)
	assert.NotContains(t, strings.ToLower(err.Error()), "secret-token")

	_, err = redislock.LockerWithValue(ctx, lc, "tenant:secret-token", func(context.Context) (int, error) {
		t.Fatal("contended LockerWithValue must not run callback")
		return 0, nil
	})
	require.Error(t, err)
	assert.NotContains(t, strings.ToLower(err.Error()), "secret-token")
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

func TestLocker_TTLExpiration(t *testing.T) {
	mr, client := setupRedis(t)
	ctx := context.Background()

	lc := redislock.NewLocker(client, redislock.WithTTL(100*time.Millisecond))
	l, ok, err := lc.Acquire(ctx, "test:lock")
	require.NoError(t, err)
	require.True(t, ok)
	defer func() { _ = l.Release(ctx) }()

	mr.FastForward(200 * time.Millisecond)

	// After TTL expiry the key is gone; a fresh Acquire succeeds.
	l2, ok, err := lc.Acquire(ctx, "test:lock")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, l2.Release(ctx))
}

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

func TestNewLocker_NilClientPanics(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "redislock: NewLocker requires a non-nil Redis client", func() {
		_ = redislock.NewLocker(nil)
	})
}
