package idempotency

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// MemoryStore is an in-memory Store for testing. Not suitable for production
// (no cross-process sharing).
type MemoryStore struct {
	mu      sync.RWMutex
	items   map[string]memEntry
	locks   map[string]memLock
	clock   func() time.Time
	logger  *slog.Logger
	runMu   sync.Mutex
	started bool

	setCount     uint64
	tryLockCount uint64
}

// MemoryStoreOption configures a MemoryStore.
type MemoryStoreOption func(*MemoryStore)

// WithMemoryStoreClock sets the time source. Useful for deterministic
// testing without time.Sleep. Panics on nil to fail fast at construction
// rather than dereferencing a nil func on the first store operation.
func WithMemoryStoreClock(fn func() time.Time) MemoryStoreOption {
	if fn == nil {
		panic("idempotency: WithMemoryStoreClock requires a non-nil time source")
	}
	return func(m *MemoryStore) { m.clock = fn }
}

// WithMemoryStoreLogger sets the *slog.Logger used by the store to
// surface security-relevant signals: fingerprint mismatches (same
// Idempotency-Key reused with a different request body — buggy retry
// or replay-with-tampering) at INFO, and best-effort token-mismatch
// Unlocks at DEBUG. When unset the store falls back to [slog.Default].
// Matches the kit's per-package [WithLogger] convention.
func WithMemoryStoreLogger(l *slog.Logger) MemoryStoreOption {
	return func(m *MemoryStore) {
		if l != nil {
			m.logger = l
		}
	}
}

type memEntry struct {
	resp        CachedResponse
	fingerprint []byte
	expiresAt   time.Time
}

type memLock struct {
	token       string
	fingerprint []byte
	expiresAt   time.Time
}

// NewMemoryStore creates a new in-memory idempotency store.
func NewMemoryStore(opts ...MemoryStoreOption) *MemoryStore {
	m := &MemoryStore{
		items: make(map[string]memEntry),
		locks: make(map[string]memLock),
		clock: time.Now,
	}
	for _, o := range opts {
		if o == nil {
			panic("idempotency: NewMemoryStore option must not be nil")
		}
		o(m)
	}
	if m.logger == nil {
		m.logger = slog.Default()
	}
	return m
}

func (m *MemoryStore) now() time.Time { return m.clock() }

func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return errors.New("idempotency: nil context")
	}
	return ctx.Err()
}

// Get returns a cached response for the key, applying fingerprint comparison
// if a non-nil fingerprint is supplied.
func (m *MemoryStore) Get(ctx context.Context, key string, fingerprint []byte) (*CachedResponse, bool, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, false, err
	}
	if err := m.ready(); err != nil {
		return nil, false, err
	}
	if err := ValidateStorageKey(key); err != nil {
		return nil, false, err
	}
	m.mu.RLock()
	entry, ok := m.items[key]
	m.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	if m.now().After(entry.expiresAt) {
		var stillLive bool
		entry, stillLive = m.recheckExpiredEntry(key)
		if !stillLive {
			return nil, false, nil
		}
	}
	if fingerprint != nil && (entry.fingerprint == nil || len(entry.fingerprint) != len(fingerprint) || subtle.ConstantTimeCompare(entry.fingerprint, fingerprint) != 1) {
		// Same Idempotency-Key, different request body fingerprint.
		// Almost always a buggy retry; occasionally a replay attempt
		// with mutated body. Surface so security monitoring can spot
		// the pattern. INFO because callers HTTP-translate this to
		// 422 — the operator should know without dashboards lighting
		// up.
		m.logger.Info("idempotency: fingerprint mismatch on cached response",
			redact.String("key", key),
		)
		return nil, true, nil
	}
	if err := ValidateCachedResponse(entry.resp); err != nil {
		return nil, false, err
	}
	return cloneResponse(entry.resp), false, nil
}

// recheckExpiredEntry resolves the race between Get's read snapshot and its
// expired-entry cleanup. A concurrent Set may have replaced the snapshot; in
// that case the fresh entry must be returned rather than deleted or reported
// as a miss.
func (m *MemoryStore) recheckExpiredEntry(key string) (memEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.items[key]
	if !ok {
		return memEntry{}, false
	}
	if m.now().After(e.expiresAt) {
		delete(m.items, key)
		return memEntry{}, false
	}
	return e, true
}

// evictInterval controls how often Set() scans for expired entries.
const evictInterval = 100

// tryLockEvictInterval controls how often TryLock() scans for expired locks.
// Set() also sweeps locks, but a churning workload whose handlers crash after
// TryLock (never reaching Set/Unlock) would otherwise leak abandoned locks
// indefinitely, because nothing on the lock-acquisition path reclaimed them.
const tryLockEvictInterval = 100

// evictBudget caps the number of entries one Set-time eviction pass scans
// under the write lock. With 10k items this previously walked the whole
// map, blocking concurrent reads/writes for the duration. Bounding the
// scan keeps Set's worst-case latency proportional to evictBudget rather
// than the map size; entries missed in one pass are picked up by the
// next pass or by [MemoryStore.Run]'s background sweeper.
const evictBudget = 256

