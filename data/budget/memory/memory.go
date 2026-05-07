// Package memory implements an in-process [budget.Budget] keyed by
// arbitrary string with a fixed-window reset period.
//
// State per key is a single (period bucket id, used) pair held in a
// [sync.Map]; admit and peek are constant-time. A bucket is replaced
// atomically when a Consume call observes a stale period — there is
// no background sweep, the data structure self-compacts on access.
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

	"github.com/bds421/rho-kit/data/budget"
)

// Budget is a per-key in-process [budget.Budget] with a fixed-window
// reset period.
type Budget struct {
	cap    int64         // per-period cap; > 0
	period time.Duration // window length; > 0
	now    func() time.Time

	buckets sync.Map // map[string]*bucket
}

// bucket holds the per-key state. We store the period id atomically
// so a Peek path that races with the Consume that rolls the period
// observes a consistent snapshot without acquiring a lock.
//
// The mutex serialises the read-modify-write inside a single bucket;
// the outer sync.Map serialises insertion of new keys.
type bucket struct {
	mu      sync.Mutex
	periodN atomic.Int64 // floor(unixNs / periodNs); identifies the window
	used    atomic.Int64 // amount consumed in the current window
}

// Option configures a [Budget].
type Option func(*Budget)

// WithClock overrides the time source (tests only). Must not be nil.
func WithClock(now func() time.Time) Option {
	return func(b *Budget) { b.now = now }
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
		cap:    cap,
		period: period,
		now:    time.Now,
	}
	for _, o := range opts {
		o(b)
	}
	if b.now == nil {
		panic("budget/memory: clock must not be nil")
	}
	return b
}

// periodOf returns the integer period id and the wall-clock instant
// the next window begins. Period ids are floor(unixNs / periodNs)
// in the UTC frame so two replicas with synchronised clocks always
// agree on the boundary.
func (b *Budget) periodOf(t time.Time) (int64, time.Time) {
	periodNs := int64(b.period)
	id := t.UTC().UnixNano() / periodNs
	nextStart := time.Unix(0, (id+1)*periodNs).UTC()
	return id, nextStart
}

// loadOrInitBucket returns the bucket for key, creating it if it
// does not exist. The returned bucket may carry a stale period id;
// callers re-validate under the bucket mutex.
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

	// Compaction: if the bucket id is stale, reset used and adopt
	// the current period. Replacing in-place under the mutex avoids
	// a churn of new bucket allocations on every roll-over.
	if bk.periodN.Load() != periodID {
		bk.periodN.Store(periodID)
		bk.used.Store(0)
	}

	used := bk.used.Load()
	if used+amount > b.cap {
		remaining := b.cap - used
		if remaining < 0 {
			remaining = 0
		}
		return false, remaining, time.Until(nextStart), nil
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
	// Snapshot under the bucket mutex so a concurrent rollover does
	// not return a torn (stale-id, fresh-used) pair.
	bk.mu.Lock()
	defer bk.mu.Unlock()
	if bk.periodN.Load() != periodID {
		// Period rolled over since the last write: by definition
		// nothing has been spent in the current window yet.
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
		// Period rolled over since last write — nothing has been
		// charged yet, refund collapses to a no-op.
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
