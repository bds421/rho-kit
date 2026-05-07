// Package tokenbucket implements a per-key in-memory token bucket
// limiter. Each unique key gets its own bucket of capacity tokens that
// refills at refill tokens/second.
//
// Use this for:
//   - High-throughput single-instance services where allowing brief
//     bursts (up to capacity) is acceptable.
//   - Local rate limits that complement a cross-instance limit (e.g.
//     a Redis-backed limit for the global SLA + a local bucket to
//     keep one bad client from saturating the queue).
//
// For smoothed (no-burst) behaviour, use data/ratelimit/gcra.
// For cross-instance limits, use data/ratelimit/redis (TODO).
package tokenbucket

import (
	"context"
	"sync"
	"time"

	"github.com/bds421/rho-kit/data/ratelimit"
)

// Limiter is a per-key token-bucket [ratelimit.Limiter].
//
// The bucket map grows as new keys arrive. Idle keys are evicted by an
// optional sweeper goroutine; without one the map can grow unbounded
// for high-cardinality keyspaces (e.g. per-IP limits with no upper
// bound on distinct IPs). For high-cardinality keys, prefer the
// gcra subpackage which has the same growth profile but with built-in
// LRU bounds.
type Limiter struct {
	capacity float64
	refill   float64 // tokens per second
	now      func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens  float64
	updated time.Time
}

// Option configures a [Limiter].
type Option func(*Limiter)

// WithClock overrides the time source for tests.
func WithClock(now func() time.Time) Option {
	return func(l *Limiter) { l.now = now }
}

// New constructs a Limiter where each key has a bucket of `capacity`
// tokens that refills at `refillPerSec` tokens per second. capacity
// must be > 0; refillPerSec must be > 0.
func New(capacity, refillPerSec float64, opts ...Option) *Limiter {
	if capacity <= 0 {
		panic("tokenbucket: capacity must be > 0")
	}
	if refillPerSec <= 0 {
		panic("tokenbucket: refillPerSec must be > 0")
	}
	l := &Limiter{
		capacity: capacity,
		refill:   refillPerSec,
		now:      time.Now,
		buckets:  make(map[string]*bucket),
	}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Allow consumes one token from key's bucket if available. retryAfter
// is the time to wait until the next token would refill, when allowed
// is false.
func (l *Limiter) Allow(_ context.Context, key string) (bool, time.Duration, error) {
	if key == "" {
		return false, 0, ratelimit.ErrInvalidKey
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.capacity, updated: now}
		l.buckets[key] = b
	}

	// Refill since last update.
	elapsed := now.Sub(b.updated).Seconds()
	if elapsed > 0 {
		b.tokens = minf(l.capacity, b.tokens+elapsed*l.refill)
		b.updated = now
	}

	if b.tokens >= 1 {
		b.tokens -= 1
		return true, 0, nil
	}

	// retryAfter = how long until enough refills to reach 1 token.
	deficit := 1 - b.tokens
	wait := time.Duration(deficit / l.refill * float64(time.Second))
	return false, wait, nil
}

// Len returns the number of tracked keys. Useful in tests.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
