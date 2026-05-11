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
func (w *Watchable[T]) Get() T {
	return w.value.Load().(wrapper[T]).val
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
// A panicking subscriber is recovered and logged so that remaining
// subscribers are still notified.
func (w *Watchable[T]) Set(val T) {
	w.setMu.Lock()
	defer w.setMu.Unlock()

	old := w.value.Load().(wrapper[T]).val
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
// Returns a cancel function that unregisters the subscriber.
func (w *Watchable[T]) OnChange(fn func(old, new T)) func() {
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
