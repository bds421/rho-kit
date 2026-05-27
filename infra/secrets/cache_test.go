package secrets_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/secrets/v2"
)

type fakeLoader struct {
	mu      sync.Mutex
	values  map[string]string
	version string
	calls   atomic.Int32
	failNow atomic.Bool
}

func newFakeLoader(values map[string]string) *fakeLoader {
	return &fakeLoader{values: values, version: "v1"}
}

func (f *fakeLoader) Get(_ context.Context, key string) (secrets.Secret, error) {
	f.calls.Add(1)
	if f.failNow.Load() {
		return secrets.Secret{}, fmt.Errorf("upstream down: %w", secrets.ErrLoaderUnavailable)
	}
	f.mu.Lock()
	v, ok := f.values[key]
	version := f.version
	f.mu.Unlock()
	if !ok {
		return secrets.Secret{}, secrets.ErrSecretNotFound
	}
	return secrets.MakeSecret([]byte(v), version), nil
}

func (f *fakeLoader) update(key, value string) {
	f.mu.Lock()
	f.values[key] = value
	f.version = "v" + fmt.Sprintf("%d", f.calls.Load()+1)
	f.mu.Unlock()
}

func TestCachedLoader_HitDoesNotCallUpstream(t *testing.T) {
	inner := newFakeLoader(map[string]string{"k": "v"})
	c, err := secrets.NewCachedLoader(inner,
		secrets.WithCacheMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)

	_, err = c.Get(context.Background(), "k")
	require.NoError(t, err)
	_, err = c.Get(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, int32(1), inner.calls.Load())
}

func TestCachedLoader_NotFoundSurfaced(t *testing.T) {
	inner := newFakeLoader(map[string]string{})
	c, err := secrets.NewCachedLoader(inner,
		secrets.WithCacheMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)
	_, err = c.Get(context.Background(), "missing")
	require.ErrorIs(t, err, secrets.ErrSecretNotFound)
}

func TestCachedLoader_StaleFallback(t *testing.T) {
	inner := newFakeLoader(map[string]string{"k": "v"})
	c, err := secrets.NewCachedLoader(inner,
		secrets.WithCacheTTL(20*time.Millisecond),
		secrets.WithCacheRefreshAfter(10*time.Millisecond),
		secrets.WithCacheMaxStale(time.Hour),
		secrets.WithCacheMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)

	// Prime cache.
	first, err := c.Get(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, "v", first.Value.RevealString())

	// Wait past TTL, then break upstream.
	time.Sleep(40 * time.Millisecond)
	inner.failNow.Store(true)

	stale, err := c.Get(context.Background(), "k")
	require.NoError(t, err, "expected stale fallback when upstream down and within MaxStale")
	require.Equal(t, "v", stale.Value.RevealString())
}

func TestCachedLoader_StaleFallbackExpiresAtMaxStale(t *testing.T) {
	inner := newFakeLoader(map[string]string{"k": "v"})
	c, err := secrets.NewCachedLoader(inner,
		secrets.WithCacheTTL(10*time.Millisecond),
		secrets.WithCacheRefreshAfter(5*time.Millisecond),
		secrets.WithCacheMaxStale(20*time.Millisecond),
		secrets.WithCacheMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)

	_, err = c.Get(context.Background(), "k")
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	inner.failNow.Store(true)

	_, err = c.Get(context.Background(), "k")
	require.Error(t, err, "stale beyond MaxStale should surface the error")
	require.ErrorIs(t, err, secrets.ErrLoaderUnavailable)
}

func TestCachedLoader_RejectsRefreshAfterGreaterThanTTL(t *testing.T) {
	_, err := secrets.NewCachedLoader(newFakeLoader(nil),
		secrets.WithCacheTTL(1*time.Minute),
		secrets.WithCacheRefreshAfter(2*time.Minute),
	)
	require.Error(t, err)
}

func TestCachedLoader_Invalidate(t *testing.T) {
	inner := newFakeLoader(map[string]string{"k": "v"})
	c, err := secrets.NewCachedLoader(inner,
		secrets.WithCacheMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)

	_, err = c.Get(context.Background(), "k")
	require.NoError(t, err)

	inner.update("k", "v2")
	c.Invalidate("k")
	got, err := c.Get(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, "v2", got.Value.RevealString())
}

func TestCachedLoader_SingleflightCoalescesConcurrentMisses(t *testing.T) {
	inner := &slowLoader{value: "v"}
	c, err := secrets.NewCachedLoader(inner,
		secrets.WithCacheMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Get(context.Background(), "k")
		}()
	}
	wg.Wait()
	require.LessOrEqual(t, inner.calls.Load(), int32(2), "singleflight should coalesce; got %d calls", inner.calls.Load())
}

type slowLoader struct {
	value string
	calls atomic.Int32
}

func (s *slowLoader) Get(_ context.Context, _ string) (secrets.Secret, error) {
	s.calls.Add(1)
	time.Sleep(20 * time.Millisecond)
	return secrets.MakeSecret([]byte(s.value), "v"), nil
}

func TestRotatingProvider(t *testing.T) {
	inner := newFakeLoader(map[string]string{"k": "v"})
	c, err := secrets.NewCachedLoader(inner,
		secrets.WithCacheMetricsRegisterer(prometheus.NewRegistry()),
	)
	require.NoError(t, err)

	provider := secrets.NewRotatingProvider(c, "k", 0)
	val, err := provider()
	require.NoError(t, err)
	require.Equal(t, "v", val)
}

func TestRotatingProvider_PanicsOnNilLoader(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on nil loader")
		}
	}()
	_ = secrets.NewRotatingProvider(nil, "k", 0)
}

func TestRotatingProvider_PanicsOnEmptyKey(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on empty key")
		}
	}()
	_ = secrets.NewRotatingProvider(newFakeLoader(nil), "", 0)
}

func TestRotatingProvider_NotFoundSurfacesWrappedError(t *testing.T) {
	inner := newFakeLoader(map[string]string{})
	provider := secrets.NewRotatingProvider(inner, "missing", 0)
	_, err := provider()
	require.ErrorIs(t, err, secrets.ErrSecretNotFound)
}

var _ = errors.Is // keep import alive on older toolchains
