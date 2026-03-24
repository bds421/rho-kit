package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// ComputeFunc computes a cache value. Returns the value, its TTL, and any error.
type ComputeFunc[T any] func(ctx context.Context) (T, time.Duration, error)

// envelope is the JSON structure stored in the cache backend.
// It wraps the actual value with an expiration timestamp for
// stale-while-revalidate support.
type envelope[T any] struct {
	Value     T     `json:"v"`
	ExpiresAt int64 `json:"e"` // unix nanoseconds
}

// computeConfig holds configuration for a ComputeCache.
type computeConfig struct {
	staleTTL time.Duration
	metrics  *computeMetrics
	name     string
}

// ComputeOption configures a ComputeCache.
type ComputeOption func(*computeConfig)

// WithStaleTTL sets the stale-while-revalidate window. After the primary TTL
// expires, stale data is served for up to this duration while a background
// refresh runs. Zero means no stale serving (default).
func WithStaleTTL(d time.Duration) ComputeOption {
	return func(cfg *computeConfig) {
		if d >= 0 {
			cfg.staleTTL = d
		}
	}
}

// WithComputeMetricsRegisterer attaches pre-built metrics to the ComputeCache.
// Use NewComputeMetrics to create the metrics, or WithComputePrometheusMetrics
// for a one-step option.
func WithComputeMetricsRegisterer(m *computeMetrics) ComputeOption {
	return func(cfg *computeConfig) {
		cfg.metrics = m
	}
}

// WithComputeName sets the Prometheus metric label for this cache instance.
func WithComputeName(name string) ComputeOption {
	return func(cfg *computeConfig) {
		cfg.name = name
	}
}

// ComputeCache wraps a Cache backend with singleflight deduplication and
// stale-while-revalidate support. Only one goroutine computes a given key
// at a time; concurrent callers wait for the in-flight computation.
type ComputeCache[T any] struct {
	backend Cache
	prefix  string
	group   singleflight.Group
	cfg     computeConfig

	// bgMu protects bgWg from concurrent Done calls during shutdown.
	bgMu sync.Mutex
	bgWg sync.WaitGroup
}

// NewComputeCache creates a ComputeCache that wraps the given backend.
// The prefix is prepended to all keys to avoid collisions.
//
// Returns an error if the prefix contains invalid characters or is too long.
func NewComputeCache[T any](backend Cache, prefix string, opts ...ComputeOption) (*ComputeCache[T], error) {
	if strings.ContainsAny(prefix, "\x00\n\r") {
		return nil, fmt.Errorf("cache prefix contains invalid characters (null byte, newline, or carriage return)")
	}
	if len(prefix) > MaxKeyLen/2 {
		return nil, fmt.Errorf("cache prefix length %d exceeds maximum of %d bytes", len(prefix), MaxKeyLen/2)
	}

	cfg := computeConfig{name: "default"}
	for _, o := range opts {
		o(&cfg)
	}

	return &ComputeCache[T]{
		backend: backend,
		prefix:  prefix,
		cfg:     cfg,
	}, nil
}

// fullKey validates the user-provided key and returns the combined prefix+key.
func (cc *ComputeCache[T]) fullKey(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	full := cc.prefix + key
	if len(full) > MaxKeyLen {
		return "", fmt.Errorf("cache key with prefix exceeds maximum length of %d bytes (prefix=%d, key=%d)",
			MaxKeyLen, len(cc.prefix), len(key))
	}
	return full, nil
}

// GetOrCompute retrieves a cached value or computes it if missing/expired.
//
// Behavior:
//   - Cache hit (not expired): returns cached value immediately.
//   - Cache hit (expired, within stale window): returns stale value,
//     triggers async background refresh via singleflight.
//   - Cache miss: uses singleflight to deduplicate concurrent calls,
//     computes the value, stores it, and returns.
//
// Errors from fn are propagated to all waiters and are NOT cached.
func (cc *ComputeCache[T]) GetOrCompute(ctx context.Context, key string, fn ComputeFunc[T]) (T, error) {
	var zero T

	full, err := cc.fullKey(key)
	if err != nil {
		return zero, err
	}

	// Try the backend first.
	data, getErr := cc.backend.Get(ctx, full)
	if getErr == nil {
		return cc.handleHit(ctx, full, key, data, fn)
	}

	// Cache miss — compute via singleflight.
	cc.recordMiss()
	return cc.computeAndStore(ctx, full, fn)
}

