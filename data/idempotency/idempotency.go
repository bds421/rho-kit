// Package idempotency defines the Store interface and types for idempotent
// request handling. The HTTP middleware implementation lives in
// [httpx/middleware/idempotency], with backend-specific stores in
// [pgstore] (PostgreSQL) and [redisstore] (Redis).
package idempotency

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// ErrLockLost indicates the caller no longer holds the processing lock for a
// key — typically because the lock TTL expired and another caller acquired
// it before this caller's Set/Unlock ran. Backends return this so the
// middleware can avoid clobbering a fresher response.
var ErrLockLost = errors.New("idempotency: caller no longer holds the lock")

// Store persists and retrieves cached responses keyed by idempotency key.
//
// All methods accept a request fingerprint (typically SHA-256 of the request
// body, or a canonicalised subset of headers + body) so the backend can
// reject reuse of the same idempotency key with a *different* request — the
// standard mitigation against the "client retried with mutated body"
// failure mode that turns idempotent retry into silent data corruption
// (Stripe-style 422 on body mismatch).
//
// Pass nil for fingerprint to disable the comparison. The HTTP middleware
// always passes a fingerprint; direct callers must opt in to the safety.
type Store interface {
	// Get returns the cached response for the key.
	//
	// Return contract:
	//   - (resp, false, nil)  — cached response found, fingerprint matches
	//                           (or fingerprint argument is nil)
	//   - (nil,  false, nil)  — no cached response
	//   - (nil,  true,  nil)  — cached response exists but its fingerprint
	//                           differs from the supplied one. Caller MUST
	//                           treat this as 422 Unprocessable Entity.
	//   - (nil,  false, err)  — backend error
	Get(ctx context.Context, key string, fingerprint []byte) (*CachedResponse, bool, error)

	// TryLock attempts to acquire a processing lock for the key.
	//
	// Return contract:
	//   - (token, false, true,  nil) — lock acquired; caller MUST pass token
	//                                  to Set / Unlock
	//   - ("",    false, false, nil) — lock held by a concurrent processor with
	//                                  the *same* fingerprint (or fingerprint
	//                                  comparison disabled). Caller should
	//                                  treat this as 409 Conflict.
	//   - ("",    true,  false, nil) — key holds a lock or cached response with
	//                                  a *different* fingerprint. Caller MUST
	//                                  treat this as 422 Unprocessable Entity.
	//   - ("",    false, false, err) — backend error
	TryLock(ctx context.Context, key string, fingerprint []byte, ttl time.Duration) (token string, fingerprintMismatch bool, ok bool, err error)

	// Set stores the response, atomically replacing the lock row. The token
	// must be the one returned from the TryLock that started this critical
	// section. Returns [ErrLockLost] if the caller's token no longer matches
	// the current lock owner — a sign the TTL expired mid-handler and another
	// caller has already taken the slot.
	Set(ctx context.Context, key, token string, resp CachedResponse, ttl time.Duration) error

	// Unlock releases the processing lock for the caller's token. No-ops
	// safely if the lock has already expired or been released. Returns nil
	// (NOT ErrLockLost) on token mismatch — Unlock is a best-effort cleanup
	// path (e.g. on handler panic) and should not surface lock-loss to the
	// caller; the cached response was either already written or will not be.
	Unlock(ctx context.Context, key, token string) error
}

// CachedResponse stores the HTTP response data for replay.
type CachedResponse struct {
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers"`
	Body       []byte              `json:"body"`
}

// GenerateToken returns a 32-character hex-encoded random token. Backends use
// this for the owner-token of an acquired lock; the middleware does not
// inspect tokens itself — it just round-trips them between TryLock and
// Set/Unlock.
func GenerateToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("idempotency: failed to generate token: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// memoryStoreMaxEntries caps the in-memory store before lazy eviction runs.
// Prevents unbounded memory growth in long-running tests or misuse outside
// of test environments.
const memoryStoreMaxEntries = 10_000

// MemoryStore is an in-memory Store for testing. Not suitable for production
// (no cross-process sharing).
type MemoryStore struct {
	mu       sync.RWMutex
	items    map[string]memEntry
	locks    map[string]memLock
	setCount uint64
	clock    func() time.Time
}

// MemoryStoreOption configures a MemoryStore.
type MemoryStoreOption func(*MemoryStore)

// WithMemoryStoreClock sets the time source. Useful for deterministic testing
// without time.Sleep.
func WithMemoryStoreClock(fn func() time.Time) MemoryStoreOption {
	return func(m *MemoryStore) { m.clock = fn }
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
		o(m)
	}
	return m
}

