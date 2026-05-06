package ratelimit

import (
	"context"
	"hash/fnv"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/bds421/rho-kit/httpx"
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
}

// KeyedOption configures a KeyedRateLimiter.
type KeyedOption func(*KeyedRateLimiter)

// WithKeyedClock sets the time source for the keyed rate limiter.
// Useful for deterministic testing without time.Sleep.
func WithKeyedClock(fn func() time.Time) KeyedOption {
	return func(rl *KeyedRateLimiter) { rl.now = fn }
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
	}
	for _, opt := range opts {
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

// Allow checks whether the given key is within its rate limit.
func (rl *KeyedRateLimiter) Allow(key string) (allowed bool, retryAfter int) {
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
			return true, 0
		}
		entry.count++
		if entry.count <= rl.limit {
			return true, 0
		}
		seconds := int(math.Ceil(entry.windowEnd.Sub(now).Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		return false, seconds
	}

	s.entries.Add(key, &keyedRateLimitEntry{count: 1, windowEnd: now.Add(rl.window)})
	return true, 0
}

// Run starts the cleanup goroutine that evicts expired entries. Blocks until ctx is cancelled.
// Cleanup runs at 2× the rate limit window to allow entries to fully expire
// before eviction, matching the IP rate limiter's cleanup cadence.
func (rl *KeyedRateLimiter) Run(ctx context.Context) {
	ticker := time.NewTicker(rl.window * 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("panic in keyed rate limiter cleanup", "panic", r)
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
	now := rl.now()
	for i := range rl.shards {
		s := &rl.shards[i]
		s.mu.Lock()
		keys := s.entries.Keys()
		limit := min(len(keys), maxCleanupPerShard)
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
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			skip, handled := handleDegradation(w, r, rl.health, rl.degradation)
			if handled {
				return
			}
			if skip {
				next.ServeHTTP(w, r)
				return
			}

			key := keyFunc(r)
			allowed, retryAfter := rl.Allow(key)
			if !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				httpx.WriteError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