// handleHit processes a cache hit, checking freshness and triggering
// background refresh for stale entries.
func (cc *ComputeCache[T]) handleHit(
	ctx context.Context,
	full string,
	_ string,
	data []byte,
	fn ComputeFunc[T],
) (T, error) {
	var zero T

	var env envelope[T]
	if err := json.Unmarshal(data, &env); err != nil {
		return zero, fmt.Errorf("cache compute unmarshal: %w", err)
	}

	now := time.Now().UnixNano()
	if now < env.ExpiresAt {
		// Fresh hit.
		cc.recordHit()
		return env.Value, nil
	}

	// Expired but still within backend TTL (stale window).
	cc.recordStaleServe()
	cc.triggerBackgroundRefresh(full, fn)
	return env.Value, nil
}

// computeAndStore runs fn through singleflight, stores the result, and returns.
func (cc *ComputeCache[T]) computeAndStore(ctx context.Context, full string, fn ComputeFunc[T]) (T, error) {
	var zero T

	result, err, _ := cc.group.Do(full, func() (interface{}, error) {
		return cc.executeCompute(ctx, full, fn)
	})
	if err != nil {
		cc.recordError()
		return zero, err
	}

	val, ok := result.(T)
	if !ok {
		return zero, fmt.Errorf("cache compute: unexpected result type")
	}
	return val, nil
}

// executeCompute calls fn, marshals the result into an envelope, and stores it.
func (cc *ComputeCache[T]) executeCompute(ctx context.Context, full string, fn ComputeFunc[T]) (T, error) {
	var zero T

	val, ttl, err := fn(ctx)
	if err != nil {
		return zero, err
	}

	if ttl < 0 {
		ttl = 0
	}

	env := envelope[T]{
		Value:     val,
		ExpiresAt: time.Now().Add(ttl).UnixNano(),
	}

	envData, marshalErr := json.Marshal(env)
	if marshalErr != nil {
		return zero, fmt.Errorf("cache compute marshal: %w", marshalErr)
	}

	// Backend TTL = primary TTL + stale window.
	backendTTL := ttl + cc.cfg.staleTTL
	if storeErr := cc.backend.Set(ctx, full, envData, backendTTL); storeErr != nil {
		// Store failure is non-fatal; return the computed value.
		return val, nil
	}

	return val, nil
}

// triggerBackgroundRefresh starts an async refresh using singleflight.DoChan.
func (cc *ComputeCache[T]) triggerBackgroundRefresh(full string, fn ComputeFunc[T]) {
	cc.bgMu.Lock()
	cc.bgWg.Add(1)
	cc.bgMu.Unlock()

	ch := cc.group.DoChan(full, func() (interface{}, error) {
		// Use a detached context for background work so it isn't
		// cancelled when the original request completes.
		bgCtx := context.Background()
		val, err := cc.executeCompute(bgCtx, full, fn)
		if err != nil {
			cc.recordError()
		}
		return val, err
	})

	go func() {
		defer cc.bgWg.Done()
		<-ch
	}()
}

// Wait blocks until all background refresh goroutines complete.
// Primarily useful in tests to ensure deterministic behavior.
func (cc *ComputeCache[T]) Wait() {
	cc.bgWg.Wait()
}

// recordHit increments the hit counter if metrics are configured.
func (cc *ComputeCache[T]) recordHit() {
	if cc.cfg.metrics != nil {
		cc.cfg.metrics.hits.WithLabelValues(cc.cfg.name).Inc()
	}
}

// recordMiss increments the miss counter if metrics are configured.
func (cc *ComputeCache[T]) recordMiss() {
	if cc.cfg.metrics != nil {
		cc.cfg.metrics.misses.WithLabelValues(cc.cfg.name).Inc()
	}
}

// recordStaleServe increments the stale serve counter if metrics are configured.
func (cc *ComputeCache[T]) recordStaleServe() {
	if cc.cfg.metrics != nil {
		cc.cfg.metrics.staleServes.WithLabelValues(cc.cfg.name).Inc()
	}
}

// recordError increments the error counter if metrics are configured.
func (cc *ComputeCache[T]) recordError() {
	if cc.cfg.metrics != nil {
		cc.cfg.metrics.errors.WithLabelValues(cc.cfg.name).Inc()
	}
}
