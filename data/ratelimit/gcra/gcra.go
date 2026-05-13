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
package gcra

import (
	"context"
	"sync"
	"time"
	"weak"

	"github.com/bds421/rho-kit/data/v2/ratelimit"
)

// defaultSweepInterval is the period between background passes that
// remove cold keys whose theoretical arrival time has elapsed.
const defaultSweepInterval = 5 * time.Minute

// Limiter is a per-key GCRA [ratelimit.Limiter].
type Limiter struct {
	rate  time.Duration
	burst int
	now   func() time.Time

	mu   sync.Mutex
	tats map[string]time.Time

	sweepInterval time.Duration
	stopOnce      sync.Once
	stopCh        chan struct{}
	doneCh        chan struct{}
}

// Option configures a [Limiter].
type Option func(*Limiter)

// WithClock overrides the time source for tests.
func WithClock(now func() time.Time) Option {
	if now == nil {
		panic("gcra: clock must not be nil")
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
		panic("gcra: period must be > 0")
	}
	if burst < 1 {
		panic("gcra: burst must be >= 1")
	}
	rate := period / time.Duration(burst)
	if rate <= 0 {
		panic("gcra: period/burst rounds to zero (burst exceeds period in nanoseconds); pick a longer period or smaller burst")
	}
	l := &Limiter{
		rate:          rate,
		burst:         burst,
		now:           time.Now,
		tats:          make(map[string]time.Time),
		sweepInterval: defaultSweepInterval,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}
	for _, o := range opts {
		if o == nil {
			panic("gcra: option must not be nil")
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
	if l == nil || l.rate <= 0 || l.burst < 1 || l.now == nil || l.tats == nil {
		return ratelimit.ErrInvalidLimiter
	}
	return nil
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

// sweep drops keys whose TAT is in the past — those have no live
// rate-limit state (the next Allow would treat them as fresh).
func (l *Limiter) sweep() {
	if l.ready() != nil {
		return
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, tat := range l.tats {
		if !tat.After(now) {
			delete(l.tats, k)
		}
	}
}

// Allow reports whether key's next event is permitted. retryAfter is
// the time-until-next-allowed when denied.
func (l *Limiter) Allow(_ context.Context, key string) (bool, time.Duration, error) {
	if err := l.ready(); err != nil {
		return false, 0, err
	}
	if err := ratelimit.ValidateKey(key); err != nil {
		return false, 0, err
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	tat, ok := l.tats[key]
	if !ok || tat.Before(now) {
		tat = now
	}
	allowAt := tat.Add(-time.Duration(l.burst-1) * l.rate)
	if now.Before(allowAt) {
		return false, allowAt.Sub(now) + time.Nanosecond, nil
	}
	l.tats[key] = tat.Add(l.rate)
	return true, 0, nil
}

// Len returns the number of tracked keys. Useful in tests.
func (l *Limiter) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.tats)
}
