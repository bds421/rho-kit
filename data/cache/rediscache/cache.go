package rediscache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"

	"github.com/bds421/rho-kit/core/v2/redact"
	sharedcache "github.com/bds421/rho-kit/data/v2/cache"
	"github.com/bds421/rho-kit/infra/redis/v2"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Compile-time interface compliance check.
var _ sharedcache.Cache = (*Cache)(nil)

// defaultMaxValueSize is the default maximum value size for cache entries (10 MiB).
const defaultMaxValueSize = 10 << 20

// Metrics holds Prometheus collectors for cache hit/miss monitoring.
type Metrics struct {
	hits   *prometheus.CounterVec
	misses *prometheus.CounterVec
}

// MetricsOption configures the rediscache metric constructor.
// Standardised across the kit so every package exposes
// `NewMetrics(opts ...MetricsOption)`.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for cache
// metrics. The kit-canonical name on the inner [MetricsOption] type;
// callers building a Cache through [NewCache] should pass
// [WithMetricsRegisterer] (the CacheOption variant) instead.
// Defaults to [prometheus.DefaultRegisterer]; passing nil panics.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("rediscache: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers cache metrics. Pass
// [WithRegisterer] to use a non-default registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("rediscache: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer

	m := &Metrics{
		hits: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Subsystem: "cache",
				Name:      "hits_total",
				Help:      "Total cache hits.",
			},
			[]string{"name"},
		),
		misses: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "redis",
				Subsystem: "cache",
				Name:      "misses_total",
				Help:      "Total cache misses.",
			},
			[]string{"name"},
		),
	}

	m.hits = promutil.MustRegisterOrGet(reg, m.hits)
	m.misses = promutil.MustRegisterOrGet(reg, m.misses)

	return m
}

var defaultMetrics = sync.OnceValue(func() *Metrics { return NewMetrics() })

// Cache implements Cache using a Redis backend.
type Cache struct {
	client       goredis.UniversalClient
	name         string // for metrics labeling
	maxValueSize int    // max value size in bytes; 0 = no limit
	metrics      *Metrics
	logger       *slog.Logger
}

// CacheOption configures a Cache.
type CacheOption func(*Cache)

// WithCacheMaxValueSize sets the maximum value size in bytes for cache entries.
// Default is 10 MiB. Set to 0 to disable the limit (use with caution).
// Negative values panic.
func WithCacheMaxValueSize(n int) CacheOption {
	if n < 0 {
		panic("rediscache: WithCacheMaxValueSize requires n >= 0")
	}
	return func(rc *Cache) {
		rc.maxValueSize = n
	}
}

// WithMetricsRegisterer sets the Prometheus registerer for cache
// metrics. If not set, prometheus.DefaultRegisterer is used.
// Replaces the v1 WithCacheRegisterer spelling so cache option naming
// matches the kit-wide convention.
func WithMetricsRegisterer(reg prometheus.Registerer) CacheOption {
	return func(rc *Cache) {
		if reg == nil {
			rc.metrics = NewMetrics()
			return
		}
		rc.metrics = NewMetrics(WithRegisterer(reg))
	}
}

// WithLogger sets the *slog.Logger used by the cache to surface the
// TOCTOU value-too-large race (a writer replaces the cached value
// with one exceeding the configured cap between STRLEN and GET).
// That race is rare but security-relevant — a hostile or legacy
// writer can otherwise force allocations larger than the configured
// cap. When unset the cache falls back to [slog.Default]. Matches
// the kit's per-package [WithLogger] convention.
func WithLogger(l *slog.Logger) CacheOption {
	return func(rc *Cache) {
		if l != nil {
			rc.logger = l
		}
	}
}

