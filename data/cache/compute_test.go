package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestBackend creates a MemoryCache suitable for testing.
func newTestBackend(t *testing.T) *MemoryCache {
	t.Helper()
	mc, err := NewMemoryCache()
	require.NoError(t, err)
	t.Cleanup(func() { _ = mc.Close() })
	return mc
}

func TestComputeCache_BasicMissAndHit(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "test:", WithComputeName("basic"))
	require.NoError(t, err)

	var calls atomic.Int32
	fn := func(ctx context.Context) (string, time.Duration, error) {
		calls.Add(1)
		return "hello", 10 * time.Minute, nil
	}

	// First call: miss, triggers compute.
	val, err := cc.GetOrCompute(context.Background(), "key1", fn)
	require.NoError(t, err)
	assert.Equal(t, "hello", val)
	assert.Equal(t, int32(1), calls.Load())

	// Ristretto needs a sync to make the write visible.
	backend.Sync()

	// Second call: hit from cache.
	val, err = cc.GetOrCompute(context.Background(), "key1", fn)
	require.NoError(t, err)
	assert.Equal(t, "hello", val)
	assert.Equal(t, int32(1), calls.Load(), "fn should not be called again on hit")
}

func TestComputeCache_ConcurrentSingleflight(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[int](backend, "sf:")
	require.NoError(t, err)

	var calls atomic.Int32
	started := make(chan struct{})

	fn := func(ctx context.Context) (int, time.Duration, error) {
		calls.Add(1)
		<-started // block until released
		return 42, 5 * time.Minute, nil
	}

	const goroutines = 10
	var wg sync.WaitGroup
	results := make([]int, goroutines)
	errs := make([]error, goroutines)

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = cc.GetOrCompute(context.Background(), "shared", fn)
		}(i)
	}

	// Give goroutines time to start and hit singleflight.
	time.Sleep(50 * time.Millisecond)
	close(started)
	wg.Wait()

	for i := range goroutines {
		assert.NoError(t, errs[i])
		assert.Equal(t, 42, results[i])
	}
	assert.Equal(t, int32(1), calls.Load(), "singleflight should deduplicate to one call")
}

func TestComputeCache_StaleWhileRevalidate(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "stale:",
		WithStaleTTL(5*time.Minute),
		WithComputeName("stale"),
	)
	require.NoError(t, err)

	var calls atomic.Int32
	fn := func(ctx context.Context) (string, time.Duration, error) {
		n := calls.Add(1)
		if n == 1 {
			return "v1", 50 * time.Millisecond, nil // very short TTL
		}
		return "v2", 10 * time.Minute, nil
	}

	// First call: compute v1.
	val, err := cc.GetOrCompute(context.Background(), "k", fn)
	require.NoError(t, err)
	assert.Equal(t, "v1", val)

	backend.Sync()

	// Wait for primary TTL to expire but stay within stale window.
	time.Sleep(100 * time.Millisecond)

	// Should return stale v1 and trigger background refresh.
	val, err = cc.GetOrCompute(context.Background(), "k", fn)
	require.NoError(t, err)
	assert.Equal(t, "v1", val, "should serve stale value immediately")

	// Wait for background refresh to complete.
	cc.Wait()
	backend.Sync()

	// Now should get the refreshed v2.
	val, err = cc.GetOrCompute(context.Background(), "k", fn)
	require.NoError(t, err)
	assert.Equal(t, "v2", val, "should serve refreshed value")
	assert.Equal(t, int32(2), calls.Load())
}

func TestComputeCache_ErrorNotCached(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "err:")
	require.NoError(t, err)

	computeErr := errors.New("compute failed")
	var calls atomic.Int32

	fn := func(ctx context.Context) (string, time.Duration, error) {
		n := calls.Add(1)
		if n == 1 {
			return "", 0, computeErr
		}
		return "recovered", 10 * time.Minute, nil
	}

	// First call: error.
	_, err = cc.GetOrCompute(context.Background(), "fail", fn)
	require.Error(t, err)
	assert.ErrorIs(t, err, computeErr)

	// Second call: retry succeeds.
	val, err := cc.GetOrCompute(context.Background(), "fail", fn)
	require.NoError(t, err)
	assert.Equal(t, "recovered", val)
	assert.Equal(t, int32(2), calls.Load())
}

