package oauth2

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bds421/rho-kit/core/v2/secret"
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
// accumulate without bound. Overwriting a live session with the same id
// zeroizes the replaced secrets when they do not share pointers with the
// incoming session.
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
	if existing, ok := m.sessions[id]; ok {
		// Zero replaced secrets when they are distinct objects from the
		// incoming session (same-pointer reuse is left alone).
		if existing.sess.AccessToken != nil && existing.sess.AccessToken != sess.AccessToken {
			existing.sess.AccessToken.Zero()
		}
		if existing.sess.RefreshToken != nil && existing.sess.RefreshToken != sess.RefreshToken {
			existing.sess.RefreshToken.Zero()
		}
	}
	m.sessions[id] = memorySession{sess: sess, expiresAt: now.Add(ttl)}
	return nil
}

// Get implements [SessionStore]. Returns ErrSessionNotFound if the
// session is missing OR expired.
//
// The returned [Session] is a deep copy: AccessToken/RefreshToken are
// fresh [secret.String] values and Claims is a shallow-cloned map. Callers
// may retain the snapshot across a concurrent Delete/eviction without the
// store-owned secret buffers being zeroized under them.
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
	return cloneSession(entry.sess), nil
}

// cloneSession returns a Session whose secret pointers and Claims map
// do not alias the store-owned values, so zeroize-on-Delete cannot
// corrupt a concurrent caller's snapshot.
func cloneSession(s Session) Session {
	out := Session{
		SessionID: s.SessionID,
		UserID:    s.UserID,
		Expiry:    s.Expiry,
	}
	if s.AccessToken != nil {
		out.AccessToken = secret.NewFromString(s.AccessToken.RevealString())
	}
	if s.RefreshToken != nil && !s.RefreshToken.IsEmpty() {
		out.RefreshToken = secret.NewFromString(s.RefreshToken.RevealString())
	}
	if s.Claims != nil {
		out.Claims = make(map[string]any, len(s.Claims))
		for k, v := range s.Claims {
			out.Claims[k] = v
		}
	}
	return out
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
//
// Live entries are capped at [DefaultMaxStateEntries] (overridable via
// [WithMaxStateEntries]) so unauthenticated GET /login cannot grow the
// map unboundedly during the state TTL window.
type MemoryStateStore struct {
	mu         sync.Mutex
	entries    map[string]memoryStateEntry
	maxEntries int
}

type memoryStateEntry struct {
	entry     StateEntry
	expiresAt time.Time
}

// DefaultMaxStateEntries is the default cap on live CSRF state entries
// in [MemoryStateStore]. At ~1k logins/s with a 10-minute TTL this is
// well below the multi-hundred-MB failure mode; raise only when a
// single-process deployment legitimately needs a larger burst window.
const DefaultMaxStateEntries = 10_000

// ErrStateStoreFull is returned by [MemoryStateStore.Put] when the live
// entry cap is reached after sweeping expired entries.
var ErrStateStoreFull = errors.New("oauth2: state store full")

// MemoryStateStoreOption configures [NewMemoryStateStore].
type MemoryStateStoreOption func(*MemoryStateStore)

// WithMaxStateEntries sets the maximum number of live state entries.
// Values <= 0 panic.
func WithMaxStateEntries(n int) MemoryStateStoreOption {
	if n <= 0 {
		panic("oauth2: WithMaxStateEntries requires a positive limit")
	}
	return func(m *MemoryStateStore) { m.maxEntries = n }
}

// NewMemoryStateStore returns a fresh empty store.
func NewMemoryStateStore(opts ...MemoryStateStoreOption) *MemoryStateStore {
	m := &MemoryStateStore{
		entries:    make(map[string]memoryStateEntry),
		maxEntries: DefaultMaxStateEntries,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
}

// Put implements [StateStore]. Opportunistically sweeps already-expired
// entries so abandoned logins (a callback that never arrives) cannot
// accumulate without bound. Returns [ErrStateStoreFull] when the live
// entry cap is exceeded after the sweep.
func (m *MemoryStateStore) Put(_ context.Context, state string, entry StateEntry, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for key, existing := range m.entries {
		if now.After(existing.expiresAt) {
			delete(m.entries, key)
		}
	}
	if _, exists := m.entries[state]; !exists && len(m.entries) >= m.maxEntries {
		return ErrStateStoreFull
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

// Take atomically returns and deletes a state entry (single-use
// consume). Used by the callback path so concurrent callbacks cannot
// both observe the same state.
func (m *MemoryStateStore) Take(_ context.Context, state string) (StateEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[state]
	if !ok {
		return StateEntry{}, ErrStateNotFound
	}
	delete(m.entries, state)
	if time.Now().After(entry.expiresAt) {
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
