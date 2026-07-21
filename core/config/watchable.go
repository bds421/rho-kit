package config

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Watchable holds a config value that can be atomically swapped with
// subscriber notification. Safe for concurrent use.
//
// Construct via [NewWatchable]. The zero value is intentionally not
// usable: Get/Set/OnChange panic with a clear message rather than an
// opaque interface-conversion or nil-map error.
//
// Reads via Get are lock-free (atomic.Value). Writes via Set atomically
// replace the value and notify all registered subscribers synchronously
// in store order — concurrent Set(A) and Set(B) deliver consistent
// (old, new) pairs to every subscriber so derived state cannot diverge
// from the canonical sequence.
type Watchable[T any] struct {
	value       atomic.Value // holds wrapper[T]
	subscribers map[uint64]func(old, new T)
	nextID      uint64
	mu          sync.Mutex // protects subscribers map (re-entrant from Set callbacks)
	setMu       sync.Mutex // serialises Set so notifications fire in store order
}

// wrapper avoids the atomic.Value constraint that stored types must be
// identical across calls (interface boxing of different concrete types).
type wrapper[T any] struct {
	val T
}

// NewWatchable creates a Watchable initialised with the given value.
func NewWatchable[T any](initial T) *Watchable[T] {
	w := &Watchable[T]{
		subscribers: make(map[uint64]func(old, new T)),
	}
	w.value.Store(wrapper[T]{val: initial})
	return w
}

// Get returns the current config value. Lock-free read.
//
// Panics if w is the zero value (not constructed via [NewWatchable]) —
// the zero Watchable has never stored a value, so Load returns nil and
// the type assertion would otherwise produce an opaque interface-
// conversion panic.
func (w *Watchable[T]) Get() T {
	if w == nil {
		panic("config: Watchable must be constructed with NewWatchable")
	}
	raw := w.value.Load()
	if raw == nil {
		panic("config: Watchable must be constructed with NewWatchable")
	}
	return raw.(wrapper[T]).val
}

// Set atomically replaces the value and notifies all subscribers.
// Subscribers are called synchronously in the caller's goroutine in the
// same order Set was invoked across goroutines — setMu serialises the
// store-and-notify pair so concurrent Set(A) and Set(B) produce
// consistent (old, new) sequences for every subscriber. A slow
// subscriber blocks other Set callers; pair with a buffered channel
// inside the subscriber if that matters.
//
// Subscribers may safely call OnChange (re-entrant) — the per-Set lock
// (setMu) is distinct from the subscriber-map lock (mu).
//
// Subscribers must NOT call Set on the same Watchable from within their
// callback. Set holds setMu for the entire synchronous notification pass,
// and setMu is not reentrant, so a re-entrant Set self-deadlocks the calling
// goroutine permanently and blocks all future Set callers. To normalise or
// clamp a freshly-set value, do it in the loadFn / before calling Set, not
// from a subscriber.
//
// A panicking subscriber is recovered and logged so that remaining
// subscribers are still notified.
//
// Panics if w is the zero value (not constructed via [NewWatchable]).
func (w *Watchable[T]) Set(val T) {
	if w == nil {
		panic("config: Watchable must be constructed with NewWatchable")
	}

	w.setMu.Lock()
	defer w.setMu.Unlock()

	raw := w.value.Load()
	if raw == nil {
		panic("config: Watchable must be constructed with NewWatchable")
	}
	old := raw.(wrapper[T]).val
	w.value.Store(wrapper[T]{val: val})

	w.mu.Lock()
	subs := make([]func(old, new T), 0, len(w.subscribers))
	for _, fn := range w.subscribers {
		subs = append(subs, fn)
	}
	w.mu.Unlock()

	for _, fn := range subs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("config: subscriber panicked", redact.Panic(r))
				}
			}()
			fn(old, val)
		}()
	}
}

// OnChange registers a callback invoked on every Set call, even if the
// new value is equal to the old value. To filter unchanged values,
// compare old and new in the callback.
//
// Subscribers are notified in non-deterministic order (map iteration).
// Do not depend on notification ordering between subscribers.
//
// Panics if fn is nil — a nil callback would silently no-op on every
// Set, defeating the purpose of subscribing and producing surprising
// runtime recover-and-log noise. Wave 68 closed a hostile-review
// finding that the prior nil-tolerant path admitted wiring bugs.
//
// Returns a cancel function that unregisters the subscriber.
//
// Panics if w is the zero value (not constructed via [NewWatchable]).
func (w *Watchable[T]) OnChange(fn func(old, new T)) func() {
	if w == nil || w.subscribers == nil {
		panic("config: Watchable must be constructed with NewWatchable")
	}
	if fn == nil {
		panic("config: Watchable.OnChange requires a non-nil callback")
	}
	w.mu.Lock()
	id := w.nextID
	w.nextID++
	w.subscribers[id] = fn
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		delete(w.subscribers, id)
		w.mu.Unlock()
	}
}
