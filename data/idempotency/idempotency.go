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
	"fmt"
	"io"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// ErrLockLost indicates the caller no longer holds the processing lock for a
// key — typically because the lock TTL expired and another caller acquired
// it before this caller's Set/Unlock ran. Backends return this so the
// middleware can avoid clobbering a fresher response.
var ErrLockLost = errors.New("idempotency: caller no longer holds the lock")

// ErrInvalidTTL is returned by [Store.TryLock] and [Store.Set] when the TTL
// is non-positive. The three backends previously disagreed dangerously about
// TTL=0: Redis SET NX with EX 0 creates a permanent lock, MemoryStore treats
// it as immediately expired, and pgstore rounds sub-second durations to 0.
// Returning a typed error from every backend means direct callers (bypassing
// the middleware) get a deterministic failure instead of one of three silent
// failure modes.
var ErrInvalidTTL = errors.New("idempotency: ttl must be positive")

// ErrInvalidStore is returned when a Store method is invoked on a nil or
// otherwise uninitialized store implementation.
var ErrInvalidStore = errors.New("idempotency: store is not initialized")

// ErrInvalidCachedResponse marks a response that cannot be safely stored and
// replayed by idempotency backends.
var ErrInvalidCachedResponse = errors.New("idempotency: invalid cached response")

// ErrKeyEmpty is returned when an idempotency key is empty.
var ErrKeyEmpty = errors.New("idempotency: key must not be empty")

// ErrKeyTooLong is returned when an idempotency key exceeds MaxKeyLen bytes.
var ErrKeyTooLong = errors.New("idempotency: key exceeds maximum length")

// ErrKeyInvalidChars is returned when an idempotency key contains bytes that
// can corrupt logs, UTF-8 sinks, or backend protocol framing.
var ErrKeyInvalidChars = errors.New("idempotency: key contains invalid characters")

// MaxKeyLen bounds raw idempotency keys accepted by Store implementations.
// HTTP middleware hashes client-supplied keys before storage; this cap protects
// direct Store callers and custom integrations.
const MaxKeyLen = 256

var tokenRandReader io.Reader = rand.Reader

// ValidateKey checks that key is safe for all Store backends.
func ValidateKey(key string) error {
	if key == "" {
		return ErrKeyEmpty
	}
	if len(key) > MaxKeyLen {
		return ErrKeyTooLong
	}
	if containsInvalidKeyRune(key) {
		return ErrKeyInvalidChars
	}
	return nil
}

