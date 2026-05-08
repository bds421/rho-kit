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
	now    func() time.Time

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
func WithClock(now func() time.Time) Option {
	if now == nil {
		panic("budget/memory: WithClock requires a non-nil time source")
	}
	return func(b *Budget) { b.now = now }
}

// WithSweeper overrides the interval at which the background sweeper
// removes buckets whose period has rolled over and that carry no live
// state. interval <= 0 disables the sweeper entirely (the caller is
// responsible for bounding cardinality).
func WithSweeper(interval time.Duration) Option {
	return func(b *Budget) { b.sweepInterval = interval }
}

// New constructs a Budget allowing up to `cap` units per `period`.
//
// Examples:
//
//   - New(1_000_000, time.Hour) — one million tokens per hour.
//   - New(50, 24*time.Hour)     — fifty operations per day.
//
// Panics on cap <= 0 or period <= 0; misconfiguration here is
// almost always a bug rather than a recoverable runtime condition.
func New(cap int64, period time.Duration, opts ...Option) *Budget {
	if cap <= 0 {
		panic("budget/memory: cap must be > 0")
	}
	if period <= 0 {
		panic("budget/memory: period must be > 0")
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
		o(b)
	}
	if b.now == nil {
		panic("budget/memory: clock must not be nil")
	}
	if b.sweepInterval > 0 {
		go b.sweepLoop()
	} else {
		close(b.doneCh)
	}
	return b
}

// Stop terminates the background sweeper. Safe to call multiple
// times. After Stop, the budget continues to admit and refund as
// normal but no longer evicts cold keys.
func (b *Budget) Stop() {
	b.stopOnce.Do(func() {
		close(b.stopCh)
		<-b.doneCh
	})
}

func (b *Budget) sweepLoop() {
	defer close(b.doneCh)
	t := time.NewTicker(b.sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-b.stopCh:
			return
		case <-t.C:
			b.sweep()
		}
	}
}

// sweep removes buckets whose period id is older than the current
// period. A bucket newly created in the current period and observed
// by a concurrent Consume is preserved because its periodN is the
// current period id by construction.
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
func (b *Budget) Consume(_ context.Context, key string, amount int64) (bool, int64, time.Duration, error) {
	if key == "" {
		return false, 0, 0, budget.ErrInvalidKey
	}
	if amount < 0 {
		return false, 0, 0, budget.ErrInvalidAmount
	}
	now := b.now()
	periodID, nextStart := b.periodOf(now)
	bk := b.loadOrInitBucket(key, periodID)

	bk.mu.Lock()
	defer bk.mu.Unlock()

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
func (b *Budget) Peek(_ context.Context, key string) (int64, error) {
	if key == "" {
		return 0, budget.ErrInvalidKey
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
func (b *Budget) Refund(_ context.Context, key string, amount int64) (int64, error) {
	if key == "" {
		return 0, budget.ErrInvalidKey
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
	n := 0
	b.buckets.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}
