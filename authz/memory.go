package authz

import (
	"context"
	"sync"
)

// Memory is an in-memory [Decider] for tests and local development.
// It stores explicit (subject, action, resource) → allow tuples and
// returns [ErrDenied] for anything not registered.
//
// Thread-safe for concurrent reads and writes. Tests construct via
// [NewMemory], populate with Grant, and pass to handlers.
type Memory struct {
	mu     sync.RWMutex
	allows map[memoryKey]struct{}
}

type memoryKey struct {
	subject  string
	action   string
	resource string
}

// NewMemory returns an empty in-memory decider.
func NewMemory() *Memory {
	return &Memory{allows: map[memoryKey]struct{}{}}
}

// Grant records that subject is allowed to perform action on
// resource. Subsequent Allow calls with the same triple return nil.
func (m *Memory) Grant(subject, action, resource string) {
	m.mu.Lock()
	m.allows[memoryKey{subject, action, resource}] = struct{}{}
	m.mu.Unlock()
}

// Revoke removes a previously-granted permission. No-op if the
// permission was never granted.
func (m *Memory) Revoke(subject, action, resource string) {
	m.mu.Lock()
	delete(m.allows, memoryKey{subject, action, resource})
	m.mu.Unlock()
}

// Allow implements [Decider]. Returns nil iff the (subject, action,
// resource) triple was previously granted; otherwise [ErrDenied].
func (m *Memory) Allow(_ context.Context, subject, action, resource string) error {
	m.mu.RLock()
	_, ok := m.allows[memoryKey{subject, action, resource}]
	m.mu.RUnlock()
	if !ok {
		return ErrDenied
	}
	return nil
}
