package rediscache

import (
	"context"
	"time"

	sharedcache "github.com/bds421/rho-kit/data/v2/cache"
	"github.com/bds421/rho-kit/infra/redis/v2"
)

// Compile-time interface compliance checks. DegradedCache implements
// the bulk/CAS fast paths too so cache.SetNX keeps cross-process
// compute-once and cache.MGet/MSet keep pipelining instead of silently
// downgrading to the per-key fallbacks of the free functions.
var (
	_ sharedcache.Cache     = (*DegradedCache)(nil)
	_ sharedcache.BulkCache = (*DegradedCache)(nil)
)

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
		panic("rediscache: NewDegradedCache primary cache must not be nil")
	}
	if conn == nil {
		panic("rediscache: NewDegradedCache connection must not be nil")
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
	if err := redis.ApplyDegradation(ctx, dc.conn, dc.policy); err != nil {
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
	if err := redis.ApplyDegradation(ctx, dc.conn, dc.policy); err != nil {
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
	if err := redis.ApplyDegradation(ctx, dc.conn, dc.policy); err != nil {
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
	if err := redis.ApplyDegradation(ctx, dc.conn, dc.policy); err != nil {
		return false, err
	}
	if dc.fallback != nil {
		return dc.fallback.Exists(ctx, key)
	}
	return false, nil
}

// MGet retrieves multiple values. When healthy, forwards to the
// primary's pipelined MGet. When degraded, delegates to the fallback
// (preserving its bulk fast path via [sharedcache.MGet]) or returns an
// empty map after the policy permits it.
func (dc *DegradedCache) MGet(ctx context.Context, keys []string) (map[string][]byte, error) {
	if err := dc.ready(); err != nil {
		return nil, err
	}
	if dc.conn.Healthy() {
		return dc.primary.MGet(ctx, keys)
	}
	if err := redis.ApplyDegradation(ctx, dc.conn, dc.policy); err != nil {
		return nil, err
	}
	if dc.fallback != nil {
		return sharedcache.MGet(ctx, dc.fallback, keys)
	}
	if err := sharedcache.ValidateBulkKeys(keys); err != nil {
		return nil, err
	}
	return map[string][]byte{}, nil
}

// MSet stores multiple values. When healthy, forwards to the primary's
// pipelined MSet. When degraded, delegates to the fallback (preserving
// its bulk fast path via [sharedcache.MSet]) or is a no-op after the
// policy permits it.
func (dc *DegradedCache) MSet(ctx context.Context, items map[string][]byte, ttl time.Duration) error {
	if err := dc.ready(); err != nil {
		return err
	}
	if dc.conn.Healthy() {
		return dc.primary.MSet(ctx, items, ttl)
	}
	if err := redis.ApplyDegradation(ctx, dc.conn, dc.policy); err != nil {
		return err
	}
	if dc.fallback != nil {
		return sharedcache.MSet(ctx, dc.fallback, items, ttl)
	}
	return nil
}

// SetNX stores a value only if the key does not already exist. When
// healthy, forwards to the primary's atomic, cross-process SetNX. When
// degraded, delegates to the fallback (preserving its native SetNX via
// [sharedcache.SetNX]).
//
// With no fallback, SetNX fails closed: it returns (false, nil) rather
// than fabricating a compute-once win. Set/Delete may no-op under
// passthrough degradation, but SetNX is a mutual-exclusion primitive —
// reporting ok=true without persisting would let every replica during a
// Redis outage believe it won the slot and duplicate side effects
// (email, webhook, billing event). Callers that need an error rather
// than a lost claim should use [redis.FailFastPolicy].
func (dc *DegradedCache) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	if err := dc.ready(); err != nil {
		return false, err
	}
	if err := sharedcache.ValidateKey(key); err != nil {
		return false, err
	}
	if dc.conn.Healthy() {
		return dc.primary.SetNX(ctx, key, value, ttl)
	}
	if err := redis.ApplyDegradation(ctx, dc.conn, dc.policy); err != nil {
		return false, err
	}
	if dc.fallback != nil {
		return sharedcache.SetNX(ctx, dc.fallback, key, value, ttl)
	}
	// Fail closed: no exclusivity was recorded, so the caller must not
	// treat this as a compute-once win.
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
