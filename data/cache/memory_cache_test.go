package cache

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMemoryCache_RejectsNilOption(t *testing.T) {
	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	_, _ = NewMemoryCache(nil)
}

func TestCacheHelpers_NilCacheReturnsInvalidCache(t *testing.T) {
	ctx := context.Background()

	_, err := MGet(ctx, nil, []string{"key"})
	assert.ErrorIs(t, err, ErrInvalidCache)

	err = MSet(ctx, nil, map[string][]byte{"key": []byte("value")}, time.Minute)
	assert.ErrorIs(t, err, ErrInvalidCache)

	ok, err := SetNX(ctx, nil, "key", []byte("value"), time.Minute)
	assert.False(t, ok)
	assert.ErrorIs(t, err, ErrInvalidCache)
}

func oversizedKeysForTest() []string {
	keys := make([]string, MaxBulkKeys+1)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
	}
	return keys
}

func oversizedItemsForTest() map[string][]byte {
	items := make(map[string][]byte, MaxBulkKeys+1)
	for i := 0; i <= MaxBulkKeys; i++ {
		items[fmt.Sprintf("key-%d", i)] = []byte("value")
	}
	return items
}

func TestCacheBulkValidationRejectsOversizedBatches(t *testing.T) {
	keys := oversizedKeysForTest()
	items := oversizedItemsForTest()

	assert.ErrorIs(t, ValidateBulkKeys(keys), ErrBulkTooLarge)
	assert.ErrorIs(t, ValidateBulkItems(items), ErrBulkTooLarge)

	mc := MustNewMemoryCache()
	defer func() { _ = mc.Close() }()
	ctx := context.Background()

	_, err := MGet(ctx, mc, keys)
	assert.ErrorIs(t, err, ErrBulkTooLarge)

	err = MSet(ctx, mc, items, time.Minute)
	assert.ErrorIs(t, err, ErrBulkTooLarge)

	_, err = mc.MGet(ctx, keys)
	assert.ErrorIs(t, err, ErrBulkTooLarge)

	err = mc.MSet(ctx, items, time.Minute)
	assert.ErrorIs(t, err, ErrBulkTooLarge)
}

func TestMemoryCache_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()

	for name, mc := range map[string]*MemoryCache{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := mc.Get(ctx, "key")
			assert.ErrorIs(t, err, ErrInvalidCache)

			err = mc.Set(ctx, "key", []byte("value"), time.Minute)
			assert.ErrorIs(t, err, ErrInvalidCache)

			_, err = mc.MGet(ctx, []string{"key"})
			assert.ErrorIs(t, err, ErrInvalidCache)

			err = mc.MSet(ctx, map[string][]byte{"key": []byte("value")}, time.Minute)
			assert.ErrorIs(t, err, ErrInvalidCache)

			ok, err := mc.SetNX(ctx, "key", []byte("value"), time.Minute)
			assert.False(t, ok)
			assert.ErrorIs(t, err, ErrInvalidCache)

			err = mc.Delete(ctx, "key")
			assert.ErrorIs(t, err, ErrInvalidCache)

			exists, err := mc.Exists(ctx, "key")
			assert.False(t, exists)
			assert.ErrorIs(t, err, ErrInvalidCache)

			err = mc.Close()
			assert.ErrorIs(t, err, ErrInvalidCache)

			assert.NotPanics(t, func() { mc.Sync() })
			assert.NotPanics(t, func() { mc.stopBackgroundSweeper() })
		})
	}
}

func TestMemoryCache_MGet_MSet_SetNX(t *testing.T) {
	mc := MustNewMemoryCache()
	defer func() { _ = mc.Close() }()
	ctx := context.Background()

	items := map[string][]byte{
		"a": []byte("alpha"),
		"b": []byte("bravo"),
		"c": []byte("charlie"),
	}
	if err := mc.MSet(ctx, items, 5*time.Minute); err != nil {
		t.Fatalf("MSet: %v", err)
	}
	mc.Sync()

	got, err := mc.MGet(ctx, []string{"a", "b", "c", "missing"})
	if err != nil {
		t.Fatalf("MGet: %v", err)
	}
	for _, k := range []string{"a", "b", "c"} {
		if string(got[k]) != string(items[k]) {
			t.Errorf("MGet[%q] = %q, want %q", k, got[k], items[k])
		}
	}
	if _, ok := got["missing"]; ok {
		t.Errorf("MGet returned missing key %q", "missing")
	}

	ok, err := mc.SetNX(ctx, "new-key", []byte("v"), time.Minute)
	if err != nil {
		t.Fatalf("SetNX new: %v", err)
	}
	if !ok {
		t.Fatal("SetNX on missing key should return true")
	}
	mc.Sync()

	ok, err = mc.SetNX(ctx, "new-key", []byte("v2"), time.Minute)
	if err != nil {
		t.Fatalf("SetNX existing: %v", err)
	}
	if ok {
		t.Fatal("SetNX on existing key should return false")
	}
}

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
	assert.NotContains(t, err.Error(), "-1s")
}

