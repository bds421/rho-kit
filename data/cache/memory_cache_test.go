package cache

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryCache_SetAndGet(t *testing.T) {
	cache := MustNewMemoryCache()
	ctx := context.Background()

	err := cache.Set(ctx, "key1", []byte("value1"), time.Minute)
	require.NoError(t, err)
	cache.Sync()

	val, err := cache.Get(ctx, "key1")
	require.NoError(t, err)
	assert.Equal(t, []byte("value1"), val)
}

func TestMemoryCache_GetMiss(t *testing.T) {
	cache := MustNewMemoryCache()
	ctx := context.Background()

	_, err := cache.Get(ctx, "nonexistent")
	assert.True(t, errors.Is(err, ErrCacheMiss))
}

func TestMemoryCache_Expiration(t *testing.T) {
	cache := MustNewMemoryCache()
	ctx := context.Background()

	err := cache.Set(ctx, "expiring", []byte("value"), 50*time.Millisecond)
	require.NoError(t, err)
	cache.Sync()

	// Should exist immediately.
	val, err := cache.Get(ctx, "expiring")
	require.NoError(t, err)
	assert.Equal(t, []byte("value"), val)

	// Wait for expiration.
	time.Sleep(100 * time.Millisecond)

	_, err = cache.Get(ctx, "expiring")
	assert.True(t, errors.Is(err, ErrCacheMiss))
}

func TestMemoryCache_ZeroTTL_NoExpiration(t *testing.T) {
	cache := MustNewMemoryCache()
	ctx := context.Background()

	err := cache.Set(ctx, "persistent", []byte("value"), 0)
	require.NoError(t, err)
	cache.Sync()

	// Should persist.
	val, err := cache.Get(ctx, "persistent")
	require.NoError(t, err)
	assert.Equal(t, []byte("value"), val)
}

func TestMemoryCache_Delete(t *testing.T) {
	cache := MustNewMemoryCache()
	ctx := context.Background()

	_ = cache.Set(ctx, "key", []byte("value"), time.Minute)
	cache.Sync()

	err := cache.Delete(ctx, "key")
	require.NoError(t, err)

	_, err = cache.Get(ctx, "key")
	assert.True(t, errors.Is(err, ErrCacheMiss))
}

func TestMemoryCache_Delete_NonExistent(t *testing.T) {
	cache := MustNewMemoryCache()
	ctx := context.Background()

	err := cache.Delete(ctx, "nonexistent")
	assert.NoError(t, err)
}

