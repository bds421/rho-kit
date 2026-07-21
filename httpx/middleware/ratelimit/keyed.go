package ratelimit

import (
	"context"
	"errors"
	"hash/fnv"
	"log/slog"
	"math"
	"net/http"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
)

type keyedRateLimitEntry struct {
	count     int
	windowEnd time.Time
}

// defaultMaxKeyedPerShard limits each shard's LRU size to prevent OOM.
const defaultMaxKeyedPerShard = 10_000

type keyedShard struct {
	mu      sync.Mutex
	entries *lru.Cache[string, *keyedRateLimitEntry]
}

// KeyedLimiter implements a sharded fixed-window rate limiter keyed by an
// arbitrary string. Sharding reduces mutex contention under high concurrency,
// matching the approach used by [Limiter]. The type satisfies
// [lifecycle.Component] so callers can register it directly with a
// lifecycle.Runner.
//
// Concurrency: AllowKey is safe for concurrent use. Start must only be
// called once per instance (guarded by an internal started flag).
type KeyedLimiter struct {
	shards      [numShards]keyedShard
	limit       int
	window      time.Duration
	now         func() time.Time
	health      HealthIndicator
	degradation DegradationHandler
	metrics     *Metrics
	name        string
	maxPerShard int

	startMu sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	doneCh  chan struct{}
}

// KeyedOption configures a KeyedLimiter.
type KeyedOption func(*KeyedLimiter)

// WithKeyedClock sets the time source for the keyed rate limiter.
// Useful for deterministic testing without time.Sleep. Panics on nil
// to fail fast at construction rather than dereferencing a nil func
// on the first request through the limiter.
func WithKeyedClock(fn func() time.Time) KeyedOption {
	if fn == nil {
		panic("middleware/ratelimit: WithKeyedClock requires a non-nil time source")
	}
	return func(rl *KeyedLimiter) { rl.now = fn }
}

// WithKeyedMetrics attaches Prometheus metrics to the keyed rate
// limiter. The limiter also registers itself with the metrics'
// active-keys collector so a misconfigured key extractor that explodes
// the per-shard LRU surfaces as
// `http_ratelimit_keyed_limiter_active_keys{limiter}` before it pages
// on memory.
//
// Registration with the active-keys collector is deferred to the end of
// [NewKeyedLimiter] so the limiter is only published once its shards and
// name are fully initialized; publishing mid-construction would race a
// concurrent scrape that reads those fields.
//
// Lifetime requirement: the active-keys collector retains a reference to every
// limiter attached to it and exposes no untrack path, so a metrics-attached
// KeyedLimiter must be a process-lifetime singleton. Do NOT attach short-lived
// or per-request limiters via this option — each one is pinned for the life of
// the metrics registry (including its shard LRUs) and keeps emitting a stale
// active_keys series on every scrape.
func WithKeyedMetrics(m *Metrics) KeyedOption {
	if m == nil {
		panic("middleware/ratelimit: WithKeyedMetrics requires non-nil metrics")
	}
	return func(rl *KeyedLimiter) {
		rl.metrics = m
	}
}

// WithKeyedLimiterName sets the low-cardinality limiter label used by
// Prometheus metrics. Use static names such as "api_key" or "login".
func WithKeyedLimiterName(name string) KeyedOption {
	name = normalizeLimiterName(name)
	return func(rl *KeyedLimiter) { rl.name = name }
}

// WithKeyedMaxPerShard sets the per-shard LRU capacity for distinct keys
// (default 10_000). See [WithMaxPerShard] on Limiter for rationale.
//
// Panics if n <= 0.
func WithKeyedMaxPerShard(n int) KeyedOption {
	if n <= 0 {
		panic("middleware/ratelimit: WithKeyedMaxPerShard requires a positive size")
	}
	return func(l *KeyedLimiter) { l.maxPerShard = n }
}


// NewKeyedLimiter creates a rate limiter allowing limit requests per window per key.
// Panics if limit or window are not positive — these indicate misconfiguration.
func NewKeyedLimiter(limit int, window time.Duration, opts ...KeyedOption) *KeyedLimiter {
	if limit <= 0 {
		panic("middleware/ratelimit: NewKeyedLimiter limit must be positive")
	}
	if window <= 0 {
		panic("middleware/ratelimit: NewKeyedLimiter window must be positive")
	}
	rl := &KeyedLimiter{
		limit:       limit,
		window:      window,
		now:         time.Now,
		name:        defaultLimiterName,
		maxPerShard: defaultMaxKeyedPerShard,
	}
	for _, opt := range opts {
		if opt == nil {
			panic("middleware/ratelimit: NewKeyedLimiter option must not be nil")
		}
		opt(rl)
	}
	for i := range rl.shards {
		cap := rl.maxPerShard
	if cap <= 0 {
		cap = defaultMaxKeyedPerShard
	}
	cache, _ := lru.New[string, *keyedRateLimitEntry](cap)
		rl.shards[i].entries = cache
	}
	// Publish to the active-keys collector only after shards and name are
	// fully initialized. trackKeyedLimiter takes the collector mutex, which
	// establishes the happens-before a concurrent scrape's Collect relies on
	// when it reads rl.shards and rl.Name().
	rl.metrics.trackKeyedLimiter(rl)
	return rl
}