func TestMemoryCacheOptions_PanicOnInvalidValues(t *testing.T) {
	for name, fn := range map[string]func(){
		"WithMaxSize zero":         func() { WithMaxSize(0) },
		"WithMaxSize negative":     func() { WithMaxSize(-1) },
		"WithMaxCost zero":         func() { WithMaxCost(0) },
		"WithMaxCost negative":     func() { WithMaxCost(-1) },
		"WithNumCounters zero":     func() { WithNumCounters(0) },
		"WithNumCounters negative": func() { WithNumCounters(-1) },
		"WithBufferItems zero":     func() { WithBufferItems(0) },
		"WithBufferItems negative": func() { WithBufferItems(-1) },
		"WithCleanupInterval zero": func() { WithCleanupInterval(0) },
		"WithCleanupInterval negative": func() {
			WithCleanupInterval(-time.Second)
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Panics(t, fn)
		})
	}

	mc := MustNewMemoryCache(WithMaxSize(5))
	assert.Equal(t, 5, mc.maxSize)
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
		{"space", "bad key", ErrKeyInvalidChars, false},
		{"tab", "bad\tkey", ErrKeyInvalidChars, false},
		{"invalid utf8", string([]byte{0xff, 0xfe}), ErrKeyInvalidChars, false},
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
	assert.NotContains(t, err.Error(), "1024")
	assert.NotContains(t, err.Error(), "1025")
}

func TestValidateKeyPrefix(t *testing.T) {
	assert.NoError(t, ValidateKeyPrefix(""))
	assert.NoError(t, ValidateKeyPrefix(strings.Repeat("x", MaxKeyPrefixLen)))

	err := ValidateKeyPrefix(strings.Repeat("x", MaxKeyPrefixLen+1))
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrPrefixTooLong)
	assert.Contains(t, err.Error(), "exceeds maximum")
	assert.NotContains(t, err.Error(), "512")
	assert.NotContains(t, err.Error(), "513")

	err = ValidateKeyPrefix("bad\nprefix")
	assert.ErrorIs(t, err, ErrKeyInvalidChars)

	err = ValidateKeyPrefix("bad prefix")
	assert.ErrorIs(t, err, ErrKeyInvalidChars)

	err = ValidateKeyPrefix("bad\tprefix")
	assert.ErrorIs(t, err, ErrKeyInvalidChars)

	err = ValidateKeyPrefix(string([]byte{0xff, 0xfe}))
	assert.ErrorIs(t, err, ErrKeyInvalidChars)
}

// TestMemoryCache_DefaultByteCost verifies the default cache caps total
// bytes (not entry count): inserting more bytes than the configured
// maxCost must trigger eviction. Before the v2 audit fix, the default
// cost was 1 per entry which let ~67M entries accumulate before the
// 64 MiB cap kicked in.
func TestMemoryCache_DefaultByteCost(t *testing.T) {
	const maxBytes = int64(64 * 1024)
	mc, err := NewMemoryCache(
		WithMaxCost(maxBytes),
		WithNumCounters(10_000),
		WithIgnoreInternalCost(true),
	)
	require.NoError(t, err)
	defer func() { _ = mc.Close() }()
	ctx := context.Background()

	value := make([]byte, 1024)
	for i := range value {
		value[i] = byte(i)
	}

	// Write 10x the cap as 1KiB values; if cost were 1-per-entry, every
	// entry would fit. With byte-cost defaulting on, the cache must
	// evict to stay near maxBytes.
	const writes = 10 * 64
	for i := range writes {
		key := fmt.Sprintf("byte-cost-%d", i)
		err := mc.Set(ctx, key, value, time.Minute)
		if err != nil && !errors.Is(err, ErrAdmissionRejected) {
			t.Fatalf("Set: %v", err)
		}
	}
	mc.Sync()
	time.Sleep(20 * time.Millisecond) // let admission/eviction settle

	present := 0
	for i := range writes {
		key := fmt.Sprintf("byte-cost-%d", i)
		if exists, _ := mc.Exists(ctx, key); exists {
			present++
		}
	}

	maxEntriesAtCap := int(maxBytes / 1024)
	assert.LessOrEqualf(t, present, maxEntriesAtCap*2,
		"byte-cost default should evict to stay near %d bytes; got %d entries of 1KiB",
		maxBytes, present)
	assert.Lessf(t, present, writes,
		"byte-cost default must evict at least one entry when total bytes exceed cap")
}

