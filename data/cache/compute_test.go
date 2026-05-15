package cache

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
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

func TestComputeOptions_PanicOnInvalidDurations(t *testing.T) {
	for name, fn := range map[string]func(){
		"WithStaleTTL negative":        func() { WithStaleTTL(-time.Second) },
		"WithRefreshTimeout zero":      func() { WithRefreshTimeout(0) },
		"WithRefreshTimeout negative":  func() { WithRefreshTimeout(-time.Second) },
		"WithComputeTimeout zero":      func() { WithComputeTimeout(0) },
		"WithComputeTimeout negative":  func() { WithComputeTimeout(-time.Second) },
		"WithMetricsRegisterer nil":    func() { WithComputeMetricsRegisterer(nil) },
		"WithLogger nil":               func() { WithComputeLogger(nil) },
		"WithComputeName empty":        func() { WithComputeName("") },
		"WithComputeName newline":      func() { WithComputeName("bad\nname") },
		"WithComputeName invalid utf8": func() { WithComputeName(string([]byte{0xff})) },
		"WithComputeName too long":     func() { WithComputeName(strings.Repeat("a", 257)) },
	} {
		t.Run(name, func(t *testing.T) {
			assert.Panics(t, fn)
		})
	}
}

func TestNewComputeCache_RejectsNilOption(t *testing.T) {
	backend := newTestBackend(t)

	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	_, _ = NewComputeCache[string](backend, "nilopt:", nil)
}

func TestComputeCache_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()
	fn := func(context.Context) (string, time.Duration, error) {
		return "value", time.Minute, nil
	}

	for name, cc := range map[string]*ComputeCache[string]{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := cc.GetOrCompute(ctx, "key", fn)
			assert.ErrorIs(t, err, ErrInvalidCache)

			err = cc.Close()
			assert.ErrorIs(t, err, ErrInvalidCache)

			assert.NotPanics(t, func() { cc.Wait() })
		})
	}
}

func TestComputeCache_NilComputeFuncReturnsError(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "nilfn:")
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	_, err = cc.GetOrCompute(context.Background(), "key", nil)
	assert.ErrorIs(t, err, ErrInvalidComputeFunc)
}

func TestComputeCache_BackendSetLogRedactsKeyAndError(t *testing.T) {
	backend := &faultyBackend{Cache: newTestBackend(t)}
	backend.setErr.Store(errors.New("redis rejected tenant-secret-key"))
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))

	cc, err := NewComputeCache[string](backend, "tenant-prefix:", WithComputeLogger(logger))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })

	got, err := cc.GetOrCompute(context.Background(), "tenant-secret-key", func(context.Context) (string, time.Duration, error) {
		return "value", time.Minute, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "value", got)

	out := buf.String()
	assert.Contains(t, out, `"key"`)
	assert.NotContains(t, out, "tenant-prefix")
	assert.NotContains(t, out, "tenant-secret-key")
	assert.NotContains(t, out, "redis rejected")
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
	defer func() { _ = cc.Close() }()

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
	defer func() { _ = cc.Close() }()

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
	defer func() { _ = cc.Close() }()

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
	defer func() { _ = cc.Close() }()

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

func TestComputeCache_ComputePanicReturnsError(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "panic:")
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	_, err = cc.GetOrCompute(context.Background(), "boom", func(context.Context) (string, time.Duration, error) {
		panic("compute exploded")
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ComputeFunc panicked")
	assert.Contains(t, err.Error(), "<redacted panic value: string>")
	assert.NotContains(t, err.Error(), "compute exploded")
}

func TestComputeCache_BackgroundRefreshPanicDoesNotCrash(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "bgpanic:",
		WithStaleTTL(time.Minute),
		WithRefreshTimeout(time.Second),
	)
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	var calls atomic.Int32
	fn := func(context.Context) (string, time.Duration, error) {
		if calls.Add(1) == 1 {
			return "v1", 20 * time.Millisecond, nil
		}
		panic("background refresh exploded")
	}

	val, err := cc.GetOrCompute(context.Background(), "k", fn)
	require.NoError(t, err)
	assert.Equal(t, "v1", val)
	backend.Sync()
	time.Sleep(30 * time.Millisecond)

	val, err = cc.GetOrCompute(context.Background(), "k", fn)
	require.NoError(t, err)
	assert.Equal(t, "v1", val)

	cc.Wait()
	assert.Equal(t, int32(2), calls.Load())
}

func TestComputeCache_ContextCancellation(t *testing.T) {
	// FR-048 [MED] contract: a cancelled caller returns ctx.Err()
	// promptly instead of blocking behind a slow compute. The
	// underlying compute still runs to completion (on a detached
	// context) so other waiters benefit from the work.
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "ctx:")
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	var calls int32
	fn := func(_ context.Context) (string, time.Duration, error) {
		atomic.AddInt32(&calls, 1)
		return "ok", 5 * time.Minute, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	val, err := cc.GetOrCompute(ctx, "cancelled", fn)
	assert.ErrorIs(t, err, context.Canceled, "cancelled caller must return ctx.Err()")
	assert.Equal(t, "", val)

	// Give the detached compute a moment to land in the backend.
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&calls) >= 1
	}, time.Second, 5*time.Millisecond, "compute did not run on the detached context")
}

