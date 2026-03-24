package cache

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrCacheClosed is returned by GetOrCompute when the ComputeCache has been closed.
var ErrCacheClosed = errors.New("cache: ComputeCache is closed")

// ComputeFunc computes a cache value. Returns the value, its TTL, and any error.
type ComputeFunc[T any] func(ctx context.Context) (T, time.Duration, error)

// envelope is the structure stored in the cache backend.
// It wraps the actual value with an expiration timestamp for
// stale-while-revalidate support.
type envelope struct {
	Value     []byte `json:"v"`
	ExpiresAt int64  `json:"e"` // unix nanoseconds
}

// computeConfig holds configuration for a ComputeCache.
type computeConfig struct {
	staleTTL       time.Duration
	refreshTimeout time.Duration
	metrics        *ComputeMetrics
	name           string
}

// defaultRefreshTimeout is used when no WithRefreshTimeout option is provided.
const defaultRefreshTimeout = 30 * time.Second

// ComputeOption configures a ComputeCache.
type ComputeOption func(*computeConfig)

// WithStaleTTL sets the stale-while-revalidate window. After the primary TTL
// expires, stale data is served for up to this duration while a background
// refresh runs. Zero means no stale serving (default).
// Negative values are silently ignored (the default zero value is used).
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
func WithComputeMetricsRegisterer(m *ComputeMetrics) ComputeOption {
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

// WithRefreshTimeout sets the timeout for background refresh operations.
// Values <= 0 are ignored and the default (30 seconds) is used.
func WithRefreshTimeout(d time.Duration) ComputeOption {
	return func(cfg *computeConfig) {
		if d > 0 {
			cfg.refreshTimeout = d
		}
	}
}

// ComputeCache wraps a Cache backend with singleflight deduplication and
// stale-while-revalidate support. Only one goroutine computes a given key
// at a time; concurrent callers wait for the in-flight computation.
type ComputeCache[T any] struct {
	backend Cache
	prefix  string
	codec   Codec[T]
	group   singleflight.Group
	cfg     computeConfig

	bgWg      sync.WaitGroup
	cancelBg  context.CancelFunc
	bgCtx     context.Context
	closeOnce sync.Once
	closed    atomic.Bool
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

	cfg := computeConfig{
		name:           "default",
		refreshTimeout: defaultRefreshTimeout,
	}
	for _, o := range opts {
		o(&cfg)
	}

	bgCtx, cancelBg := context.WithCancel(context.Background())

	return &ComputeCache[T]{
		backend:  backend,
		prefix:   prefix,
		codec:    JSONCodec[T]{},
		cfg:      cfg,
		bgCtx:    bgCtx,
		cancelBg: cancelBg,
	}, nil
}

// NewComputeCacheWithCodec creates a ComputeCache with a custom Codec.
// If codec is nil, JSONCodec[T] is used.
func NewComputeCacheWithCodec[T any](backend Cache, prefix string, codec Codec[T], opts ...ComputeOption) (*ComputeCache[T], error) {
	cc, err := NewComputeCache[T](backend, prefix, opts...)
	if err != nil {
		return nil, err
	}
	if codec != nil {
		cc.codec = codec
	}
	return cc, nil
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

	if cc.closed.Load() {
		return zero, ErrCacheClosed
	}

	full, err := cc.fullKey(key)
	if err != nil {
		return zero, err
	}

	// Try the backend first.
	data, getErr := cc.backend.Get(ctx, full)
	if getErr == nil {
		return cc.handleHit(ctx, full, data, fn)
	}
	if !errors.Is(getErr, ErrCacheMiss) {
		// Backend error (e.g., Redis timeout) — fall through to compute
		// but record the error for observability.
		cc.recordError()
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
	data []byte,
	fn ComputeFunc[T],
) (T, error) {
	val, expiresAt, err := cc.decodeEnvelope(data)
	if err != nil {
		cc.recordError()
		cc.recordMiss()
		return cc.computeAndStore(ctx, full, fn)
	}

	now := time.Now().UnixNano()
	if now < expiresAt {
		// Fresh hit.
		cc.recordHit()
		return val, nil
	}

	// Expired but still within backend TTL (stale window).
	if cc.cfg.staleTTL == 0 {
		// No stale serving configured; treat expired entry as a miss.
		cc.recordMiss()
		return cc.computeAndStore(ctx, full, fn)
	}
	cc.recordStaleServe()
	cc.triggerBackgroundRefresh(full, fn)
	return val, nil
}

// decodeEnvelope unmarshals the envelope and returns the decoded value and expiration.
func (cc *ComputeCache[T]) decodeEnvelope(data []byte) (T, int64, error) {
	var zero T
	var env envelope
	if err := envelopeCodec.Unmarshal(data, &env); err != nil {
		return zero, 0, err
	}
	var val T
	if err := cc.codec.Unmarshal(env.Value, &val); err != nil {
		return zero, 0, err
	}
	return val, env.ExpiresAt, nil
}

// envelopeCodec is used for the outer envelope (always JSON).
var envelopeCodec = JSONCodec[envelope]{}

// computeAndStore runs fn through singleflight, stores the result, and returns.
func (cc *ComputeCache[T]) computeAndStore(ctx context.Context, full string, fn ComputeFunc[T]) (T, error) {
	var zero T

	// Use WithoutCancel so that if the first caller's context is cancelled,
	// other waiters sharing this singleflight call are not affected.
	computeCtx := context.WithoutCancel(ctx)
	result, err, shared := cc.group.Do(full, func() (interface{}, error) {
		return cc.executeCompute(computeCtx, full, fn)
	})
	if err != nil {
		if !shared {
			cc.recordError()
		}
		return zero, err
	}

	// Guard against nil result (e.g., when T is an interface type).
	if result == nil {
		return zero, nil
	}
	val, ok := result.(T)
	if !ok {
		return zero, fmt.Errorf("cache compute: unexpected result type %T", result)
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

	valBytes, marshalErr := cc.codec.Marshal(val)
	if marshalErr != nil {
		return zero, fmt.Errorf("cache compute marshal: %w", marshalErr)
	}

	env := envelope{
		Value:     valBytes,
		ExpiresAt: time.Now().Add(ttl).UnixNano(),
	}

	envData, marshalErr := envelopeCodec.Marshal(env)
	if marshalErr != nil {
		return zero, fmt.Errorf("cache compute envelope marshal: %w", marshalErr)
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
	cc.bgWg.Add(1)

	ch := cc.group.DoChan(full, func() (interface{}, error) {
		// Use a timeout-scoped context derived from the background context
		// created at construction time. This prevents unbounded refresh
		// operations and allows Close() to cancel in-flight refreshes.
		bgCtx, cancel := context.WithTimeout(cc.bgCtx, cc.cfg.refreshTimeout)
		defer cancel()
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

// Close cancels all background refresh operations and waits for them to
// finish. After Close returns, no new background refreshes will be started.
// Close is idempotent; calling it multiple times is safe.
// Implements io.Closer.
func (cc *ComputeCache[T]) Close() error {
	cc.closeOnce.Do(func() {
		cc.closed.Store(true)
		cc.cancelBg()
	})
	cc.bgWg.Wait()
	return nil
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
