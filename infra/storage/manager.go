package storage

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
)

// Manager holds named Storage backends, similar to Laravel's Storage::disk().
// It is safe for concurrent use.
//
// Usage:
//
//	mgr := storage.NewManager()
//	mgr.Register("local", localBackend)
//	mgr.Register("s3", s3Backend).SetDefault("s3")
//
//	mgr.Disk("s3").Put(ctx, key, r, meta)  // explicit disk
//	mgr.Default().Put(ctx, key, r, meta)    // default disk
type Manager struct {
	mu          sync.RWMutex
	backends    map[string]Storage
	order       []string // tracks first-registered name for Default(); Names() returns sorted copy
	defaultName string
}

// NewManager creates an empty Manager.
func NewManager() *Manager {
	return &Manager{
		backends: make(map[string]Storage),
	}
}

// Register adds a named backend to the manager.
// Panics if name is empty or already registered.
// Returns the Manager for fluent chaining.
func (m *Manager) Register(name string, backend Storage) *Manager {
	if name == "" {
		panic("storage.Manager: name must not be empty")
	}
	if backend == nil {
		panic("storage.Manager: backend must not be nil")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.backends[name]; ok {
		panic("storage.Manager: disk already registered")
	}

	m.backends[name] = backend
	m.order = append(m.order, name)
	return m
}

// Disk returns the backend registered under name.
// Panics if name is not registered (fail-fast, consistent with Builder pattern).
func (m *Manager) Disk(name string) Storage {
	m.mu.RLock()
	defer m.mu.RUnlock()

	b, ok := m.backends[name]
	if !ok {
		panic("storage.Manager: disk not registered")
	}
	return b
}

// SetDefault sets the default disk name. The name must already be registered.
// Returns the Manager for fluent chaining.
func (m *Manager) SetDefault(name string) *Manager {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.backends[name]; !ok {
		panic("storage.Manager: default disk is not registered")
	}
	m.defaultName = name
	return m
}

// Default returns the default backend.
// If no default was set explicitly, the first registered backend is used.
// Panics if no backends are registered, or if the order/backends invariant
// is violated (Default is documented self-checking so a future Unregister
// path that forgets to splice `order` fails loudly here instead of returning
// a nil backend at request time).
func (m *Manager) Default() Storage {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.defaultName != "" {
		s, ok := m.backends[m.defaultName]
		if !ok {
			panic("storage.Manager: default disk has no backend")
		}
		return s
	}
	if len(m.order) == 0 {
		panic("storage.Manager: no backends registered")
	}
	first := m.order[0]
	s, ok := m.backends[first]
	if !ok {
		// Invariant violation: order slice references a backend not in the
		// map. This previously could happen only via a hypothetical
		// Unregister; the explicit panic surfaces it loudly so the bug is
		// caught at Default() time rather than at request time via nil deref.
		panic("storage.Manager: order references backend absent from map")
	}
	return s
}

// Names returns all registered disk names in alphabetical order.
// Note: the order differs from [Default], which returns the first-registered disk.
// Do not assume Names()[0] is the default disk.
func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, len(m.order))
	copy(names, m.order)
	sort.Strings(names)
	return names
}

// Has reports whether a disk with the given name is registered.
func (m *Manager) Has(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, ok := m.backends[name]
	return ok
}

// Close closes all registered backends that implement io.Closer in reverse
// registration order. This ensures that decorators (e.g., encryption wrappers)
// are closed before the backends they wrap.
// Returns a joined error of all individual close failures.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for i := len(m.order) - 1; i >= 0; i-- {
		name := m.order[i]
		backend := m.backends[name]
		if closer, ok := backend.(io.Closer); ok {
			if err := closer.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close disk: %w", err))
			}
		}
	}
	return errors.Join(errs...)
}
