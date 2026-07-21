package timeoutbudget

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrBudgetExhausted is returned by [Budget.WithRemaining] when
// the budget has zero or negative time remaining. Callers
// typically translate this to a 504 (or a domain-appropriate
// error) for the inbound request.
var ErrBudgetExhausted = errors.New("timeoutbudget: exhausted")

// Budget tracks a request-scoped time allocation that multiple
// downstream calls share. Safe for concurrent use.
type Budget struct {
	deadline    time.Time
	total       time.Duration
	reservation time.Duration
	// gen advances on Clear so outstanding restore funcs from earlier
	// reservations become no-ops and cannot desynchronize a later
	// reservation after a clear wiped the total (see WithReservation).
	gen uint64
	mu  sync.Mutex
	now func() time.Time
}

// Option configures [New].
type Option func(*Budget)

// WithClock overrides time.Now — used by tests for deterministic
// remaining-time calculations.
func WithClock(now func() time.Time) Option {
	if now == nil {
		panic("timeoutbudget: WithClock requires a non-nil clock")
	}
	return func(b *Budget) { b.now = now }
}

// New constructs a Budget with the supplied total duration. The
// returned context inherits the parent's cancellation and adds
// the budget deadline; pass it to downstream code that calls
// [FromContext] or [WithRemaining].
//
// Panics on total <= 0 — a zero-budget request makes no sense
// and is treated as misconfiguration.
func New(parent context.Context, total time.Duration, opts ...Option) (context.Context, *Budget, context.CancelFunc) {
	if parent == nil {
		panic("timeoutbudget: New requires a non-nil parent context")
	}
	if total <= 0 {
		panic("timeoutbudget: New requires a positive total")
	}
	now := time.Now
	b := &Budget{now: now}
	for _, opt := range opts {
		if opt == nil {
			panic("timeoutbudget: New option must not be nil")
		}
		opt(b)
	}
	b.total = total
	b.deadline = b.now().Add(total)

	// Compose with parent's deadline (whichever is tighter wins).
	ctx, cancel := context.WithDeadline(parent, b.deadline)
	ctx = contextWith(ctx, b)
	return ctx, b, cancel
}

// Remaining returns the time left in the budget, after deducting
// any active reservation. Returns 0 when exhausted; never
// negative. Uses the injected clock so fake-clock tests are
// deterministic.
func (b *Budget) Remaining() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	rem := b.deadline.Sub(b.now()) - b.reservation
	if rem < 0 {
		return 0
	}
	return rem
}

// Used returns how much of the total budget has elapsed since
// [New] — the complement of the raw (reservation-ignoring)
// remaining time. Clamped to [0, total]: never negative, and
// caps at the total once past the deadline. Pairs with
// [Budget.Remaining] for "where did the request's time go"
// observability.
func (b *Budget) Used() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	used := b.total - b.deadline.Sub(b.now())
	if used < 0 {
		return 0
	}
	if used > b.total {
		return b.total
	}
	return used
}

// Reservation returns the time currently reserved for post-call
// work via [WithReservation].
func (b *Budget) Reservation() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reservation
}

// WithReservation sets a duration the Budget will hold back from
// `Remaining()` so downstream calls cannot consume time the
// caller needs for post-call work (audit log write, lock
// release, metrics emit). Pass d <= 0 to clear every active
// reservation (see also [Budget.Clear]).
//
// Returns a restore function the caller MUST defer to undo the
// reservation when the post-call section is done. This is
// idiomatic in Go for "reserve a slot, restore on done":
//
//	restore := budget.WithReservation(50 * time.Millisecond)
//	defer restore()
//
// Restore subtracts exactly this call's own contribution rather
// than restoring an absolute snapshot, so it is safe for concurrent
// / overlapping (non-LIFO) reservations. A clear (d <= 0 or
// [Budget.Clear]) advances an internal generation so any restore
// from a reservation granted before the clear becomes a no-op —
// otherwise a deferred restore could silently shrink a sibling
// reservation that was granted after the clear.
func (b *Budget) WithReservation(d time.Duration) (restore func()) {
	b.mu.Lock()
	var gen uint64
	if d > 0 {
		b.reservation += d
		gen = b.gen
	} else {
		b.reservation = 0
		b.gen++
		gen = b.gen
	}
	b.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			if d <= 0 {
				return
			}
			b.mu.Lock()
			// Skip if a clear happened after this reservation was granted.
			if b.gen == gen {
				b.reservation -= d
				if b.reservation < 0 {
					b.reservation = 0
				}
			}
			b.mu.Unlock()
		})
	}
}

// Clear drops every active reservation and invalidates outstanding
// restore funcs from prior WithReservation calls. Prefer Clear over
// WithReservation(0) when the intent is "forget all holds" rather
// than "reserve nothing and get a no-op restore".
func (b *Budget) Clear() {
	b.mu.Lock()
	b.reservation = 0
	b.gen++
	b.mu.Unlock()
}

// WithRemaining returns a child context whose deadline is the
// budget's remaining time. When the budget is exhausted, returns
// the parent ctx and ErrBudgetExhausted with a nil cancel — the
// caller MUST check the error before invoking the downstream.
//
// The returned cancel is always safe to defer; when the budget
// is exhausted it is a no-op.
func (b *Budget) WithRemaining(parent context.Context) (context.Context, context.CancelFunc, error) {
	if parent == nil {
		return nil, func() {}, errors.New("timeoutbudget: WithRemaining requires a non-nil parent context")
	}
	rem := b.Remaining()
	if rem <= 0 {
		return parent, func() {}, ErrBudgetExhausted
	}
	ctx, cancel := context.WithTimeout(parent, rem)
	return ctx, cancel, nil
}

// Context key plumbing. Unexported so callers go through
// [FromContext] / the ctx returned by [New].
type budgetKeyT struct{}

var budgetKey budgetKeyT

func contextWith(ctx context.Context, b *Budget) context.Context {
	return context.WithValue(ctx, budgetKey, b)
}

// FromContext returns the Budget attached by [New], or nil when
// none. Use this in handler code that wants to introspect or
// take a child timeout WITHOUT plumbing the Budget pointer
// through every signature.
func FromContext(ctx context.Context) *Budget {
	if ctx == nil {
		return nil
	}
	if v, ok := ctx.Value(budgetKey).(*Budget); ok {
		return v
	}
	return nil
}
