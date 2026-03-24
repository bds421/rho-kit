package config

import (
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
	subscribers []func(old, new T)
	mu          sync.Mutex // protects subscribers slice and write ordering
}

// wrapper avoids the atomic.Value constraint that stored types must be
// identical across calls (interface boxing of different concrete types).
type wrapper[T any] struct {
	val T
}

// NewWatchable creates a Watchable initialised with the given value.
func NewWatchable[T any](initial T) *Watchable[T] {
	w := &Watchable[T]{}
	w.value.Store(wrapper[T]{val: initial})
	return w
}

// Get returns the current config value. Lock-free read.
func (w *Watchable[T]) Get() T {
	return w.value.Load().(wrapper[T]).val
}

// Set atomically replaces the value and notifies all subscribers.
// Subscribers are called synchronously in the caller's goroutine.
// The write lock ensures concurrent Set calls read a consistent old value.
func (w *Watchable[T]) Set(new T) {
	w.mu.Lock()
	old := w.value.Load().(wrapper[T]).val
	w.value.Store(wrapper[T]{val: new})
	// Grab the subscriber snapshot under lock (already copy-on-write from OnChange).
	subs := w.subscribers
	w.mu.Unlock()

	for _, fn := range subs {
		fn(old, new)
	}
}

// OnChange registers a callback invoked on every Set call, even if the
// new value is equal to the old value. To filter unchanged values,
// compare old and new in the callback.
func (w *Watchable[T]) OnChange(fn func(old, new T)) {
	w.mu.Lock()
	// Append to a new slice to avoid mutating any snapshot held by Set.
	w.subscribers = append(append([]func(old, new T){}, w.subscribers...), fn)
	w.mu.Unlock()
}
