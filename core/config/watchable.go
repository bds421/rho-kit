package config

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

// Watchable holds a config value that can be atomically swapped with
// subscriber notification. Safe for concurrent use.
//
// Reads via Get are lock-free (atomic.Value). Writes via Set atomically
// replace the value and notify all registered subscribers synchronously.
type Watchable[T any] struct {
	value       atomic.Value // holds wrapper[T]
	subscribers map[uint64]func(old, new T)
	nextID      uint64
	mu          sync.Mutex // protects subscribers map and write ordering
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
// Subscribers are called synchronously in the caller's goroutine.
// A panicking subscriber is recovered and logged so that remaining
// subscribers are still notified.
// The write lock ensures concurrent Set calls read a consistent old value.
func (w *Watchable[T]) Set(val T) {
	w.mu.Lock()
	old := w.value.Load().(wrapper[T]).val
	w.value.Store(wrapper[T]{val: val})
	// Snapshot subscriber map under lock.
	subs := make(map[uint64]func(old, new T), len(w.subscribers))
	for id, fn := range w.subscribers {
		subs[id] = fn
	}
	w.mu.Unlock()

	for _, fn := range subs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("config: subscriber panicked", "panic", r)
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
