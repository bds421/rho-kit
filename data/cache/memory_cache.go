package cache

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/dgraph-io/ristretto/v2"
)

// MemoryCache implements Cache using an in-memory Ristretto cache with TTL support.
// Suitable for testing, single-instance deployments, or as a local L1 cache.
type MemoryCache struct {
	cache           *ristretto.Cache[string, []byte]
	maxSize         int
	maxCost         int64
	numCounters     int64
	bufferItems     int64
	metricsEnabled  bool
	costFunc        func(value []byte) int64
	ignoreIntCost   bool
	cleanupInterval time.Duration
}

// MemoryCacheOption configures a MemoryCache.
type MemoryCacheOption func(*MemoryCache)

// WithMaxSize sets the maximum number of entries by treating each entry
// as cost=1. Values <= 0 are ignored.
func WithMaxSize(n int) MemoryCacheOption {
	return func(mc *MemoryCache) {
		if n > 0 {
			mc.maxSize = n
		}
	}
}

// WithMaxCost sets the maximum total cache cost.
// Use WithCostFunc or WithByteCost to control how costs are computed.
func WithMaxCost(cost int64) MemoryCacheOption {
	return func(mc *MemoryCache) {
		if cost > 0 {
			mc.maxCost = cost
		}
	}
}

// WithNumCounters sets the number of TinyLFU counters (recommended: 10x items).
func WithNumCounters(n int64) MemoryCacheOption {
	return func(mc *MemoryCache) {
		if n > 0 {
			mc.numCounters = n
		}
	}
}

// WithBufferItems sets the get buffer size (default: 64).
func WithBufferItems(n int64) MemoryCacheOption {
	return func(mc *MemoryCache) {
		if n > 0 {
			mc.bufferItems = n
		}
	}
}

// WithMetrics enables or disables cache metrics (enabled by default).
func WithMetrics(enabled bool) MemoryCacheOption {
	return func(mc *MemoryCache) {
		mc.metricsEnabled = enabled
	}
}

// WithCostFunc sets a custom cost function for values.
// When set, Set uses cost=0 so Ristretto calls this function.
func WithCostFunc(fn func(value []byte) int64) MemoryCacheOption {
	return func(mc *MemoryCache) {
		mc.costFunc = fn
	}
}

// WithByteCost uses len(value) as the item cost (bytes).
func WithByteCost() MemoryCacheOption {
	return WithCostFunc(func(value []byte) int64 { return int64(len(value)) })
}

// WithIgnoreInternalCost ignores Ristretto's internal storage cost.
func WithIgnoreInternalCost(ignore bool) MemoryCacheOption {
	return func(mc *MemoryCache) {
		mc.ignoreIntCost = ignore
	}
}

// WithCleanupInterval configures a background goroutine that removes expired
// entries at the given interval. Callers MUST call Close() to stop the
// background goroutine; failing to do so leaks a goroutine for the lifetime
// of the process.
func WithCleanupInterval(d time.Duration) MemoryCacheOption {
	return func(mc *MemoryCache) {
		mc.cleanupInterval = d
	}
}

// NewMemoryCache creates an in-memory cache.
func NewMemoryCache(opts ...MemoryCacheOption) (*MemoryCache, error) {
	mc := &MemoryCache{metricsEnabled: true}
	for _, o := range opts {
		o(mc)
	}

	maxCost := mc.maxCost
	numCounters := mc.numCounters
	if maxCost <= 0 {
		maxCost = int64(math.MaxInt64)
		if mc.maxSize > 0 {
			maxCost = int64(mc.maxSize)
		}
	}
	if numCounters <= 0 {
		numCounters = int64(1_000_000)
		if mc.maxSize > 0 {
			numCounters = int64(mc.maxSize) * 10
		}
	}
	bufferItems := mc.bufferItems
	if bufferItems <= 0 {
		bufferItems = 64
	}

	// Default to 60s cleanup interval so TTL-expired entries are always
	// evicted, even if WithCleanupInterval is not explicitly called.
	// Ristretto interprets TtlTickerDurationInSec=0 as "disable TTL cleanup"
	// which would cause expired entries to accumulate in memory.
	ttlSeconds := int64(60)
	if mc.cleanupInterval > 0 {
		ttlSeconds = int64(math.Max(1, mc.cleanupInterval.Seconds()))
	}

	cache, err := ristretto.NewCache(&ristretto.Config[string, []byte]{
		NumCounters:            numCounters,
		MaxCost:                maxCost,
		BufferItems:            bufferItems,
		Metrics:                mc.metricsEnabled,
		Cost:                   mc.costFunc,
		IgnoreInternalCost:     mc.ignoreIntCost,
		TtlTickerDurationInSec: ttlSeconds,
	})
	if err != nil {
		return nil, fmt.Errorf("memory cache: init failed: %w", err)
	}
	mc.cache = cache

	return mc, nil
}

// MustNewMemoryCache is like NewMemoryCache but panics on error.
// Use in init() or main() where failure is unrecoverable.
func MustNewMemoryCache(opts ...MemoryCacheOption) *MemoryCache {
	mc, err := NewMemoryCache(opts...)
	if err != nil {
		panic(err)
	}
	return mc
}

// Get retrieves a value. Returns ErrCacheMiss if not found or expired.
func (mc *MemoryCache) Get(_ context.Context, key string) ([]byte, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	value, ok := mc.cache.Get(key)
	if !ok {
		return nil, ErrCacheMiss
	}

	// Return a copy to preserve immutability.
	result := make([]byte, len(value))
	copy(result, value)
	return result, nil
}

// Set stores a value with an expiration. Zero TTL means no expiration.
// Returns an error if TTL is negative (likely a programming error).
func (mc *MemoryCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if ttl < 0 {
		return fmt.Errorf("cache set: TTL must not be negative (got %v)", ttl)
	}
	// Copy value to prevent caller mutation.
	stored := make([]byte, len(value))
	copy(stored, value)

	cost := int64(1)
	if mc.costFunc != nil {
		cost = 0
	}
	if !mc.cache.SetWithTTL(key, stored, cost, ttl) {
		// Ristretto's TinyLFU admission policy rejected the entry — this is
		// normal behavior (the policy predicts the entry won't be accessed
		// frequently enough to justify evicting an existing one). Not an error.
		return nil
	}
	// Note: no Wait() call here. Ristretto batches writes for throughput;
	// the entry will be visible after the next batch flush. Use Sync() if
	// immediate visibility is required (e.g. in tests).
	return nil
}

// Sync blocks until all pending set operations are visible.
// This is primarily useful in tests to ensure deterministic read-after-write.
func (mc *MemoryCache) Sync() {
	mc.cache.Wait()
}

// Delete removes a key.
func (mc *MemoryCache) Delete(_ context.Context, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	mc.cache.Del(key)
	return nil
}

// Exists checks whether a non-expired key exists.
func (mc *MemoryCache) Exists(_ context.Context, key string) (bool, error) {
	if err := ValidateKey(key); err != nil {
		return false, err
	}
	_, ok := mc.cache.Get(key)
	return ok, nil
}

// Close stops the underlying cache workers (including the TTL cleanup
// goroutine started by WithCleanupInterval). Implements io.Closer.
// Callers MUST call Close when the cache is no longer needed; failing to
// do so leaks goroutines. In server lifecycle code, register Close as a
// shutdown hook or use defer.
func (mc *MemoryCache) Close() error {
	mc.cache.Close()
	return nil
}
