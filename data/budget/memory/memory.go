// Package memory implements an in-process [budget.Budget] keyed by
// arbitrary string with a fixed-window reset period.
//
// State per key is a single (period bucket id, used) pair held in a
// [sync.Map]; admit and peek are constant-time. A bucket is replaced
// atomically when a Consume call observes a stale period. By default
// a background sweeper periodically removes buckets whose period has
// rolled over and which carry no live state, so high-cardinality
// keyspaces do not accumulate.
//
// Use this when:
//
//   - The same budget does NOT need to apply across multiple
//     replicas (per-process anti-runaway, single-instance services,
//     tests).
//   - You want zero external dependencies.
//
// For cross-replica budgets use data/budget/redis.
package memory

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
	"weak"

	"github.com/bds421/rho-kit/core/v2/clock"
	"github.com/bds421/rho-kit/data/v2/budget"
)

// defaultSweepInterval is the period between background passes that
// remove cold keys whose window has rolled over.
const defaultSweepInterval = 5 * time.Minute

// Budget is a per-key in-process [budget.Budget] with a fixed-window
// reset period.
type Budget struct {
	cap    int64
	period time.Duration
	now    clock.Func

	buckets sync.Map // map[string]*bucket

	sweepInterval time.Duration
	stopOnce      sync.Once
	stopCh        chan struct{}
	doneCh        chan struct{}
}

type bucket struct {
	mu      sync.Mutex
	periodN atomic.Int64
	used    atomic.Int64
}

// Option configures a [Budget].
type Option func(*Budget)

// WithClock overrides the time source (tests only). Panics on nil to
// fail loudly at construction rather than dereferencing a nil func on
// the first Consume/Peek/Refund.
func WithClock(now clock.Func) Option {
	if now == nil {
		panic("budget/memory: WithClock requires a non-nil time source")
	}
	return func(b *Budget) { b.now = now }
}

// WithSweeper overrides the interval at which the background sweeper
// removes buckets whose period has rolled over and that carry no live
// state. The interval must be positive; use [WithoutSweeper] to opt out.
func WithSweeper(interval time.Duration) Option {
	if interval <= 0 {
		panic("budget/memory: WithSweeper requires a positive interval")
	}
	return func(b *Budget) { b.sweepInterval = interval }
}

// WithoutSweeper disables the background sweeper. Use only when the
// caller bounds key cardinality externally.
func WithoutSweeper() Option {
	return func(b *Budget) { b.sweepInterval = 0 }
}

// New constructs a Budget allowing up to `cap` units per `period`.
//
// Examples:
//
//   - Open(1_000_000, time.Hour) — one million tokens per hour.
//   - Open(50, 24*time.Hour)     — fifty operations per day.
//
// The Open* prefix marks this constructor as side-effecting: it spawns
// a background sweeper goroutine that holds only a weak reference to
// the budget, so a forgotten Close does not pin it forever. Pair with
// [Budget.Close] in shutdown wiring for deterministic cleanup.
//
// Panics on cap <= 0 or period <= 0; misconfiguration here is
// almost always a bug rather than a recoverable runtime condition.
// Replaces the v1 New() spelling so the lifecycle obligation is
// visible at the call site.
func New(cap int64, period time.Duration, opts ...Option) *Budget {
	if cap <= 0 {
		panic("budget/memory: New: cap must be > 0")
	}
	if period <= 0 {
		panic("budget/memory: New: period must be > 0")
	}
	b := &Budget{
		cap:           cap,
		period:        period,
		now:           time.Now,
		sweepInterval: defaultSweepInterval,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}
	for _, o := range opts {
		if o == nil {
			panic("budget/memory: New: option must not be nil")
		}
		o(b)
	}
	if b.now == nil {
		panic("budget/memory: New: clock must not be nil")
	}
	if b.sweepInterval > 0 {
		// Weak-ref sweeper: a forgotten Close cannot keep the goroutine
		// alive past the Budget's reachability — same design rationale
		// as data/ratelimit/tokenbucket.runSweeper.
		go runSweeper(weak.Make(b), b.sweepInterval, b.stopCh, b.doneCh)
	} else {
		close(b.doneCh)
	}
	return b
}

