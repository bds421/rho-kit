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
	mu          sync.RWMutex // protects subscribers slice only
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
func (w *Watchable[T]) Set(new T) {
	old := w.value.Load().(wrapper[T]).val
	w.value.Store(wrapper[T]{val: new})

	w.mu.RLock()
	// Copy the slice reference under lock so we iterate a stable snapshot.
	subs := w.subscribers
	w.mu.RUnlock()

	for _, fn := range subs {
		fn(old, new)
	}
}

// OnChange registers a callback invoked when the value changes.
// The callback receives the old and new values.
func (w *Watchable[T]) OnChange(fn func(old, new T)) {
	w.mu.Lock()
	// Append to a new slice to avoid mutating any snapshot held by Set.
	w.subscribers = append(append([]func(old, new T){}, w.subscribers...), fn)
	w.mu.Unlock()
}
