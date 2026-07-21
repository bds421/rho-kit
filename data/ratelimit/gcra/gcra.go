// Package gcra implements a per-key in-memory GCRA (Generic Cell Rate
// Algorithm) [ratelimit.Limiter].
//
// GCRA enforces a smooth rate: events are allowed only when at least
// `period/burst` time has elapsed since the previous allowed event,
// modulo a burst tolerance. The algorithm trades the bursty edge of
// fixed-window / token-bucket limiters for a uniformly-paced output
// that matches what most upstream APIs actually want from their
// callers.
//
// Reference: GCRA in I.371 (ITU). The state per key is a single
// timestamp ("theoretical arrival time"); update is constant time.
//
// Use this for:
//   - Outbound API quotas where the upstream rate-limits us and we
//     don't want to burst at the start of each window.
//   - Per-tenant request smoothing.
//
// Idle keys are evicted by an optional sweeper goroutine (see
// [WithSweeper]); without one the map can grow unbounded for
// high-cardinality keyspaces.
//
// Hot-path concurrency: the TAT map is sharded so distinct keys do not
// serialise on a single limiter-wide mutex (same motivation as the
// tokenbucket package's per-key locking). Sweep passes are budgeted so
// a high-cardinality map walk cannot stall every Allow for a full O(n)
// scan under the shard lock.
package gcra

import (
	"context"
	"hash/fnv"
	"sync"
	"time"
	"weak"

	"github.com/bds421/rho-kit/core/v2/clock"
	"github.com/bds421/rho-kit/data/v2/ratelimit"
)

// defaultSweepInterval is the period between background passes that
// remove cold keys whose theoretical arrival time has elapsed.
const defaultSweepInterval = 5 * time.Minute

// numShards spreads Allow contention across independent mutexes. 16 is
// enough to keep high-cardinality multi-tenant traffic from pile-ups on
// one lock while remaining cheap to iterate in the sweeper.
const numShards = 16

// sweepBudget caps how many map entries a single sweep pass inspects
// per shard under that shard's mutex. Mirrors idempotency.MemoryStore's
// evictBudget: unbounded full-map walks under the same lock Allow needs
// create avoidable hot-path tail latency.
const sweepBudget = 256

// Limiter is a per-key GCRA [ratelimit.Limiter].
//
// Safe for concurrent use — Allow takes the per-shard mutex for the
// key; Close is idempotent and joins the sweeper goroutine.
type Limiter struct {
	rate  time.Duration
	burst int
	now   clock.Func

	shards [numShards]gcraShard

	sweepInterval time.Duration
	// sweepCursor resumes budgeted sweeps across ticks so cold keys
	// in every shard are eventually reclaimed even when a single pass
	// hits the budget.
	sweepCursor uint32
	stopOnce    sync.Once
	stopCh      chan struct{}
	doneCh      chan struct{}
}

type gcraShard struct {
	mu   sync.Mutex
	tats map[string]time.Time
}

// Option configures a [Limiter].
type Option func(*Limiter)

// WithClock overrides the time source for tests.
func WithClock(now clock.Func) Option {
	if now == nil {
		panic("gcra: WithClock clock must not be nil")
	}
	return func(l *Limiter) { l.now = now }
}

// WithSweeper overrides the interval at which the background sweeper
// removes cold keys whose TAT lies in the past (which means another
// admit there would not be rate-limited anyway). The interval must be
// positive; use [WithoutSweeper] to opt out.
func WithSweeper(interval time.Duration) Option {
	if interval <= 0 {
		panic("gcra: WithSweeper requires a positive interval")
	}
	return func(l *Limiter) { l.sweepInterval = interval }
}

// WithoutSweeper disables the background sweeper. Use only when the
// caller bounds key cardinality externally.
func WithoutSweeper() Option {
	return func(l *Limiter) { l.sweepInterval = 0 }
}