func (b *Budget) ready() error {
	if b == nil || b.cap <= 0 || b.period <= 0 || b.now == nil {
		return budget.ErrInvalidBudget
	}
	return nil
}

// ctxErr returns ctx.Err() for non-nil ctx, or nil otherwise.
// Symmetry with rediscache: a nil ctx is treated as not-cancelled
// rather than rejected, matching the kit-wide convention that "no
// ctx" is equivalent to context.Background().
func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// Close terminates the background sweeper. Safe to call multiple
// times. After Close, the budget continues to admit and refund as
// normal but no longer evicts cold keys. Always returns nil — the
// signature matches [io.Closer] so the Budget can be wired into
// resource-cleanup helpers, but the shutdown path itself cannot fail.
func (b *Budget) Close() error {
	if b == nil || b.stopCh == nil || b.doneCh == nil {
		return nil
	}
	b.stopOnce.Do(func() {
		close(b.stopCh)
		<-b.doneCh
	})
	return nil
}

// runSweeper is a free function so the goroutine never holds a strong
// reference to the Budget. See data/ratelimit/tokenbucket.runSweeper
// for the design rationale.
func runSweeper(weakB weak.Pointer[Budget], interval time.Duration, stopCh, doneCh chan struct{}) {
	defer close(doneCh)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-t.C:
			b := weakB.Value()
			if b == nil {
				return
			}
			b.sweep()
		}
	}
}

// sweep removes buckets whose period id is older than the current
// period. Consume verifies the bucket is still the live map entry
// before mutating it, so a concurrent sweep cannot orphan an in-flight
// increment.
func (b *Budget) sweep() {
	currentPeriod, _ := b.periodOf(b.now())
	b.buckets.Range(func(k, v any) bool {
		bk := v.(*bucket)
		if bk.periodN.Load() < currentPeriod {
			b.buckets.CompareAndDelete(k, v)
		}
		return true
	})
}

func (b *Budget) periodOf(t time.Time) (int64, time.Time) {
	periodNs := int64(b.period)
	id := t.UTC().UnixNano() / periodNs
	nextStart := time.Unix(0, (id+1)*periodNs).UTC()
	return id, nextStart
}

func (b *Budget) loadOrInitBucket(key string, currentPeriod int64) *bucket {
	if v, ok := b.buckets.Load(key); ok {
		return v.(*bucket)
	}
	fresh := &bucket{}
	fresh.periodN.Store(currentPeriod)
	v, _ := b.buckets.LoadOrStore(key, fresh)
	return v.(*bucket)
}