func TestMemoryCache_Exists(t *testing.T) {
	cache := MustNewMemoryCache()
	ctx := context.Background()

	_ = cache.Set(ctx, "key", []byte("value"), time.Minute)
	cache.Sync()

	exists, err := cache.Exists(ctx, "key")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = cache.Exists(ctx, "nonexistent")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestMemoryCache_Exists_Expired(t *testing.T) {
	cache := MustNewMemoryCache()
	ctx := context.Background()

	_ = cache.Set(ctx, "key", []byte("value"), 50*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	exists, err := cache.Exists(ctx, "key")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestMemoryCache_Overwrite(t *testing.T) {
	cache := MustNewMemoryCache()
	ctx := context.Background()

	_ = cache.Set(ctx, "key", []byte("v1"), time.Minute)
	cache.Sync()
	_ = cache.Set(ctx, "key", []byte("v2"), time.Minute)
	cache.Sync()

	val, err := cache.Get(ctx, "key")
	require.NoError(t, err)
	assert.Equal(t, []byte("v2"), val)
}

func TestMemoryCache_MaxSize_Eviction(t *testing.T) {
	cache := MustNewMemoryCache(WithMaxSize(3))
	ctx := context.Background()

	_ = cache.Set(ctx, "a", []byte("1"), time.Minute)
	_ = cache.Set(ctx, "b", []byte("2"), time.Minute)
	_ = cache.Set(ctx, "c", []byte("3"), time.Minute)
	_ = cache.Set(ctx, "d", []byte("4"), time.Minute) // triggers eviction
	cache.Sync()

	count := 0
	for _, key := range []string{"a", "b", "c", "d"} {
		exists, err := cache.Exists(ctx, key)
		require.NoError(t, err)
		if exists {
			count++
		}
	}
	assert.LessOrEqual(t, count, 3)
}

func TestMemoryCache_ExpiredEntryNotReturned(t *testing.T) {
	cache := MustNewMemoryCache()
	ctx := context.Background()

	require.NoError(t, cache.Set(ctx, "ephemeral", []byte("1"), 10*time.Millisecond))
	require.NoError(t, cache.Set(ctx, "durable", []byte("2"), time.Minute))

	// Wait for TTL to expire.
	time.Sleep(50 * time.Millisecond)

	// Expired entry must not be returned.
	_, err := cache.Get(ctx, "ephemeral")
	assert.True(t, errors.Is(err, ErrCacheMiss), "expired key should return ErrCacheMiss")

	// Non-expired entry should still be accessible.
	val, err := cache.Get(ctx, "durable")
	assert.NoError(t, err)
	assert.Equal(t, []byte("2"), val)
}

func TestMemoryCache_GetReturnsDefensiveCopy(t *testing.T) {
	cache := MustNewMemoryCache()
	ctx := context.Background()

	_ = cache.Set(ctx, "key", []byte("original"), time.Minute)
	cache.Sync()

	val, _ := cache.Get(ctx, "key")
	val[0] = 'X' // mutate the returned slice

	// Cached value should be unaffected.
	val2, _ := cache.Get(ctx, "key")
	assert.Equal(t, []byte("original"), val2)
}

func TestMemoryCache_SetStoresDefensiveCopy(t *testing.T) {
	cache := MustNewMemoryCache()
	ctx := context.Background()

	input := []byte("original")
	_ = cache.Set(ctx, "key", input, time.Minute)
	cache.Sync()

	input[0] = 'X' // mutate the input slice

	val, _ := cache.Get(ctx, "key")
	assert.Equal(t, []byte("original"), val)
}

func TestMemoryCache_CleanupInterval(t *testing.T) {
	cache := MustNewMemoryCache(WithCleanupInterval(50 * time.Millisecond))
	defer func() { _ = cache.Close() }()
	ctx := context.Background()

	_ = cache.Set(ctx, "short", []byte("value"), 30*time.Millisecond)
	_ = cache.Set(ctx, "long", []byte("value"), time.Minute)

	time.Sleep(150 * time.Millisecond)

	_, err := cache.Get(ctx, "short")
	assert.True(t, errors.Is(err, ErrCacheMiss))
	_, err = cache.Get(ctx, "long")
	assert.NoError(t, err)
}

func TestMemoryCache_ImplementsCacheInterface(t *testing.T) {
	var _ Cache = (*MemoryCache)(nil)
}

func TestMemoryCache_Get_EmptyKey(t *testing.T) {
	mc := MustNewMemoryCache()
	_, err := mc.Get(context.Background(), "")
	assert.ErrorIs(t, err, ErrKeyEmpty)
}

func TestMemoryCache_Set_EmptyKey(t *testing.T) {
	mc := MustNewMemoryCache()
	err := mc.Set(context.Background(), "", []byte("v"), time.Minute)
	assert.ErrorIs(t, err, ErrKeyEmpty)
}

func TestMemoryCache_Delete_EmptyKey(t *testing.T) {
	mc := MustNewMemoryCache()
	err := mc.Delete(context.Background(), "")
	assert.ErrorIs(t, err, ErrKeyEmpty)
}

func TestMemoryCache_Exists_EmptyKey(t *testing.T) {
	mc := MustNewMemoryCache()
	_, err := mc.Exists(context.Background(), "")
	assert.ErrorIs(t, err, ErrKeyEmpty)
}

func TestMemoryCache_Get_NullByteKey(t *testing.T) {
	mc := MustNewMemoryCache()
	_, err := mc.Get(context.Background(), "bad\x00key")
	assert.ErrorIs(t, err, ErrKeyInvalidChars)
}

func TestMemoryCache_CleanupStopsOnClose(t *testing.T) {
	mc := MustNewMemoryCache(WithCleanupInterval(50 * time.Millisecond))
	ctx := context.Background()

	_ = mc.Set(ctx, "key", []byte("value"), 30*time.Millisecond)
	_ = mc.Close()

	// After close, cleanup goroutine should have stopped. The expired
	// entry may or may not have been cleaned before close — what matters
	// is that Close doesn't panic or leak.
	time.Sleep(100 * time.Millisecond) // no goroutine leak
}

func TestMemoryCache_Set_NegativeTTL(t *testing.T) {
	mc := MustNewMemoryCache()
	err := mc.Set(context.Background(), "key", []byte("v"), -time.Second)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TTL must not be negative")
}

func TestWithMaxSize_IgnoresNonPositive(t *testing.T) {
	mc := MustNewMemoryCache(WithMaxSize(0))
	assert.Equal(t, 0, mc.maxSize)

	mc2 := MustNewMemoryCache(WithMaxSize(-1))
	assert.Equal(t, 0, mc2.maxSize) // default 0 (unlimited)

	mc3 := MustNewMemoryCache(WithMaxSize(5))
	assert.Equal(t, 5, mc3.maxSize)
}

func TestMemoryCache_Exists_LazyEvictsExpired(t *testing.T) {
	mc := MustNewMemoryCache()
	ctx := context.Background()

	_ = mc.Set(ctx, "expiring", []byte("v"), 30*time.Millisecond)
	time.Sleep(60 * time.Millisecond)

	exists, err := mc.Exists(ctx, "expiring")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestMemoryCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	cache := MustNewMemoryCache(WithMaxSize(100))
	ctx := context.Background()

	const goroutines = 20
	const opsPerGoroutine = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			for i := range opsPerGoroutine {
				key := fmt.Sprintf("key-%d-%d", id, i%10)
				val := []byte(fmt.Sprintf("val-%d-%d", id, i))

				// Set
				_ = cache.Set(ctx, key, val, time.Minute)

				// Get (may miss if another goroutine deleted it)
				_, _ = cache.Get(ctx, key)

				// Exists
				_, _ = cache.Exists(ctx, key)

				// Delete every 3rd iteration
				if i%3 == 0 {
					_ = cache.Delete(ctx, key)
				}
			}
		}(g)
	}

	wg.Wait()

	// If we get here without a race detector failure or panic, the
	// cache is safe for concurrent access.
}

func TestValidateKey(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		wantErr   error
		wantNoErr bool
	}{
		{"valid", "user:123:profile", nil, true},
		{"empty", "", ErrKeyEmpty, false},
		{"null byte", "bad\x00key", ErrKeyInvalidChars, false},
		{"newline", "bad\nkey", ErrKeyInvalidChars, false},
		{"carriage return", "bad\rkey", ErrKeyInvalidChars, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateKey(tt.key)
			if tt.wantNoErr {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

func TestValidateKey_TooLong(t *testing.T) {
	longKey := strings.Repeat("x", MaxKeyLen+1)
	err := ValidateKey(longKey)
	assert.ErrorIs(t, err, ErrKeyTooLong)
}
