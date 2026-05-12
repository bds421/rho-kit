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

// KeyedRateLimiter implements a sharded fixed-window rate limiter keyed by an
// arbitrary string. Sharding reduces mutex contention under high concurrency,
// matching the approach used by [RateLimiter].
type KeyedRateLimiter struct {
	shards      [numShards]keyedShard
	limit       int
	window      time.Duration
	now         func() time.Time
	health      HealthIndicator
	degradation DegradationHandler
	metrics     *Metrics
	name        string
	runMu       sync.Mutex
	started     bool
}

// KeyedOption configures a KeyedRateLimiter.
type KeyedOption func(*KeyedRateLimiter)

// WithKeyedClock sets the time source for the keyed rate limiter.
// Useful for deterministic testing without time.Sleep. Panics on nil
// to fail fast at construction rather than dereferencing a nil func
// on the first request through the limiter.
func WithKeyedClock(fn func() time.Time) KeyedOption {
	if fn == nil {
		panic("ratelimit: WithKeyedClock requires a non-nil time source")
	}
	return func(rl *KeyedRateLimiter) { rl.now = fn }
}

// WithKeyedMetrics attaches Prometheus metrics to the keyed rate limiter.
func WithKeyedMetrics(m *Metrics) KeyedOption {
	if m == nil {
		panic("ratelimit: WithKeyedMetrics requires non-nil metrics")
	}
	return func(rl *KeyedRateLimiter) { rl.metrics = m }
}

// WithKeyedLimiterName sets the low-cardinality limiter label used by
// Prometheus metrics. Use static names such as "api_key" or "login".
func WithKeyedLimiterName(name string) KeyedOption {
	name = normalizeLimiterName(name)
	return func(rl *KeyedRateLimiter) { rl.name = name }
}

// NewKeyedRateLimiter creates a rate limiter allowing limit requests per window per key.
// Panics if limit or window are not positive — these indicate misconfiguration.
func NewKeyedRateLimiter(limit int, window time.Duration, opts ...KeyedOption) *KeyedRateLimiter {
	if limit <= 0 {
		panic("ratelimit: limit must be positive")
	}
	if window <= 0 {
		panic("ratelimit: window must be positive")
	}
	rl := &KeyedRateLimiter{
		limit:  limit,
		window: window,
		now:    time.Now,
		name:   defaultLimiterName,
	}
	for _, opt := range opts {
		if opt == nil {
			panic("ratelimit: NewKeyedRateLimiter option must not be nil")
		}
		opt(rl)
	}
	for i := range rl.shards {
		cache, _ := lru.New[string, *keyedRateLimitEntry](defaultMaxKeyedPerShard)
		rl.shards[i].entries = cache
	}
	return rl
}

// getShard returns the shard for the given key using FNV-1a hashing.
func (rl *KeyedRateLimiter) getShard(key string) *keyedShard {
	h := fnv.New32a()
	h.Write([]byte(key))
	return &rl.shards[h.Sum32()%numShards]
}

func (rl *KeyedRateLimiter) ready() error {
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
// fail closed and are not stored. Call [KeyedRateLimiter.AllowKey] when the
// caller needs to distinguish invalid keys from throttled keys.
func (rl *KeyedRateLimiter) Allow(key string) (allowed bool, retryAfter int) {
	allowed, retryAfter, err := rl.AllowKey(key)
	if err != nil {
		return false, 1
	}
	return allowed, retryAfter
}

// AllowKey checks whether the given key is within its rate limit and returns
// an error for invalid keys or uninitialized limiters.
func (rl *KeyedRateLimiter) AllowKey(key string) (allowed bool, retryAfter int, err error) {
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
		if now.After(entry.windowEnd) {
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

	s.entries.Add(key, &keyedRateLimitEntry{count: 1, windowEnd: now.Add(rl.window)})
	rl.observeDecision(rateLimitOutcomeAllowed)
	return true, 0, nil
}

// Run starts the cleanup goroutine that evicts expired entries. Blocks until ctx is cancelled.
// Cleanup runs at 2× the rate limit window to allow entries to fully expire
// before eviction, matching the IP rate limiter's cleanup cadence.
func (rl *KeyedRateLimiter) Run(ctx context.Context) error {
	if err := rl.ready(); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("ratelimit: KeyedRateLimiter.Run requires a non-nil context")
	}
	rl.runMu.Lock()
	if rl.started {
		rl.runMu.Unlock()
		return errors.New("ratelimit: KeyedRateLimiter.Run already started")
	}
	rl.started = true
	rl.runMu.Unlock()

	ticker := time.NewTicker(cleanupInterval(rl.window))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("panic in keyed rate limiter cleanup", redact.Panic(r))
					}
				}()
				rl.cleanup()
			}()
		}
	}
}

// cleanup evicts expired entries from all shards. Scans at most
// maxCleanupPerShard entries per shard to bound allocation.
func (rl *KeyedRateLimiter) cleanup() {
	if rl.ready() != nil {
		return
	}
	now := rl.now()
	for i := range rl.shards {
		s := &rl.shards[i]
		// Match the IP limiter's two-phase cleanup: Keys allocates a
		// snapshot, so do it outside the shard lock that protects AllowKey.
		s.mu.Lock()
		keys := s.entries.Keys()
		s.mu.Unlock()

		limit := min(len(keys), maxCleanupPerShard)
		s.mu.Lock()
		for _, key := range keys[:limit] {
			entry, ok := s.entries.Peek(key)
			if ok && now.After(entry.windowEnd) {
				s.entries.Remove(key)
			}
		}
		s.mu.Unlock()
	}
}

// KeyedRateLimitMiddleware returns middleware that rate-limits requests using
// the provided KeyedRateLimiter. The keyFunc extracts the rate-limit key from
// each request (e.g., user ID, API key, IP address).
// When degradation is configured via [WithKeyedDegradation], the middleware
// checks the health indicator before enforcing rate limits.
func KeyedRateLimitMiddleware(rl *KeyedRateLimiter, keyFunc func(r *http.Request) string) func(http.Handler) http.Handler {
	if rl == nil {
		panic("ratelimit: KeyedRateLimitMiddleware requires a non-nil limiter")
	}
	if keyFunc == nil {
		panic("ratelimit: KeyedRateLimitMiddleware requires a non-nil key function")
	}
	return func(next http.Handler) http.Handler {
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

func (rl *KeyedRateLimiter) observeDecision(outcome string) {
	if rl == nil {
		return
	}
	rl.metrics.observeDecision(rl.name, rateLimitKindKeyed, outcome)
}

func (rl *KeyedRateLimiter) observeRetryAfter(seconds float64) {
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
