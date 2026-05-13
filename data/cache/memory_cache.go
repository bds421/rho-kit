package cache

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
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
	entryCost       bool
	ignoreIntCost   bool
	cleanupInterval time.Duration

	// setNXMu serialises SetNX operations for atomicity within this
	// process. Ristretto's underlying SetWithTTL is not test-and-set, so
	// without this two concurrent SetNX calls could both observe "key
	// missing" and both write — defeating the whole point of NX.
	setNXMu sync.Mutex

	// stopSweeper signals the background nxClaims-sweeper goroutine to
	// exit. Closed by Close.
	stopSweeper        chan struct{}
	closeOnce          sync.Once
	ristrettoCloseOnce sync.Once

	// nxClaims tracks keys that have been successfully claimed via SetNX.
	// Ristretto buffers SetWithTTL writes AND its TinyLFU admission policy
	// may silently reject entries, so a subsequent Get cannot reliably
	// observe a prior SetNX. Claims here give true test-and-set semantics
	// within the process; entries are cleaned up on Delete or once their
	// recorded expiry passes.
	nxClaims sync.Map
}

// nxClaim records when a SetNX claim expires. A zero expiresAt means the
// claim has no TTL and lives until Delete.
type nxClaim struct {
	expiresAt time.Time
}

// MemoryCacheOption configures a MemoryCache.
type MemoryCacheOption func(*MemoryCache)

// WithMaxSize sets the maximum number of entries by treating each entry
// as cost=1. Implies WithEntryCost — when set, the default byte-based cost
// accounting is disabled and the cache is bounded by entry count instead.
// The value must be positive.
func WithMaxSize(n int) MemoryCacheOption {
	if n <= 0 {
		panic("cache: WithMaxSize requires n > 0")
	}
	return func(mc *MemoryCache) {
		mc.maxSize = n
		mc.entryCost = true
	}
}

// WithEntryCost forces every entry to count as cost=1 regardless of value
// size. Use this when the cache is sized by entry count rather than bytes.
// Mutually exclusive with WithByteCost / WithCostFunc; the last option wins.
func WithEntryCost() MemoryCacheOption {
	return func(mc *MemoryCache) {
		mc.entryCost = true
		mc.costFunc = nil
	}
}

// WithMaxCost sets the maximum total cache cost.
// Use WithCostFunc or WithByteCost to control how costs are computed.
func WithMaxCost(cost int64) MemoryCacheOption {
	if cost <= 0 {
		panic("cache: WithMaxCost requires cost > 0")
	}
	return func(mc *MemoryCache) {
		mc.maxCost = cost
	}
}

// WithNumCounters sets the number of TinyLFU counters (recommended: 10x items).
func WithNumCounters(n int64) MemoryCacheOption {
	if n <= 0 {
		panic("cache: WithNumCounters requires n > 0")
	}
	return func(mc *MemoryCache) {
		mc.numCounters = n
	}
}

// WithBufferItems sets the get buffer size (default: 64).
func WithBufferItems(n int64) MemoryCacheOption {
	if n <= 0 {
		panic("cache: WithBufferItems requires n > 0")
	}
	return func(mc *MemoryCache) {
		mc.bufferItems = n
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
		mc.entryCost = false
	}
}

// WithByteCost uses len(value) as the item cost (bytes). This is the
// default for unconfigured caches; the option exists for explicit
// documentation of intent and to override a prior WithEntryCost.
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
	if d <= 0 {
		panic("cache: WithCleanupInterval requires a positive duration")
	}
	return func(mc *MemoryCache) {
		mc.cleanupInterval = d
	}
}

// NewMemoryCache creates an in-memory cache.
//
// Cost accounting: by default the cache is bounded by bytes — every entry
// counts as len(value) and the default MaxCost is 64 MiB. Use WithMaxSize
// or WithEntryCost to switch to entry-count accounting; use WithCostFunc
// to plug in a custom cost (e.g. struct size including overhead).
func NewMemoryCache(opts ...MemoryCacheOption) (*MemoryCache, error) {
	mc := &MemoryCache{metricsEnabled: true}
	for _, o := range opts {
		if o == nil {
			panic("cache: NewMemoryCache option must not be nil")
		}
		o(mc)
	}

	if mc.costFunc == nil && !mc.entryCost {
		mc.costFunc = func(value []byte) int64 { return int64(len(value)) }
	}

	maxCost := mc.maxCost
	numCounters := mc.numCounters
	if maxCost <= 0 {
		maxCost = int64(64 * 1024 * 1024)
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
	mc.stopSweeper = make(chan struct{})
	go mc.sweepNXClaims()

	return mc, nil
}

// sweepNXClaims runs a periodic background pass over nxClaims, deleting
// entries whose recorded expiry has passed. Without this, the map grows
// unbounded for the process lifetime when callers churn through many
// distinct SetNX keys (per-user idempotency keys, ephemeral feature
// flags, etc.) — a slow leak the agent reviewer flagged.
func (mc *MemoryCache) sweepNXClaims() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-mc.stopSweeper:
			return
		case now := <-t.C:
			mc.nxClaims.Range(func(k, v any) bool {
				c, ok := v.(nxClaim)
				if !ok {
					return true
				}
				if !c.expiresAt.IsZero() && now.After(c.expiresAt) {
					mc.nxClaims.Delete(k)
				}
				return true
			})
		}
	}
}

