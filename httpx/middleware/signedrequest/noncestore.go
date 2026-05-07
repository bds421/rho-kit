package signedrequest

import (
	"sync"
	"time"
)

// MemoryNonceStore is an in-process [NonceStore] backed by a sync.Map.
// Entries expire after TTL on the next SeenOrStore call that probes
// the same nonce, plus a periodic sweep on every Nth call.
//
// Use this for single-instance deployments where replay protection
// only needs to span this process. Multi-instance deployments should
// implement NonceStore against a shared store (Redis, Memcached) so
// a replayed nonce caught by one replica is rejected by every other.
type MemoryNonceStore struct {
	ttl        time.Duration
	mu         sync.Mutex
	seen       map[string]time.Time
	sweepEvery int
	calls      int
	now        func() time.Time
}

// NewMemoryNonceStore returns a MemoryNonceStore with the given TTL.
// TTL must be ≥ 2 × maximum clock skew of the verifier; the verifier
// rejects timestamps outside that window so any nonce older than TTL
// can no longer race a replay attempt.
func NewMemoryNonceStore(ttl time.Duration) *MemoryNonceStore {
	if ttl <= 0 {
		panic("signedrequest: NonceStore TTL must be > 0")
	}
	return &MemoryNonceStore{
		ttl:        ttl,
		seen:       make(map[string]time.Time),
		sweepEvery: 256,
		now:        time.Now,
	}
}

// SeenOrStore reports whether nonce was first seen by this store.
func (m *MemoryNonceStore) SeenOrStore(nonce string) (bool, error) {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls++
	if m.calls%m.sweepEvery == 0 {
		m.sweepLocked(now)
	}

	if at, ok := m.seen[nonce]; ok && now.Sub(at) < m.ttl {
		return false, nil
	}
	m.seen[nonce] = now
	return true, nil
}

func (m *MemoryNonceStore) sweepLocked(now time.Time) {
	for k, at := range m.seen {
		if now.Sub(at) >= m.ttl {
			delete(m.seen, k)
		}
	}
}

// Len returns the count of entries currently held. Useful in tests.
func (m *MemoryNonceStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.seen)
}
