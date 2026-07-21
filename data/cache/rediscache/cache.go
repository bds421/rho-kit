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

// cappedGetScript returns the value only when STRLEN is within the cap,
// in a single Redis round trip. Shape:
//
//	{0}           — key missing
//	{-1, strlen}  — oversize
//	{1, value}    — hit
//
// Atomic relative to the STRLEN+GET pair, so the previous TOCTOU branch
// (and its warn log) is unnecessary on the hot path.
var cappedGetScript = goredis.NewScript(`
local n = redis.call('STRLEN', KEYS[1])
local cap = tonumber(ARGV[1])
if n > cap then
  return {-1, n}
end
local v = redis.call('GET', KEYS[1])
if not v then
  return {0}
end
return {1, v}
`)

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
	prefix       string // Redis key namespace; default is name + ":"
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
// Panics on a nil registerer — same fail-fast contract as [WithRegisterer]
// — so a conditionally-nil registerer cannot silently pollute the global
// default registry.
// Replaces the v1 WithCacheRegisterer spelling so cache option naming
// matches the kit-wide convention.
func WithMetricsRegisterer(reg prometheus.Registerer) CacheOption {
	if reg == nil {
		panic("rediscache: WithMetricsRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(rc *Cache) {
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

// WithKeyPrefix sets the Redis key namespace prepended to every cache key.
// Default is the cache name plus a trailing colon (e.g. name "orders" →
// "orders:"), so two NewCache instances on a shared Redis cannot collide
// by omitting a prefix. Pass "" only when the caller deliberately wants
// a flat keyspace (e.g. migrating an existing unprefixed deployment);
// an empty prefix is accepted but logs no automatic isolation.
func WithKeyPrefix(p string) CacheOption {
	return func(rc *Cache) {
		rc.prefix = p
	}
}

// NewCache creates a Redis-backed cache. The name is used for Prometheus
// metric labels and, by default, as the Redis key namespace (name + ":").
// Override with [WithKeyPrefix] when a different isolation key is needed.
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
		prefix:       name + ":",
		maxValueSize: defaultMaxValueSize,
	}
	for _, o := range opts {
		if o == nil {
			panic("rediscache: NewCache option must not be nil")
		}
		o(rc)
	}
	// Only fall back to the default-registry metrics when no option set
	// them. Evaluating defaultMetrics() in the struct literal above would
	// register hits_total/misses_total on prometheus.DefaultRegisterer on
	// the first NewCache call even when every caller passes
	// WithMetricsRegisterer, polluting the global registry.
	if rc.metrics == nil {
		rc.metrics = defaultMetrics()
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
// When a positive maxValueSize is configured (the default), the cap is
// enforced by a single Lua script that pairs STRLEN with GET atomically —
// one Redis RTT and no TOCTOU window between the size probe and the read.
// With maxValueSize disabled the path is a plain GET.
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
		res, err := cappedGetScript.Run(ctx, rc.client, []string{rc.redisKey(key)}, rc.maxValueSize).Result()
		if err != nil {
			if isServerCmdError(err) {
				rc.metrics.misses.WithLabelValues(rc.name).Inc()
				return nil, sharedcache.ErrCacheMiss
			}
			return nil, redact.WrapError("redis cache get", err)
		}
		pair, ok := res.([]any)
		if !ok || len(pair) < 1 {
			return nil, errors.New("rediscache: unexpected capped-get script result shape")
		}
		tag, ok := pair[0].(int64)
		if !ok {
			return nil, errors.New("rediscache: unexpected capped-get script result shape")
		}
		switch tag {
		case 0:
			rc.metrics.misses.WithLabelValues(rc.name).Inc()
			return nil, sharedcache.ErrCacheMiss
		case -1:
			rc.metrics.misses.WithLabelValues(rc.name).Inc()
			return nil, fmt.Errorf("redis cache get: %w", sharedcache.ErrValueTooLarge)
		case 1:
			if len(pair) < 2 {
				return nil, errors.New("rediscache: unexpected capped-get script result shape")
			}
			switch v := pair[1].(type) {
			case string:
				val = []byte(v)
			case []byte:
				val = v
			default:
				return nil, errors.New("rediscache: unexpected capped-get value type")
			}
			rc.metrics.hits.WithLabelValues(rc.name).Inc()
			return val, nil
		default:
			return nil, errors.New("rediscache: unexpected capped-get script tag")
		}
	}
	val, err = rc.client.Get(ctx, rc.redisKey(key)).Bytes()
	if errors.Is(err, goredis.Nil) || isServerCmdError(err) {
		// WRONGTYPE and other per-key server errors are treated as miss,
		// matching MGet, so a poisoned co-tenant key cannot fail the
		// request path while the bulk path succeeds.
		rc.metrics.misses.WithLabelValues(rc.name).Inc()
		return nil, sharedcache.ErrCacheMiss
	}
	if err != nil {
		return nil, redact.WrapError("redis cache get", err)
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
		return fmt.Errorf("cache set: %w", sharedcache.ErrValueTooLarge)
	}
	if err := rc.client.Set(ctx, rc.redisKey(key), value, ttl).Err(); err != nil {
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
	if err := rc.client.Del(ctx, rc.redisKey(key)).Err(); err != nil {
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
	n, err := rc.client.Exists(ctx, rc.redisKey(key)).Result()
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
		rkeys := make([]string, len(keys))
		for i, k := range keys {
			rkeys[i] = rc.redisKey(k)
		}
		vals, mgetErr := rc.client.MGet(ctx, rkeys...).Result()
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
				// A non-string MGET reply (e.g. a wrong-typed key) yields
				// no usable value; count it as a miss to match the nil
				// case and the capped path's per-key miss accounting.
				rc.metrics.misses.WithLabelValues(rc.name).Inc()
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
		strLens[i] = pipe.StrLen(ctx, rc.redisKey(k))
	}
	// Exec returns the first command error, which may be a per-key
	// server reply (e.g. WRONGTYPE for a co-tenant's list/hash). Those
	// are handled per command below; only transport/pipeline failures
	// abort the batch. See isServerCmdError.
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, goredis.Nil) && !isServerCmdError(err) {
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
			// A per-key server error (e.g. WRONGTYPE for a key holding
			// a list/hash) is treated as a miss, matching the uncapped
			// MGET path where Redis returns nil for wrong-typed keys.
			// Failing the whole batch here would let one hostile
			// co-tenant deny every capped request containing that key.
			if isServerCmdError(slErr) {
				rc.metrics.misses.WithLabelValues(rc.name).Inc()
				continue
			}
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
		gets[i] = pipe.Get(ctx, rc.redisKey(k))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, goredis.Nil) && !isServerCmdError(err) {
		return nil, redact.WrapError("redis cache mget get pipeline", err)
	}
	for i, k := range getKeys {
		val, gErr := gets[i].Bytes()
		if errors.Is(gErr, goredis.Nil) {
			rc.metrics.misses.WithLabelValues(rc.name).Inc()
			continue
		}
		if gErr != nil {
			// As with STRLEN above, a per-key server error (e.g. the
			// value's type changed to a list/hash between STRLEN and
			// GET) is treated as a miss rather than failing the batch.
			if isServerCmdError(gErr) {
				rc.metrics.misses.WithLabelValues(rc.name).Inc()
				continue
			}
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

// isServerCmdError reports whether err is a per-key error reply returned
// by the Redis server (e.g. WRONGTYPE) as opposed to a transport or
// pipeline failure. Server command errors implement [goredis.Error];
// connection/transport errors do not. The redis.Nil sentinel is also a
// server reply but callers treat it explicitly as a miss before reaching
// here, so it is excluded.
func isServerCmdError(err error) bool {
	if err == nil || errors.Is(err, goredis.Nil) {
		return false
	}
	var re goredis.Error
	return errors.As(err, &re)
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
			return fmt.Errorf("cache mset: %w", sharedcache.ErrValueTooLarge)
		}
	}
	pipe := rc.client.Pipeline()
	for k, v := range items {
		pipe.Set(ctx, rc.redisKey(k), v, ttl)
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
		return false, fmt.Errorf("cache setnx: %w", sharedcache.ErrValueTooLarge)
	}
	ok, err = rc.client.SetNX(ctx, rc.redisKey(key), value, ttl).Result()
	if err != nil {
		return false, redact.WrapError("redis cache setnx", err)
	}
	return ok, nil
}

func (rc *Cache) redisKey(key string) string {
	if rc.prefix == "" {
		return key
	}
	return rc.prefix + key
}

func (rc *Cache) ready() error {
	if rc == nil || rc.client == nil || rc.name == "" || rc.metrics == nil || rc.metrics.hits == nil || rc.metrics.misses == nil {
		return sharedcache.ErrInvalidCache
	}
	return nil
}