// stopBackgroundSweeper closes the nxClaims sweeper. Safe to call
// multiple times; the existing Close() invokes it.
func (mc *MemoryCache) stopBackgroundSweeper() {
	if mc == nil {
		return
	}
	mc.closeOnce.Do(func() {
		if mc.stopSweeper != nil {
			close(mc.stopSweeper)
		}
	})
}

// MustNewMemoryCache is like NewMemoryCache but panics on error.
// Use in init() or main() where failure is unrecoverable.
func MustNewMemoryCache(opts ...MemoryCacheOption) *MemoryCache {
	mc, err := NewMemoryCache(opts...)
	if err != nil {
		panic("cache: memory cache configuration is invalid")
	}
	return mc
}

// Get retrieves a value. Returns ErrCacheMiss if not found or expired.
func (mc *MemoryCache) Get(ctx context.Context, key string) ([]byte, error) {
	if err := mc.ready(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
func (mc *MemoryCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := mc.ready(); err != nil {
		return err
	}
	if err := ValidateKey(key); err != nil {
		return err
	}
	if ttl < 0 {
		return fmt.Errorf("cache set: TTL must not be negative")
	}
	stored := make([]byte, len(value))
	copy(stored, value)

	cost := int64(1)
	if mc.costFunc != nil {
		cost = 0
	}
	if !mc.cache.SetWithTTL(key, stored, cost, ttl) {
		return ErrAdmissionRejected
	}
	return nil
}

// Sync blocks until all pending set operations are visible.
// This is primarily useful in tests to ensure deterministic read-after-write.
func (mc *MemoryCache) Sync() {
	if mc == nil || mc.cache == nil {
		return
	}
	mc.cache.Wait()
}

// MGet implements [BulkCache] by fanning out per-key Get calls. The Ristretto
// backend doesn't expose a true multi-get, but the fan-out runs lock-free.
// Missing keys are silently absent from the result.
func (mc *MemoryCache) MGet(ctx context.Context, keys []string) (map[string][]byte, error) {
	if err := mc.ready(); err != nil {
		return nil, err
	}
	if err := ValidateBulkKeys(keys); err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(keys))
	for _, k := range keys {
		v, err := mc.Get(ctx, k)
		if err != nil {
			if errors.Is(err, ErrCacheMiss) {
				continue
			}
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// MSet implements [BulkCache] by fanning out per-key Set calls. Stops at the
// first error; partial writes may be visible.
func (mc *MemoryCache) MSet(ctx context.Context, items map[string][]byte, ttl time.Duration) error {
	if err := mc.ready(); err != nil {
		return err
	}
	if err := ValidateBulkItems(items); err != nil {
		return err
	}
	for k, v := range items {
		if err := mc.Set(ctx, k, v, ttl); err != nil {
			return err
		}
	}
	return nil
}

// SetNX implements [BulkCache]. Atomic only within a single process; for
// cross-process compute-once, use the Redis-backed BulkCache.
//
// Implementation: Ristretto buffers SetWithTTL writes AND its TinyLFU
// admission policy may silently reject entries, so a subsequent Get
// cannot reliably observe a prior SetNX. The mutex serialises the path;
// we track claims in nxClaims for the duration of the requested TTL so
// that follow-up SetNX calls see the claim independent of Ristretto's
// admission decisions and buffer flushes.
func (mc *MemoryCache) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	if err := mc.ready(); err != nil {
		return false, err
	}
	if err := ValidateKey(key); err != nil {
		return false, err
	}
	mc.setNXMu.Lock()
	defer mc.setNXMu.Unlock()

	if existing, claimed := mc.nxClaims.Load(key); claimed {
		c := existing.(nxClaim)
		if c.expiresAt.IsZero() || time.Now().Before(c.expiresAt) {
			return false, nil
		}
		mc.nxClaims.Delete(key)
	}
	if _, ok := mc.cache.Get(key); ok {
		return false, nil
	}

	if err := mc.Set(ctx, key, value, ttl); err != nil {
		// Admission rejection means the value never made it into the cache,
		// so recording a claim would block legitimate retries against an
		// empty slot until the recorded TTL elapsed.
		return false, err
	}
	mc.cache.Wait()

	claim := nxClaim{}
	if ttl > 0 {
		claim.expiresAt = time.Now().Add(ttl)
	}
	mc.nxClaims.Store(key, claim)
	return true, nil
}

// Delete removes a key.
//
// Holds setNXMu around the cache+claim removal so a concurrent SetNX
// cannot interleave its claim store between this Delete's two clears —
// otherwise the stale claim would outlive the entry and block legitimate
// re-claims until the original TTL elapsed.
func (mc *MemoryCache) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := mc.ready(); err != nil {
		return err
	}
	if err := ValidateKey(key); err != nil {
		return err
	}
	mc.setNXMu.Lock()
	defer mc.setNXMu.Unlock()
	mc.cache.Del(key)
	mc.nxClaims.Delete(key)
	return nil
}

// Exists checks whether a non-expired key exists.
func (mc *MemoryCache) Exists(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := mc.ready(); err != nil {
		return false, err
	}
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
//
// Idempotent and safe for concurrent calls — the underlying ristretto
// cache is Close()-d exactly once. MemoryCache is safe for concurrent use
// across all methods.
func (mc *MemoryCache) Close() error {
	if err := mc.ready(); err != nil {
		return err
	}
	mc.stopBackgroundSweeper()
	mc.ristrettoCloseOnce.Do(func() {
		mc.cache.Close()
	})
	return nil
}

func (mc *MemoryCache) ready() error {
	if mc == nil || mc.cache == nil {
		return ErrInvalidCache
	}
	return nil
}
