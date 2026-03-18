// Package idempotency defines the Store interface and types for idempotent
// request handling. The HTTP middleware implementation lives in
// [middleware/idempotency], and a Redis-backed store is in
// [redis/idempotencystore].
package idempotency

import (
	"context"
	"sync"
	"time"
)

// Store persists and retrieves cached responses keyed by idempotency key.
type Store interface {
	// Get returns a cached response for the key, or (nil, nil) if not found.
	Get(ctx context.Context, key string) (*CachedResponse, error)
	// Set stores a response for the key with the given TTL.
	Set(ctx context.Context, key string, resp CachedResponse, ttl time.Duration) error
	// TryLock attempts to acquire a processing lock for the key with the given TTL.
	// Returns true if the lock was acquired (caller should process the request),
	// or false if the key is already being processed by another request.
	TryLock(ctx context.Context, key string, ttl time.Duration) (bool, error)
	// Unlock releases the processing lock for the key.
	Unlock(ctx context.Context, key string) error
}

// CachedResponse stores the HTTP response data for replay.
type CachedResponse struct {
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers"`
	Body       []byte              `json:"body"`
}

// memoryStoreMaxEntries is the maximum number of entries in the MemoryStore
// before lazy eviction runs. Prevents unbounded memory growth in long-running
// tests or misuse outside of test environments.
const memoryStoreMaxEntries = 10_000

// MemoryStore is an in-memory Store for testing. Not suitable for production
// (no cross-process sharing).
type MemoryStore struct {
	mu       sync.RWMutex
	items    map[string]memEntry
	locks    map[string]time.Time // key → lock expiry time
	setCount uint64               // calls to Set(); drives periodic eviction
	clock    func() time.Time     // injectable for deterministic testing
}

// MemoryStoreOption configures a MemoryStore.
type MemoryStoreOption func(*MemoryStore)

// WithMemoryStoreClock sets the time source. Useful for deterministic testing
// without time.Sleep.
func WithMemoryStoreClock(fn func() time.Time) MemoryStoreOption {
	return func(m *MemoryStore) { m.clock = fn }
}

type memEntry struct {
	resp      CachedResponse
	expiresAt time.Time
}

// NewMemoryStore creates a new in-memory idempotency store.
func NewMemoryStore(opts ...MemoryStoreOption) *MemoryStore {
	m := &MemoryStore{
		items: make(map[string]memEntry),
		locks: make(map[string]time.Time),
		clock: time.Now,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

func (m *MemoryStore) now() time.Time {
	return m.clock()
}

// Get returns a cached response for the key, or (nil, nil) if not found.
// Expired entries are cleaned up lazily on read to prevent unbounded
// accumulation under read-heavy, write-light workloads.
func (m *MemoryStore) Get(_ context.Context, key string) (*CachedResponse, error) {
	m.mu.RLock()
	entry, ok := m.items[key]
	if !ok {
		m.mu.RUnlock()
		return nil, nil
	}
	if m.now().After(entry.expiresAt) {
		m.mu.RUnlock()
		m.mu.Lock()
		if e, still := m.items[key]; still && m.now().After(e.expiresAt) {
			delete(m.items, key)
		}
		m.mu.Unlock()
		return nil, nil
	}
	defer m.mu.RUnlock()
	resp := entry.resp
	cp := CachedResponse{
		StatusCode: resp.StatusCode,
		Headers:    make(map[string][]string, len(resp.Headers)),
		Body:       make([]byte, len(resp.Body)),
	}
	for k, vals := range resp.Headers {
		vcp := make([]string, len(vals))
		copy(vcp, vals)
		cp.Headers[k] = vcp
	}
	copy(cp.Body, resp.Body)
	return &cp, nil
}

// evictInterval controls how often Set() scans for expired entries.
// This ensures low-traffic stores don't accumulate unbounded expired entries.
const evictInterval = 100

// Set stores a response for the key with TTL enforcement.
// Expired entries are lazily evicted every evictInterval calls or when the
// store exceeds memoryStoreMaxEntries.
func (m *MemoryStore) Set(_ context.Context, key string, resp CachedResponse, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setCount++
	// Evict expired entries periodically or when the store grows too large.
	if len(m.items) >= memoryStoreMaxEntries || m.setCount%evictInterval == 0 {
		now := m.now()
		for k, entry := range m.items {
			if now.After(entry.expiresAt) {
				delete(m.items, k)
			}
		}
		// Also clean expired lock entries to prevent unbounded growth
		// from orphaned locks (e.g., crashed request handlers).
		for k, expiry := range m.locks {
			if now.After(expiry) {
				delete(m.locks, k)
			}
		}
	}
	cp := CachedResponse{
		StatusCode: resp.StatusCode,
		Headers:    make(map[string][]string, len(resp.Headers)),
		Body:       make([]byte, len(resp.Body)),
	}
	for k, vals := range resp.Headers {
		vcp := make([]string, len(vals))
		copy(vcp, vals)
		cp.Headers[k] = vcp
	}
	copy(cp.Body, resp.Body)
	m.items[key] = memEntry{
		resp:      cp,
		expiresAt: m.now().Add(ttl),
	}
	return nil
}

// TryLock attempts to acquire a processing lock for the key.
// Returns true if the lock was acquired, false if already locked.
// Expired locks are automatically reclaimed.
func (m *MemoryStore) TryLock(_ context.Context, key string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if expiry, locked := m.locks[key]; locked && m.now().Before(expiry) {
		return false, nil
	}
	m.locks[key] = m.now().Add(ttl)
	return true, nil
}

// Unlock releases the processing lock for the key.
func (m *MemoryStore) Unlock(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.locks, key)
	return nil
}