// sweepInterval is the default cadence for [MemoryStore.Run]'s background
// sweeper. Operators that don't run Run() still get the bounded eviction
// inside Set(); Run is the path that keeps the working set clean during
// quiet periods between writes.
const sweepInterval = 30 * time.Second

// Set stores the response under the caller's token. Returns ErrLockLost if
// the lock for the key has been taken by another caller (or has expired).
// Returns [ErrInvalidTTL] when ttl <= 0.
func (m *MemoryStore) Set(ctx context.Context, key, token string, resp CachedResponse, ttl time.Duration) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	if err := m.ready(); err != nil {
		return err
	}
	if err := ValidateStorageKey(key); err != nil {
		return err
	}
	if ttl <= 0 {
		return ErrInvalidTTL
	}
	if err := ValidateCachedResponse(resp); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Verify the caller still holds the lock (token match + not expired).
	// Capture the fingerprint from the validated lock entry *before* any
	// opportunistic sweep: sweepExpiredLocked takes a fresh m.now() and can
	// delete this lock if its TTL elapsed between the ownership check and
	// the write, which would make m.locks[key] a zero value and silently
	// store a nil fingerprint (disabling body-mismatch detection).
	var fingerprint []byte
	if l, ok := m.locks[key]; ok {
		if subtle.ConstantTimeCompare([]byte(l.token), []byte(token)) != 1 || m.now().After(l.expiresAt) {
			return ErrLockLost
		}
		fingerprint = l.fingerprint
	} else {
		// No lock present — either it expired and was reclaimed, or Set
		// was called without TryLock. Either way the caller has no
		// authority to write here.
		return ErrLockLost
	}

	m.setCount++
	// Only the periodic evictInterval path runs under Set: when the working
	// set is entirely live long-TTL keys, a len>=maxEntries trigger would
	// scan up to evictBudget entries under the write lock on every write
	// and reclaim nothing (review-12). Background Run() + the interval tick
	// still bound amortized expired-entry cost.
	if m.setCount%evictInterval == 0 {
		m.sweepExpiredLocked(evictBudget)
	}

	// Re-check ownership after the sweep: if the lock was reclaimed mid-
	// call the documented ErrLockLost contract applies.
	if l, ok := m.locks[key]; !ok || subtle.ConstantTimeCompare([]byte(l.token), []byte(token)) != 1 {
		return ErrLockLost
	}

	m.items[key] = memEntry{
		resp:        copyResponseForStorage(resp),
		fingerprint: cloneBytes(fingerprint),
		expiresAt:   m.now().Add(ttl),
	}
	delete(m.locks, key)
	return nil
}

