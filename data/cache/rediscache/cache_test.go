package rediscache

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
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

// TestGet_OversizeValueErrorsBeforeAllocation pins H-003: a foreign
// writer that stored a value above the cap must be rejected via
// STRLEN before the full body is materialised into Go memory. The
// returned error must surface ErrValueTooLarge so callers can
// distinguish poisoned slots from cache misses.
func TestGet_OversizeValueErrorsBeforeAllocation(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewCache(client, "test", WithCacheMaxValueSize(64))
	require.NoError(t, err)

	// Foreign writer stores 128 bytes — twice the cap.
	require.NoError(t, client.Set(context.Background(), "poisoned", make([]byte, 128), time.Minute).Err())

	_, err = rc.Get(context.Background(), "poisoned")
	require.ErrorIs(t, err, sharedcache.ErrValueTooLarge,
		"oversize get must surface ErrValueTooLarge, not ErrCacheMiss or a bare wrapped error")
}

// TestMGet_OversizeValueDroppedSilently pins the H-003 MGet contract:
// an oversize entry within a batch must NOT fail the whole batch and
// must NOT allocate the body. Other keys in the batch return normally.
func TestMGet_OversizeValueDroppedSilently(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	rc, err := NewCache(client, "test", WithCacheMaxValueSize(64))
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, client.Set(ctx, "ok", []byte("payload"), time.Minute).Err())
	require.NoError(t, client.Set(ctx, "poisoned", make([]byte, 128), time.Minute).Err())

	out, err := rc.MGet(ctx, []string{"ok", "poisoned", "missing"})
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), out["ok"])
	_, hasPoisoned := out["poisoned"]
	assert.False(t, hasPoisoned, "oversize entry must drop silently from MGet result")
	_, hasMissing := out["missing"]
	assert.False(t, hasMissing)
}