func containsInvalidKeyRune(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

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
// passes a fingerprint by default for unsafe methods (POST/PUT/PATCH);
// direct callers must opt in to the safety.
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
	//
	// ttl MUST be positive; backends return [ErrInvalidTTL] for ttl <= 0
	// instead of silently disagreeing about the meaning of zero (Redis would
	// otherwise create a permanent lock, MemoryStore would treat it as
	// instantly expired, pgstore would round to zero seconds).
	TryLock(ctx context.Context, key string, fingerprint []byte, ttl time.Duration) (token string, fingerprintMismatch bool, ok bool, err error)

	// Set stores the response, atomically replacing the lock row. The token
	// must be the one returned from the TryLock that started this critical
	// section. Returns [ErrLockLost] if the caller's token no longer matches
	// the current lock owner — a sign the TTL expired mid-handler and another
	// caller has already taken the slot. Returns [ErrInvalidTTL] for ttl <= 0.
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

const (
	// MaxCachedBodyBytes matches the HTTP middleware's capture limit. Direct
	// Store callers get the same safe default instead of persisting unbounded
	// response bodies into Redis, Postgres, or memory.
	MaxCachedBodyBytes = 1 << 20

	// MaxCachedHeaders bounds the number of distinct replayed response headers.
	MaxCachedHeaders = 64

	// MaxCachedHeaderValues bounds repeated values for a single response header.
	MaxCachedHeaderValues = 64

	// MaxCachedHeaderNameBytes caps each response header field name.
	MaxCachedHeaderNameBytes = 128

	// MaxCachedHeaderValueBytes caps each response header value.
	MaxCachedHeaderValueBytes = 8 * 1024
)

// ValidateCachedResponse checks that resp can be safely stored and replayed as
// an HTTP response. Backends call this on Set and Get so direct Store callers
// and corrupted backend rows fail closed instead of replaying invalid status
// codes, header names, header values, or unbounded bodies.
func ValidateCachedResponse(resp CachedResponse) error {
	if resp.StatusCode < 100 || resp.StatusCode > 999 {
		return fmt.Errorf("%w: status code must be between 100 and 999", ErrInvalidCachedResponse)
	}
	if len(resp.Body) > MaxCachedBodyBytes {
		return fmt.Errorf("%w: body exceeds maximum length", ErrInvalidCachedResponse)
	}
	if len(resp.Headers) > MaxCachedHeaders {
		return fmt.Errorf("%w: header count exceeds maximum", ErrInvalidCachedResponse)
	}
	for name, values := range resp.Headers {
		if err := validateCachedHeaderName(name); err != nil {
			return err
		}
		if len(values) > MaxCachedHeaderValues {
			return fmt.Errorf("%w: header value count exceeds maximum", ErrInvalidCachedResponse)
		}
		for _, value := range values {
			if err := validateCachedHeaderValue(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCachedHeaderName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: header name must not be empty", ErrInvalidCachedResponse)
	}
	if len(name) > MaxCachedHeaderNameBytes {
		return fmt.Errorf("%w: header name exceeds maximum length", ErrInvalidCachedResponse)
	}
	for i := 0; i < len(name); i++ {
		if !isCachedHeaderNameByte(name[i]) {
			return fmt.Errorf("%w: header name contains invalid character", ErrInvalidCachedResponse)
		}
	}
	return nil
}

func validateCachedHeaderValue(value string) error {
	if len(value) > MaxCachedHeaderValueBytes {
		return fmt.Errorf("%w: header value exceeds maximum length", ErrInvalidCachedResponse)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: header value contains invalid UTF-8", ErrInvalidCachedResponse)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: header value contains control character", ErrInvalidCachedResponse)
		}
	}
	return nil
}

func isCachedHeaderNameByte(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z':
		return true
	case 'A' <= c && c <= 'Z':
		return true
	case '0' <= c && c <= '9':
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

// GenerateToken returns a 32-character hex-encoded random token. Backends use
// this for the owner-token of an acquired lock; the middleware does not
// inspect tokens itself — it just round-trips them between TryLock and
// Set/Unlock.
func GenerateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(tokenRandReader, b); err != nil {
		return "", fmt.Errorf("idempotency: generate lock token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// memoryStoreMaxEntries caps the in-memory store before lazy eviction runs.
// Prevents unbounded memory growth in long-running tests or misuse outside
// of test environments.
const memoryStoreMaxEntries = 10_000

// MemoryStore is an in-memory Store for testing. Not suitable for production
// (no cross-process sharing).
type MemoryStore struct {
	mu      sync.RWMutex
	items   map[string]memEntry
	locks   map[string]memLock
	clock   func() time.Time
	runMu   sync.Mutex
	started bool

	setCount uint64
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
	return m
}

func (m *MemoryStore) now() time.Time { return m.clock() }

// Get returns a cached response for the key, applying fingerprint comparison
// if a non-nil fingerprint is supplied.
func (m *MemoryStore) Get(_ context.Context, key string, fingerprint []byte) (*CachedResponse, bool, error) {
	if err := m.ready(); err != nil {
		return nil, false, err
	}
	if err := ValidateKey(key); err != nil {
		return nil, false, err
	}
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
	if err := ValidateCachedResponse(entry.resp); err != nil {
		return nil, false, err
	}
	return cloneResponse(entry.resp), false, nil
}

// evictInterval controls how often Set() scans for expired entries.
const evictInterval = 100

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
func (m *MemoryStore) Set(_ context.Context, key, token string, resp CachedResponse, ttl time.Duration) error {
	if err := m.ready(); err != nil {
		return err
	}
	if err := ValidateKey(key); err != nil {
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
		m.sweepExpiredLocked(evictBudget)
	}

	m.items[key] = memEntry{
		resp:        copyResponseForStorage(resp),
		fingerprint: cloneBytes(m.locks[key].fingerprint),
		expiresAt:   m.now().Add(ttl),
	}
	delete(m.locks, key)
	return nil
}

// TryLock implements the contract from [Store.TryLock]. Returns
// [ErrInvalidTTL] when ttl <= 0.
func (m *MemoryStore) TryLock(_ context.Context, key string, fingerprint []byte, ttl time.Duration) (string, bool, bool, error) {
	if err := m.ready(); err != nil {
		return "", false, false, err
	}
	if err := ValidateKey(key); err != nil {
		return "", false, false, err
	}
	if ttl <= 0 {
		return "", false, false, ErrInvalidTTL
	}
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
func (m *MemoryStore) Unlock(_ context.Context, key, token string) error {
	if err := m.ready(); err != nil {
		return err
	}
	if err := ValidateKey(key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.locks[key]; ok && l.token == token {
		delete(m.locks, key)
	}
	return nil
}

// sweepExpiredLocked deletes up to budget expired entries (items + locks).
// Caller MUST hold m.mu.Lock(). budget <= 0 means unbounded — used only by
// tests; production callers should pass [evictBudget].
func (m *MemoryStore) sweepExpiredLocked(budget int) {
	now := m.now()
	scanned := 0
	for k, entry := range m.items {
		if budget > 0 && scanned >= budget {
			return
		}
		scanned++
		if now.After(entry.expiresAt) {
			delete(m.items, k)
		}
	}
	for k, l := range m.locks {
		if budget > 0 && scanned >= budget {
			return
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