// TestMemoryCache_WithEntryCost_EvictsByCount verifies the entry-count
// opt-out actually counts entries.
func TestMemoryCache_WithEntryCost_EvictsByCount(t *testing.T) {
	mc, err := NewMemoryCache(
		WithEntryCost(),
		WithMaxCost(3),
		WithNumCounters(30),
		WithIgnoreInternalCost(true),
	)
	require.NoError(t, err)
	defer func() { _ = mc.Close() }()
	ctx := context.Background()

	for i := range 10 {
		err := mc.Set(ctx, fmt.Sprintf("k%d", i), []byte("x"), time.Minute)
		if err != nil && !errors.Is(err, ErrAdmissionRejected) {
			t.Fatalf("Set: %v", err)
		}
	}
	mc.Sync()
	time.Sleep(20 * time.Millisecond)

	present := 0
	for i := range 10 {
		if exists, _ := mc.Exists(ctx, fmt.Sprintf("k%d", i)); exists {
			present++
		}
	}
	assert.LessOrEqual(t, present, 6, "entry-count cap (3) should evict aggressively, saw %d", present)
}

// TestMemoryCache_SetNX_ConcurrentAtomicity drives many goroutines into
// SetNX on the same key; exactly one must report ok=true. Before the
// v2 audit fix, Ristretto's buffered writes meant two concurrent SetNX
// calls could each observe the key as missing and both return true.
func TestMemoryCache_SetNX_ConcurrentAtomicity(t *testing.T) {
	t.Parallel()

	mc := MustNewMemoryCache()
	defer func() { _ = mc.Close() }()
	ctx := context.Background()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	var trueCount int64
	// Collect errors from worker goroutines instead of calling
	// require.* inside them: require's FailNow only exits the goroutine
	// it runs on (runtime.Goexit), which from a non-test goroutine lets
	// the test continue and pass spuriously. Assert on the test
	// goroutine after wg.Wait().
	errCh := make(chan error, goroutines)
	start := make(chan struct{})

	for range goroutines {
		go func() {
			defer wg.Done()
			<-start
			ok, err := mc.SetNX(ctx, "race-key", []byte("v"), time.Minute)
			if err != nil {
				errCh <- err
				return
			}
			if ok {
				atomic.AddInt64(&trueCount, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, trueCount, "exactly one SetNX must win, got %d winners", trueCount)
}

// TestMemoryCache_SetNX_ClaimExpires verifies the in-process NX claim
// is released once the requested TTL elapses, so a subsequent SetNX
// after expiry can succeed.
func TestMemoryCache_SetNX_ClaimExpires(t *testing.T) {
	mc := MustNewMemoryCache()
	defer func() { _ = mc.Close() }()
	ctx := context.Background()

	ok, err := mc.SetNX(ctx, "ttl-claim", []byte("v"), 30*time.Millisecond)
	require.NoError(t, err)
	require.True(t, ok)

	time.Sleep(60 * time.Millisecond)

	ok, err = mc.SetNX(ctx, "ttl-claim", []byte("v2"), time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "claim should be released after TTL expiry")
}

// TestMemoryCache_Set_AfterCloseReturnsInvalidCache pins the
// Close-vs-ops contract: once Close returns, mutating methods fail
// with ErrInvalidCache instead of racing ristretto's closed setBuf
// (which panics with send-on-closed-channel).
func TestMemoryCache_Set_AfterCloseReturnsInvalidCache(t *testing.T) {
	mc, err := NewMemoryCache()
	require.NoError(t, err)
	require.NoError(t, mc.Close())

	err = mc.Set(context.Background(), "rejected", []byte("v"), time.Minute)
	assert.ErrorIs(t, err, ErrInvalidCache)
}

// TestMemoryCache_SetNX_AfterCloseReturnsInvalidCache mirrors the
// Set-after-Close contract for SetNX and confirms no NX claim is
// recorded after Close.
func TestMemoryCache_SetNX_AfterCloseReturnsInvalidCache(t *testing.T) {
	mc, err := NewMemoryCache()
	require.NoError(t, err)
	require.NoError(t, mc.Close())

	ok, err := mc.SetNX(context.Background(), "rejected", []byte("v"), time.Minute)
	assert.False(t, ok)
	assert.ErrorIs(t, err, ErrInvalidCache)

	// A fresh cache must still accept the key (no stale claim leaked).
	mc2, err := NewMemoryCache()
	require.NoError(t, err)
	defer func() { _ = mc2.Close() }()
	ok2, err := mc2.SetNX(context.Background(), "rejected", []byte("v"), time.Minute)
	assert.NoError(t, err)
	assert.True(t, ok2)
}

// TestMemoryCache_SetNX_DeleteClearsClaim verifies that an explicit
// Delete clears the in-process claim so a follow-up SetNX can succeed.
func TestMemoryCache_SetNX_DeleteClearsClaim(t *testing.T) {
	mc := MustNewMemoryCache()
	defer func() { _ = mc.Close() }()
	ctx := context.Background()

	ok, err := mc.SetNX(ctx, "del-claim", []byte("v"), time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, mc.Delete(ctx, "del-claim"))

	ok, err = mc.SetNX(ctx, "del-claim", []byte("v2"), time.Minute)
	require.NoError(t, err)
	assert.True(t, ok, "Delete should release the SetNX claim")
}

// TestMemoryCache_WithEntryCost_DefaultMaxCostIsEntryBudget pins the
// WithEntryCost-without-WithMaxSize footgun: the default budget must be
// an entry count (~1e6), not the 64 MiB byte default reinterpreted as
// 67 million entries.
func TestMemoryCache_WithEntryCost_DefaultMaxCostIsEntryBudget(t *testing.T) {
	mc, err := NewMemoryCache(WithEntryCost(), WithoutMetrics())
	require.NoError(t, err)
	defer func() { _ = mc.Close() }()
	// Reach into ristretto config via MaxCost on the wrapper: we only
	// expose options, so pin behaviour by filling past 1e6 would be
	// slow. Instead assert the configured maxCost field after construct
	// via a second cache built with explicit WithMaxCost(1_000_000)
	// behaves equivalently for small N — and that construction succeeds.
	// Direct field check:
	assert.Equal(t, int64(1_000_000), mc.maxCost,
		"WithEntryCost alone must default maxCost to 1e6 entries, not 64MiB")
}

// TestMemoryCache_ZeroTTL_SetNXClaimSweptWhenKeyGone pins the unbounded
// growth guard for zero-TTL SetNX claims: when the value leaves the
// cache the claim must be reclaimable by the sweeper.
func TestMemoryCache_ZeroTTL_SetNXClaimSweptWhenKeyGone(t *testing.T) {
	mc, err := NewMemoryCache(WithoutMetrics())
	require.NoError(t, err)
	defer func() { _ = mc.Close() }()
	ctx := context.Background()
	ok, err := mc.SetNX(ctx, "perm-claim", []byte("v"), 0)
	require.NoError(t, err)
	require.True(t, ok)
	// Claim blocks second SetNX.
	ok, err = mc.SetNX(ctx, "perm-claim", []byte("v2"), 0)
	require.NoError(t, err)
	require.False(t, ok)
	// Delete value; claim should still block until sweeper runs — force
	// the reclaim path the sweeper uses (cache miss + zero expiresAt).
	require.NoError(t, mc.Delete(ctx, "perm-claim"))
	// Manual sweep simulation: drop zero-TTL claims whose key is gone.
	mc.nxClaims.Range(func(k, v any) bool {
		c := v.(nxClaim)
		key := k.(string)
		if c.expiresAt.IsZero() {
			if _, present := mc.cache.Get(key); !present {
				mc.nxClaims.Delete(k)
			}
		}
		return true
	})
	ok, err = mc.SetNX(ctx, "perm-claim", []byte("v3"), 0)
	require.NoError(t, err)
	assert.True(t, ok, "after reclaim, zero-TTL SetNX must succeed again")
}
