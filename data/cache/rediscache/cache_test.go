package rediscache

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sharedcache "github.com/bds421/rho-kit/data/v2/cache"
)

func newTestClient(t *testing.T) goredis.UniversalClient {
	t.Helper()
	mr := miniredis.RunT(t)
	return goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
}

// TestNewCache_NilClientPanics verifies the constructor fails fast
// rather than letting a miswired cache dereference nil on first use.
func TestNewCache_NilClientPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil client")
		}
	}()
	_, _ = NewCache(nil, "test")
}

func TestNewCache_InvalidName(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	_, err := NewCache(client, "")
	assert.Error(t, err)
}

func TestNewCache_RejectsNilOption(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	_, _ = NewCache(client, "test", nil)
}

func TestCache_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()

	for name, rc := range map[string]*Cache{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := rc.Get(ctx, "key")
			assert.ErrorIs(t, err, sharedcache.ErrInvalidCache)

			err = rc.Set(ctx, "key", []byte("value"), time.Minute)
			assert.ErrorIs(t, err, sharedcache.ErrInvalidCache)

			_, err = rc.MGet(ctx, []string{"key"})
			assert.ErrorIs(t, err, sharedcache.ErrInvalidCache)

			err = rc.MSet(ctx, map[string][]byte{"key": []byte("value")}, time.Minute)
			assert.ErrorIs(t, err, sharedcache.ErrInvalidCache)

			ok, err := rc.SetNX(ctx, "key", []byte("value"), time.Minute)
			assert.False(t, ok)
			assert.ErrorIs(t, err, sharedcache.ErrInvalidCache)

			err = rc.Delete(ctx, "key")
			assert.ErrorIs(t, err, sharedcache.ErrInvalidCache)

			exists, err := rc.Exists(ctx, "key")
			assert.False(t, exists)
			assert.ErrorIs(t, err, sharedcache.ErrInvalidCache)
		})
	}
}

func TestCache_GetMiss(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewCache(client, "test")
	require.NoError(t, err)

	_, getErr := rc.Get(context.Background(), "nonexistent")
	assert.ErrorIs(t, getErr, sharedcache.ErrCacheMiss)
}

func TestCache_SetAndGet(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewCache(client, "test")
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, rc.Set(ctx, "key1", []byte("value1"), time.Minute))

	val, getErr := rc.Get(ctx, "key1")
	require.NoError(t, getErr)
	assert.Equal(t, []byte("value1"), val)
}

func TestCache_Delete(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewCache(client, "test")
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, rc.Set(ctx, "del-key", []byte("value"), time.Minute))
	require.NoError(t, rc.Delete(ctx, "del-key"))

	_, getErr := rc.Get(ctx, "del-key")
	assert.ErrorIs(t, getErr, sharedcache.ErrCacheMiss)
}

func TestCache_Exists(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewCache(client, "test")
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

func TestCache_NegativeTTLDoesNotReflectValue(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewCache(client, "test")
	require.NoError(t, err)

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "set",
			run: func() error {
				return rc.Set(context.Background(), "key", []byte("val"), -time.Second)
			},
		},
		{
			name: "mset",
			run: func() error {
				return rc.MSet(context.Background(), map[string][]byte{"key": []byte("val")}, -time.Second)
			},
		},
		{
			name: "setnx",
			run: func() error {
				_, err := rc.SetNX(context.Background(), "key", []byte("val"), -time.Second)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "TTL must not be negative")
			assert.NotContains(t, err.Error(), "-1s")
		})
	}
}

func TestCache_Set_ExceedsMaxValueSize(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewCache(client, "test", WithCacheMaxValueSize(10))
	require.NoError(t, err)

	err = rc.Set(context.Background(), "key", make([]byte, 20), time.Minute)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")
	assert.NotContains(t, err.Error(), "10")
	assert.NotContains(t, err.Error(), "20")
}

func TestCache_MSet_ExceedsMaxValueSizeDoesNotReflectKey(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewCache(client, "test", WithCacheMaxValueSize(10))
	require.NoError(t, err)

	err = rc.MSet(context.Background(), map[string][]byte{
		"secret-token-key": make([]byte, 20),
	}, time.Minute)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")
	assert.NotContains(t, err.Error(), "secret-token-key")
	assert.NotContains(t, err.Error(), "10")
	assert.NotContains(t, err.Error(), "20")
}

func oversizedRedisKeysForTest() []string {
	keys := make([]string, sharedcache.MaxBulkKeys+1)
	for i := range keys {
		keys[i] = "key-" + strconv.Itoa(i)
	}
	return keys
}

func oversizedRedisItemsForTest() map[string][]byte {
	items := make(map[string][]byte, sharedcache.MaxBulkKeys+1)
	for i := 0; i <= sharedcache.MaxBulkKeys; i++ {
		items["key-"+strconv.Itoa(i)] = []byte("value")
	}
	return items
}

func TestCache_BulkOperationsRejectOversizedBatches(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewCache(client, "test")
	require.NoError(t, err)

	_, err = rc.MGet(context.Background(), oversizedRedisKeysForTest())
	assert.ErrorIs(t, err, sharedcache.ErrBulkTooLarge)

	err = rc.MSet(context.Background(), oversizedRedisItemsForTest(), time.Minute)
	assert.ErrorIs(t, err, sharedcache.ErrBulkTooLarge)
}

func TestCache_InvalidKey(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewCache(client, "test")
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

func TestWithCacheMaxValueSize_PanicsOnNegative(t *testing.T) {
	assert.Panics(t, func() {
		WithCacheMaxValueSize(-1)
	})
}

func TestWithCacheMaxValueSize_ZeroDisablesLimit(t *testing.T) {
	rc := &Cache{maxValueSize: defaultMaxValueSize}
	WithCacheMaxValueSize(0)(rc)
	assert.Equal(t, 0, rc.maxValueSize)
}