// TryLock implements the contract from [Store.TryLock]. Returns
// [ErrInvalidTTL] when ttl <= 0.
func (m *MemoryStore) TryLock(ctx context.Context, key string, fingerprint []byte, ttl time.Duration) (string, bool, bool, error) {
	if err := ctxErr(ctx); err != nil {
		return "", false, false, err
	}
	if err := m.ready(); err != nil {
		return "", false, false, err
	}
	if err := ValidateStorageKey(key); err != nil {
		return "", false, false, err
	}
	if ttl <= 0 {
		return "", false, false, ErrInvalidTTL
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()

	// Opportunistically reclaim abandoned locks. Without this, a workload
	// whose handlers crash after TryLock (never reaching Set or Unlock) would
	// leak expired locks indefinitely, since Set's sweep is the only other
	// path that touches the locks map. Bounded by evictBudget so lock
	// acquisition stays responsive even with high key cardinality.
	m.tryLockCount++
	if len(m.locks) >= memoryStoreMaxEntries || m.tryLockCount%tryLockEvictInterval == 0 {
		m.sweepExpiredLocksLocked(evictBudget)
	}

	// If a cached response with mismatched fingerprint exists and is still
	// fresh, the key has been *consumed* with different bytes — 422.
	if entry, ok := m.items[key]; ok && !now.After(entry.expiresAt) {
		if fingerprint != nil && (entry.fingerprint == nil || len(entry.fingerprint) != len(fingerprint) || subtle.ConstantTimeCompare(entry.fingerprint, fingerprint) != 1) {
			m.logger.Info("idempotency: fingerprint mismatch on cached response (TryLock)",
				redact.String("key", key),
			)
			return "", true, false, nil
		}
		// Cached response with matching fingerprint already exists; caller
		// should not have called TryLock — return contended (caller will
		// re-Get and replay).
		return "", false, false, nil
	}

	if l, locked := m.locks[key]; locked && !now.After(l.expiresAt) {
		if fingerprint != nil && (l.fingerprint == nil || len(l.fingerprint) != len(fingerprint) || subtle.ConstantTimeCompare(l.fingerprint, fingerprint) != 1) {
			m.logger.Info("idempotency: fingerprint mismatch on in-progress lock",
				redact.String("key", key),
			)
			return "", true, false, nil
		}
		return "", false, false, nil
	}

	token, err := GenerateToken()
	if err != nil {
		return "", false, false, err
	}
	m.locks[key] = memLock{
		token:       token,
		fingerprint: cloneBytes(fingerprint),
		expiresAt:   now.Add(ttl),
	}
	return token, false, true, nil
}

// Unlock releases the processing lock if the caller's token still owns it.
// Best-effort cleanup: token mismatch is silently ignored (returns nil).
func (m *MemoryStore) Unlock(ctx context.Context, key, token string) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	if err := m.ready(); err != nil {
		return err
	}
	if err := ValidateStorageKey(key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.locks[key]; ok {
		if subtle.ConstantTimeCompare([]byte(l.token), []byte(token)) == 1 {
			delete(m.locks, key)
		} else {
			// Best-effort no-op: caller's token doesn't match the
			// current holder. Usually means TTL expired and another
			// caller now owns the lock. Debug-log so repeated
			// occurrences are visible.
			m.logger.Debug("idempotency: Unlock with non-matching token (lock taken over)",
				redact.String("key", key),
			)
		}
	}
	return nil
}

// sweepExpiredLocked deletes expired entries from both the items map and the
// locks map. Each map gets its own independent scan budget so a full items map
// can never starve the lock sweep: a single shared counter previously meant an
// items map larger than budget consumed the entire allowance before the locks
// loop ran, leaving abandoned locks (TryLock without a following Set/Unlock) to
// accumulate without bound. Caller MUST hold m.mu.Lock(). budget <= 0 means
// unbounded — used only by tests; production callers should pass [evictBudget].
func (m *MemoryStore) sweepExpiredLocked(budget int) {
	now := m.now()
	scanned := 0
	for k, entry := range m.items {
		if budget > 0 && scanned >= budget {
			break
		}
		scanned++
		if now.After(entry.expiresAt) {
			delete(m.items, k)
		}
	}
	m.sweepExpiredLocksLocked(budget)
}

// sweepExpiredLocksLocked deletes up to budget expired locks. Caller MUST hold
// m.mu.Lock(). Split out so the lock-acquisition path (TryLock) can reclaim
// abandoned locks without spending its scan budget walking the items map.
func (m *MemoryStore) sweepExpiredLocksLocked(budget int) {
	now := m.now()
	scanned := 0
	for k, l := range m.locks {
		if budget > 0 && scanned >= budget {
			break
		}
		scanned++
		if now.After(l.expiresAt) {
			delete(m.locks, k)
		}
	}
}

// Run sweeps expired entries periodically until ctx is cancelled. Bounded
// per-pass scan budget (evictBudget) so a long-running service with large
// idempotency-key cardinality stays responsive even under contention.
//
// Optional — Set() also evicts opportunistically — but recommended for
// any service that holds a MemoryStore across more than a few thousand
// keys. Wire into the lifecycle runner like other background goroutines:
//
//	mc.Lifecycle.AddFunc("idem-sweeper", store.Run)
func (m *MemoryStore) Run(ctx context.Context) error {
	if err := m.ready(); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("idempotency: MemoryStore.Run requires a non-nil context")
	}
	m.runMu.Lock()
	if m.started {
		m.runMu.Unlock()
		return errors.New("idempotency: MemoryStore.Run already started")
	}
	m.started = true
	m.runMu.Unlock()
	defer func() {
		m.runMu.Lock()
		m.started = false
		m.runMu.Unlock()
	}()

	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			m.mu.Lock()
			m.sweepExpiredLocked(evictBudget)
			m.mu.Unlock()
		}
	}
}

func (m *MemoryStore) ready() error {
	if m == nil || m.items == nil || m.locks == nil || m.clock == nil {
		return ErrInvalidStore
	}
	return nil
}

func cloneResponse(resp CachedResponse) *CachedResponse {
	cp := copyResponseForStorage(resp)
	return &cp
}

func copyResponseForStorage(resp CachedResponse) CachedResponse {
	cp := CachedResponse{
		StatusCode: resp.StatusCode,
		Headers:    make(map[string][]string, len(resp.Headers)),
	}
	if resp.Body != nil {
		cp.Body = append([]byte(nil), resp.Body...)
	}
	for k, vals := range resp.Headers {
		vcp := make([]string, len(vals))
		copy(vcp, vals)
		cp.Headers[k] = vcp
	}
	return cp
}

// cloneBytes returns an independent copy of b that preserves nil-vs-empty
// distinction. A non-nil empty fingerprint ([]byte{}) must stay non-nil:
// the fingerprint guards in Get/TryLock only compare when both sides are
// non-nil, so collapsing []byte{} to nil would silently disable mismatch
// detection for that key. This mirrors the SQL backends, where an empty
// bytea is stored non-NULL and keeps mismatch detection active.
func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp
}
