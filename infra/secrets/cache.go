package secrets

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/core/v2/secret"
)

// CachedLoader wraps any [Loader] with a TTL cache, stale-while-revalidate
// background refresh, single-flight on cache miss, and stale-on-error
// fallback. Suitable for hot paths (every DB connection asks for the
// current password) where the underlying Loader's RPS budget is finite.
type CachedLoader struct {
	inner   Loader
	cfg     cacheConfig
	mu      sync.Mutex
	entries map[string]*cacheEntry
	sf      *singleflight
	metrics *cacheMetrics
}

type cacheConfig struct {
	ttl          time.Duration
	refreshAfter time.Duration
	maxStale     time.Duration
	logger       *slog.Logger
	registerer   prometheus.Registerer
	now          func() time.Time
}

type cacheEntry struct {
	value     Secret
	expiresAt time.Time
	refreshAt time.Time
}

// CacheOption configures a [CachedLoader].
type CacheOption func(*cacheConfig)

// WithCacheTTL sets the hard expiry (default 10m). After TTL a Get
// blocks until a fresh value is fetched (unless a stale-window value
// is available — see [WithCacheMaxStale]).
func WithCacheTTL(d time.Duration) CacheOption {
	if d <= 0 {
		panic("secrets: WithCacheTTL requires positive duration")
	}
	return func(c *cacheConfig) { c.ttl = d }
}

// WithCacheRefreshAfter sets the stale-while-revalidate threshold
// (default 5m). Once an entry is older than refreshAfter, the next Get
// returns the cached value immediately AND triggers a background
// refresh. Must be < TTL.
func WithCacheRefreshAfter(d time.Duration) CacheOption {
	if d <= 0 {
		panic("secrets: WithCacheRefreshAfter requires positive duration")
	}
	return func(c *cacheConfig) { c.refreshAfter = d }
}

// WithCacheMaxStale sets the upper bound on how stale a fallback value
// is allowed to be when the Loader returns [ErrLoaderUnavailable].
// Default 1h. Returns the cached value with a warn log when within
// the stale window; surfaces the error past it.
func WithCacheMaxStale(d time.Duration) CacheOption {
	if d <= 0 {
		panic("secrets: WithCacheMaxStale requires positive duration")
	}
	return func(c *cacheConfig) { c.maxStale = d }
}

// WithCacheLogger overrides the logger (default slog.Default).
func WithCacheLogger(l *slog.Logger) CacheOption {
	return func(c *cacheConfig) { c.logger = l }
}

// WithCacheMetricsRegisterer pins the Prometheus registerer (default
// DefaultRegisterer).
func WithCacheMetricsRegisterer(reg prometheus.Registerer) CacheOption {
	if reg == nil {
		panic("secrets: WithCacheMetricsRegisterer requires non-nil registerer")
	}
	return func(c *cacheConfig) { c.registerer = reg }
}

// NewCachedLoader wraps inner with a cache. Returns an error if inner
// is nil or config validation fails.
func NewCachedLoader(inner Loader, opts ...CacheOption) (*CachedLoader, error) {
	if inner == nil {
		return nil, errors.New("secrets: NewCachedLoader requires non-nil Loader")
	}
	cfg := cacheConfig{
		ttl:          10 * time.Minute,
		refreshAfter: 5 * time.Minute,
		maxStale:     1 * time.Hour,
		registerer:   prometheus.DefaultRegisterer,
		now:          time.Now,
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("secrets: cache option must not be nil")
		}
		opt(&cfg)
	}
	if cfg.refreshAfter >= cfg.ttl {
		return nil, errors.New("secrets: WithCacheRefreshAfter must be < WithCacheTTL")
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	m, err := newCacheMetrics(cfg.registerer)
	if err != nil {
		return nil, err
	}
	return &CachedLoader{
		inner:   inner,
		cfg:     cfg,
		entries: make(map[string]*cacheEntry),
		sf:      newSingleflight(),
		metrics: m,
	}, nil
}

