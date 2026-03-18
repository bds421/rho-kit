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
)

func newTestClient(t *testing.T) goredis.UniversalClient {
	t.Helper()
	mr := miniredis.RunT(t)
	return goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
}

func TestNewRedisCache_InvalidName(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	_, err := NewRedisCache(client, "")
	assert.Error(t, err)
}

func TestRedisCache_GetMiss(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewRedisCache(client, "test")
	require.NoError(t, err)

	_, getErr := rc.Get(context.Background(), "nonexistent")
	assert.ErrorIs(t, getErr, sharedcache.ErrCacheMiss)
}

func TestRedisCache_SetAndGet(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewRedisCache(client, "test")
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, rc.Set(ctx, "key1", []byte("value1"), time.Minute))

	val, getErr := rc.Get(ctx, "key1")
	require.NoError(t, getErr)
	assert.Equal(t, []byte("value1"), val)
}

func TestRedisCache_Delete(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewRedisCache(client, "test")
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, rc.Set(ctx, "del-key", []byte("value"), time.Minute))
	require.NoError(t, rc.Delete(ctx, "del-key"))

	_, getErr := rc.Get(ctx, "del-key")
	assert.ErrorIs(t, getErr, sharedcache.ErrCacheMiss)
}

func TestRedisCache_Exists(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewRedisCache(client, "test")
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, rc.Set(ctx, "exists-key", []byte("value"), time.Minute))

	exists, existsErr := rc.Exists(ctx, "exists-key")
	require.NoError(t, existsErr)
	assert.True(t, exists)

	exists, existsErr = rc.Exists(ctx, "no-key")
	require.NoError(t, existsErr)
	assert.False(t, exists)
}

func TestRedisCache_Set_NegativeTTL(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewRedisCache(client, "test")
	require.NoError(t, err)

	err = rc.Set(context.Background(), "key", []byte("val"), -time.Second)
	assert.Error(t, err)
}

func TestRedisCache_Set_ExceedsMaxValueSize(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewRedisCache(client, "test", WithCacheMaxValueSize(10))
	require.NoError(t, err)

	err = rc.Set(context.Background(), "key", make([]byte, 20), time.Minute)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds max")
}

func TestRedisCache_InvalidKey(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewRedisCache(client, "test")
	require.NoError(t, err)
	ctx := context.Background()

	_, getErr := rc.Get(ctx, "")
	assert.Error(t, getErr)

	setErr := rc.Set(ctx, "", []byte("val"), time.Minute)
	assert.Error(t, setErr)

	delErr := rc.Delete(ctx, "")
	assert.Error(t, delErr)

	_, existsErr := rc.Exists(ctx, "")
	assert.Error(t, existsErr)
}

func TestWithCacheMaxValueSize_IgnoresNegative(t *testing.T) {
	rc := &RedisCache{maxValueSize: defaultMaxValueSize}
	WithCacheMaxValueSize(-1)(rc)
	assert.Equal(t, defaultMaxValueSize, rc.maxValueSize)
}

func TestWithCacheMaxValueSize_ZeroDisablesLimit(t *testing.T) {
	rc := &RedisCache{maxValueSize: defaultMaxValueSize}
	WithCacheMaxValueSize(0)(rc)
	assert.Equal(t, 0, rc.maxValueSize)
}
