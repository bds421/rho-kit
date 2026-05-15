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
	"math"
	"sync"
	"time"
	"weak"

	"github.com/bds421/rho-kit/core/v2/clock"
	"github.com/bds421/rho-kit/data/v2/ratelimit"
)

const (
	// defaultSweepInterval is the period between background passes that
	// remove cold buckets that have refilled fully.
	defaultSweepInterval = 5 * time.Minute

	maxRetryAfter                = time.Duration(1<<63 - 1)
	minRepresentableRefillPerSec = float64(time.Second) / float64(maxRetryAfter)
)

// Limiter is a per-key token-bucket [ratelimit.Limiter].
//
// The bucket map grows as new keys arrive. Idle keys are evicted by a
// background sweeper goroutine; disable it with [WithoutSweeper] only
// when caller cardinality is already bounded.
//
// Safe for concurrent use — Allow takes a per-bucket mutex; Close is
// idempotent and joins the sweeper goroutine.
type Limiter struct {
	capacity float64
	refill   float64
	now      clock.Func

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
func WithClock(now clock.Func) Option {
	if now == nil {
		panic("tokenbucket: clock must not be nil")
	}
	return func(l *Limiter) { l.now = now }
}

// WithSweeper overrides the interval at which the background sweeper
// removes buckets that have fully refilled (and would therefore be
// indistinguishable from a freshly-created bucket on the next Allow).
// The interval must be positive; use [WithoutSweeper] to opt out.
func WithSweeper(interval time.Duration) Option {
	if interval <= 0 {
		panic("tokenbucket: WithSweeper requires a positive interval")
	}
	return func(l *Limiter) { l.sweepInterval = interval }
}

// WithoutSweeper disables the background sweeper. Use only when the
// caller bounds key cardinality externally.
func WithoutSweeper() Option {
	return func(l *Limiter) { l.sweepInterval = 0 }
}

// Open constructs a Limiter where each key has a bucket of `capacity`
// tokens that refills at `refillPerSec` tokens per second. capacity
// must be finite and > 0; refillPerSec must be finite, > 0, and high
// enough that a one-token retry interval fits in time.Duration.
//
// New constructs a Limiter and spawns a background sweeper goroutine
// that holds only a weak reference to the limiter, so a forgotten
// Close does not pin it forever. Pair with [Limiter.Close] in
// shutdown wiring for deterministic cleanup.
func New(capacity, refillPerSec float64, opts ...Option) *Limiter {
	if !validPositiveFinite(capacity) {
		panic("tokenbucket: capacity must be finite and > 0")
	}
	if !validRefillPerSec(refillPerSec) {
		panic("tokenbucket: refillPerSec must be finite, > 0, and produce a representable retry interval")
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
		if o == nil {
			panic("tokenbucket: option must not be nil")
		}
		o(l)
	}
	if l.sweepInterval > 0 {
		// The sweeper holds a weak.Pointer to the Limiter so a caller
		// that forgets Close cannot keep this goroutine alive forever:
		// once their last strong reference drops, the next tick sees
		// weak.Value() == nil and the sweeper exits. Close remains the
		// synchronous, deterministic shutdown path.
		go runSweeper(weak.Make(l), l.sweepInterval, l.stopCh, l.doneCh)
	} else {
		close(l.doneCh)
	}
	return l
}

func (l *Limiter) ready() error {
	if l == nil || !validPositiveFinite(l.capacity) || !validRefillPerSec(l.refill) || l.now == nil || l.buckets == nil {
		return ratelimit.ErrInvalidLimiter
	}
	return nil
}

// ctxErr returns ctx.Err() for non-nil ctx, or nil otherwise.
// Matches the budget/memory convention: a nil ctx is treated as
// context.Background() rather than rejected.
func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// Close terminates the background sweeper. Safe to call multiple
// times. Always returns nil — the signature matches [io.Closer] so the
// Limiter can be wired into resource-cleanup helpers, but the
// shutdown path itself cannot fail.
func (l *Limiter) Close() error {
	if l == nil || l.stopCh == nil || l.doneCh == nil {
		return nil
	}
	l.stopOnce.Do(func() {
		close(l.stopCh)
		<-l.doneCh
	})
	return nil
}

// runSweeper is a free function (not a method) so the goroutine never
// holds a strong reference to the Limiter — it uses weak.Pointer to
// upgrade-on-tick. If the upgrade fails the parent was GC'd; exit so
// the goroutine doesn't outlive the object.
func runSweeper(weakL weak.Pointer[Limiter], interval time.Duration, stopCh, doneCh chan struct{}) {
	defer close(doneCh)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-t.C:
			l := weakL.Value()
			if l == nil {
				return
			}
			l.sweep()
		}
	}
}

// sweep removes buckets that have refilled to (or above) capacity at
// the current instant; their state is indistinguishable from a fresh
// bucket and freeing the entry costs nothing semantically.
func (l *Limiter) sweep() {
	if l.ready() != nil {
		return
	}
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
//
// Honours context cancellation symmetrically with the Redis-backed
// limiter: a cancelled or already-expired ctx returns ctx.Err() before
// taking the bucket lock, and again after acquiring it, so contended
// callers cannot silently spend a token after their request has been
// cancelled.
func (l *Limiter) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	if err := ctxErr(ctx); err != nil {
		return false, 0, err
	}
	if err := l.ready(); err != nil {
		return false, 0, err
	}
	if err := ratelimit.ValidateKey(key); err != nil {
		return false, 0, err
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if err := ctxErr(ctx); err != nil {
		return false, 0, err
	}

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
	return false, retryAfter(deficit, l.refill), nil
}

// Len returns the number of tracked keys. Useful in tests.
func (l *Limiter) Len() int {
	if l == nil {
		return 0
	}
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

func validPositiveFinite(v float64) bool {
	return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func validRefillPerSec(v float64) bool {
	return validPositiveFinite(v) && v > minRepresentableRefillPerSec
}

func retryAfter(deficit, refill float64) time.Duration {
	waitNanos := deficit / refill * float64(time.Second)
	if math.IsNaN(waitNanos) {
		return 0
	}
	if math.IsInf(waitNanos, 1) || waitNanos >= float64(maxRetryAfter) {
		return maxRetryAfter
	}
	if waitNanos <= 1 {
		return time.Nanosecond
	}
	return time.Duration(waitNanos)
}