// getShard returns the shard for the given key using FNV-1a hashing.
func (rl *KeyedLimiter) getShard(key string) *keyedShard {
	h := fnv.New32a()
	h.Write([]byte(key))
	return &rl.shards[h.Sum32()%numShards]
}

func (rl *KeyedLimiter) ready() error {
	if rl == nil || rl.limit <= 0 || rl.window <= 0 || rl.now == nil {
		return ErrInvalidLimiter
	}
	for i := range rl.shards {
		if rl.shards[i].entries == nil {
			return ErrInvalidLimiter
		}
	}
	return nil
}

// Allow checks whether the given key is within its rate limit. Invalid keys
// fail closed and are not stored. Call [KeyedLimiter.AllowKey] when the
// caller needs to distinguish invalid keys from throttled keys.
func (rl *KeyedLimiter) Allow(key string) (allowed bool, retryAfter int) {
	allowed, retryAfter, err := rl.AllowKey(key)
	if err != nil {
		return false, 1
	}
	return allowed, retryAfter
}

// AllowKey checks whether the given key is within its rate limit and returns
// an error for invalid keys or uninitialized limiters.
func (rl *KeyedLimiter) AllowKey(key string) (allowed bool, retryAfter int, err error) {
	if err := rl.ready(); err != nil {
		rl.observeDecision(rateLimitOutcomeUnavailable)
		return false, 0, err
	}
	if err := ValidateKey(key); err != nil {
		rl.observeDecision(rateLimitOutcomeInvalidKey)
		return false, 0, err
	}
	now := rl.now()
	s := rl.getShard(key)
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries.Get(key)
	if ok {
		// Mutating *keyedRateLimitEntry through the cached pointer is safe:
		// the shard mutex serialises all access. Get() already marked it recently used.
		//
		// Use !now.Before(windowEnd) (i.e. now >= windowEnd) so the window is
		// half-open [start, end) — identical to the IP Limiter's
		// `elapsed >= window`. At the exact boundary instant both siblings
		// start a fresh window rather than counting against the old one.
		if !now.Before(entry.windowEnd) {
			entry.count = 1
			entry.windowEnd = now.Add(rl.window)
			rl.observeDecision(rateLimitOutcomeAllowed)
			return true, 0, nil
		}
		entry.count++
		if entry.count <= rl.limit {
			rl.observeDecision(rateLimitOutcomeAllowed)
			return true, 0, nil
		}
		seconds := int(math.Ceil(entry.windowEnd.Sub(now).Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		rl.observeDecision(rateLimitOutcomeLimited)
		rl.observeRetryAfter(float64(seconds))
		return false, seconds, nil
	}

	if s.entries.Add(key, &keyedRateLimitEntry{count: 1, windowEnd: now.Add(rl.window)}) {
		rl.observeLRUEviction()
	}
	rl.observeDecision(rateLimitOutcomeAllowed)
	return true, 0, nil
}

func (rl *KeyedLimiter) observeLRUEviction() {
	if rl == nil || rl.metrics == nil {
		return
	}
	rl.metrics.observeLRUEviction(rl.name, "keyed")
}

// Name returns the limiter's configured name so the type satisfies the
// [lifecycle.Component]-adjacent naming convention used by the Runner.
func (rl *KeyedLimiter) Name() string {
	if rl == nil || rl.name == "" {
		return defaultLimiterName
	}
	return rl.name
}

// Start launches the cleanup goroutine that evicts expired entries and
// blocks until ctx is cancelled OR [KeyedLimiter.Stop] is invoked.
// Cleanup runs at 2× the rate limit window to allow entries to fully
// expire before eviction, matching the IP rate limiter's cleanup cadence.
// Start must only be called once; subsequent calls return an error.
func (rl *KeyedLimiter) Start(ctx context.Context) error {
	if err := rl.ready(); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("ratelimit: KeyedLimiter.Start requires a non-nil context")
	}
	rl.startMu.Lock()
	if rl.started {
		rl.startMu.Unlock()
		return errors.New("ratelimit: KeyedLimiter.Start already started")
	}
	if rl.stopped {
		// Stop ran before Start and latched stopped=true. Launching the
		// cleanup loop now would orphan a goroutine the prior Stop already
		// promised to wait on. Reject, mirroring lifecycle.FuncComponent.
		rl.startMu.Unlock()
		return errors.New("ratelimit: KeyedLimiter.Start already stopped")
	}
	rl.started = true
	runCtx, cancel := context.WithCancel(ctx)
	rl.cancel = cancel
	done := make(chan struct{})
	rl.doneCh = done
	rl.startMu.Unlock()

	defer close(done)
	defer cancel()

	ticker := time.NewTicker(cleanupInterval(rl.window))
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			return nil
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("panic in keyed rate limiter cleanup",
							slog.String("limiter", rl.Name()),
							redact.Panic(r),
						)
					}
				}()
				rl.cleanup()
			}()
		}
	}
}