// NewCache creates a Redis-backed cache. The name is used for
// Prometheus metric labels to distinguish multiple cache instances.
// Returns an error if name is invalid. Panics if client is nil — a
// miswired cache would otherwise dereference nil on first use.
func NewCache(client goredis.UniversalClient, name string, opts ...CacheOption) (*Cache, error) {
	if client == nil {
		panic("rediscache: NewCache requires a non-nil Redis client")
	}
	if err := redis.ValidateName(name, "cache"); err != nil {
		return nil, err
	}
	rc := &Cache{
		client:       client,
		name:         name,
		maxValueSize: defaultMaxValueSize,
		metrics:      defaultMetrics(),
	}
	for _, o := range opts {
		if o == nil {
			panic("rediscache: NewCache option must not be nil")
		}
		o(rc)
	}
	if rc.logger == nil {
		rc.logger = slog.Default()
	}
	return rc, nil
}

// Get retrieves a value from Redis. Returns [sharedcache.ErrCacheMiss] on
// redis.Nil and [sharedcache.ErrValueTooLarge] when a stored value exceeds
// the configured cap.
//
// The cap is enforced via STRLEN before GET so a hostile or legacy writer
// that stored a multi-MB value cannot force this process to allocate the
// full response body before the cap runs. A post-GET length check still
// runs to catch the rare TOCTOU window where the value is replaced between
// STRLEN and GET.
func (rc *Cache) Get(ctx context.Context, key string) (val []byte, err error) {
	ctx, span := rc.startSpan(ctx, "cache.Get")
	defer func() { recordResult(span, err); span.End() }()
	if err := rc.ready(); err != nil {
		return nil, err
	}
	if err := sharedcache.ValidateKey(key); err != nil {
		return nil, err
	}
	if rc.maxValueSize > 0 {
		sz, err := rc.client.StrLen(ctx, key).Result()
		if err != nil {
			return nil, redact.WrapError("redis cache get strlen", err)
		}
		if sz > int64(rc.maxValueSize) {
			rc.metrics.misses.WithLabelValues(rc.name).Inc()
			return nil, fmt.Errorf("redis cache get: %w", sharedcache.ErrValueTooLarge)
		}
		// STRLEN==0 covers both "missing" and "empty stored value"; both
		// are safe to forward to GET, which distinguishes them via redis.Nil.
	}
	val, err = rc.client.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		rc.metrics.misses.WithLabelValues(rc.name).Inc()
		return nil, sharedcache.ErrCacheMiss
	}
	if err != nil {
		return nil, redact.WrapError("redis cache get", err)
	}
	// TOCTOU guard: another writer may have replaced the value with a
	// larger one between STRLEN and GET. The cap check is cheap and
	// preserves the contract.
	if rc.maxValueSize > 0 && len(val) > rc.maxValueSize {
		// Surface the race so operators can correlate with writer
		// audit logs — repeated occurrences suggest a misbehaving
		// or hostile writer attempting to bypass the cap.
		rc.logger.Warn("rediscache: TOCTOU value-too-large after STRLEN passed",
			"cache", rc.name,
			"size_bytes", len(val),
			"max_bytes", rc.maxValueSize,
			redact.String("key", key),
		)
		return nil, fmt.Errorf("redis cache get: %w", sharedcache.ErrValueTooLarge)
	}
	rc.metrics.hits.WithLabelValues(rc.name).Inc()
	return val, nil
}

// Set stores a value in Redis with the given TTL. Zero TTL means no expiration.
// Returns an error if TTL is negative or the value exceeds the configured maximum size.
func (rc *Cache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) (err error) {
	ctx, span := rc.startSpan(ctx, "cache.Set")
	defer func() { recordResult(span, err); span.End() }()
	if err := rc.ready(); err != nil {
		return err
	}
	if err := sharedcache.ValidateKey(key); err != nil {
		return err
	}
	if ttl < 0 {
		return fmt.Errorf("cache set: TTL must not be negative")
	}
	if rc.maxValueSize > 0 && len(value) > rc.maxValueSize {
		return fmt.Errorf("cache value exceeds maximum size")
	}
	if err := rc.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return redact.WrapError("redis cache set", err)
	}
	return nil
}

