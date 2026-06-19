package authz

import (
	"context"
	"sync"
)

// MemoryStore is an in-memory [Decider] for tests and local development.
// It stores explicit (subject, action, resource) → allow tuples and
// returns [ErrDenied] for anything not registered.
//
// Thread-safe for concurrent reads and writes. Tests construct via
// [NewMemoryStore], populate with Grant, and pass to handlers.
type MemoryStore struct {
	mu     sync.RWMutex
	allows map[memoryKey]struct{}
}

type memoryKey struct {
	subject  string
	action   string
	resource string
}

// NewMemory returns an empty in-memory decider.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{allows: map[memoryKey]struct{}{}}
}

// Grant records that subject is allowed to perform action on
// resource. Subsequent Allow calls with the same triple return nil.
func (m *MemoryStore) Grant(subject, action, resource string) {
	mustValidateRequest(Request{Subject: subject, Action: action, Resource: resource})
	m.mu.Lock()
	m.allows[memoryKey{subject, action, resource}] = struct{}{}
	m.mu.Unlock()
}

// Revoke removes a previously-granted permission. No-op if the
// permission was never granted.
func (m *MemoryStore) Revoke(subject, action, resource string) {
	mustValidateRequest(Request{Subject: subject, Action: action, Resource: resource})
	m.mu.Lock()
	delete(m.allows, memoryKey{subject, action, resource})
	m.mu.Unlock()
}

// Allow implements [Decider]. Returns nil iff the (subject, action,
// resource) triple was previously granted; otherwise [ErrDenied].
func (m *MemoryStore) Allow(ctx context.Context, subject, action, resource string) error {
	if m == nil {
		return ErrNoDecider
	}
	if ctx == nil {
		return ErrInvalidContext
	}
	if err := ValidateRequest(Request{Subject: subject, Action: action, Resource: resource}); err != nil {
		return err
	}
	m.mu.RLock()
	_, ok := m.allows[memoryKey{subject, action, resource}]
	m.mu.RUnlock()
	if !ok {
		return ErrDenied
	}
	return nil
}

func mustValidateRequest(req Request) {
	if err := ValidateRequest(req); err != nil {
		panic("authz/memory: invalid request")
	}
}
