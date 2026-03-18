//go:build integration

package rediscache

import (
	"context"
	"errors"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sharedcache "github.com/bds421/rho-kit/data/cache"
	"github.com/bds421/rho-kit/infra/redis"
	"github.com/bds421/rho-kit/infra/redis/redistest"
)

type testUser struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

func redisClient(t *testing.T) goredis.UniversalClient {
	t.Helper()
	url := redistest.Start(t)
	opts, err := goredis.ParseURL(url)
	require.NoError(t, err)
	conn, err := redis.Connect(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn.Client()
}

func TestRedisCache_SetAndGet(t *testing.T) {
	client := redisClient(t)

	rc, err := NewRedisCache(client, "test")
	require.NoError(t, err)
	ctx := context.Background()

	err = rc.Set(ctx, "test:key1", []byte("value1"), time.Minute)
	require.NoError(t, err)

	val, err := rc.Get(ctx, "test:key1")
	require.NoError(t, err)
	assert.Equal(t, []byte("value1"), val)
}

func TestRedisCache_GetMiss(t *testing.T) {
	client := redisClient(t)

	rc, err := NewRedisCache(client, "test")
	require.NoError(t, err)

	_, getErr := rc.Get(context.Background(), "test:nonexistent")
	assert.True(t, errors.Is(getErr, sharedcache.ErrCacheMiss))
}

func TestRedisCache_Expiration(t *testing.T) {
	client := redisClient(t)

	rc, err := NewRedisCache(client, "test")
	require.NoError(t, err)
	ctx := context.Background()

	err = rc.Set(ctx, "test:expiring", []byte("value"), 100*time.Millisecond)
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	_, getErr := rc.Get(ctx, "test:expiring")
	assert.True(t, errors.Is(getErr, sharedcache.ErrCacheMiss))
}

func TestRedisCache_Delete(t *testing.T) {
	client := redisClient(t)

	rc, err := NewRedisCache(client, "test")
	require.NoError(t, err)
	ctx := context.Background()

	rc.Set(ctx, "test:del", []byte("value"), time.Minute)
	err = rc.Delete(ctx, "test:del")
	require.NoError(t, err)

	_, getErr := rc.Get(ctx, "test:del")
	assert.True(t, errors.Is(getErr, sharedcache.ErrCacheMiss))
}

func TestRedisCache_Exists(t *testing.T) {
	client := redisClient(t)

	rc, err := NewRedisCache(client, "test")
	require.NoError(t, err)
	ctx := context.Background()

	rc.Set(ctx, "test:exists", []byte("value"), time.Minute)

	exists, existsErr := rc.Exists(ctx, "test:exists")
	require.NoError(t, existsErr)
	assert.True(t, exists)

	exists, existsErr = rc.Exists(ctx, "test:no")
	require.NoError(t, existsErr)
	assert.False(t, exists)
}

// --- TypedCache with Redis backend ---

func TestTypedCache_Redis_SetAndGet(t *testing.T) {
	client := redisClient(t)

	rc, err := NewRedisCache(client, "typed-test")
	require.NoError(t, err)
	typed, typedErr := sharedcache.NewTypedCache[testUser](rc, "user:")
	require.NoError(t, typedErr)
	ctx := context.Background()

	user := testUser{Name: "Alice", Email: "alice@test.com"}
	err = typed.Set(ctx, "1", user, time.Minute)
	require.NoError(t, err)

	got, getErr := typed.Get(ctx, "1")
	require.NoError(t, getErr)
	assert.Equal(t, user, got)
}
