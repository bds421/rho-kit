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
// For cross-instance limits, use data/ratelimit/redis.
package tokenbucket

import (
	"context"
	"sync"
	"time"

	"github.com/bds421/rho-kit/data/ratelimit"
)

// defaultSweepInterval is the period between background passes that
// remove cold buckets that have refilled fully.
const defaultSweepInterval = 5 * time.Minute

// Limiter is a per-key token-bucket [ratelimit.Limiter].
//
// The bucket map grows as new keys arrive. Idle keys are evicted by a
// background sweeper goroutine; disable it with [WithSweeper](0) only
// when caller cardinality is already bounded.
type Limiter struct {
	capacity float64
	refill   float64
	now      func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket

	sweepInterval time.Duration
	stopOnce      sync.Once
	stopCh        chan struct{}
	doneCh        chan struct{}
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

// WithSweeper overrides the interval at which the background sweeper
// removes buckets that have fully refilled (and would therefore be
// indistinguishable from a freshly-created bucket on the next Allow).
// interval <= 0 disables the sweeper.
func WithSweeper(interval time.Duration) Option {
	return func(l *Limiter) { l.sweepInterval = interval }
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
		capacity:      capacity,
		refill:        refillPerSec,
		now:           time.Now,
		buckets:       make(map[string]*bucket),
		sweepInterval: defaultSweepInterval,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}
	for _, o := range opts {
		o(l)
	}
	if l.sweepInterval > 0 {
		go l.sweepLoop()
	} else {
		close(l.doneCh)
	}
	return l
}

// Stop terminates the background sweeper. Safe to call multiple
// times.
func (l *Limiter) Stop() {
	l.stopOnce.Do(func() {
		close(l.stopCh)
		<-l.doneCh
	})
}

func (l *Limiter) sweepLoop() {
	defer close(l.doneCh)
	t := time.NewTicker(l.sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-l.stopCh:
			return
		case <-t.C:
			l.sweep()
		}
	}
}

// sweep removes buckets that have refilled to (or above) capacity at
// the current instant; their state is indistinguishable from a fresh
// bucket and freeing the entry costs nothing semantically.
func (l *Limiter) sweep() {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, b := range l.buckets {
		elapsed := now.Sub(b.updated).Seconds()
		projected := b.tokens
		if elapsed > 0 {
			projected = minf(l.capacity, b.tokens+elapsed*l.refill)
		}
		if projected >= l.capacity {
			delete(l.buckets, k)
		}
	}
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

	elapsed := now.Sub(b.updated).Seconds()
	if elapsed > 0 {
		b.tokens = minf(l.capacity, b.tokens+elapsed*l.refill)
		b.updated = now
	}

	if b.tokens >= 1 {
		b.tokens -= 1
		return true, 0, nil
	}

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
