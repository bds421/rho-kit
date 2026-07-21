package redislock_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/lock/redislock/v2"
	"github.com/bds421/rho-kit/infra/redis/v2"
)

func setupDegradedRedis(t *testing.T) (*miniredis.Miniredis, *redis.Connection) {
	t.Helper()
	mr := miniredis.RunT(t)
	conn, err := redis.Connect(
		&goredis.Options{
			Addr:               mr.Addr(),
			MaxRetries:         -1,
			DialerRetries:      1,
			DialerRetryTimeout: time.Millisecond,
		},
		redis.WithHealthInterval(50*time.Millisecond),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return mr, conn
}

func TestDegradedLocker_AcquireAndRelease_Healthy(t *testing.T) {
	_, conn := setupDegradedRedis(t)
	ctx := context.Background()

	dl := redislock.NewDegradedLocker(conn, redis.FailFastPolicy{})

	l, ok, err := dl.Acquire(ctx, "test:degraded")
	require.NoError(t, err)
	assert.True(t, ok)

	require.NoError(t, l.Release(ctx))

	// Should be re-acquirable after release.
	l2, ok, err := dl.Acquire(ctx, "test:degraded")
	require.NoError(t, err)
	assert.True(t, ok)
	require.NoError(t, l2.Release(ctx))
}

func TestDegradedLocker_FailFast_WhenUnhealthy(t *testing.T) {
	mr, conn := setupDegradedRedis(t)
	ctx := context.Background()

	dl := redislock.NewDegradedLocker(conn, redis.FailFastPolicy{})

	mr.Close()
	require.Eventually(t, func() bool {
		return !conn.Healthy()
	}, 5*time.Second, 10*time.Millisecond)

	_, ok, err := dl.Acquire(ctx, "test:degraded:failfast")
	assert.False(t, ok)
	assert.ErrorIs(t, err, redislock.ErrUnavailable)
}

func TestDegradedLocker_Passthrough_WhenUnhealthy(t *testing.T) {
	mr, conn := setupDegradedRedis(t)
	ctx := context.Background()

	dl := redislock.NewDegradedLocker(conn, redis.PassthroughPolicy{})

	mr.Close()
	require.Eventually(t, func() bool {
		return !conn.Healthy()
	}, 5*time.Second, 10*time.Millisecond)

	_, ok, err := dl.Acquire(ctx, "test:degraded:passthrough")
	assert.False(t, ok)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, redislock.ErrUnavailable)
}

func TestDegradedLocker_WithLock_FailFast(t *testing.T) {
	mr, conn := setupDegradedRedis(t)
	ctx := context.Background()

	dl := redislock.NewDegradedLocker(conn, redis.FailFastPolicy{})

	mr.Close()
	require.Eventually(t, func() bool {
		return !conn.Healthy()
	}, 5*time.Second, 10*time.Millisecond)

	called := false
	err := dl.WithLock(ctx, "test:degraded:withlock", func(_ context.Context) error {
		called = true
		return nil
	})

	assert.ErrorIs(t, err, redislock.ErrUnavailable)
	assert.False(t, called, "fn should not be called when Redis is unavailable")
}

func TestDegradedLocker_WithLock_Healthy(t *testing.T) {
	_, conn := setupDegradedRedis(t)
	ctx := context.Background()

	dl := redislock.NewDegradedLocker(conn, redis.FailFastPolicy{})

	called := false
	err := dl.WithLock(ctx, "test:degraded:withlock:healthy", func(_ context.Context) error {
		called = true
		return nil
	})

	require.NoError(t, err)
	assert.True(t, called)
}

func TestNewDegradedLocker_PanicsOnNilConnection(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil connection")
		}
	}()
	redislock.NewDegradedLocker(nil, redis.FailFastPolicy{})
}

func TestNewDegradedLocker_PanicsOnNilPolicy(t *testing.T) {
	_, conn := setupDegradedRedis(t)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil policy")
		}
	}()
	redislock.NewDegradedLocker(conn, nil)
}
