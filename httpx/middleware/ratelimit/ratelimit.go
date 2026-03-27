package ratelimit

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/bds421/rho-kit/httpx"
	"github.com/bds421/rho-kit/httpx/middleware/clientip"
)

const numShards = 16

type visitor struct {
	count    int
	windowAt time.Time
}

type shard struct {
	mu       sync.Mutex
	visitors *lru.Cache[string, *visitor]
}

// defaultMaxPerShard limits each shard's LRU size to prevent OOM from IP-spray attacks.
const defaultMaxPerShard = 10_000

// RateLimiter is a sharded fixed-window rate limiter keyed by IP address.
// Sharding reduces mutex contention under high concurrency.
type RateLimiter struct {
	shards         [numShards]shard
	limit          int
	window         time.Duration
	now            func() time.Time
	trustedProxies []*net.IPNet
	maxPerShard    int
	health         HealthIndicator
	degradation    DegradationHandler
}

// RateLimiterOption configures optional RateLimiter behaviour.
type RateLimiterOption func(*RateLimiter)

// WithClock sets a custom time source (useful for testing).
func WithClock(fn func() time.Time) RateLimiterOption {
	return func(rl *RateLimiter) { rl.now = fn }
}

// WithTrustedProxies sets the CIDRs from which X-Forwarded-For is trusted.
func WithTrustedProxies(cidrs []string) RateLimiterOption {
	return func(rl *RateLimiter) {
		rl.trustedProxies = clientip.ParseTrustedProxies(cidrs)
	}
}

// NewRateLimiter creates a rate limiter that allows limit requests per window per IP.
// Panics if limit or window are not positive — these indicate misconfiguration.
func NewRateLimiter(limit int, window time.Duration, opts ...RateLimiterOption) *RateLimiter {
	if limit <= 0 {
		panic("ratelimit: limit must be positive")
	}
	if window <= 0 {
		panic("ratelimit: window must be positive")
	}
	rl := &RateLimiter{
		limit:       limit,
		window:      window,
		now:         time.Now,
		maxPerShard: defaultMaxPerShard,
	}
	for _, opt := range opts {
		opt(rl)
	}
	if len(rl.trustedProxies) == 0 {
		rl.trustedProxies = clientip.ParseTrustedProxies(nil)
	}
	for i := range rl.shards {
		// IMPORTANT: Do NOT use lru.NewWithEvict here. The shard mutex is held
		// during Add() calls, and an OnEvict callback would execute under that
		// same lock, risking deadlock if it touches any shard state.
		cache, _ := lru.New[string, *visitor](rl.maxPerShard)
		rl.shards[i].visitors = cache
	}
	return rl
}

// getShard returns the shard for the given IP using FNV-1a hashing.
func (rl *RateLimiter) getShard(ip string) *shard {
	h := fnv.New32a()
	h.Write([]byte(ip))
	return &rl.shards[h.Sum32()%numShards]
}

// allow checks if the IP is within the rate limit. Returns (allowed, windowRemaining).
// windowRemaining is only meaningful when allowed is false.
func (rl *RateLimiter) allow(ip string) (bool, time.Duration) {
	s := rl.getShard(ip)
	s.mu.Lock()
	defer s.mu.Unlock()

	now := rl.now()
	v, ok := s.visitors.Get(ip)
	if ok {
		// Mutating *visitor through the cached pointer is safe: the shard mutex
		// serialises all access. Replacing via Add() would be wasteful since
		// Get() already marked this entry as recently used.
		elapsed := now.Sub(v.windowAt)
		if elapsed >= rl.window {
			v.count = 1
			v.windowAt = now
			return true, 0
		}
		v.count++
		if v.count <= rl.limit {
			return true, 0
		}
		remaining := rl.window - elapsed
		if remaining < 0 {
			remaining = 0
		}
		return false, remaining
	}

	s.visitors.Add(ip, &visitor{count: 1, windowAt: now})
	return true, 0
}

// maxCleanupPerShard limits the number of keys scanned per shard per cleanup
// cycle to prevent large allocations from Keys() under IP-spray attacks.
const maxCleanupPerShard = 1000

// cleanup evicts expired visitors from all shards. Keys() is called
// outside the lock to avoid holding the mutex during the full-slice
// allocation under DDoS (IP-spray) conditions. The subsequent eviction
// loop re-checks under the lock.
func (rl *RateLimiter) cleanup() {
	cutoff := rl.now().Add(-rl.window)
	for i := range rl.shards {
		s := &rl.shards[i]
		// Snapshot keys without holding the lock — avoids blocking
		// concurrent allow() calls during the O(n) Keys() allocation.
		s.mu.Lock()
		keys := s.visitors.Keys()
		s.mu.Unlock()

		limit := min(len(keys), maxCleanupPerShard)
		s.mu.Lock()
		for _, ip := range keys[:limit] {
			v, ok := s.visitors.Peek(ip)
			if ok && v.windowAt.Before(cutoff) {
				s.visitors.Remove(ip)
			}
		}
		s.mu.Unlock()
	}
}

// Run starts the periodic cleanup goroutine. Blocks until ctx is cancelled.
// Cleanup runs at 2× the rate limit window to amortize scan cost while
// ensuring expired entries don't accumulate beyond one extra window.
func (rl *RateLimiter) Run(ctx context.Context) {
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
						slog.Error("panic in rate limiter cleanup", "panic", r)
					}
				}()
				rl.cleanup()
			}()
		}
	}
}

// clientIP extracts the real client IP using proxy-aware logic.
func (rl *RateLimiter) clientIP(r *http.Request) string {
	return clientip.ClientIPWithTrustedProxies(r, rl.trustedProxies)
}

// ClientIP extracts the real client IP from the request, using the same
// proxy-aware logic as the rate limiter middleware.
func (rl *RateLimiter) ClientIP(r *http.Request) string {
	return rl.clientIP(r)
}

// Middleware returns an HTTP middleware that rejects requests exceeding the rate limit.
// When degradation is configured via [WithDegradation], the middleware checks
// the health indicator before enforcing rate limits.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		skip, handled := handleDegradation(w, r, rl.health, rl.degradation)
		if handled {
			return
		}
		if skip {
			next.ServeHTTP(w, r)
			return
		}

		ip := rl.clientIP(r)
		allowed, remaining := rl.allow(ip)
		if !allowed {
			retryAfter := int(math.Ceil(remaining.Seconds()))
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			httpx.WriteError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		next.ServeHTTP(w, r)
	})
}