// Stop cancels the cleanup goroutine launched by [KeyedLimiter.Start]
// and waits for it to exit. Stop is idempotent; calls before Start, after
// the goroutine has already exited, or after a prior Stop are no-ops.
func (rl *KeyedLimiter) Stop(ctx context.Context) error {
	if rl == nil {
		return nil
	}
	rl.startMu.Lock()
	if !rl.started || rl.stopped {
		rl.stopped = true
		rl.startMu.Unlock()
		return nil
	}
	rl.stopped = true
	cancel := rl.cancel
	done := rl.doneCh
	rl.startMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	if ctx == nil {
		<-done
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// cleanup evicts expired entries from all shards. Scans at most
// maxCleanupPerShard entries per shard to bound allocation.
func (rl *KeyedLimiter) cleanup() {
	if rl.ready() != nil {
		return
	}
	now := rl.now()
	for i := range rl.shards {
		s := &rl.shards[i]
		// Match the IP limiter's two-phase cleanup: snapshot Keys under the
		// shard lock (Keys is not concurrent-safe), then re-lock for the
		// per-key Peek/Remove pass so AllowKey is not blocked during the
		// Keys allocation window only briefly.
		s.mu.Lock()
		keys := s.entries.Keys()
		s.mu.Unlock()

		limit := min(len(keys), maxCleanupPerShard)
		s.mu.Lock()
		for _, key := range keys[:limit] {
			entry, ok := s.entries.Peek(key)
			// Match Allow's half-open boundary: an entry whose window has
			// ended (now >= windowEnd) is expired and may be evicted.
			if ok && !now.Before(entry.windowEnd) {
				s.entries.Remove(key)
			}
		}
		s.mu.Unlock()
	}
}

// KeyedMiddleware returns chain-shape middleware that rate-limits requests
// using the provided KeyedLimiter. The keyFunc extracts the rate-limit
// key from each request (e.g., user ID, API key, IP address). When
// degradation is configured via [WithKeyedDegradation], the middleware
// checks the health indicator before enforcing rate limits.
func KeyedMiddleware(rl *KeyedLimiter, keyFunc func(r *http.Request) string) func(http.Handler) http.Handler {
	if rl == nil {
		panic("middleware/ratelimit: KeyedMiddleware requires a non-nil limiter")
	}
	if keyFunc == nil {
		panic("middleware/ratelimit: KeyedMiddleware requires a non-nil key function")
	}
	return func(next http.Handler) http.Handler {
		// Fail fast at wiring time, matching Middleware's contract, instead of
		// deferring to a nil-pointer panic on the first allowed request.
		if next == nil {
			panic("middleware/ratelimit: KeyedMiddleware requires a non-nil next handler")
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			skip, handled, outcome := handleDegradation(w, r, rl.health, rl.degradation)
			if outcome != "" {
				rl.observeDecision(outcome)
			}
			if handled {
				return
			}
			if skip {
				next.ServeHTTP(w, r)
				return
			}

			key, ok := safeRateLimitKey(keyFunc, r)
			if !ok {
				rl.observeDecision(rateLimitOutcomeUnavailable)
				httpx.WriteError(w, http.StatusServiceUnavailable, "rate limit unavailable")
				return
			}
			allowed, retryAfter, err := rl.AllowKey(key)
			if err != nil {
				if errors.Is(err, ErrInvalidKey) {
					httpx.WriteError(w, http.StatusBadRequest, "invalid rate limit key")
					return
				}
				httpx.WriteError(w, http.StatusServiceUnavailable, "rate limit unavailable")
				return
			}
			if !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				httpx.WriteError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (rl *KeyedLimiter) observeDecision(outcome string) {
	if rl == nil {
		return
	}
	rl.metrics.observeDecision(rl.name, rateLimitKindKeyed, outcome)
}

func (rl *KeyedLimiter) observeRetryAfter(seconds float64) {
	if rl == nil {
		return
	}
	rl.metrics.observeRetryAfter(rl.name, rateLimitKindKeyed, seconds)
}

func safeRateLimitKey(fn func(*http.Request) string, r *http.Request) (key string, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Default().Error("ratelimit: key function panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			key, ok = "", false
		}
	}()
	return fn(r), true
}
