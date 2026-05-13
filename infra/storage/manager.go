package storage

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// ErrManagerClosed is returned by [Manager.Register], [Manager.Backend],
// [Manager.Default], and [Manager.SetDefault] after the Manager has been
// [Manager.Close]-d. Callers shouldn't normally see this error in
// production — typically the application has already shut down by the
// time backends are closed — but the explicit sentinel makes
// post-shutdown races diagnosable.
var ErrManagerClosed = errors.New("storage.Manager: already closed")

// Manager holds named Storage backends, similar to Laravel's Storage::disk().
// It is safe for concurrent use. [Manager.Close] is idempotent and gates
// further registrations / lookups behind [ErrManagerClosed].
//
// Usage:
//
//	mgr := storage.NewManager()
//	mgr.Register("local", localBackend)
//	mgr.Register("s3", s3Backend).SetDefault("s3")
//
//	backend, err := mgr.Backend("s3")  // dynamic lookup; not-found is typed
//	if err != nil { ... }
//	backend.Put(ctx, key, r, meta)
//	mgr.Default().Put(ctx, key, r, meta) // default disk (panic if none registered)
type Manager struct {
	mu          sync.RWMutex
	backends    map[string]Storage
	order       []string // tracks first-registered name for Default(); Names() returns sorted copy
	defaultName string
	closed      bool
}

// NewManager creates an empty Manager.
func NewManager() *Manager {
	return &Manager{
		backends: make(map[string]Storage),
	}
}

// Register adds a named backend to the manager.
// Panics if name is empty or already registered, or if the Manager is
// closed (post-shutdown registration is a wiring bug).
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

	if m.closed {
		panic("storage.Manager: Register after Close")
	}
	if _, ok := m.backends[name]; ok {
		panic("storage.Manager: backend already registered")
	}

	m.backends[name] = backend
	m.order = append(m.order, name)
	return m
}

// Backend returns the backend registered under name.
//
// Unlike Default(), which is a startup-time wiring guarantee, Backend
// is a runtime lookup — callers may pass a name resolved from request
// input (e.g. a tenant-configured bucket). Returns an
// [apperror.NotFoundError] so HTTP/gRPC adapters map it to 404 rather
// than crashing the request.
func (m *Manager) Backend(name string) (Storage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, ErrManagerClosed
	}
	b, ok := m.backends[name]
	if !ok {
		return nil, apperror.NewNotFound("storage_backend", name)
	}
	return b, nil
}

// MustBackend returns the backend registered under name or panics if
// the name is not registered. Use this only for callers that genuinely
// have startup-time wiring (the backend name is a compile-time constant
// or comes from validated configuration). Prefer [Backend] for any
// lookup driven by request input.
func (m *Manager) MustBackend(name string) Storage {
	b, err := m.Backend(name)
	if err != nil {
		panic(fmt.Sprintf("storage.Manager: %s", err))
	}
	return b
}

// SetDefault sets the default backend name. The name must already be
// registered. Returns the Manager for fluent chaining. Panics if the
// Manager is closed — post-shutdown reconfiguration is a wiring bug.
func (m *Manager) SetDefault(name string) *Manager {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		panic("storage.Manager: SetDefault after Close")
	}
	if _, ok := m.backends[name]; !ok {
		panic("storage.Manager: default backend is not registered")
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
			panic("storage.Manager: default backend missing from registry")
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

// Names returns all registered backend names in alphabetical order.
// Note: the order differs from [Default], which returns the first-registered
// backend. Do not assume Names()[0] is the default.
func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, len(m.order))
	copy(names, m.order)
	sort.Strings(names)
	return names
}

// Has reports whether a backend with the given name is registered.
func (m *Manager) Has(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, ok := m.backends[name]
	return ok
}

// Close closes all registered backends that implement io.Closer in reverse
// registration order. This ensures that decorators (e.g., encryption wrappers)
// are closed before the backends they wrap.
//
// Idempotent — a second call is a no-op (returns nil). After Close,
// further [Manager.Register] panics and [Manager.Backend] /
// [Manager.SetDefault] return [ErrManagerClosed]. Returns a joined error
// of all individual close failures.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}
	m.closed = true

	var errs []error
	for i := len(m.order) - 1; i >= 0; i-- {
		name := m.order[i]
		if err := Close(m.backends[name]); err != nil {
			errs = append(errs, fmt.Errorf("close backend: %w", err))
		}
	}
	return errors.Join(errs...)
}
