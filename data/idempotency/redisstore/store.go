// Package redisstore provides a Redis-backed implementation of the
// idempotency.Store interface for multi-instance deployments.
//
// Each lock is a Redis string whose value encodes both the owner token and
// the request fingerprint, separated by ':'. SET NX gives us atomic acquire;
// a Lua script handles compare-then-replace for both Set and Unlock so the
// caller's token is required to mutate or release the slot.
//
// Cached responses are stored as a JSON envelope under the same key, with
// the fingerprint embedded so Get can detect "same key, different body"
// reuse and return [idempotency.ErrLockLost] is reserved for token-mismatch
// on Set; the Get / TryLock paths surface body-mismatch via the
// `fingerprintMismatch` return value per the [idempotency.Store] contract.
package redisstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/data/v2/idempotency"
)

// setIfLockedScript atomically replaces the lock value with the cached
// response envelope iff the caller still owns the lock.
//
// Returns "OK" on success, "LOST" on token mismatch (caller must surface
// idempotency.ErrLockLost).
//
//	KEYS[1] = key
//	ARGV[1] = expected lock value (token:fingerprintB64)
//	ARGV[2] = new payload (envelope JSON)
//	ARGV[3] = TTL in milliseconds
var setIfLockedScript = goredis.NewScript(`
local cur = redis.call("GET", KEYS[1])
if cur == false or cur ~= ARGV[1] then
	return "LOST"
end
redis.call("SET", KEYS[1], ARGV[2], "PX", tonumber(ARGV[3]))
return "OK"
`)

