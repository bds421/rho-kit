package redislock_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/lock/redislock"
	"github.com/bds421/rho-kit/infra/redis"
)

func setupDegradedRedis(t *testing.T) (*miniredis.Miniredis, *redis.Connection) {
	t.Helper()
	mr := miniredis.RunT(t)
	conn, err := redis.Connect(
		&goredis.Options{Addr: mr.Addr()},
		redis.WithHealthInterval(50*time.Millisecond),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return mr, conn
}

func TestDegradedLock_AcquireAndRelease_Healthy(t *testing.T) {
	_, conn := setupDegradedRedis(t)
	ctx := context.Background()

	dl := redislock.NewDegraded(conn, "test:degraded", redis.FailFastPolicy{})

	acquired, err := dl.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)

	err = dl.Release(ctx)
	require.NoError(t, err)

	// Should be re-acquirable after release.
	acquired, err = dl.Acquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)
}

func TestDegradedLock_FailFast_WhenUnhealthy(t *testing.T) {
	mr, conn := setupDegradedRedis(t)
	ctx := context.Background()

	dl := redislock.NewDegraded(conn, "test:degraded:failfast", redis.FailFastPolicy{})

	// Stop Redis to make connection unhealthy.
	mr.Close()
	// Wait for health check to detect the failure.
	require.Eventually(t, func() bool {
		return !conn.Healthy()
	}, 5*time.Second, 10*time.Millisecond)

	acquired, err := dl.Acquire(ctx)
	assert.False(t, acquired)
	assert.ErrorIs(t, err, redislock.ErrUnavailable)
}

func TestDegradedLock_Passthrough_WhenUnhealthy(t *testing.T) {
	mr, conn := setupDegradedRedis(t)
	ctx := context.Background()

	dl := redislock.NewDegraded(conn, "test:degraded:passthrough", redis.PassthroughPolicy{})

	// Stop Redis.
	mr.Close()
	require.Eventually(t, func() bool {
		return !conn.Healthy()
	}, 5*time.Second, 10*time.Millisecond)

	// With passthrough, the operation is delegated to the underlying lock,
	// which will fail with a Redis connection error (not ErrUnavailable).
	acquired, err := dl.Acquire(ctx)
	assert.False(t, acquired)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, redislock.ErrUnavailable)
}

func TestDegradedLock_WithLock_FailFast(t *testing.T) {
	mr, conn := setupDegradedRedis(t)
	ctx := context.Background()

	dl := redislock.NewDegraded(conn, "test:degraded:withlock", redis.FailFastPolicy{})

	mr.Close()
	require.Eventually(t, func() bool {
		return !conn.Healthy()
	}, 5*time.Second, 10*time.Millisecond)

	called := false
	err := dl.WithLock(ctx, func(_ context.Context) error {
		called = true
		return nil
	})

	assert.ErrorIs(t, err, redislock.ErrUnavailable)
	assert.False(t, called, "fn should not be called when Redis is unavailable")
}

func TestDegradedLock_WithLock_Healthy(t *testing.T) {
	_, conn := setupDegradedRedis(t)
	ctx := context.Background()

	dl := redislock.NewDegraded(conn, "test:degraded:withlock:healthy", redis.FailFastPolicy{})

	called := false
	err := dl.WithLock(ctx, func(_ context.Context) error {
		called = true
		return nil
	})

	require.NoError(t, err)
	assert.True(t, called)
}

func TestDegradedLock_Extend_FailFast(t *testing.T) {
	mr, conn := setupDegradedRedis(t)
	ctx := context.Background()

	dl := redislock.NewDegraded(conn, "test:degraded:extend", redis.FailFastPolicy{})

	acquired, err := dl.Acquire(ctx)
	require.NoError(t, err)
	require.True(t, acquired)

	mr.Close()
	require.Eventually(t, func() bool {
		return !conn.Healthy()
	}, 5*time.Second, 10*time.Millisecond)

	_, err = dl.Extend(ctx)
	assert.ErrorIs(t, err, redislock.ErrUnavailable)
}

func TestDegradedLock_Release_FailFast(t *testing.T) {
	mr, conn := setupDegradedRedis(t)
	ctx := context.Background()

	dl := redislock.NewDegraded(conn, "test:degraded:release", redis.FailFastPolicy{})

	acquired, err := dl.Acquire(ctx)
	require.NoError(t, err)
	require.True(t, acquired)

	mr.Close()
	require.Eventually(t, func() bool {
		return !conn.Healthy()
	}, 5*time.Second, 10*time.Millisecond)

	err = dl.Release(ctx)
	assert.ErrorIs(t, err, redislock.ErrUnavailable)
}

func TestDegradedLock_TTL(t *testing.T) {
	_, conn := setupDegradedRedis(t)

	dl := redislock.NewDegraded(conn, "test:ttl", redis.FailFastPolicy{},
		redislock.WithTTL(15*time.Second),
	)

	assert.Equal(t, 15*time.Second, dl.TTL())
}

func TestNewDegraded_PanicsOnNilConnection(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil connection")
		}
	}()
	redislock.NewDegraded(nil, "key", redis.FailFastPolicy{})
}

func TestNewDegraded_PanicsOnNilPolicy(t *testing.T) {
	_, conn := setupDegradedRedis(t)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil policy")
		}
	}()
	redislock.NewDegraded(conn, "key", nil)
}
