package oauth2

import (
	"context"
	"sync"
	"time"
)

// MemorySessionStore is an in-process [SessionStore] for tests and
// single-process services. Production deployments with multiple
// replicas should back sessions with Redis or Postgres.
type MemorySessionStore struct {
	mu       sync.Mutex
	sessions map[string]memorySession
}

type memorySession struct {
	sess      Session
	expiresAt time.Time
}

// NewMemorySessionStore returns a fresh empty store.
func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{sessions: make(map[string]memorySession)}
}

// Put implements [SessionStore]. Opportunistically sweeps already-expired
// sessions so abandoned entries (a login whose owner never returns) cannot
// accumulate without bound.
func (m *MemorySessionStore) Put(_ context.Context, id string, sess Session, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for key, entry := range m.sessions {
		if now.After(entry.expiresAt) {
			// Zeroize secrets on eviction, matching Get/Delete.
			if entry.sess.AccessToken != nil {
				entry.sess.AccessToken.Zero()
			}
			if entry.sess.RefreshToken != nil {
				entry.sess.RefreshToken.Zero()
			}
			delete(m.sessions, key)
		}
	}
	m.sessions[id] = memorySession{sess: sess, expiresAt: now.Add(ttl)}
	return nil
}

// Get implements [SessionStore]. Returns ErrSessionNotFound if the
// session is missing OR expired.
func (m *MemorySessionStore) Get(_ context.Context, id string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.sessions[id]
	if !ok {
		return Session{}, ErrSessionNotFound
	}
	if time.Now().After(entry.expiresAt) {
		// Zeroize secrets on expiry.
		if entry.sess.AccessToken != nil {
			entry.sess.AccessToken.Zero()
		}
		if entry.sess.RefreshToken != nil {
			entry.sess.RefreshToken.Zero()
		}
		delete(m.sessions, id)
		return Session{}, ErrSessionNotFound
	}
	return entry.sess, nil
}

// Delete implements [SessionStore]. Zeroizes the session's secrets
// before removal so the bytes don't linger in memory.
func (m *MemorySessionStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry, ok := m.sessions[id]; ok {
		if entry.sess.AccessToken != nil {
			entry.sess.AccessToken.Zero()
		}
		if entry.sess.RefreshToken != nil {
			entry.sess.RefreshToken.Zero()
		}
		delete(m.sessions, id)
	}
	return nil
}

// MemoryStateStore is an in-process [StateStore] for tests and
// single-process services. Production deployments should use Redis.
type MemoryStateStore struct {
	mu      sync.Mutex
	entries map[string]memoryStateEntry
}

type memoryStateEntry struct {
	entry     StateEntry
	expiresAt time.Time
}

// NewMemoryStateStore returns a fresh empty store.
func NewMemoryStateStore() *MemoryStateStore {
	return &MemoryStateStore{entries: make(map[string]memoryStateEntry)}
}

// Put implements [StateStore]. Opportunistically sweeps already-expired
// entries so abandoned logins (a callback that never arrives) cannot
// accumulate without bound.
func (m *MemoryStateStore) Put(_ context.Context, state string, entry StateEntry, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for key, existing := range m.entries {
		if now.After(existing.expiresAt) {
			delete(m.entries, key)
		}
	}
	m.entries[state] = memoryStateEntry{entry: entry, expiresAt: now.Add(ttl)}
	return nil
}

// Get implements [StateStore]. Returns ErrStateNotFound if the entry
// is missing OR expired.
func (m *MemoryStateStore) Get(_ context.Context, state string) (StateEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[state]
	if !ok {
		return StateEntry{}, ErrStateNotFound
	}
	if time.Now().After(entry.expiresAt) {
		delete(m.entries, state)
		return StateEntry{}, ErrStateNotFound
	}
	return entry.entry, nil
}

// Delete implements [StateStore]. Idempotent.
func (m *MemoryStateStore) Delete(_ context.Context, state string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, state)
	return nil
}
