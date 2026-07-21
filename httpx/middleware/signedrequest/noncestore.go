package signedrequest

import (
	"context"
	"errors"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// ErrInvalidNonceStore is returned when a MemoryNonceStore method is invoked
// on a nil or otherwise uninitialized store.
var ErrInvalidNonceStore = errors.New("signedrequest: nonce store is not initialized")

// MemoryNonceStore is an in-process [NonceStore] backed by a
// mutex-guarded map.
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

// MemoryOption configures a [MemoryNonceStore].
type MemoryOption func(*MemoryNonceStore)

// defaultSweepEvery is the call cadence on which MemoryNonceStore
// runs its O(n) expired-entry walk. 256 is a small enough integer
// that even at very low RPS the map cannot grow unbounded for long,
// and large enough that the per-call overhead of the modulus check
// is negligible. Tune via [WithSweepEvery] for traffic shapes that
// favour a different trade-off.
const defaultSweepEvery = 256

// WithSweepEvery sets how often (in number of SeenOrStore calls) the
// store walks the map and deletes expired entries. The walk holds
// the store's lock for the duration of the iteration, so smaller
// values mean more frequent O(n) pauses; larger values mean the map
// can grow further before the next reclamation. The default
// ([defaultSweepEvery]) is a balanced choice for general traffic.
//
// Pick smaller values (e.g. 16) for low-throughput services where
// memory matters more than per-call latency tail; pick larger values
// (e.g. 4096) for high-RPS services where the periodic sweep pause
// is more visible than the additional retained memory.
//
// n must be > 0 — the constructor panics on zero or negative.
func WithSweepEvery(n int) MemoryOption {
	return func(m *MemoryNonceStore) {
		if n <= 0 {
			panic("signedrequest: WithSweepEvery requires n > 0")
		}
		m.sweepEvery = n
	}
}

// NewMemoryNonceStore returns a MemoryNonceStore with the given TTL.
// TTL must be ≥ 2 × maximum clock skew of the verifier; the verifier
// rejects timestamps outside that window so any nonce older than TTL
// can no longer race a replay attempt.
//
// Use [WithSweepEvery] to override the default sweep cadence.
func NewMemoryNonceStore(ttl time.Duration, opts ...MemoryOption) *MemoryNonceStore {
	if ttl <= 0 {
		panic("signedrequest: NonceStore TTL must be > 0")
	}
	m := &MemoryNonceStore{
		ttl:        ttl,
		seen:       make(map[string]time.Time),
		sweepEvery: defaultSweepEvery,
		now:        time.Now,
	}
	for _, o := range opts {
		if o == nil {
			panic("signedrequest: NewMemoryNonceStore memory nonce store option must not be nil")
		}
		o(m)
	}
	return m
}

// SeenOrStore reports whether nonce was first seen by this store. The
// ctx is honored only as a cancellation check at entry — the in-memory
// path holds no external resources, so there is nothing to release.
func (m *MemoryNonceStore) SeenOrStore(ctx context.Context, nonce string) (bool, error) {
	if err := m.ready(); err != nil {
		return false, err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return false, err
		}
	}
	if invalidStoreNonce(nonce) {
		return false, ErrNonceInvalid
	}
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

func (m *MemoryNonceStore) ready() error {
	if m == nil ||
		m.ttl <= 0 ||
		m.seen == nil ||
		m.sweepEvery <= 0 ||
		m.now == nil {
		return ErrInvalidNonceStore
	}
	return nil
}

func invalidStoreNonce(nonce string) bool {
	if nonce == "" || len(nonce) > storeNonceKeyMaxLen || !utf8.ValidString(nonce) {
		return true
	}
	for _, r := range nonce {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

func (m *MemoryNonceStore) sweepLocked(now time.Time) {
	for k, at := range m.seen {
		if now.Sub(at) >= m.ttl {
			delete(m.seen, k)
		}
	}
}

// TTL implements [NonceStoreTTL] so [Middleware] can enforce the
// TTL >= 2×maxClockSkew replay-protection invariant at construction.
func (m *MemoryNonceStore) TTL() time.Duration {
	if m == nil {
		return 0
	}
	return m.ttl
}

// Len returns the count of entries currently held. Useful in tests.
func (m *MemoryNonceStore) Len() int {
	if m == nil || m.seen == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.seen)
}