// Get returns a fresh-enough secret for key. Strategy:
//
//   - hit, not stale:    return cached value, no fetch.
//   - hit, refresh-due:  return cached value, spawn background refresh.
//   - hit, expired:      single-flight foreground fetch; on error, return
//     the stale value if within MaxStale (warn-log) else the error.
//   - miss:              single-flight foreground fetch.
func (c *CachedLoader) Get(ctx context.Context, key string) (Secret, error) {
	now := c.cfg.now()
	c.mu.Lock()
	entry, ok := c.entries[key]
	c.mu.Unlock()

	if ok && now.Before(entry.expiresAt) {
		c.metrics.hits.Inc()
		if now.After(entry.refreshAt) {
			c.spawnRefresh(key)
		}
		return copyForCaller(entry.value), nil
	}

	// Miss or expired — coalesce concurrent fetches per key.
	val, err := c.sf.do(key, func() (Secret, error) {
		c.metrics.misses.Inc()
		return c.fetchAndStore(ctx, key, now)
	})
	if err == nil {
		return copyForCaller(val), nil
	}
	// On loader-unavailable, fall back to a stale cached value if
	// within the stale window. Surface other errors directly.
	if !errors.Is(err, ErrLoaderUnavailable) || !ok {
		return Secret{}, err
	}
	staleAge := now.Sub(entry.expiresAt)
	if staleAge > c.cfg.maxStale {
		c.metrics.staleExceeded.Inc()
		return Secret{}, err
	}
	c.metrics.staleFallbacks.Inc()
	c.cfg.logger.Warn("secrets: returning stale cached value (loader unavailable)",
		slog.String("key", key),
		slog.Duration("stale_age", staleAge),
		redact.Error(err),
	)
	return copyForCaller(entry.value), nil
}

// Invalidate drops the cached entry for key (and zeroes its secret
// value). Use after a rotation event the cache should not have served.
func (c *CachedLoader) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[key]; ok {
		entry.value.Value.Zero()
		delete(c.entries, key)
	}
}

func (c *CachedLoader) fetchAndStore(ctx context.Context, key string, now time.Time) (Secret, error) {
	val, err := c.inner.Get(ctx, key)
	if err != nil {
		return Secret{}, err
	}
	if val.FetchedAt.IsZero() {
		val.FetchedAt = now
	}
	c.mu.Lock()
	// Zero the prior secret's bytes before overwriting.
	if prior, ok := c.entries[key]; ok && prior.value.Value != nil {
		prior.value.Value.Zero()
	}
	c.entries[key] = &cacheEntry{
		value:     val,
		expiresAt: now.Add(c.cfg.ttl),
		refreshAt: now.Add(c.cfg.refreshAfter),
	}
	c.mu.Unlock()
	return val, nil
}

// spawnRefresh fires a background fetch. Errors are logged but do not
// invalidate the cached entry: the next foreground Get will retry.
func (c *CachedLoader) spawnRefresh(key string) {
	go func() {
		// A panicking loader (now propagated by singleflight rather than
		// poisoning the key) must not crash the process from this
		// background goroutine — recover and log it like a failed
		// refresh. The foreground Get path still surfaces the panic to
		// its own caller.
		defer func() {
			if r := recover(); r != nil {
				c.metrics.refreshErrors.Inc()
				c.cfg.logger.Warn("secrets: background refresh panicked",
					slog.String("key", key),
					redact.Panic(r),
				)
			}
		}()
		// Use a short standalone context so a cancelled caller ctx
		// doesn't abort the refresh.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := c.sf.do(key, func() (Secret, error) {
			c.metrics.refreshes.Inc()
			return c.fetchAndStore(ctx, key, c.cfg.now())
		})
		if err != nil {
			c.metrics.refreshErrors.Inc()
			c.cfg.logger.Warn("secrets: background refresh failed",
				slog.String("key", key),
				redact.Error(err),
			)
		}
	}()
}

// copyForCaller returns a Secret whose Value is an independent
// [secret.String] copy of src's bytes. The cache hands callers a copy
// rather than the shared cache-owned buffer so that:
//
//   - a caller following the documented `defer s.Value.Zero()` contract
//     only wipes its own copy, not the cache's shared entry; and
//   - the cache zeroing a displaced/invalidated value (fetchAndStore,
//     Invalidate) cannot zero a buffer a concurrent caller is still
//     using.
//
// The cache retains sole ownership of the zeroizable backing buffer.
func copyForCaller(src Secret) Secret {
	out := src
	if src.Value != nil {
		b := src.Value.Reveal()
		out.Value = secret.New(b)
		for i := range b {
			b[i] = 0
		}
	}
	return out
}

// MakeSecret is a small constructor backends use to build a [Secret]
// from raw bytes — keeps the zeroization wiring (secret.String) in
// one place so backend implementations don't reinvent it.
func MakeSecret(b []byte, version string) Secret {
	return Secret{
		Value:   secret.New(b),
		Version: version,
	}
}
