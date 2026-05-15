package rediscache

import (
	"context"
	"time"

	sharedcache "github.com/bds421/rho-kit/data/v2/cache"
	"github.com/bds421/rho-kit/infra/redis/v2"
)

// Compile-time interface compliance check.
var _ sharedcache.Cache = (*DegradedCache)(nil)

// DegradedCache wraps a primary Cache and a fallback Cache. When the
// Redis connection is healthy, all operations go to the primary. When
// unhealthy, the degradation policy decides whether to delegate to the
// fallback or return an error.
//
// The fallback is typically a MemoryCache (passthrough) or nil (fail-fast).
// DegradedCache never mutates its inputs.
type DegradedCache struct {
	primary  *Cache
	fallback sharedcache.Cache
	conn     *redis.Connection
	policy   redis.DegradationPolicy
}

// DegradedCacheOption configures a DegradedCache.
type DegradedCacheOption func(*DegradedCache)

// WithDegradationPolicy overrides the default PassthroughPolicy.
func WithDegradationPolicy(p redis.DegradationPolicy) DegradedCacheOption {
	if p == nil {
		panic("rediscache: WithDegradationPolicy requires a non-nil policy")
	}
	return func(dc *DegradedCache) {
		dc.policy = p
	}
}

// NewDegradedCache creates a cache that delegates to primary when Redis is
// healthy and to fallback when Redis is unhealthy. The fallback may be nil
// if the policy is FailFast (all operations error when degraded).
//
// When fallback is nil and Redis is unhealthy, Get returns ErrCacheMiss and
// Set/Delete/Exists return the policy's OnUnavailable error (or nil for
// passthrough).
func NewDegradedCache(
	primary *Cache,
	fallback sharedcache.Cache,
	conn *redis.Connection,
	opts ...DegradedCacheOption,
) *DegradedCache {
	if primary == nil {
		panic("rediscache: NewDegradedCache: primary cache must not be nil")
	}
	if conn == nil {
		panic("rediscache: NewDegradedCache: connection must not be nil")
	}

	dc := &DegradedCache{
		primary:  primary,
		fallback: fallback,
		conn:     conn,
		policy:   redis.PassthroughPolicy{},
	}
	for _, o := range opts {
		if o == nil {
			panic("rediscache: NewDegradedCache option must not be nil")
		}
		o(dc)
	}
	return dc
}

// Policy returns the current degradation policy name.
func (dc *DegradedCache) Policy() string {
	if dc == nil || dc.policy == nil {
		return "invalid"
	}
	return dc.policy.Name()
}

// Healthy reports whether the underlying Redis connection is healthy.
func (dc *DegradedCache) Healthy() bool {
	if dc == nil || dc.conn == nil {
		return false
	}
	return dc.conn.Healthy()
}

// Get retrieves a value. When degraded, returns from fallback or ErrCacheMiss.
func (dc *DegradedCache) Get(ctx context.Context, key string) ([]byte, error) {
	if err := dc.ready(); err != nil {
		return nil, err
	}
	if err := sharedcache.ValidateKey(key); err != nil {
		return nil, err
	}
	if dc.conn.Healthy() {
		return dc.primary.Get(ctx, key)
	}
	if err := dc.policy.OnUnavailable(ctx); err != nil {
		return nil, err
	}
	if dc.fallback != nil {
		return dc.fallback.Get(ctx, key)
	}
	return nil, sharedcache.ErrCacheMiss
}

// Set stores a value. When degraded, delegates to fallback or returns policy error.
func (dc *DegradedCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := dc.ready(); err != nil {
		return err
	}
	if err := sharedcache.ValidateKey(key); err != nil {
		return err
	}
	if dc.conn.Healthy() {
		return dc.primary.Set(ctx, key, value, ttl)
	}
	if err := dc.policy.OnUnavailable(ctx); err != nil {
		return err
	}
	if dc.fallback != nil {
		return dc.fallback.Set(ctx, key, value, ttl)
	}
	return nil
}

// Delete removes a key. When degraded, delegates to fallback or returns policy error.
func (dc *DegradedCache) Delete(ctx context.Context, key string) error {
	if err := dc.ready(); err != nil {
		return err
	}
	if err := sharedcache.ValidateKey(key); err != nil {
		return err
	}
	if dc.conn.Healthy() {
		return dc.primary.Delete(ctx, key)
	}
	if err := dc.policy.OnUnavailable(ctx); err != nil {
		return err
	}
	if dc.fallback != nil {
		return dc.fallback.Delete(ctx, key)
	}
	return nil
}

// Exists checks whether a key exists. When degraded, delegates to fallback
// or returns false.
func (dc *DegradedCache) Exists(ctx context.Context, key string) (bool, error) {
	if err := dc.ready(); err != nil {
		return false, err
	}
	if err := sharedcache.ValidateKey(key); err != nil {
		return false, err
	}
	if dc.conn.Healthy() {
		return dc.primary.Exists(ctx, key)
	}
	if err := dc.policy.OnUnavailable(ctx); err != nil {
		return false, err
	}
	if dc.fallback != nil {
		return dc.fallback.Exists(ctx, key)
	}
	return false, nil
}

func (dc *DegradedCache) ready() error {
	if dc == nil || dc.primary == nil || dc.conn == nil || dc.policy == nil {
		return sharedcache.ErrInvalidCache
	}
	if err := dc.primary.ready(); err != nil {
		return err
	}
	return nil
}
