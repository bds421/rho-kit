package secrets_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/secrets/v2"
)

// fakeClock is a manually advanced clock for deterministic
// stale-while-revalidate tests. now() is read concurrently by the
// background refresh goroutine, so reads/writes go through a mutex.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{t: start} }

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// refreshLoader is a Loader whose returned value/version and
// failure mode are controllable, and that records every call. It also
// supports a per-call block so a test can hold a refresh open and observe
// that concurrent Gets do not pile up extra refreshes.
type refreshLoader struct {
	mu      sync.Mutex
	value   string
	version int
	fail    bool
	failErr error

	calls atomic.Int32

	gate chan struct{} // if non-nil, each Get blocks until receiving
}

func newRefreshLoader(value string) *refreshLoader {
	return &refreshLoader{value: value, version: 1}
}

func (r *refreshLoader) Get(_ context.Context, _ string) (secrets.Secret, error) {
	r.calls.Add(1)
	if gate := r.gateCh(); gate != nil {
		<-gate
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return secrets.Secret{}, r.failErr
	}
	return secrets.MakeSecret([]byte(r.value), fmt.Sprintf("v%d", r.version)), nil
}

func (r *refreshLoader) gateCh() chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gate
}

func (r *refreshLoader) set(value string, version int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.value = value
	r.version = version
}

func (r *refreshLoader) setFail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail = true
	r.failErr = err
}

// TestCachedLoader_RefreshDueHitServesCachedAndUpdates exercises the
// stale-while-revalidate happy path: a refresh-due Get returns the OLD
// cached value immediately, asynchronously fetches a new one, and the
// next Get sees the refreshed value. Driven by an injected clock so the
// refresh window is crossed deterministically.
func TestCachedLoader_RefreshDueHitServesCachedAndUpdates(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	inner := newRefreshLoader("old")
	c, err := secrets.NewCachedLoader(inner,
		secrets.WithCacheTTL(10*time.Minute),
		secrets.WithCacheRefreshAfter(5*time.Minute),
		secrets.WithCacheMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)
	c.SetClock(clock.now)

	// Prime the cache.
	first, err := c.Get(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, "old", first.Value.RevealString())
	require.Equal(t, int32(1), inner.calls.Load())

	// Backend now serves a new value; advance past refreshAfter (but not
	// past TTL) so the next Get is a refresh-due hit.
	inner.set("new", 2)
	clock.advance(6 * time.Minute)

	// Refresh-due hit returns the OLD (still-valid) cached value at once.
	served, err := c.Get(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, "old", served.Value.RevealString(),
		"refresh-due hit must serve the cached value immediately, not block")

	// The background refresh runs asynchronously; wait for it to land.
	require.Eventually(t, func() bool {
		return inner.calls.Load() >= 2 && c.MetricRefreshes() >= 1
	}, 2*time.Second, 5*time.Millisecond, "background refresh should have fetched")

	require.Equal(t, float64(0), c.MetricRefreshErrors(),
		"successful refresh must not record a refresh error")

	// A subsequent hit (still within TTL) now serves the refreshed value
	// with no further upstream call.
	callsBefore := inner.calls.Load()
	got, err := c.Get(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, "new", got.Value.RevealString(),
		"after background refresh the cache must serve the new value")
	require.Equal(t, callsBefore, inner.calls.Load(),
		"reading the refreshed value must be a pure cache hit")
}

// TestCachedLoader_FailedRefreshPreservesEntry asserts a background
// refresh that errors does NOT invalidate the cached entry: the cache
// keeps serving the previously fetched value and records a refresh error.
func TestCachedLoader_FailedRefreshPreservesEntry(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	inner := newRefreshLoader("old")
	c, err := secrets.NewCachedLoader(inner,
		secrets.WithCacheTTL(10*time.Minute),
		secrets.WithCacheRefreshAfter(5*time.Minute),
		secrets.WithCacheMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)
	c.SetClock(clock.now)

	_, err = c.Get(context.Background(), "k")
	require.NoError(t, err)

	// Break the backend and cross the refresh threshold.
	inner.setFail(fmt.Errorf("upstream blip: %w", secrets.ErrLoaderUnavailable))
	clock.advance(6 * time.Minute)

	served, err := c.Get(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, "old", served.Value.RevealString())

	// Wait for the failing background refresh to record its error.
	require.Eventually(t, func() bool {
		return c.MetricRefreshErrors() >= 1
	}, 2*time.Second, 5*time.Millisecond, "failed refresh should record a refresh error")

	// The entry must still be intact: a follow-up Get (still within TTL)
	// is a cache hit serving the old value, not a forced foreground fetch.
	callsBefore := inner.calls.Load()
	got, err := c.Get(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, "old", got.Value.RevealString(),
		"failed background refresh must not drop the cached entry")
	// Another refresh-due hit may spawn one more attempt, but the value
	// served is still the cached one — and the call count must not jump by
	// a foreground fetch on this path.
	require.LessOrEqual(t, inner.calls.Load()-callsBefore, int32(1),
		"a failed refresh must keep serving cached, not force a foreground fetch")
}

// TestCachedLoader_ConcurrentRefreshDueHitsSpawnOneRefresh covers the
// performance fix: a burst of refresh-due Gets while a refresh is still
// in flight must coalesce into a SINGLE background fetch, not one per Get.
func TestCachedLoader_ConcurrentRefreshDueHitsSpawnOneRefresh(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	inner := newRefreshLoader("old")
	c, err := secrets.NewCachedLoader(inner,
		secrets.WithCacheTTL(10*time.Minute),
		secrets.WithCacheRefreshAfter(5*time.Minute),
		secrets.WithCacheMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)
	c.SetClock(clock.now)

	// Prime, then install a gate so the refresh fetch blocks until we
	// release it — holding the refresh open while the burst arrives.
	_, err = c.Get(context.Background(), "k")
	require.NoError(t, err)
	primeCalls := inner.calls.Load() // == 1

	gate := make(chan struct{})
	inner.mu.Lock()
	inner.gate = gate
	inner.mu.Unlock()

	clock.advance(6 * time.Minute)

	// Burst of refresh-due hits. Each returns immediately (cached value);
	// only the first should spawn a refresh goroutine, the rest are no-ops
	// because a refresh is already in flight.
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, gerr := c.Get(context.Background(), "k")
			require.NoError(t, gerr)
			require.Equal(t, "old", got.Value.RevealString())
		}()
	}
	wg.Wait()

	// Wait until the single refresh goroutine has actually entered the
	// (blocked) loader call, so the in-flight comparison is meaningful.
	require.Eventually(t, func() bool {
		return inner.calls.Load() == primeCalls+1
	}, 2*time.Second, 2*time.Millisecond,
		"exactly one background refresh fetch should be in flight")

	// Release the blocked refresh and let it finish.
	close(gate)
	inner.mu.Lock()
	inner.gate = nil
	inner.mu.Unlock()

	require.Eventually(t, func() bool {
		return c.MetricRefreshes() >= 1
	}, 2*time.Second, 5*time.Millisecond)

	require.Equal(t, primeCalls+1, inner.calls.Load(),
		"50 concurrent refresh-due hits must coalesce to ONE background fetch")
	require.Equal(t, float64(1), c.MetricRefreshes(),
		"only one background refresh should have been recorded")
}