// Delete removes a key from Redis.
func (rc *Cache) Delete(ctx context.Context, key string) (err error) {
	ctx, span := rc.startSpan(ctx, "cache.Delete")
	defer func() { recordResult(span, err); span.End() }()
	if err := rc.ready(); err != nil {
		return err
	}
	if err := sharedcache.ValidateKey(key); err != nil {
		return err
	}
	if err := rc.client.Del(ctx, key).Err(); err != nil {
		return redact.WrapError("redis cache delete", err)
	}
	return nil
}

// Exists checks whether a key exists in Redis.
func (rc *Cache) Exists(ctx context.Context, key string) (exists bool, err error) {
	ctx, span := rc.startSpan(ctx, "cache.Exists")
	defer func() { recordResult(span, err); span.End() }()
	if err := rc.ready(); err != nil {
		return false, err
	}
	if err := sharedcache.ValidateKey(key); err != nil {
		return false, err
	}
	n, err := rc.client.Exists(ctx, key).Result()
	if err != nil {
		return false, redact.WrapError("redis cache exists", err)
	}
	return n > 0, nil
}

// MGet retrieves multiple values via a STRLEN+GET pipeline per key so
// oversize foreign-written values are detected before allocation.
// Missing keys are silently absent from the returned map; oversized
// keys are also dropped from the returned map but counted as misses
// — failing the whole batch on a single poisoned entry would let one
// hostile co-tenant deny the entire request, which is worse than
// silent omission for cache reads. Callers needing strict oversize
// When a max-value cap is configured, MGet issues two round-trips:
// STRLEN for every key, then GET only for keys whose value fits the
// cap. Wave 66 split this from a single pipelined STRLEN+GET round-
// trip because the earlier implementation read every value's full
// bytes over the wire before discarding oversized ones — defeating
// the cap's purpose of bounding heap and bandwidth per request.
// Without a cap configured, MGet stays one round-trip via MGET.
func (rc *Cache) MGet(ctx context.Context, keys []string) (out map[string][]byte, err error) {
	ctx, span := rc.startSpan(ctx, "cache.MGet")
	span.SetAttributes(attribute.Int("kit.cache.batch_size", len(keys)))
	defer func() { recordResult(span, err); span.End() }()
	if err := rc.ready(); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return map[string][]byte{}, nil
	}
	if err := sharedcache.ValidateBulkKeys(keys); err != nil {
		return nil, err
	}

	// Without a cap configured, MGET in a single round-trip is the
	// efficient path. Skip the per-key pipeline.
	if rc.maxValueSize <= 0 {
		vals, mgetErr := rc.client.MGet(ctx, keys...).Result()
		if mgetErr != nil {
			return nil, redact.WrapError("redis cache mget", mgetErr)
		}
		out = make(map[string][]byte, len(keys))
		for i, v := range vals {
			if v == nil {
				rc.metrics.misses.WithLabelValues(rc.name).Inc()
				continue
			}
			s, ok := v.(string)
			if !ok {
				continue
			}
			rc.metrics.hits.WithLabelValues(rc.name).Inc()
			out[keys[i]] = []byte(s)
		}
		return out, nil
	}

	// Round-trip 1: STRLEN every key. The pipeline carries no GET so
	// no oversize bytes traverse the wire here.
	pipe := rc.client.Pipeline()
	strLens := make([]*goredis.IntCmd, len(keys))
	for i, k := range keys {
		strLens[i] = pipe.StrLen(ctx, k)
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, goredis.Nil) {
		return nil, redact.WrapError("redis cache mget strlen pipeline", err)
	}

	// Decide which keys are under the cap. STRLEN returns 0 for
	// missing keys (Redis treats them as empty); we issue GET only
	// for under-cap candidates and count clear miss/oversize cases
	// up front.
	getKeys := make([]string, 0, len(keys))
	out = make(map[string][]byte, len(keys))
	for i, k := range keys {
		sz, slErr := strLens[i].Result()
		if slErr != nil {
			return nil, redact.WrapError("redis cache mget strlen", slErr)
		}
		if sz > int64(rc.maxValueSize) {
			rc.metrics.misses.WithLabelValues(rc.name).Inc()
			continue
		}
		getKeys = append(getKeys, k)
	}
	if len(getKeys) == 0 {
		return out, nil
	}

	// Round-trip 2: GET only the under-cap keys.
	pipe = rc.client.Pipeline()
	gets := make([]*goredis.StringCmd, len(getKeys))
	for i, k := range getKeys {
		gets[i] = pipe.Get(ctx, k)
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, goredis.Nil) {
		return nil, redact.WrapError("redis cache mget get pipeline", err)
	}
	for i, k := range getKeys {
		val, gErr := gets[i].Bytes()
		if errors.Is(gErr, goredis.Nil) {
			rc.metrics.misses.WithLabelValues(rc.name).Inc()
			continue
		}
		if gErr != nil {
			return nil, redact.WrapError("redis cache mget get", gErr)
		}
		if len(val) > rc.maxValueSize {
			// TOCTOU: value grew between STRLEN and GET.
			rc.metrics.misses.WithLabelValues(rc.name).Inc()
			continue
		}
		rc.metrics.hits.WithLabelValues(rc.name).Inc()
		out[k] = val
	}
	return out, nil
}

