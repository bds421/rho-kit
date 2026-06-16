// Package clock standardises the "swappable now() function" pattern
// the kit's data, crypto, and httpx packages all reinvent. Most kit
// types accept an optional WithClock(now func() time.Time) override
// for tests; this package gives them a shared type so test fixtures
// work uniformly and so future code does not redefine the same idiom
// inconsistently.
//
// Why a function value, not an interface: every existing call site
// only needs the current time. Adding Sleep / After / NewTicker on
// top would force every consumer to satisfy the larger surface even
// when they only call Now. The narrower contract is also easier to
// stub from a test — a closure over a variable beats a struct with
// five methods.
//
// Standard usage:
//
//	type Foo struct {
//	    now clock.Func
//	}
//
//	func WithClock(fn clock.Func) Option {
//	    return func(o *opts) { o.now = fn }
//	}
//
//	func New(opts ...Option) *Foo {
//	    o := defaults
//	    for _, opt := range opts { opt(&o) }
//	    return &Foo{now: clock.OrSystem(o.now)}
//	}
package clock

import "time"

// Func is the canonical signature for the swappable-time pattern.
// Implementations must be safe for concurrent use; the system clock
// (returned by [System]) trivially is.
type Func func() time.Time

// System returns the wall-clock-backed Func. Use this as the default
// when constructing types that accept a Func override; tests can swap
// in a deterministic stub via [Fixed] or [Stub].
func System() Func { return time.Now }

// OrSystem returns fn if non-nil, otherwise [System]. Constructor
// helper: most New(...) functions assign now = clock.OrSystem(o.now)
// after applying options so the field is never nil.
func OrSystem(fn Func) Func {
	if fn == nil {
		return time.Now
	}
	return fn
}

// Fixed returns a Func that always reports t. Tests use this to
// produce a frozen timeline — every call returns the same instant
// regardless of intervening real time. Goroutine-safe because the
// returned closure only reads its captured value.
func Fixed(t time.Time) Func {
	return func() time.Time { return t }
}

// Stub is a goroutine-safe mutable clock. Tests advance it with
// [Stub.Advance] or replace its time with [Stub.Set] to drive
// time-dependent code through a deterministic schedule.
//
// The zero value reports time.Time{} (the year-zero zero value);
// pass an explicit start to [NewStub] for production-like timestamps.
type Stub struct {
	now atomicTime
}

// NewStub creates a Stub starting at t. The returned Func reads the
// stub's current time on every call, so subsequent [Stub.Set] /
// [Stub.Advance] calls take effect immediately.
func NewStub(t time.Time) *Stub {
	s := &Stub{}
	s.now.Store(t)
	return s
}

// Now returns the stub's current time. Safe for concurrent reads.
func (s *Stub) Now() time.Time { return s.now.Load() }

// Set replaces the stub's current time. Safe for concurrent writes.
func (s *Stub) Set(t time.Time) { s.now.Store(t) }

// Advance moves the stub forward by d. Negative d moves backwards —
// useful for tests that exercise clock-skew handling. The increment is
// applied atomically, so concurrent Advance calls never lose updates.
func (s *Stub) Advance(d time.Duration) {
	s.now.Add(d)
}

// Func returns a [Func] backed by this stub. Pass to constructors
// that take a clock.Func override.
func (s *Stub) Func() Func { return s.Now }
