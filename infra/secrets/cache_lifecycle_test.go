package secrets_test

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/secrets/v2"
)

// TestCachedLoader_CallerZeroDoesNotCorruptCache asserts the documented
// caller contract (doc.go: `defer s.Value.Zero()`) does not wipe the
// cache's shared buffer. A caller that zeroes its returned Secret must
// not turn every subsequent cache hit into an empty secret.
func TestCachedLoader_CallerZeroDoesNotCorruptCache(t *testing.T) {
	inner := newFakeLoader(map[string]string{"k": "v"})
	c, err := secrets.NewCachedLoader(inner,
		secrets.WithCacheMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)

	first, err := c.Get(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, "v", first.Value.RevealString())

	// Follow the documented contract: zero the secret when done.
	first.Value.Zero()

	// A subsequent hit (no upstream call) must still return the secret.
	second, err := c.Get(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, "v", second.Value.RevealString(),
		"caller Zero() corrupted the cache's shared buffer")
	require.Equal(t, int32(1), inner.calls.Load(), "second Get must be a cache hit")
}

// TestCachedLoader_RefreshDoesNotZeroInUseSecret asserts that a
// background refresh (which zeroes the displaced prior value) does not
// wipe a secret a concurrent caller is still holding from an earlier
// Get on the stale-while-revalidate path.
func TestCachedLoader_RefreshDoesNotZeroInUseSecret(t *testing.T) {
	inner := newFakeLoader(map[string]string{"k": "v"})
	c, err := secrets.NewCachedLoader(inner,
		secrets.WithCacheTTL(40*time.Millisecond),
		secrets.WithCacheRefreshAfter(10*time.Millisecond),
		secrets.WithCacheMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)

	// Prime cache.
	_, err = c.Get(context.Background(), "k")
	require.NoError(t, err)

	// Cross the refreshAfter threshold so the next Get triggers a
	// background refresh AND returns the (about-to-be-displaced) value.
	time.Sleep(20 * time.Millisecond)
	held, err := c.Get(context.Background(), "k")
	require.NoError(t, err)

	// Let the background refresh complete; it will store a new value and
	// (buggily) zero the prior shared buffer the caller still holds.
	require.Eventually(t, func() bool {
		return inner.calls.Load() >= 2
	}, time.Second, 5*time.Millisecond, "background refresh should have run")
	time.Sleep(10 * time.Millisecond)

	require.Equal(t, "v", held.Value.RevealString(),
		"background refresh zeroed a secret the caller was still using")
}

// TestCachedLoader_InvalidateDoesNotZeroInUseSecret asserts Invalidate
// does not wipe a secret a caller obtained from a prior Get.
func TestCachedLoader_InvalidateDoesNotZeroInUseSecret(t *testing.T) {
	inner := newFakeLoader(map[string]string{"k": "v"})
	c, err := secrets.NewCachedLoader(inner,
		secrets.WithCacheMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)

	held, err := c.Get(context.Background(), "k")
	require.NoError(t, err)

	c.Invalidate("k")

	require.Equal(t, "v", held.Value.RevealString(),
		"Invalidate zeroed a secret the caller was still holding")
}

// TestSingleflight_PanicDoesNotDeadlockKey asserts that a panicking
// loader does not poison the single-flight key. After a recovered
// panic, subsequent Gets for the same key must not block forever.
func TestSingleflight_PanicDoesNotDeadlockKey(t *testing.T) {
	inner := &panicOnceLoader{}
	c, err := secrets.NewCachedLoader(inner,
		secrets.WithCacheMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)

	// First Get triggers a panic in the loader; recover it like a
	// handler-recovery middleware would.
	func() {
		defer func() { _ = recover() }()
		_, _ = c.Get(context.Background(), "k")
	}()

	// A subsequent Get for the same key must complete, not deadlock.
	done := make(chan struct{})
	var (
		got  secrets.Secret
		gerr error
	)
	go func() {
		got, gerr = c.Get(context.Background(), "k")
		close(done)
	}()

	select {
	case <-done:
		require.NoError(t, gerr)
		require.Equal(t, "v", got.Value.RevealString())
	case <-time.After(2 * time.Second):
		t.Fatal("Get deadlocked after a recovered loader panic (poisoned single-flight key)")
	}
}

// panicOnceLoader panics on its first Get and serves a value thereafter.
type panicOnceLoader struct {
	mu       sync.Mutex
	panicked bool
}

func (p *panicOnceLoader) Get(_ context.Context, _ string) (secrets.Secret, error) {
	p.mu.Lock()
	first := !p.panicked
	p.panicked = true
	p.mu.Unlock()
	if first {
		panic("loader boom")
	}
	return secrets.MakeSecret([]byte("v"), "v1"), nil
}

// TestCachedLoader_StaleFallbackLogRedactsLoaderError asserts the
// stale-fallback warn log does not render the raw loader error text,
// which can carry backend topology / payload data, instead of going
// through redact.
func TestCachedLoader_StaleFallbackLogRedactsLoaderError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	inner := newFakeLoader(map[string]string{"k": "v"})
	c, err := secrets.NewCachedLoader(inner,
		secrets.WithCacheTTL(20*time.Millisecond),
		secrets.WithCacheRefreshAfter(10*time.Millisecond),
		secrets.WithCacheMaxStale(time.Hour),
		secrets.WithCacheLogger(logger),
		secrets.WithCacheMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)

	_, err = c.Get(context.Background(), "k")
	require.NoError(t, err)

	time.Sleep(40 * time.Millisecond)
	inner.failNow.Store(true)

	_, err = c.Get(context.Background(), "k")
	require.NoError(t, err, "expected stale fallback")

	logged := buf.String()
	require.NotContains(t, logged, "upstream down",
		"raw loader error text leaked into the stale-fallback log")
}