func TestComputeCache_ContextCancellation(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "ctx:")
	require.NoError(t, err)

	fn := func(ctx context.Context) (string, time.Duration, error) {
		<-ctx.Done()
		return "", 0, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := cc.GetOrCompute(ctx, "cancelled", fn)
		assert.ErrorIs(t, err, context.Canceled)
	}()

	cancel()
	<-done
}

func TestComputeCache_Metrics(t *testing.T) {
	backend := newTestBackend(t)
	reg := prometheus.NewPedanticRegistry()
	metrics := NewComputeMetrics(reg)

	cc, err := NewComputeCache[string](backend, "m:",
		WithComputeMetricsRegisterer(metrics),
		WithComputeName("test_cache"),
	)
	require.NoError(t, err)

	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "val", 10 * time.Minute, nil
	}

	// Miss + compute.
	_, err = cc.GetOrCompute(context.Background(), "a", fn)
	require.NoError(t, err)
	backend.Sync()

	// Hit.
	_, err = cc.GetOrCompute(context.Background(), "a", fn)
	require.NoError(t, err)

	assertCounterValue(t, metrics.misses, "test_cache", 1)
	assertCounterValue(t, metrics.hits, "test_cache", 1)
}

func TestComputeCache_ZeroTTL(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "z:")
	require.NoError(t, err)

	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "no-expire", 0, nil
	}

	val, err := cc.GetOrCompute(context.Background(), "k", fn)
	require.NoError(t, err)
	assert.Equal(t, "no-expire", val)
}

func TestComputeCache_PrefixValidation(t *testing.T) {
	backend := newTestBackend(t)

	// Invalid prefix with null byte.
	_, err := NewComputeCache[string](backend, "bad\x00prefix:")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid characters")

	// Prefix too long (use 'a' bytes to avoid invalid char check).
	longBytes := make([]byte, MaxKeyLen/2+1)
	for i := range longBytes {
		longBytes[i] = 'a'
	}
	_, err = NewComputeCache[string](backend, string(longBytes))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")
}

func TestComputeCache_KeyValidation(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "kv:")
	require.NoError(t, err)

	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "val", time.Minute, nil
	}

	// Empty key.
	_, err = cc.GetOrCompute(context.Background(), "", fn)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrKeyEmpty)

	// Key with null byte.
	_, err = cc.GetOrCompute(context.Background(), "bad\x00key", fn)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrKeyInvalidChars)
}

func TestComputeCache_ErrorMetrics(t *testing.T) {
	backend := newTestBackend(t)
	reg := prometheus.NewPedanticRegistry()
	metrics := NewComputeMetrics(reg)

	cc, err := NewComputeCache[string](backend, "em:",
		WithComputeMetricsRegisterer(metrics),
		WithComputeName("err_cache"),
	)
	require.NoError(t, err)

	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "", 0, errors.New("boom")
	}

	_, err = cc.GetOrCompute(context.Background(), "k", fn)
	require.Error(t, err)

	assertCounterValue(t, metrics.errors, "err_cache", 1)
	assertCounterValue(t, metrics.misses, "err_cache", 1)
}

// assertCounterValue checks a prometheus CounterVec label has the expected value.
func assertCounterValue(t *testing.T, cv *prometheus.CounterVec, label string, expected float64) {
	t.Helper()
	counter, err := cv.GetMetricWithLabelValues(label)
	require.NoError(t, err)
	var m dto.Metric
	require.NoError(t, counter.Write(&m))
	assert.Equal(t, expected, m.GetCounter().GetValue())
}
