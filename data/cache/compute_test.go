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

// faultyBackend wraps a Cache and injects errors for testing.
type faultyBackend struct {
	Cache
	getErr atomic.Value // stores error
	setErr atomic.Value // stores error
}

func (fb *faultyBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if v := fb.getErr.Load(); v != nil {
		if err, ok := v.(error); ok {
			return nil, err
		}
	}
	return fb.Cache.Get(ctx, key)
}

func (fb *faultyBackend) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if v := fb.setErr.Load(); v != nil {
		if err, ok := v.(error); ok {
			return err
		}
	}
	return fb.Cache.Set(ctx, key, value, ttl)
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
	// With the WithoutCancel fix, cancelling the caller's context does NOT
	// propagate into the compute function. The compute function runs to
	// completion so that other singleflight waiters are not affected.
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "ctx:")
	require.NoError(t, err)

	fn := func(ctx context.Context) (string, time.Duration, error) {
		// The compute context is detached, so ctx.Done() should not fire.
		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		default:
		}
		return "ok", 5 * time.Minute, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling GetOrCompute

	// Despite the cancelled parent context, the compute should succeed
	// because WithoutCancel detaches the compute context.
	val, err := cc.GetOrCompute(ctx, "cancelled", fn)
	require.NoError(t, err)
	assert.Equal(t, "ok", val)
}

func TestComputeCache_ContextCancellationDoesNotAffectOtherCallers(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "ctxleak:")
	require.NoError(t, err)

	started := make(chan struct{})
	proceed := make(chan struct{})

	fn := func(ctx context.Context) (string, time.Duration, error) {
		close(started) // signal that compute has started
		<-proceed      // wait for test to release
		return "result", 10 * time.Minute, nil
	}

	// Caller 1: will be cancelled.
	ctx1, cancel1 := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var val1 string
	var err1 error
	wg.Add(1)
	go func() {
		defer wg.Done()
		val1, err1 = cc.GetOrCompute(ctx1, "shared", fn)
	}()

	// Wait for fn to start executing.
	<-started

	// Caller 2: should NOT be affected by caller 1's cancellation.
	var val2 string
	var err2 error
	wg.Add(1)
	go func() {
		defer wg.Done()
		val2, err2 = cc.GetOrCompute(context.Background(), "shared", fn)
	}()

	// Give caller 2 time to join the singleflight group.
	time.Sleep(50 * time.Millisecond)

	// Cancel caller 1's context — should NOT affect the compute.
	cancel1()

	// Release the compute function.
	close(proceed)
	wg.Wait()

	// Both callers should get the result because WithoutCancel protects the compute.
	require.NoError(t, err1, "caller 1 should succeed despite context cancellation")
	assert.Equal(t, "result", val1)
	require.NoError(t, err2, "caller 2 should not be affected by caller 1's cancellation")
	assert.Equal(t, "result", val2)
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

func TestComputeCache_UnmarshalFailure(t *testing.T) {
	backend := newTestBackend(t)
	reg := prometheus.NewPedanticRegistry()
	metrics := NewComputeMetrics(reg)

	cc, err := NewComputeCache[string](backend, "corrupt:",
		WithComputeMetricsRegisterer(metrics),
		WithComputeName("corrupt_cache"),
	)
	require.NoError(t, err)

	// Write corrupt data directly into the backend.
	err = backend.Set(context.Background(), "corrupt:k", []byte("not-valid-json{{{"), 10*time.Minute)
	require.NoError(t, err)
	backend.Sync()

	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "fresh", 10 * time.Minute, nil
	}

	// Unmarshal failure should fall through to recompute, not return an error.
	val, err := cc.GetOrCompute(context.Background(), "k", fn)
	require.NoError(t, err)
	assert.Equal(t, "fresh", val)

	// Should record an error (unmarshal) and a miss (recompute).
	assertCounterValue(t, metrics.errors, "corrupt_cache", 1)
	assertCounterValue(t, metrics.misses, "corrupt_cache", 1)
}

func TestComputeCache_BackendSetFailure(t *testing.T) {
	mem := newTestBackend(t)
	fb := &faultyBackend{Cache: mem}
	fb.setErr.Store(errors.New("write failed"))

	cc, err := NewComputeCache[string](fb, "setfail:")
	require.NoError(t, err)

	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "computed", 10 * time.Minute, nil
	}

	// Compute should succeed even though the backend Set fails.
	val, err := cc.GetOrCompute(context.Background(), "k", fn)
	require.NoError(t, err)
	assert.Equal(t, "computed", val)
}

func TestComputeCache_BackendGetErrorNotTreatedAsMiss(t *testing.T) {
	mem := newTestBackend(t)
	reg := prometheus.NewPedanticRegistry()
	metrics := NewComputeMetrics(reg)

	fb := &faultyBackend{Cache: mem}
	fb.getErr.Store(errors.New("redis timeout"))

	cc, err := NewComputeCache[string](fb, "geterr:",
		WithComputeMetricsRegisterer(metrics),
		WithComputeName("geterr_cache"),
	)
	require.NoError(t, err)

	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "fallback", 10 * time.Minute, nil
	}

	val, err := cc.GetOrCompute(context.Background(), "k", fn)
	require.NoError(t, err)
	assert.Equal(t, "fallback", val)

	// Should record an error (backend Get failure) plus a miss.
	assertCounterValue(t, metrics.errors, "geterr_cache", 1)
	assertCounterValue(t, metrics.misses, "geterr_cache", 1)
}

func TestComputeCache_StaleTTLZeroNoStaleServing(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "nostalettl:")
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

	// Wait for primary TTL to expire.
	time.Sleep(100 * time.Millisecond)

	// With staleTTL=0, this should recompute (not serve stale).
	val, err = cc.GetOrCompute(context.Background(), "k", fn)
	require.NoError(t, err)
	assert.Equal(t, "v2", val, "should NOT serve stale value when staleTTL=0")
	assert.Equal(t, int32(2), calls.Load())
}