// New constructs a Limiter that allows up to `burst` events within any
// `period` duration, smoothed at `period/burst` per event.
//
// Examples:
//   - New(time.Second, 10): 10 events/sec smoothed (one every 100ms,
//     burst tolerance 10).
//   - New(time.Minute, 60): 60 events/min smoothed (one every second).
//
// Panics if period/burst rounds to zero — that produces a degenerate
// limiter that admits every event without spacing.
func New(period time.Duration, burst int, opts ...Option) *Limiter {
	if period <= 0 {
		panic("gcra: New period must be > 0")
	}
	if burst < 1 {
		panic("gcra: New burst must be >= 1")
	}
	rate := period / time.Duration(burst)
	if rate <= 0 {
		panic("gcra: New period/burst rounds to zero (burst exceeds period in nanoseconds); pick a longer period or smaller burst")
	}
	l := &Limiter{
		rate:          rate,
		burst:         burst,
		now:           time.Now,
		sweepInterval: defaultSweepInterval,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}
	for i := range l.shards {
		l.shards[i].tats = make(map[string]time.Time)
	}
	for _, o := range opts {
		if o == nil {
			panic("gcra: New option must not be nil")
		}
		o(l)
	}
	if l.sweepInterval > 0 {
		// Weak.Pointer-backed sweeper: a forgotten Close cannot keep
		// the goroutine alive past the Limiter's reachability lifetime
		// — see tokenbucket.runSweeper for the design rationale.
		go runSweeper(weak.Make(l), l.sweepInterval, l.stopCh, l.doneCh)
	} else {
		close(l.doneCh)
	}
	return l
}

func (l *Limiter) ready() error {
	if l == nil || l.rate <= 0 || l.burst < 1 || l.now == nil {
		return ratelimit.ErrInvalidLimiter
	}
	for i := range l.shards {
		if l.shards[i].tats == nil {
			return ratelimit.ErrInvalidLimiter
		}
	}
	return nil
}

// ctxErr returns ctx.Err() for non-nil ctx, or nil otherwise.
// Matches the tokenbucket/budget convention: a nil ctx is treated as
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

// runSweeper is a free function so the goroutine never holds a strong
// reference to the Limiter. The weak.Pointer upgrade fails once the
// last caller drops their reference, at which point the sweeper exits
// on its own — Close remains the synchronous shutdown path.
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

func (l *Limiter) shardIndex(key string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return h.Sum32() % numShards
}

func (l *Limiter) shardFor(key string) *gcraShard {
	return &l.shards[l.shardIndex(key)]
}

// sweep drops keys whose TAT is in the past — those have no live
// rate-limit state (the next Allow would treat them as fresh). Each
// pass walks at most sweepBudget entries of one shard (round-robin
// via sweepCursor) so Allow on other keys is not blocked by a full
// high-cardinality map scan.
func (l *Limiter) sweep() {
	if l.ready() != nil {
		return
	}
	now := l.now()
	idx := l.sweepCursor % numShards
	l.sweepCursor++
	sh := &l.shards[idx]
	sh.mu.Lock()
	defer sh.mu.Unlock()
	n := 0
	for k, tat := range sh.tats {
		if n >= sweepBudget {
			return
		}
		n++
		if !tat.After(now) {
			delete(sh.tats, k)
		}
	}
}

// Allow reports whether key's next event is permitted. retryAfter is
// the time-until-next-allowed when denied.
//
// Honours context cancellation symmetrically with the Redis-backed
// limiter: a cancelled or already-expired ctx returns ctx.Err() before
// taking the limiter lock, and again after acquiring it, so contended
// callers cannot silently spend a slot after their request has been
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

	sh := l.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	if err := ctxErr(ctx); err != nil {
		return false, 0, err
	}

	tat, ok := sh.tats[key]
	if !ok || tat.Before(now) {
		tat = now
	}
	allowAt := tat.Add(-time.Duration(l.burst-1) * l.rate)
	if now.Before(allowAt) {
		return false, allowAt.Sub(now) + time.Nanosecond, nil
	}
	sh.tats[key] = tat.Add(l.rate)
	return true, 0, nil
}

// Len returns the number of tracked keys. Useful in tests.
func (l *Limiter) Len() int {
	if l == nil {
		return 0
	}
	total := 0
	for i := range l.shards {
		sh := &l.shards[i]
		sh.mu.Lock()
		total += len(sh.tats)
		sh.mu.Unlock()
	}
	return total
}