// unlockIfOwnerScript atomically deletes the lock iff the caller still owns
// it. Returns 1 on successful DEL, 0 on token mismatch — the wrapper treats
// both as success because Unlock is best-effort cleanup.
//
//	KEYS[1] = key
//	ARGV[1] = expected lock value (token:fingerprintB64)
var unlockIfOwnerScript = goredis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`)

// Compile-time interface check.
var _ idempotency.Store = (*Store)(nil)

// Option configures the Store.
type Option func(*Store)

// WithKeyPrefix sets the key prefix for all stored entries.
// Default: "idempotency:".
//
// Panics on empty, invalid, or >maxKeyPrefixLen prefixes (audit FR-031) so a
// misconfiguration cannot inflate or corrupt every Redis key.
func WithKeyPrefix(prefix string) Option {
	if prefix == "" {
		panic("redisstore: WithKeyPrefix requires a non-empty prefix")
	}
	if len(prefix) > maxKeyPrefixLen {
		panic("redisstore: WithKeyPrefix prefix exceeds maximum length")
	}
	if containsInvalidStringBytes(prefix) {
		panic("redisstore: WithKeyPrefix prefix contains invalid characters")
	}
	return func(s *Store) { s.prefix = prefix }
}

// maxKeyPrefixLen caps Redis key prefixes so direct
// callers and middleware-bypassing harnesses cannot create
// pathologically long keys (audit FR-031). The middleware itself
// hashes idempotency keys to a fixed-length hex string, so production
// traffic stays well below these limits.
const maxKeyPrefixLen = 128

// Store implements idempotency.Store using Redis. Safe for concurrent
// use across processes.
type Store struct {
	client goredis.UniversalClient
	prefix string
}

// New creates a Store backed by the given Redis client. Panics if
// client is nil — a miswired store would otherwise dereference nil on
// first use.
func New(client goredis.UniversalClient, opts ...Option) *Store {
	if client == nil {
		panic("redisstore: New requires a non-nil Redis client")
	}
	s := &Store{
		client: client,
		prefix: "idempotency:",
	}
	for _, o := range opts {
		if o == nil {
			panic("redisstore: New option must not be nil")
		}
		o(s)
	}
	return s
}

func (s *Store) k(key string) string { return s.prefix + key }

// validateKey preserves the local helper name used throughout this backend
// while delegating to the Store-wide key contract.
func validateKey(key string) error {
	return idempotency.ValidateKey(key)
}

// ttlMillisRoundUp converts a duration to milliseconds, rounding sub-ms
// values up to 1ms. Redis PX accepts integer milliseconds; without rounding,
// a 500µs TTL would arrive as 0 and Redis would reject the SET.
func ttlMillisRoundUp(d time.Duration) int64 {
	ms := d / time.Millisecond
	if d%time.Millisecond != 0 {
		ms++
	}
	if ms < 1 {
		ms = 1
	}
	return int64(ms)
}

// ttlRoundUp returns a duration whose millisecond representation is at
// least 1ms. The redis client's SET path multiplies its ttl argument back
// to milliseconds, so the same rounding rule applies here as in
// [ttlMillisRoundUp].
func ttlRoundUp(d time.Duration) time.Duration {
	return time.Duration(ttlMillisRoundUp(d)) * time.Millisecond
}

// envelope is the JSON payload stored under a key once a response has been
// cached. The leading lock value (token:fingerprint) is overwritten with
// this envelope by setIfLockedScript.
type envelope struct {
	Marker string `json:"_m"` // "resp" — distinguishes from a lock value
	// Fingerprint is NOT omitempty: an empty-but-present fingerprint ([]byte{})
	// must round-trip distinctly from an absent one (nil) so mismatch detection
	// matches the SQL bytea (non-NULL empty) semantics. omitempty would collapse
	// both to absent, letting a different request body replay a key claimed with
	// an empty fingerprint.
	Fingerprint []byte                     `json:"fp"`
	Response    idempotency.CachedResponse `json:"resp"`
}

const lockMarker = "lock:"
const respMarker = "resp"

// maxStoredEntryBytes caps the size of a stored idempotency entry the
// Store will accept on read. Set-side bounds via [Store.Set]
// (which calls [idempotency.ValidateCachedResponse]) reject oversized
// writes; this is the read-side defence so a foreign
// writer (a legacy app sharing the Redis instance, a misuse, or an
// attacker with key-write but not key-read access elsewhere) cannot
// OOM the host by ballooning an existing entry. 8 MiB is comfortably
// above any realistic cached response while a hard stop short of swap.
const maxStoredEntryBytes = 8 * 1024 * 1024

// encodeLockValue produces the value stored under the key while a lock is
// held. Format: "lock:" + token + ":" + fp, where fp is "" for an ABSENT
// (nil) fingerprint and "=" + base64(fingerprint) for a PRESENT one (the
// leading "=" — not a RawStdEncoding alphabet char — marks presence so an
// empty-but-present fingerprint stays distinct from a nil one, matching the
// SQL store's NULL-vs-empty semantics).
func encodeLockValue(token string, fingerprint []byte) string {
	if fingerprint == nil {
		return lockMarker + token + ":"
	}
	return lockMarker + token + ":=" + base64.RawStdEncoding.EncodeToString(fingerprint)
}

// decodeLockValue parses a lock value. Returns ok=false if the value isn't a
// lock (e.g. it's the response envelope JSON). A nil fingerprint means none
// was set; a non-nil (possibly empty) fingerprint means one was.
func decodeLockValue(v string) (token string, fingerprint []byte, ok bool) {
	if !strings.HasPrefix(v, lockMarker) {
		return "", nil, false
	}
	rest := v[len(lockMarker):]
	idx := strings.IndexByte(rest, ':')
	if idx < 0 {
		return "", nil, false
	}
	token = rest[:idx]
	enc := rest[idx+1:]
	if enc == "" {
		return token, nil, true // absent fingerprint
	}
	if enc[0] == '=' {
		enc = enc[1:] // present marker; "" -> empty-but-present
	}
	fp, err := base64.RawStdEncoding.DecodeString(enc)
	if err != nil {
		return "", nil, false
	}
	if fp == nil {
		fp = []byte{} // present but empty; keep distinct from absent
	}
	return token, fp, true
}

// Get returns a cached response and applies fingerprint comparison if a
// non-nil fingerprint is supplied.
//
// The cap is enforced via STRLEN before GET so a hostile or legacy writer
// that stored a multi-MB value under the same key prefix cannot force this
// process to allocate the full response body before the cap runs. A
// post-GET length check still runs to catch the rare TOCTOU window where
// the value is replaced between STRLEN and GET.
func (s *Store) Get(ctx context.Context, key string, fingerprint []byte) (*idempotency.CachedResponse, bool, error) {
	ctx, span := s.startSpan(ctx, "idempotency.Get")
	defer span.End()
	cached, ok, err := s.doGet(ctx, key, fingerprint)
	recordResult(span, err)
	return cached, ok, err
}

func (s *Store) doGet(ctx context.Context, key string, fingerprint []byte) (*idempotency.CachedResponse, bool, error) {
	if err := s.ready(); err != nil {
		return nil, false, err
	}
	if err := validateKey(key); err != nil {
		return nil, false, err
	}
	redisKey := s.k(key)
	sz, err := s.client.StrLen(ctx, redisKey).Result()
	if err != nil {
		if translated := translateUnavailable(err); translated != err {
			return nil, false, translated
		}
		return nil, false, redact.WrapError("idempotencystore: get strlen", err)
	}
	if sz == 0 {
		// Distinguishing "missing" from "empty stored value" is left to
		// the GET below — Redis returns redis.Nil for missing keys.
	} else if sz > maxStoredEntryBytes {
		return nil, false, fmt.Errorf("idempotencystore: stored entry exceeds %d bytes", maxStoredEntryBytes)
	}
	data, err := s.client.Get(ctx, redisKey).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, false, nil
		}
		if translated := translateUnavailable(err); translated != err {
			return nil, false, translated
		}
		return nil, false, redact.WrapError("idempotencystore: get", err)
	}
	if len(data) > maxStoredEntryBytes {
		return nil, false, fmt.Errorf("idempotencystore: stored entry exceeds %d bytes", maxStoredEntryBytes)
	}
	// Distinguish between a lock value (in-flight) and a response envelope.
	if strings.HasPrefix(string(data), lockMarker) {
		// Lock present; fingerprint check still applies so a Get-only caller
		// can detect mismatched-body reuse in the contended state.
		_, fp, ok := decodeLockValue(string(data))
		if ok && fingerprint != nil && fp != nil && !bytes.Equal(fp, fingerprint) {
			return nil, true, nil
		}
		return nil, false, nil
	}

	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, false, redact.WrapError("idempotencystore: unmarshal cached response", err)
	}
	if env.Marker != respMarker {
		// Unrecognised payload — treat as miss to avoid silently replaying
		// arbitrary bytes.
		return nil, false, nil
	}
	if fingerprint != nil && env.Fingerprint != nil && !bytes.Equal(env.Fingerprint, fingerprint) {
		return nil, true, nil
	}
	resp := env.Response
	if err := idempotency.ValidateCachedResponse(resp); err != nil {
		return nil, false, redact.WrapError("idempotencystore: invalid cached response", err)
	}
	return &resp, false, nil
}

// TryLock implements the contract from [idempotency.Store.TryLock]. Returns
// [idempotency.ErrInvalidTTL] when ttl <= 0 — Redis SET NX with EX 0 would
// otherwise create a permanent lock.
func (s *Store) TryLock(ctx context.Context, key string, fingerprint []byte, ttl time.Duration) (string, bool, bool, error) {
	ctx, span := s.startSpan(ctx, "idempotency.TryLock")
	defer span.End()
	token, fingerprintMismatch, ok, err := s.doTryLock(ctx, key, fingerprint, ttl)
	recordResult(span, err)
	return token, fingerprintMismatch, ok, err
}

func (s *Store) doTryLock(ctx context.Context, key string, fingerprint []byte, ttl time.Duration) (string, bool, bool, error) {
	if err := s.ready(); err != nil {
		return "", false, false, err
	}
	if err := validateKey(key); err != nil {
		return "", false, false, err
	}
	if ttl <= 0 {
		return "", false, false, idempotency.ErrInvalidTTL
	}
	token, err := idempotency.GenerateToken()
	if err != nil {
		return "", false, false, err
	}
	value := encodeLockValue(token, fingerprint)

	ok, err := s.client.SetNX(ctx, s.k(key), value, ttlRoundUp(ttl)).Result()
	if err != nil {
		if translated := translateUnavailable(err); translated != err {
			return "", false, false, translated
		}
		return "", false, false, redact.WrapError("idempotencystore: lock", err)
	}
	if ok {
		return token, false, true, nil
	}

	// SETNX failed — inspect the existing value to distinguish "same
	// fingerprint, contended" from "different fingerprint, conflict".
	// Same STRLEN-before-GET guard as [Store.Get] so a poisoned slot
	// cannot OOM the lock-inspection path.
	redisKey := s.k(key)
	if sz, slErr := s.client.StrLen(ctx, redisKey).Result(); slErr != nil {
		if translated := translateUnavailable(slErr); translated != slErr {
			return "", false, false, translated
		}
		return "", false, false, redact.WrapError("idempotencystore: inspect strlen", slErr)
	} else if sz > maxStoredEntryBytes {
		return "", false, false, fmt.Errorf("idempotencystore: stored entry exceeds %d bytes", maxStoredEntryBytes)
	}
	existing, err := s.client.Get(ctx, redisKey).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			// Race: TTL expired between SETNX and GET. Caller will retry.
			return "", false, false, nil
		}
		if translated := translateUnavailable(err); translated != err {
			return "", false, false, translated
		}
	}
	if len(existing) > maxStoredEntryBytes {
		return "", false, false, fmt.Errorf("idempotencystore: stored entry exceeds %d bytes", maxStoredEntryBytes)
	}
	if err != nil {
		return "", false, false, redact.WrapError("idempotencystore: inspect", err)
	}

	// Existing slot is a lock — compare fingerprints from the lock value.
	if strings.HasPrefix(string(existing), lockMarker) {
		_, fp, ok := decodeLockValue(string(existing))
		if !ok {
			return "", false, false, nil
		}
		if fingerprint != nil && fp != nil && !bytes.Equal(fp, fingerprint) {
			return "", true, false, nil
		}
		return "", false, false, nil
	}

	// Existing slot is a cached response — compare fingerprints from the
	// envelope.
	var env envelope
	if err := json.Unmarshal(existing, &env); err != nil {
		return "", false, false, nil
	}
	if fingerprint != nil && env.Fingerprint != nil && !bytes.Equal(env.Fingerprint, fingerprint) {
		return "", true, false, nil
	}
	return "", false, false, nil
}

// Set replaces the lock value with the response envelope, atomically
// requiring that the caller still holds the lock. Returns
// [idempotency.ErrInvalidTTL] when ttl <= 0.
func (s *Store) Set(ctx context.Context, key, token string, resp idempotency.CachedResponse, ttl time.Duration) error {
	ctx, span := s.startSpan(ctx, "idempotency.Set")
	defer span.End()
	err := s.doSet(ctx, key, token, resp, ttl)
	recordResult(span, err)
	return err
}

func (s *Store) doSet(ctx context.Context, key, token string, resp idempotency.CachedResponse, ttl time.Duration) error {
	if err := s.ready(); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	if ttl <= 0 {
		return idempotency.ErrInvalidTTL
	}
	if err := idempotency.ValidateCachedResponse(resp); err != nil {
		return err
	}
	// We need the same fingerprint that was passed at TryLock time so the
	// envelope embeds it. Recover it by reading the lock value back —
	// guarded by a STRLEN pre-check so a hostile writer cannot force
	// us to allocate a multi-MB blob before deciding the lock value is
	// not even a valid encoding. Mirrors the cap Get applies.
	if sz, err := s.client.StrLen(ctx, s.k(key)).Result(); err != nil {
		if translated := translateUnavailable(err); translated != err {
			return translated
		}
		return redact.WrapError("idempotencystore: set strlen", err)
	} else if sz > maxStoredEntryBytes {
		return fmt.Errorf("idempotencystore: stored entry exceeds %d bytes", maxStoredEntryBytes)
	}
	existing, err := s.client.Get(ctx, s.k(key)).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return idempotency.ErrLockLost
		}
		if translated := translateUnavailable(err); translated != err {
			return translated
		}
		return redact.WrapError("idempotencystore: read lock", err)
	}
	if len(existing) > maxStoredEntryBytes {
		return fmt.Errorf("idempotencystore: stored entry exceeds %d bytes", maxStoredEntryBytes)
	}
	curToken, fp, ok := decodeLockValue(string(existing))
	if !ok || curToken != token {
		return idempotency.ErrLockLost
	}

	env := envelope{
		Marker:      respMarker,
		Fingerprint: fp,
		Response:    resp,
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return redact.WrapError("idempotencystore: marshal cached response", err)
	}
	expectedLockValue := encodeLockValue(token, fp)
	result, err := setIfLockedScript.Run(ctx, s.client,
		[]string{s.k(key)},
		expectedLockValue,
		payload,
		ttlMillisRoundUp(ttl),
	).Text()
	if err != nil {
		if translated := translateUnavailable(err); translated != err {
			return translated
		}
		return redact.WrapError("idempotencystore: set", err)
	}
	if result != "OK" {
		return idempotency.ErrLockLost
	}
	return nil
}

// Unlock releases the processing lock under the caller's token. Token
// mismatch is silently ignored (best-effort cleanup, e.g. on handler panic).
func (s *Store) Unlock(ctx context.Context, key, token string) error {
	ctx, span := s.startSpan(ctx, "idempotency.Unlock")
	defer span.End()
	err := s.doUnlock(ctx, key, token)
	recordResult(span, err)
	return err
}

func (s *Store) doUnlock(ctx context.Context, key, token string) error {
	if err := s.ready(); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	// We don't know the fingerprint here — but the lock value encoding is
	// "lock:token:b64fp", so we read once to recover the original value.
	// STRLEN gate mirrors Get/Set so a hostile writer cannot force a
	// multi-MB allocation under the same key prefix.
	if sz, err := s.client.StrLen(ctx, s.k(key)).Result(); err != nil {
		if translated := translateUnavailable(err); translated != err {
			return translated
		}
		return redact.WrapError("idempotencystore: unlock strlen", err)
	} else if sz > maxStoredEntryBytes {
		return fmt.Errorf("idempotencystore: stored entry exceeds %d bytes", maxStoredEntryBytes)
	}
	existing, err := s.client.Get(ctx, s.k(key)).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil
		}
		if translated := translateUnavailable(err); translated != err {
			return translated
		}
		return redact.WrapError("idempotencystore: read lock", err)
	}
	if len(existing) > maxStoredEntryBytes {
		return fmt.Errorf("idempotencystore: stored entry exceeds %d bytes", maxStoredEntryBytes)
	}
	curToken, fp, ok := decodeLockValue(string(existing))
	if !ok || curToken != token {
		// Either it's already a response envelope (Set ran), or someone
		// else holds the lock now. Either way, nothing for us to do.
		return nil
	}
	expectedLockValue := encodeLockValue(token, fp)
	if _, err := unlockIfOwnerScript.Run(ctx, s.client, []string{s.k(key)}, expectedLockValue).Result(); err != nil && !errors.Is(err, goredis.Nil) {
		if translated := translateUnavailable(err); translated != err {
			return translated
		}
		return redact.WrapError("idempotencystore: unlock", err)
	}
	return nil
}

func (s *Store) ready() error {
	if s == nil || s.client == nil || s.prefix == "" || len(s.prefix) > maxKeyPrefixLen || containsInvalidStringBytes(s.prefix) {
		return idempotency.ErrInvalidStore
	}
	return nil
}

func containsInvalidStringBytes(s string) bool {
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