// TestMGet_WrongTypeKeyDoesNotFailBatch pins that a single wrong-typed
// key (e.g. a co-tenant planting a list under a key the capped path
// then STRLENs/GETs) must NOT abort the whole batch. The uncapped MGET
// path already treats WRONGTYPE keys as misses (Redis MGET returns nil
// for them); the capped STRLEN+GET path must match that contract so one
// hostile entry cannot deny the entire request.
func TestMGet_WrongTypeKeyDoesNotFailBatch(t *testing.T) {
	for name, maxSize := range map[string]int{
		"capped":   64,
		"uncapped": 0,
	} {
		t.Run(name, func(t *testing.T) {
			client := newTestClient(t)
			t.Cleanup(func() { _ = client.Close() })

			rc, err := NewCache(client, "test", WithCacheMaxValueSize(maxSize))
			require.NoError(t, err)

			ctx := context.Background()
			require.NoError(t, client.Set(ctx, "ok", []byte("payload"), time.Minute).Err())
			// Co-tenant plants a list under "poisoned": STRLEN/GET both
			// return WRONGTYPE for it.
			require.NoError(t, client.RPush(ctx, "poisoned", "a", "b").Err())

			out, err := rc.MGet(ctx, []string{"ok", "poisoned", "missing"})
			require.NoError(t, err, "a single wrong-typed key must not fail the whole batch")
			assert.Equal(t, []byte("payload"), out["ok"])
			_, hasPoisoned := out["poisoned"]
			assert.False(t, hasPoisoned, "wrong-typed entry must be treated as a miss")
			_, hasMissing := out["missing"]
			assert.False(t, hasMissing)
		})
	}
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

// newTestCacheWithRegistry builds a cache wired to a fresh, isolated
// Prometheus registry so individual hit/miss counters can be asserted
// without interference from the package-global default registry.
func newTestCacheWithRegistry(t *testing.T, opts ...CacheOption) (*Cache, *prometheus.Registry) {
	t.Helper()
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	reg := prometheus.NewRegistry()
	opts = append(opts, WithMetricsRegisterer(reg))
	rc, err := NewCache(client, "test", opts...)
	require.NoError(t, err)
	return rc, reg
}

// TestNewCache_CustomRegistererDoesNotUseDefaultMetrics verifies that
// passing WithMetricsRegisterer wires the cache to that registry rather
// than the global default-registry singleton. The constructor must defer
// the defaultMetrics() fallback until after options run; counting a hit
// must therefore land on the custom registry's collector.
func TestNewCache_CustomRegistererDoesNotUseDefaultMetrics(t *testing.T) {
	rc, reg := newTestCacheWithRegistry(t)
	ctx := context.Background()

	// The custom registry's metrics are NOT the default singleton.
	assert.NotSame(t, defaultMetrics(), rc.metrics,
		"custom-registerer cache must not reuse the default-registry metrics")

	require.NoError(t, rc.Set(ctx, "k", []byte("v"), time.Minute))
	_, err := rc.Get(ctx, "k")
	require.NoError(t, err)

	assert.Equal(t, float64(1), testutil.ToFloat64(rc.metrics.hits.WithLabelValues("test")))
	// The hit landed on the custom registry: gathering it yields the family.
	mfs, err := reg.Gather()
	require.NoError(t, err)
	var found bool
	for _, mf := range mfs {
		if mf.GetName() == "redis_cache_hits_total" {
			found = true
		}
	}
	assert.True(t, found, "hits_total must be registered on the custom registry")
}

// TestGet_PreStrlenOversizeCountsMiss pins miss accounting on the pre-GET
// oversize path: a value whose STRLEN exceeds the cap is rejected with
// ErrValueTooLarge and counted as a miss (never a hit).
func TestGet_PreStrlenOversizeCountsMiss(t *testing.T) {
	rc, _ := newTestCacheWithRegistry(t, WithCacheMaxValueSize(64))
	ctx := context.Background()

	require.NoError(t, rc.client.Set(ctx, "poisoned", make([]byte, 128), time.Minute).Err())
	_, err := rc.Get(ctx, "poisoned")
	require.ErrorIs(t, err, sharedcache.ErrValueTooLarge)

	assert.Equal(t, float64(1), testutil.ToFloat64(rc.metrics.misses.WithLabelValues("test")),
		"oversize get must be counted as a miss")
	assert.Equal(t, float64(0), testutil.ToFloat64(rc.metrics.hits.WithLabelValues("test")))
}

// understatedStrLenClient wraps a real client but reports a STRLEN below
// the cap regardless of the stored value, deterministically driving the
// post-GET TOCTOU branch (the value passed the length pre-check but the
// GET reply exceeds the cap).
type understatedStrLenClient struct {
	goredis.UniversalClient
}

func (c understatedStrLenClient) StrLen(ctx context.Context, key string) *goredis.IntCmd {
	return goredis.NewIntResult(0, nil)
}

// TestGet_TOCTOUOversizeCountsMiss pins the consistency fix: when a value
// passes the pre-GET STRLEN check but the GET reply exceeds the cap (the
// TOCTOU window), the read must be rejected with ErrValueTooLarge AND
// counted as a miss for hit-ratio accounting — matching the pre-GET path
// and the capped-MGet TOCTOU branch.
func TestGet_TOCTOUOversizeCountsMiss(t *testing.T) {
	mr := miniredis.RunT(t)
	real := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = real.Close() })

	reg := prometheus.NewRegistry()
	rc, err := NewCache(understatedStrLenClient{real}, "test",
		WithCacheMaxValueSize(64), WithMetricsRegisterer(reg))
	require.NoError(t, err)

	ctx := context.Background()
	// STRLEN (faked to 0) passes the cap, but GET returns 128 real bytes,
	// tripping the post-GET TOCTOU guard.
	require.NoError(t, real.Set(ctx, "poisoned", make([]byte, 128), time.Minute).Err())

	_, err = rc.Get(ctx, "poisoned")
	require.ErrorIs(t, err, sharedcache.ErrValueTooLarge,
		"post-GET oversize must surface ErrValueTooLarge")

	assert.Equal(t, float64(1), testutil.ToFloat64(rc.metrics.misses.WithLabelValues("test")),
		"TOCTOU oversize get must be counted as a miss")
	assert.Equal(t, float64(0), testutil.ToFloat64(rc.metrics.hits.WithLabelValues("test")))
}

// TestMGetUncapped_NonStringReplyCountsMiss pins the consistency fix for
// the uncapped MGET path: a non-string reply (e.g. a wrong-typed key
// returned by MGET) must be counted as a miss rather than silently
// skipped, matching the nil-reply case and the capped path.
func TestMGetUncapped_NonStringReplyCountsMiss(t *testing.T) {
	rc, _ := newTestCacheWithRegistry(t, WithCacheMaxValueSize(0))
	ctx := context.Background()

	require.NoError(t, rc.client.Set(ctx, "ok", []byte("payload"), time.Minute).Err())
	// A list under "wrong" makes MGET return a non-string reply for it.
	require.NoError(t, rc.client.RPush(ctx, "wrong", "a", "b").Err())

	out, err := rc.MGet(ctx, []string{"ok", "wrong", "missing"})
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), out["ok"])

	assert.Equal(t, float64(1), testutil.ToFloat64(rc.metrics.hits.WithLabelValues("test")))
	// "wrong" (non-string) and "missing" (nil) each count as a miss.
	assert.Equal(t, float64(2), testutil.ToFloat64(rc.metrics.misses.WithLabelValues("test")),
		"non-string reply and missing key must both count as misses")
}
