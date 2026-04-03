package rediscache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"

	sharedcache "github.com/bds421/rho-kit/data/cache"
	"github.com/bds421/rho-kit/infra/redis"
	"github.com/bds421/rho-kit/observability/promutil"
)

// Compile-time interface compliance check.
var _ sharedcache.Cache = (*RedisCache)(nil)

// defaultMaxValueSize is the default maximum value size for cache entries (10 MiB).
const defaultMaxValueSize = 10 << 20

// Metrics holds Prometheus collectors for cache hit/miss monitoring.
type Metrics struct {
	hits   *prometheus.CounterVec
	misses *prometheus.CounterVec
}

// NewMetrics creates and registers cache metrics with the given registerer.
// If reg is nil, prometheus.DefaultRegisterer is used.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

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

	promutil.RegisterCollector(reg, m.hits)
	promutil.RegisterCollector(reg, m.misses)

	return m
}

var defaultMetrics = NewMetrics(nil)

// RedisCache implements Cache using a Redis backend.
type RedisCache struct {
	client       goredis.UniversalClient
	name         string // for metrics labeling
	maxValueSize int    // max value size in bytes; 0 = no limit
	metrics      *Metrics
}

// CacheOption configures a RedisCache.
type CacheOption func(*RedisCache)

// WithCacheMaxValueSize sets the maximum value size in bytes for cache entries.
// Default is 10 MiB. Set to 0 to disable the limit (use with caution).
// Negative values are ignored.
func WithCacheMaxValueSize(n int) CacheOption {
	return func(rc *RedisCache) {
		if n >= 0 {
			rc.maxValueSize = n
		}
	}
}

// WithCacheRegisterer sets the Prometheus registerer for cache metrics.
// If not set, prometheus.DefaultRegisterer is used.
func WithCacheRegisterer(reg prometheus.Registerer) CacheOption {
	return func(rc *RedisCache) {
		rc.metrics = NewMetrics(reg)
	}
}

// NewRedisCache creates a Redis-backed cache. The name is used for
// Prometheus metric labels to distinguish multiple cache instances.
// Returns an error if name is invalid.
func NewRedisCache(client goredis.UniversalClient, name string, opts ...CacheOption) (*RedisCache, error) {
	if err := redis.ValidateName(name, "cache"); err != nil {
		return nil, err
	}
	rc := &RedisCache{
		client:       client,
		name:         name,
		maxValueSize: defaultMaxValueSize,
		metrics:      defaultMetrics,
	}
	for _, o := range opts {
		o(rc)
	}
	return rc, nil
}

// Get retrieves a value from Redis. Returns cache.ErrCacheMiss on redis.Nil.
func (rc *RedisCache) Get(ctx context.Context, key string) ([]byte, error) {
	if err := sharedcache.ValidateKey(key); err != nil {
		return nil, err
	}
	val, err := rc.client.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		rc.metrics.misses.WithLabelValues(rc.name).Inc()
		return nil, sharedcache.ErrCacheMiss
	}
	if err != nil {
		return nil, fmt.Errorf("redis cache get: %w", err)
	}
	rc.metrics.hits.WithLabelValues(rc.name).Inc()
	return val, nil
}

// Set stores a value in Redis with the given TTL. Zero TTL means no expiration.
// Returns an error if TTL is negative or the value exceeds the configured maximum size.
func (rc *RedisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := sharedcache.ValidateKey(key); err != nil {
		return err
	}
	if ttl < 0 {
		return fmt.Errorf("cache set: TTL must not be negative (got %v)", ttl)
	}
	if rc.maxValueSize > 0 && len(value) > rc.maxValueSize {
		return fmt.Errorf("cache value size %d exceeds max %d", len(value), rc.maxValueSize)
	}
	if err := rc.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("redis cache set: %w", err)
	}
	return nil
}

// Delete removes a key from Redis.
func (rc *RedisCache) Delete(ctx context.Context, key string) error {
	if err := sharedcache.ValidateKey(key); err != nil {
		return err
	}
	if err := rc.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("redis cache delete: %w", err)
	}
	return nil
}

// Exists checks whether a key exists in Redis.
func (rc *RedisCache) Exists(ctx context.Context, key string) (bool, error) {
	if err := sharedcache.ValidateKey(key); err != nil {
		return false, err
	}
	n, err := rc.client.Exists(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("redis cache exists: %w", err)
	}
	return n > 0, nil
}
