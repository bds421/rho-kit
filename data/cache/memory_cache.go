package cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"runtime"
	"runtime/debug"
	"sync"
	"time"
	"weak"

	"github.com/dgraph-io/ristretto/v2"

	"github.com/bds421/rho-kit/core/v2/redact"
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
	logger          *slog.Logger

	// setNXMu serialises SetNX operations for atomicity within this
	// process. Ristretto's underlying SetWithTTL is not test-and-set, so
	// without this two concurrent SetNX calls could both observe "key
	// missing" and both write — defeating the whole point of NX.
	setNXMu sync.Mutex

	// stopSweeper signals the background nxClaims-sweeper goroutine to
	// exit. Closed by Close.
	stopSweeper chan struct{}
	closeOnce   sync.Once

	// closer owns the ristretto Close. It lives in its own heap object so a
	// runtime.AddCleanup watchdog can drive shutdown without holding a
	// reference back to the MemoryCache (which would pin it and defeat the
	// cleanup). Both explicit Close and the watchdog funnel through its Once.
	closer *ristrettoCloser

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

// ristrettoCloser owns the teardown of the underlying ristretto store. It is a
// standalone heap object (not embedded in MemoryCache) so the
// runtime.AddCleanup watchdog registered in NewMemoryCache can close the store
// when the MemoryCache becomes unreachable without holding a strong reference
// back to the MemoryCache. ristretto's own processItems goroutine strongly
// references the ristretto cache and only exits on cache.Close(), so without
// this watchdog a forgotten MemoryCache.Close leaks that goroutine (and its
// cleanup ticker) for the process lifetime — the weak-ref nxClaims sweeper
// alone cannot reclaim it.
type ristrettoCloser struct {
	cache *ristretto.Cache[string, []byte]
	once  sync.Once
}