func TestComputeCache_ContextCancellationDoesNotAffectOtherCallers(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "ctxleak:")
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

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

	// FR-048 [MED]: caller 1's cancellation aborts ITS wait but the
	// compute continues so caller 2 still gets the result.
	assert.ErrorIs(t, err1, context.Canceled, "cancelled caller must return ctx.Err()")
	assert.Equal(t, "", val1)
	require.NoError(t, err2, "uncancelled caller must receive the leader's result")
	assert.Equal(t, "result", val2)
}

func TestComputeCache_Metrics(t *testing.T) {
	backend := newTestBackend(t)
	reg := prometheus.NewPedanticRegistry()
	metrics := NewComputeMetrics(WithRegisterer(reg))

	cc, err := NewComputeCache[string](backend, "m:",
		WithComputeMetricsRegisterer(metrics),
		WithComputeName("test_cache"),
	)
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

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

func TestComputeCache_ZeroTTL_Rejected(t *testing.T) {
	// ComputeCache layers stale-while-revalidate on top of an ExpiresAt
	// timestamp. ttl=0 is meaningless in that layer (the entry would be
	// instantly stale), so ComputeFunc must return a positive TTL. This
	// diverges from the base Cache contract where ttl=0 = no expiration —
	// the divergence is documented; rejecting at the boundary prevents the
	// silent stale-on-write bug the prior behaviour produced.
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "z:")
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "no-expire", 0, nil
	}

	_, err = cc.GetOrCompute(context.Background(), "k", fn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-positive ttl")
	assert.NotContains(t, err.Error(), "0s")
}

func TestComputeCache_NegativeTTL_Rejected(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "neg:")
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "x", -1 * time.Second, nil
	}

	_, err = cc.GetOrCompute(context.Background(), "k", fn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-positive ttl")
	assert.NotContains(t, err.Error(), "-1s")
}

func TestNewComputeCache_NilBackend(t *testing.T) {
	_, err := NewComputeCache[string](nil, "prefix:")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil backend")
}

func TestComputeCache_PrefixValidation(t *testing.T) {
	backend := newTestBackend(t)

	// Invalid prefix with null byte.
	_, err := NewComputeCache[string](backend, "bad\x00prefix:")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid characters")

	_, err = NewComputeCache[string](backend, string([]byte{0xff, 0xfe}))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrKeyInvalidChars)

	// Prefix too long (use 'a' bytes to avoid invalid char check).
	longBytes := make([]byte, MaxKeyPrefixLen+1)
	for i := range longBytes {
		longBytes[i] = 'a'
	}
	_, err = NewComputeCache[string](backend, string(longBytes))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")
	assert.NotContains(t, err.Error(), "512")
	assert.NotContains(t, err.Error(), "513")
}

func TestComputeCache_KeyValidation(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "kv:")
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

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
	metrics := NewComputeMetrics(WithRegisterer(reg))

	cc, err := NewComputeCache[string](backend, "em:",
		WithComputeMetricsRegisterer(metrics),
		WithComputeName("err_cache"),
	)
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "", 0, errors.New("boom")
	}

	_, err = cc.GetOrCompute(context.Background(), "k", fn)
	require.Error(t, err)

	assertCounterValue(t, metrics.errors, "err_cache", 1)
	assertCounterValue(t, metrics.misses, "err_cache", 1)
}

// TestComputeCache_ErrorMetricsSingleflight verifies that a failing
// compute that is shared across many concurrent callers records exactly
// one error — the previous logic keyed the error metric on
// singleflight's `shared` flag, which dropped errors whenever followers
// joined the leader's call.
func TestComputeCache_ErrorMetricsSingleflight(t *testing.T) {
	backend := newTestBackend(t)
	reg := prometheus.NewPedanticRegistry()
	metrics := NewComputeMetrics(WithRegisterer(reg))

	cc, err := NewComputeCache[string](backend, "sferr:",
		WithComputeMetricsRegisterer(metrics),
		WithComputeName("sferr_cache"),
	)
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	released := make(chan struct{})
	var calls atomic.Int32
	fn := func(ctx context.Context) (string, time.Duration, error) {
		calls.Add(1)
		<-released
		return "", 0, errors.New("boom")
	}

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			_, _ = cc.GetOrCompute(context.Background(), "k", fn)
		}()
	}

	// Let them join the singleflight group, then release the failure.
	time.Sleep(50 * time.Millisecond)
	close(released)
	wg.Wait()

	assert.EqualValues(t, 1, calls.Load(), "singleflight must dedupe to a single compute")
	assertCounterValue(t, metrics.errors, "sferr_cache", 1)
	assertCounterValue(t, metrics.misses, "sferr_cache", float64(goroutines))
}