func (m *MemoryStore) now() time.Time { return m.clock() }

// Get returns a cached response for the key, applying fingerprint comparison
// if a non-nil fingerprint is supplied.
func (m *MemoryStore) Get(_ context.Context, key string, fingerprint []byte) (*CachedResponse, bool, error) {
	m.mu.RLock()
	entry, ok := m.items[key]
	m.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	if m.now().After(entry.expiresAt) {
		m.mu.Lock()
		if e, still := m.items[key]; still && m.now().After(e.expiresAt) {
			delete(m.items, key)
		}
		m.mu.Unlock()
		return nil, false, nil
	}
	if fingerprint != nil && entry.fingerprint != nil && !bytes.Equal(entry.fingerprint, fingerprint) {
		return nil, true, nil
	}
	return cloneResponse(entry.resp), false, nil
}

// evictInterval controls how often Set() scans for expired entries.
const evictInterval = 100

// Set stores the response under the caller's token. Returns ErrLockLost if
// the lock for the key has been taken by another caller (or has expired).
func (m *MemoryStore) Set(_ context.Context, key, token string, resp CachedResponse, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Verify the caller still holds the lock (token match + not expired).
	if l, ok := m.locks[key]; ok {
		if l.token != token || m.now().After(l.expiresAt) {
			return ErrLockLost
		}
	} else {
		// No lock present — either it expired and was reclaimed, or Set
		// was called without TryLock. Either way the caller has no
		// authority to write here.
		return ErrLockLost
	}

	m.setCount++
	if len(m.items) >= memoryStoreMaxEntries || m.setCount%evictInterval == 0 {
		now := m.now()
		for k, entry := range m.items {
			if now.After(entry.expiresAt) {
				delete(m.items, k)
			}
		}
		for k, l := range m.locks {
			if now.After(l.expiresAt) {
				delete(m.locks, k)
			}
		}
	}

	m.items[key] = memEntry{
		resp:        copyResponseForStorage(resp),
		fingerprint: cloneBytes(m.locks[key].fingerprint),
		expiresAt:   m.now().Add(ttl),
	}
	delete(m.locks, key)
	return nil
}

// TryLock implements the contract from [Store.TryLock].
func (m *MemoryStore) TryLock(_ context.Context, key string, fingerprint []byte, ttl time.Duration) (string, bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()

	// If a cached response with mismatched fingerprint exists and is still
	// fresh, the key has been *consumed* with different bytes — 422.
	if entry, ok := m.items[key]; ok && now.Before(entry.expiresAt) {
		if fingerprint != nil && entry.fingerprint != nil && !bytes.Equal(entry.fingerprint, fingerprint) {
			return "", true, false, nil
		}
		// Cached response with matching fingerprint already exists; caller
		// should not have called TryLock — return contended (caller will
		// re-Get and replay).
		return "", false, false, nil
	}

	if l, locked := m.locks[key]; locked && now.Before(l.expiresAt) {
		if fingerprint != nil && l.fingerprint != nil && !bytes.Equal(l.fingerprint, fingerprint) {
			return "", true, false, nil
		}
		return "", false, false, nil
	}

	token := GenerateToken()
	m.locks[key] = memLock{
		token:       token,
		fingerprint: cloneBytes(fingerprint),
		expiresAt:   now.Add(ttl),
	}
	return token, false, true, nil
}

// Unlock releases the processing lock if the caller's token still owns it.
// Best-effort cleanup: token mismatch is silently ignored (returns nil).
func (m *MemoryStore) Unlock(_ context.Context, key, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.locks[key]; ok && l.token == token {
		delete(m.locks, key)
	}
	return nil
}

func cloneResponse(resp CachedResponse) *CachedResponse {
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

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}
