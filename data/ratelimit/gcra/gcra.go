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
package gcra

import (
	"context"
	"sync"
	"time"

	"github.com/bds421/rho-kit/data/ratelimit"
)

// Limiter is a per-key GCRA [ratelimit.Limiter].
type Limiter struct {
	rate   time.Duration // emission interval; period / burst
	burst  int           // burst tolerance (cells)
	now    func() time.Time

	mu     sync.Mutex
	tats   map[string]time.Time // theoretical arrival times
}

// Option configures a [Limiter].
type Option func(*Limiter)

// WithClock overrides the time source for tests.
func WithClock(now func() time.Time) Option {
	return func(l *Limiter) { l.now = now }
}

// New constructs a Limiter that allows up to `burst` events within any
// `period` duration, smoothed at `period/burst` per event.
//
// Examples:
//   - New(time.Second, 10): 10 events/sec smoothed (one every 100ms,
//     burst tolerance 10).
//   - New(time.Minute, 60): 60 events/min smoothed (one every second).
func New(period time.Duration, burst int, opts ...Option) *Limiter {
	if period <= 0 {
		panic("gcra: period must be > 0")
	}
	if burst < 1 {
		panic("gcra: burst must be >= 1")
	}
	l := &Limiter{
		rate:  period / time.Duration(burst),
		burst: burst,
		now:   time.Now,
		tats:  make(map[string]time.Time),
	}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Allow reports whether key's next event is permitted. retryAfter is
// the time-until-next-allowed when denied.
func (l *Limiter) Allow(_ context.Context, key string) (bool, time.Duration, error) {
	if key == "" {
		return false, 0, ratelimit.ErrInvalidKey
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	tat, ok := l.tats[key]
	if !ok || tat.Before(now) {
		tat = now
	}
	// Standard GCRA: admit when (tat - now) < burst*rate, deny when ≥.
	// Equivalently, deny when now ≤ tat - burst*rate. After `burst`
	// admits at the same instant the next call must hit allowAt == now
	// and be denied.
	allowAt := tat.Add(-time.Duration(l.burst) * l.rate)
	if !now.After(allowAt) {
		return false, allowAt.Sub(now) + time.Nanosecond, nil
	}
	// Event admitted; bump TAT.
	l.tats[key] = tat.Add(l.rate)
	return true, 0, nil
}

// Len returns the number of tracked keys. Useful in tests.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.tats)
}
