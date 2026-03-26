package rediscache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sharedcache "github.com/bds421/rho-kit/data/cache"
	"github.com/bds421/rho-kit/infra/redis"
)

// testSetup creates a miniredis, a Connection, a RedisCache, and an in-memory fallback.
// The returned cleanup function stops the miniredis server.
type testEnv struct {
	mr       *miniredis.Miniredis
	conn     *redis.Connection
	primary  *RedisCache
	fallback *sharedcache.MemoryCache
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	mr := miniredis.RunT(t)
	conn, err := redis.Connect(
		&goredis.Options{Addr: mr.Addr()},
		redis.WithHealthInterval(50*time.Millisecond),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := conn.Client()
	primary, err := NewRedisCache(client, "test-degraded")
	require.NoError(t, err)

	fallback, err2 := sharedcache.NewMemoryCache()
	require.NoError(t, err2)
	t.Cleanup(func() { _ = fallback.Close() })

	return &testEnv{
		mr:       mr,
		conn:     conn,
		primary:  primary,
		fallback: fallback,
	}
}

func TestNewDegradedCache_PanicsOnNilPrimary(t *testing.T) {
	mr := miniredis.RunT(t)
	conn, err := redis.Connect(&goredis.Options{Addr: mr.Addr()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	assert.Panics(t, func() {
		NewDegradedCache(nil, nil, conn)
	})
}

func TestNewDegradedCache_PanicsOnNilConnection(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	primary, err := NewRedisCache(client, "test")
	require.NoError(t, err)

	assert.Panics(t, func() {
		NewDegradedCache(primary, nil, nil)
	})
}

func TestDegradedCache_DefaultPolicy(t *testing.T) {
	env := newTestEnv(t)
	dc := NewDegradedCache(env.primary, env.fallback, env.conn)
	assert.Equal(t, "passthrough", dc.Policy())
}

func TestDegradedCache_WithDegradationPolicy(t *testing.T) {
	env := newTestEnv(t)
	dc := NewDegradedCache(env.primary, env.fallback, env.conn,
		WithDegradationPolicy(redis.FailFastPolicy{}),
	)
	assert.Equal(t, "fail-fast", dc.Policy())
}

func TestDegradedCache_WithDegradationPolicy_NilIgnored(t *testing.T) {
	env := newTestEnv(t)
	dc := NewDegradedCache(env.primary, env.fallback, env.conn,
		WithDegradationPolicy(nil),
	)
	assert.Equal(t, "passthrough", dc.Policy())
}

func TestDegradedCache_Healthy_Get(t *testing.T) {
	env := newTestEnv(t)
	dc := NewDegradedCache(env.primary, env.fallback, env.conn)
	ctx := context.Background()

	require.NoError(t, dc.Set(ctx, "key1", []byte("value1"), time.Minute))

	val, err := dc.Get(ctx, "key1")
	require.NoError(t, err)
	assert.Equal(t, []byte("value1"), val)
}

func TestDegradedCache_Healthy_GetMiss(t *testing.T) {
	env := newTestEnv(t)
	dc := NewDegradedCache(env.primary, env.fallback, env.conn)

	_, err := dc.Get(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, sharedcache.ErrCacheMiss)
}

func TestDegradedCache_Healthy_Delete(t *testing.T) {
	env := newTestEnv(t)
	dc := NewDegradedCache(env.primary, env.fallback, env.conn)
	ctx := context.Background()

	require.NoError(t, dc.Set(ctx, "del-key", []byte("val"), time.Minute))
	require.NoError(t, dc.Delete(ctx, "del-key"))

	_, err := dc.Get(ctx, "del-key")
	assert.ErrorIs(t, err, sharedcache.ErrCacheMiss)
}

func TestDegradedCache_Healthy_Exists(t *testing.T) {
	env := newTestEnv(t)
	dc := NewDegradedCache(env.primary, env.fallback, env.conn)
	ctx := context.Background()

	require.NoError(t, dc.Set(ctx, "exists-key", []byte("val"), time.Minute))

	exists, err := dc.Exists(ctx, "exists-key")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = dc.Exists(ctx, "missing")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestDegradedCache_Degraded_Passthrough_WithFallback(t *testing.T) {
	env := newTestEnv(t)
	dc := NewDegradedCache(env.primary, env.fallback, env.conn)
	ctx := context.Background()

	// Pre-populate fallback and sync to ensure ristretto visibility.
	require.NoError(t, env.fallback.Set(ctx, "fb-key", []byte("fb-val"), time.Minute))
	env.fallback.Sync()

	// Stop miniredis to make connection unhealthy.
	env.mr.Close()
	// Wait for health check to detect the outage.
	require.Eventually(t, func() bool { return !env.conn.Healthy() }, 10*time.Second, 50*time.Millisecond)

	// Get from fallback.
	val, err := dc.Get(ctx, "fb-key")
	require.NoError(t, err)
	assert.Equal(t, []byte("fb-val"), val)

	// Set goes to fallback.
	require.NoError(t, dc.Set(ctx, "new-key", []byte("new-val"), time.Minute))
	env.fallback.Sync()
	val, err = env.fallback.Get(ctx, "new-key")
	require.NoError(t, err)
	assert.Equal(t, []byte("new-val"), val)

	// Exists checks fallback.
	exists, err := dc.Exists(ctx, "fb-key")
	require.NoError(t, err)
	assert.True(t, exists)

	// Delete on fallback.
	require.NoError(t, dc.Delete(ctx, "fb-key"))
	_, err = env.fallback.Get(ctx, "fb-key")
	assert.ErrorIs(t, err, sharedcache.ErrCacheMiss)
}

func TestDegradedCache_Degraded_Passthrough_NilFallback(t *testing.T) {
	env := newTestEnv(t)
	dc := NewDegradedCache(env.primary, nil, env.conn)
	ctx := context.Background()

	env.mr.Close()
	require.Eventually(t, func() bool { return !env.conn.Healthy() }, 10*time.Second, 50*time.Millisecond)

	// Get returns ErrCacheMiss when no fallback.
	_, err := dc.Get(ctx, "any-key")
	assert.ErrorIs(t, err, sharedcache.ErrCacheMiss)

	// Set is a no-op (passthrough, no fallback).
	assert.NoError(t, dc.Set(ctx, "key", []byte("val"), time.Minute))

	// Delete is a no-op.
	assert.NoError(t, dc.Delete(ctx, "key"))

	// Exists returns false.
	exists, err := dc.Exists(ctx, "key")
	assert.NoError(t, err)
	assert.False(t, exists)
}

func TestDegradedCache_Degraded_FailFast(t *testing.T) {
	env := newTestEnv(t)
	dc := NewDegradedCache(env.primary, env.fallback, env.conn,
		WithDegradationPolicy(redis.FailFastPolicy{}),
	)
	ctx := context.Background()

	env.mr.Close()
	require.Eventually(t, func() bool { return !env.conn.Healthy() }, 10*time.Second, 50*time.Millisecond)

	_, err := dc.Get(ctx, "key")
	assert.ErrorIs(t, err, redis.ErrUnavailable)

	err = dc.Set(ctx, "key", []byte("val"), time.Minute)
	assert.ErrorIs(t, err, redis.ErrUnavailable)

	err = dc.Delete(ctx, "key")
	assert.ErrorIs(t, err, redis.ErrUnavailable)

	_, err = dc.Exists(ctx, "key")
	assert.ErrorIs(t, err, redis.ErrUnavailable)
}

func TestDegradedCache_InvalidKey(t *testing.T) {
	env := newTestEnv(t)
	dc := NewDegradedCache(env.primary, env.fallback, env.conn)
	ctx := context.Background()

	_, err := dc.Get(ctx, "")
	assert.Error(t, err)

	err = dc.Set(ctx, "", []byte("val"), time.Minute)
	assert.Error(t, err)

	err = dc.Delete(ctx, "")
	assert.Error(t, err)

	_, err = dc.Exists(ctx, "")
	assert.Error(t, err)
}

func TestDegradedCache_Healthy_ReportsTrue(t *testing.T) {
	env := newTestEnv(t)
	dc := NewDegradedCache(env.primary, env.fallback, env.conn)
	assert.True(t, dc.Healthy())
}

func TestDegradedCache_Unhealthy_ReportsFalse(t *testing.T) {
	env := newTestEnv(t)
	dc := NewDegradedCache(env.primary, env.fallback, env.conn)

	env.mr.Close()
	require.Eventually(t, func() bool { return !env.conn.Healthy() }, 10*time.Second, 50*time.Millisecond)

	assert.False(t, dc.Healthy())
}

func TestDegradedCache_ImplementsCacheInterface(t *testing.T) {
	var _ sharedcache.Cache = (*DegradedCache)(nil)
}