// Consume implements [budget.Budget]. amount==0 returns the current
// remaining without changing state.
//
// Honours context cancellation symmetrically with the Redis-backed
// budget: a cancelled or already-expired ctx returns ctx.Err() before
// any state mutation, so memory wiring and Redis wiring agree about
// what a cancelled caller observes.
func (b *Budget) Consume(ctx context.Context, key string, amount int64) (bool, int64, time.Duration, error) {
	if err := ctxErr(ctx); err != nil {
		return false, 0, 0, err
	}
	if err := b.ready(); err != nil {
		return false, 0, 0, err
	}
	if err := budget.ValidateKey(key); err != nil {
		return false, 0, 0, err
	}
	if amount < 0 {
		return false, 0, 0, budget.ErrInvalidAmount
	}
	now := b.now()
	periodID, nextStart := b.periodOf(now)

	// Loop to retry against a sweep that evicted our bucket between the
	// load and the lock acquisition. Without this, Consume could
	// increment `used` on an orphaned bucket that the next caller can
	// no longer see, granting them a fresh full cap.
	var bk *bucket
	for {
		bk = b.loadOrInitBucket(key, periodID)
		bk.mu.Lock()
		if current, ok := b.buckets.Load(key); ok && current.(*bucket) == bk {
			break
		}
		bk.mu.Unlock()
	}
	defer bk.mu.Unlock()

	// Re-check cancellation after acquiring the bucket lock: a
	// contended caller may have been cancelled while waiting.
	if err := ctxErr(ctx); err != nil {
		return false, 0, 0, err
	}

	if bk.periodN.Load() != periodID {
		bk.periodN.Store(periodID)
		bk.used.Store(0)
	}

	used := bk.used.Load()
	// retry-after is computed against the injected clock so tests
	// with a fake clock and production wall-clock jumps both produce
	// values consistent with the period boundary above. Floor at zero
	// to defend against the rare case where the clock advances
	// between the period decision and this calculation.
	retry := nextStart.Sub(now)
	if retry < 0 {
		retry = 0
	}
	// Defensive: a future change to Refund or external state could
	// in principle leave used > cap. Treat that as "no headroom"
	// rather than letting `cap - used` underflow remaining.
	if used > b.cap {
		return false, 0, retry, nil
	}
	// Overflow-safe admission check. `used + amount` can wrap to a
	// negative int64 for amounts near math.MaxInt64; comparing
	// `amount > cap - used` keeps both sides in [0, cap].
	if amount > b.cap-used {
		return false, b.cap - used, retry, nil
	}
	if amount > 0 {
		bk.used.Add(amount)
		used += amount
	}
	return true, b.cap - used, 0, nil
}

// Peek implements [budget.Budget]. Unknown keys return the full cap.
//
// Honours context cancellation symmetrically with the Redis-backed
// budget: a cancelled or already-expired ctx returns ctx.Err() before
// any work.
func (b *Budget) Peek(ctx context.Context, key string) (int64, error) {
	if err := ctxErr(ctx); err != nil {
		return 0, err
	}
	if err := b.ready(); err != nil {
		return 0, err
	}
	if err := budget.ValidateKey(key); err != nil {
		return 0, err
	}
	now := b.now()
	periodID, _ := b.periodOf(now)

	v, ok := b.buckets.Load(key)
	if !ok {
		return b.cap, nil
	}
	bk := v.(*bucket)
	bk.mu.Lock()
	defer bk.mu.Unlock()
	if bk.periodN.Load() != periodID {
		return b.cap, nil
	}
	rem := b.cap - bk.used.Load()
	if rem < 0 {
		rem = 0
	}
	return rem, nil
}

// Refund implements [budget.Refunder]. Crediting an unknown key is a
// no-op (there is nothing to refund); refunds past the cap clamp at
// `cap` so the budget never inflates above its configured limit.
//
// Honours context cancellation symmetrically with the Redis-backed
// budget: a cancelled or already-expired ctx returns ctx.Err() before
// any state mutation.
func (b *Budget) Refund(ctx context.Context, key string, amount int64) (int64, error) {
	if err := ctxErr(ctx); err != nil {
		return 0, err
	}
	if err := b.ready(); err != nil {
		return 0, err
	}
	if err := budget.ValidateKey(key); err != nil {
		return 0, err
	}
	if amount < 0 {
		return 0, budget.ErrInvalidAmount
	}
	now := b.now()
	periodID, _ := b.periodOf(now)

	v, ok := b.buckets.Load(key)
	if !ok {
		return b.cap, nil
	}
	bk := v.(*bucket)
	bk.mu.Lock()
	defer bk.mu.Unlock()

	if bk.periodN.Load() != periodID {
		bk.periodN.Store(periodID)
		bk.used.Store(0)
		return b.cap, nil
	}
	used := bk.used.Load() - amount
	if used < 0 {
		used = 0
	}
	bk.used.Store(used)
	return b.cap - used, nil
}

// Len returns the number of tracked keys. Useful in tests.
func (b *Budget) Len() int {
	if b == nil {
		return 0
	}
	n := 0
	b.buckets.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}