// MSet stores multiple keys with a shared TTL. Implemented as a pipelined
// SET-with-EX rather than MSET because Redis MSET does not accept a TTL —
// the alternatives are MSET + per-key EXPIRE round-trips (slower, two
// network round-trips per batch) or pipelined SET.
//
// Atomicity caveat: this is NOT all-or-nothing. The pipeline is sent as
// a single batch and Redis processes the commands in order, but a
// connection failure or server crash mid-batch can leave a partial set
// of keys written. Callers that require all-or-nothing semantics must
// implement their own MULTI/EXEC or Lua-script path; the BulkCache
// contract documents the same caveat.
func (rc *Cache) MSet(ctx context.Context, items map[string][]byte, ttl time.Duration) (err error) {
	ctx, span := rc.startSpan(ctx, "cache.MSet")
	span.SetAttributes(attribute.Int("kit.cache.batch_size", len(items)))
	defer func() { recordResult(span, err); span.End() }()
	if err := rc.ready(); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	if ttl < 0 {
		return fmt.Errorf("cache mset: TTL must not be negative")
	}
	if err := sharedcache.ValidateBulkItems(items); err != nil {
		return err
	}
	for _, v := range items {
		if rc.maxValueSize > 0 && len(v) > rc.maxValueSize {
			return fmt.Errorf("cache mset: value exceeds maximum size")
		}
	}
	pipe := rc.client.Pipeline()
	for k, v := range items {
		pipe.Set(ctx, k, v, ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return redact.WrapError("redis cache mset", err)
	}
	return nil
}

// SetNX stores a value only if the key does not already exist (Redis SET NX).
// Returns true when the value was stored, false when the key already had
// a value. Atomic across replicas — use this instead of Exists+Set for
// cross-process compute-once semantics.
func (rc *Cache) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (ok bool, err error) {
	ctx, span := rc.startSpan(ctx, "cache.SetNX")
	defer func() { recordResult(span, err); span.End() }()
	if err := rc.ready(); err != nil {
		return false, err
	}
	if err := sharedcache.ValidateKey(key); err != nil {
		return false, err
	}
	if ttl < 0 {
		return false, fmt.Errorf("cache setnx: TTL must not be negative")
	}
	if rc.maxValueSize > 0 && len(value) > rc.maxValueSize {
		return false, fmt.Errorf("cache setnx: value exceeds maximum size")
	}
	ok, err = rc.client.SetNX(ctx, key, value, ttl).Result()
	if err != nil {
		return false, redact.WrapError("redis cache setnx", err)
	}
	return ok, nil
}

func (rc *Cache) ready() error {
	if rc == nil || rc.client == nil || rc.name == "" || rc.metrics == nil || rc.metrics.hits == nil || rc.metrics.misses == nil {
		return sharedcache.ErrInvalidCache
	}
	return nil
}