func TestComputeCache_UnmarshalFailure(t *testing.T) {
	backend := newTestBackend(t)
	reg := prometheus.NewPedanticRegistry()
	metrics := NewComputeMetrics(WithRegisterer(reg))

	cc, err := NewComputeCache[string](backend, "corrupt:",
		WithComputeMetricsRegisterer(metrics),
		WithComputeName("corrupt_cache"),
	)
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

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
	defer func() { _ = cc.Close() }()

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
	metrics := NewComputeMetrics(WithRegisterer(reg))

	fb := &faultyBackend{Cache: mem}
	fb.getErr.Store(errors.New("redis timeout"))

	cc, err := NewComputeCache[string](fb, "geterr:",
		WithComputeMetricsRegisterer(metrics),
		WithComputeName("geterr_cache"),
	)
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

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
	defer func() { _ = cc.Close() }()

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

// TestComputeCache_ForegroundComputeRespectsDeadline verifies that the
// caller's deadline is preserved across the singleflight detach. Before
// the v2 audit fix, computeAndStore used context.WithoutCancel which
// strips the deadline along with the cancellation, letting compute run
// past the request budget.
func TestComputeCache_ForegroundComputeRespectsDeadline(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "deadline:")
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	fn := func(ctx context.Context) (string, time.Duration, error) {
		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		case <-time.After(2 * time.Second):
			return "ignored", 5 * time.Minute, nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = cc.GetOrCompute(ctx, "deadline-key", fn)
	elapsed := time.Since(start)

	require.Error(t, err, "deadline must surface as error")
	assert.True(t, errors.Is(err, context.DeadlineExceeded),
		"compute must observe the original deadline; got %v", err)
	assert.Lessf(t, elapsed, 1500*time.Millisecond,
		"compute returned in %v; deadline should have fired well before fn's 2s timer", elapsed)
}

// TestComputeCache_ForegroundComputeIsolatesCancellation verifies the
// FR-048 contract: a cancelled caller returns ctx.Err() while the
// compute itself runs to completion on a detached context (the
// singleflight guarantee for OTHER waiters).
func TestComputeCache_ForegroundComputeIsolatesCancellation(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "isolate:")
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	computed := make(chan struct{})
	fn := func(_ context.Context) (string, time.Duration, error) {
		// Compute runs on the detached context — we don't observe ctx.Done().
		time.Sleep(50 * time.Millisecond)
		close(computed)
		return "ok", time.Minute, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = cc.GetOrCompute(ctx, "isolate-key", fn)
	assert.ErrorIs(t, err, context.Canceled, "cancelled caller must return ctx.Err()")
	<-computed // compute still runs for the next caller
}

// gatherGaugeValue reads the current value of a GaugeVec label.
func gatherGaugeValue(t *testing.T, gv *prometheus.GaugeVec, label string) float64 {
	t.Helper()
	g, err := gv.GetMetricWithLabelValues(label)
	require.NoError(t, err)
	var m dto.Metric
	require.NoError(t, g.Write(&m))
	return m.GetGauge().GetValue()
}

// gatherHistogramSampleCount reads the total sample count of a HistogramVec label.
func gatherHistogramSampleCount(t *testing.T, hv *prometheus.HistogramVec, label string) uint64 {
	t.Helper()
	o, err := hv.GetMetricWithLabelValues(label)
	require.NoError(t, err)
	h, ok := o.(prometheus.Histogram)
	require.True(t, ok, "expected prometheus.Histogram, got %T", o)
	var m dto.Metric
	require.NoError(t, h.Write(&m))
	return m.GetHistogram().GetSampleCount()
}

// TestComputeCache_SingleflightFollowerMetrics verifies the three new
// singleflight collectors fire correctly when concurrent callers race
// on the same key: one leader runs the compute (inflight observed > 0
// during the call); N-1 followers join, get counted, and have their
// wait observed.
func TestComputeCache_SingleflightFollowerMetrics(t *testing.T) {
	backend := newTestBackend(t)
	reg := prometheus.NewRegistry()
	metrics := NewComputeMetrics(WithRegisterer(reg))

	cc, err := NewComputeCache[string](backend, "sf:",
		WithComputeMetricsRegisterer(metrics),
		WithComputeName("sf_cache"),
	)
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	release := make(chan struct{})
	// inflightDuringCompute is recorded inside the leader's compute while
	// the gauge is incremented. Reading the gauge here proves the gauge
	// is observable while the singleflight leader is still running, not
	// just before/after.
	var inflightDuringCompute float64
	var sampled atomic.Bool
	fn := func(ctx context.Context) (string, time.Duration, error) {
		// Only sample once — every concurrent goroutine routes through the
		// same singleflight leader so this closure runs once.
		if sampled.CompareAndSwap(false, true) {
			inflightDuringCompute = gatherGaugeValue(t, metrics.singleflightInflight, "sf_cache")
		}
		<-release
		return "result", time.Minute, nil
	}

	const goroutines = 5
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			_, _ = cc.GetOrCompute(context.Background(), "k", fn)
		}()
	}

	// Give followers time to join the singleflight group, then release.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	assert.EqualValues(t, 1, inflightDuringCompute,
		"gauge must read 1 while the single leader's compute is running")
	assert.EqualValues(t, 0, gatherGaugeValue(t, metrics.singleflightInflight, "sf_cache"),
		"gauge must decrement back to 0 once the leader's compute completes")
	// Followers = total callers - leader (the first goroutine to claim the key).
	assertCounterValue(t, metrics.singleflightFollowers, "sf_cache", float64(goroutines-1))
	// Every follower (and only followers) records exactly one wait observation.
	assert.Equal(t, uint64(goroutines-1),
		gatherHistogramSampleCount(t, metrics.singleflightWait, "sf_cache"),
		"wait histogram must record one sample per follower (not per leader)")
}

