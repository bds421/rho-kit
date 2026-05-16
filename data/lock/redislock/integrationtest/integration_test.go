//go:build integration

package integrationtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	redislock "github.com/bds421/rho-kit/data/lock/redislock/v2"
	"github.com/bds421/rho-kit/infra/redis/redistest/v2"
	"github.com/bds421/rho-kit/infra/redis/v2"
)

func redisClient(t *testing.T) goredis.UniversalClient {
	t.Helper()
	url := redistest.Start(t)
	opts, err := goredis.ParseURL(url)
	require.NoError(t, err)
	conn, err := redis.Connect(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	t.Cleanup(func() { redistest.FlushDB(t) })
	return conn.Client()
}

func uniqueKey(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s:%d", prefix, time.Now().UnixNano())
}

// Acquire-then-Release happy path: one caller gets the lock, a contender is
// blocked, then the contender succeeds after the first holder Releases.
func TestLocker_AcquireBlocksContenderUntilRelease(t *testing.T) {
	client := redisClient(t)
	lc := redislock.NewLocker(client,
		redislock.WithTTL(5*time.Second),
		// Tight retry so contender attempt resolves quickly after release.
		redislock.WithRetry(20*time.Millisecond, 50),
	)

	ctx := context.Background()
	key := uniqueKey(t, "lock")

	first, ok, err := lc.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, ok, "first Acquire must succeed against fresh key")
	require.NotNil(t, first)

	// Contender with a short retry budget should give up.
	lcShort := redislock.NewLocker(client,
		redislock.WithTTL(5*time.Second),
		redislock.WithRetry(10*time.Millisecond, 2),
	)
	contender, ok, err := lcShort.Acquire(ctx, key)
	require.NoError(t, err)
	assert.False(t, ok, "contender must fail while first lock is held")
	assert.Nil(t, contender)

	require.NoError(t, first.Release(ctx))

	// After release, the contender (with normal retry budget) should win.
	second, ok, err := lc.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, ok, "second Acquire after Release must succeed")
	require.NoError(t, second.Release(ctx))
}

// WithLock runs the function under exclusive access and releases on return.
func TestLocker_WithLockRunsFunctionExclusively(t *testing.T) {
	client := redisClient(t)
	lc := redislock.NewLocker(client,
		redislock.WithTTL(5*time.Second),
		redislock.WithRetry(20*time.Millisecond, 50),
	)

	ctx := context.Background()
	key := uniqueKey(t, "withlock")

	called := false
	err := lc.WithLock(ctx, key, func(_ context.Context) error {
		called = true
		// Nested Acquire from a contender must fail while we hold the lock.
		lcShort := redislock.NewLocker(client,
			redislock.WithTTL(5*time.Second),
			redislock.WithRetry(5*time.Millisecond, 2),
		)
		_, ok, err := lcShort.Acquire(ctx, key)
		require.NoError(t, err)
		assert.False(t, ok, "contender must not acquire while WithLock holds the key")
		return nil
	})
	require.NoError(t, err)
	assert.True(t, called)

	// After WithLock returns, the key is releasable — a fresh Acquire wins.
	post, ok, err := lc.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, ok, "WithLock must release on return so subsequent Acquire wins")
	require.NoError(t, post.Release(ctx))
}

// A lock's TTL expires automatically, letting another caller acquire it.
func TestLocker_TTLExpiryReleasesLock(t *testing.T) {
	client := redisClient(t)
	lc := redislock.NewLocker(client,
		redislock.WithTTL(500*time.Millisecond),
		redislock.WithRetry(50*time.Millisecond, 1),
	)

	ctx := context.Background()
	key := uniqueKey(t, "ttl")

	first, ok, err := lc.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)

	// Don't release — let the TTL expire.
	time.Sleep(700 * time.Millisecond)

	second, ok, err := lc.Acquire(ctx, key)
	require.NoError(t, err)
	assert.True(t, ok, "TTL-expired lock must be re-acquirable")
	if second != nil {
		_ = second.Release(ctx)
	}
	// first.Release on an expired lock is a no-op or returns no error;
	// we don't assert its behaviour beyond not panicking.
	_ = first.Release(ctx)
}