// close stops the ristretto store exactly once. Safe to call from both the
// explicit MemoryCache.Close path and the GC cleanup watchdog (which never run
// concurrently: the watchdog only fires once the MemoryCache is unreachable,
// at which point no caller can hold it to invoke Close).
func (rc *ristrettoCloser) close() {
	if rc == nil {
		return
	}
	rc.once.Do(func() {
		if rc.cache != nil {
			rc.cache.Close()
		}
	})
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
		// Mirror WithEntryCost: clear any byte/custom cost function so the
		// cache is bounded by entry count (cost=1), honoring the documented
		// "Implies WithEntryCost" contract even when a prior WithByteCost /
		// WithCostFunc set costFunc.
		mc.costFunc = nil
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

// WithoutMetrics disables cache metrics. Metrics are enabled by default;
// this option mirrors the kit-wide WithFoo / WithoutFoo pairing and avoids
// shadowing the WithMetrics(*Metrics) signature used everywhere else.
func WithoutMetrics() MemoryCacheOption {
	return func(mc *MemoryCache) {
		mc.metricsEnabled = false
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

// WithLogger sets the *slog.Logger used to record a panic inside the
// background nxClaims-sweeper goroutine. The sweeper has no caller
// to surface a panic to; without panic recovery + logging the
// goroutine would silently exit and the unbounded-growth invariant
// for nxClaims would silently break. When unset the cache falls back
// to [slog.Default]. Matches the kit's per-package [WithLogger]
// convention.
func WithLogger(l *slog.Logger) MemoryCacheOption {
	return func(mc *MemoryCache) {
		if l != nil {
			mc.logger = l
		}
	}
}

// NewMemoryCache creates an in-memory cache.
//
// Cost accounting: by default the cache is bounded by bytes — every entry
// counts as len(value) and the default MaxCost is 64 MiB. Use WithMaxSize
// or WithEntryCost to switch to entry-count accounting; use WithCostFunc
// to plug in a custom cost (e.g. struct size including overhead).
// NewMemoryCache constructs a [MemoryCache] and starts its background
// sweeper goroutine plus ristretto's own processItems goroutine. This is a
// side-effecting constructor; pair it with [MemoryCache.Close] in shutdown
// wiring for deterministic cleanup. A forgotten Close is only eventually
// recoverable — a runtime.AddCleanup watchdog closes ristretto once the
// cache is GC'd — so it must not be relied on as a steady-state strategy
// (see Close docs).
func NewMemoryCache(opts ...MemoryCacheOption) (*MemoryCache, error) {
	mc := &MemoryCache{metricsEnabled: true}
	for _, o := range opts {
		if o == nil {
			panic("cache: NewMemoryCache option must not be nil")
		}
		o(mc)
	}
	if mc.logger == nil {
		mc.logger = slog.Default()
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
		return nil, redact.WrapError("memory cache: init failed", err)
	}
	mc.cache = cache
	mc.closer = &ristrettoCloser{cache: cache}
	mc.stopSweeper = make(chan struct{})
	// Weak-ref sweeper: if a caller forgets Close, the goroutine
	// noticing weak.Value() == nil exits on its own — see
	// data/ratelimit/tokenbucket.runSweeper for the design rationale.
	// The logger is captured by value, not via the weak ref, so a
	// sweeper panic still emits even if mc has already been GC'd.
	go runNXClaimsSweeper(weak.Make(mc), mc.stopSweeper, mc.logger)

	// Watchdog for a forgotten Close: ristretto's processItems goroutine
	// strongly references the ristretto cache and only exits on Close, so
	// the weak-ref sweeper above cannot reclaim it. AddCleanup runs once the
	// MemoryCache becomes unreachable and closes the ristretto store, which
	// stops that goroutine and its cleanup ticker. The cleanup argument is
	// the standalone closer — never mc — so registering it does not pin the
	// MemoryCache. It funnels through closer.once, so an explicit Close that
	// already ran makes this a no-op. The nxClaims sweeper needs no help
	// here: it self-terminates on its next tick once weak.Value() is nil.
	runtime.AddCleanup(mc, func(rc *ristrettoCloser) { rc.close() }, mc.closer)

	return mc, nil
}

// runNXClaimsSweeper periodically drops nxClaims entries whose recorded
// expiry has passed. Without this, the map grows unbounded for the
// process lifetime when callers churn through many distinct SetNX keys
// (per-user idempotency keys, ephemeral feature flags, etc.).
//
// The function is package-private and takes a [weak.Pointer] so the
// goroutine never holds a strong reference to the MemoryCache — a
// forgotten Close cannot keep this loop alive past the cache's
// reachability lifetime. The logger is passed by value so a panic at
// teardown can still surface even after mc has been GC'd.
func runNXClaimsSweeper(weakCache weak.Pointer[MemoryCache], stop <-chan struct{}, logger *slog.Logger) {
	defer func() {
		if r := recover(); r != nil {
			// Without recovery the goroutine would silently die and
			// nxClaims would grow unbounded — exactly the invariant
			// this sweeper exists to maintain. Logging is the only
			// observable signal because the caller has no handle on
			// the goroutine.
			logger.Error("cache: nxClaims sweeper panicked, unbounded-growth invariant broken",
				redact.Panic(r),
				"stack", string(debug.Stack()),
			)
		}
	}()
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			mc := weakCache.Value()
			if mc == nil {
				return
			}
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

// MustNewMemoryCache is like [NewMemoryCache] but panics on error.
// Use in init() or main() where failure is unrecoverable. Like
// [NewMemoryCache], this is a side-effecting constructor that starts
// background goroutines; pair it with [MemoryCache.Close].
func MustNewMemoryCache(opts ...MemoryCacheOption) *MemoryCache {
	mc, err := NewMemoryCache(opts...)
	if err != nil {
		panic("cache: MustNewMemoryCache memory cache configuration is invalid")
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
	// Flush Ristretto's write buffer before the existence check. A NEW-key
	// SetWithTTL (plain Set) only enqueues to setBuf and is not yet visible
	// via Get; without Wait a Set(k) immediately followed by SetNX(k) would
	// miss the buffered value and overwrite it while returning ok=true,
	// violating the in-process test-and-set contract. nxClaims only covers
	// prior SetNX writes, not plain Set, so the flush is required here too.
	mc.cache.Wait()
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
	// Ristretto buffers writes (including deletes); without Wait, an
	// immediate Get on the same key after Delete can still see the
	// pre-Delete value because the Del is still in the write buffer.
	// Mirror what Set does to give Delete strict-visibility semantics.
	mc.cache.Wait()
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

// Close stops the underlying cache workers (including ristretto's
// processItems goroutine and the TTL cleanup ticker). Implements io.Closer.
//
// Call Close for deterministic shutdown — it releases the ristretto store
// immediately and unblocks the nxClaims sweeper without waiting for GC.
//
// If Close is forgotten, a runtime.AddCleanup watchdog registered at
// construction closes the ristretto store once the MemoryCache becomes
// unreachable, and the nxClaims sweeper (which holds only a [weak.Pointer])
// exits on its next tick. Recovery is therefore eventual, but it depends on
// the garbage collector actually running and reclaiming the cache: until then
// ristretto's processItems goroutine and cleanup ticker keep running. Treat a
// forgotten Close as a real bug — pair every constructor with Close in
// shutdown wiring rather than relying on the GC watchdog as a steady-state
// strategy.
//
// Idempotent and safe for concurrent calls — the underlying ristretto
// cache is Close()-d exactly once. MemoryCache is safe for concurrent use
// across all methods.
func (mc *MemoryCache) Close() error {
	if err := mc.ready(); err != nil {
		return err
	}
	mc.stopBackgroundSweeper()
	mc.closer.close()
	return nil
}

func (mc *MemoryCache) ready() error {
	if mc == nil || mc.cache == nil {
		return ErrInvalidCache
	}
	return nil
}