// TestComputeCache_SingleflightSoloCallerNotFollower verifies that a
// caller that wins the singleflight race solo (no concurrent peers)
// is NOT counted as a follower and does NOT record a wait sample.
// Without this guard the metric would conflate "compute happened" with
// "follower joined a leader" — the latter being the only signal of
// useful dedup.
func TestComputeCache_SingleflightSoloCallerNotFollower(t *testing.T) {
	backend := newTestBackend(t)
	reg := prometheus.NewRegistry()
	metrics := NewComputeMetrics(WithRegisterer(reg))

	cc, err := NewComputeCache[string](backend, "solo:",
		WithComputeMetricsRegisterer(metrics),
		WithComputeName("solo_cache"),
	)
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	fn := func(context.Context) (string, time.Duration, error) {
		return "val", time.Minute, nil
	}

	_, err = cc.GetOrCompute(context.Background(), "k", fn)
	require.NoError(t, err)

	assertCounterValue(t, metrics.singleflightFollowers, "solo_cache", 0)
	assert.Equal(t, uint64(0),
		gatherHistogramSampleCount(t, metrics.singleflightWait, "solo_cache"),
		"solo caller must not produce a wait sample")
	assert.EqualValues(t, 0, gatherGaugeValue(t, metrics.singleflightInflight, "solo_cache"),
		"inflight gauge must be 0 after solo compute completes")
}

// TestComputeCache_OverflowingTTL_Rejected guards L045: ComputeFunc
// must not be able to wedge the cache with a TTL so large that
// time.Now().Add(ttl) overflows int64 nanoseconds. The kit caps at
// 10 years; values above that are rejected loudly rather than wrapping
// to a negative ExpiresAt.
func TestComputeCache_OverflowingTTL_Rejected(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "ovf:")
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	// 11 years is past the 10-year cap.
	overflowing := 11 * 365 * 24 * time.Hour
	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "v", overflowing, nil
	}

	_, err = cc.GetOrCompute(context.Background(), "k", fn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ttl")
	assert.Contains(t, err.Error(), "exceeds maximum")
}

// TestComputeCache_StaleTTLOverflow_Rejected guards the same overflow
// at construction time: staleTTL above the 10-year cap is rejected
// when the first GetOrCompute fires (the cap is enforced inside the
// compute path so the existing WithStaleTTL panic-on-negative
// validation is preserved).
func TestComputeCache_StaleTTLOverflow_Rejected(t *testing.T) {
	backend := newTestBackend(t)
	cc, err := NewComputeCache[string](backend, "ovfs:",
		WithStaleTTL(11*365*24*time.Hour),
	)
	require.NoError(t, err)
	defer func() { _ = cc.Close() }()

	fn := func(ctx context.Context) (string, time.Duration, error) {
		return "v", time.Hour, nil
	}

	_, err = cc.GetOrCompute(context.Background(), "k", fn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "staleTTL")
	assert.Contains(t, err.Error(), "exceeds maximum")
}
