// Package redisstore provides a Redis-backed implementation of the
// idempotency.Store interface for multi-instance deployments.
//
// Each lock is a Redis string whose value encodes both the owner token and
// the request fingerprint, separated by ':'. SET NX gives us atomic acquire;
// Lua scripts handle size-guarded Get and compare-then-replace for Set/
// Unlock so each operation is a single round trip and the caller's token
// is required to mutate or release the slot.
//
// Cached responses are stored as a JSON envelope under the same key, with
// the fingerprint embedded so Get can detect "same key, different body"
// reuse. Body-mismatch is surfaced via the fingerprintMismatch return value
// on Get/TryLock (per the [idempotency.Store] contract).
// [idempotency.ErrLockLost] is reserved for token-mismatch on Set/Unlock.
package redisstore

import (
	"bytes"
	"context"
	"crypto/subtle"
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

// getCappedScript returns the key value in one RTT with a server-side
// size guard (STRLEN before GET) so a hostile oversized value is never
// transferred. Returns false (redis nil) when missing, the string "TOO_LARGE"
// when over the cap, or the value bytes otherwise.
//
//	KEYS[1] = key
//	ARGV[1] = max bytes
var getCappedScript = goredis.NewScript(`
local n = redis.call("STRLEN", KEYS[1])
if n > tonumber(ARGV[1]) then
	return "TOO_LARGE"
end
return redis.call("GET", KEYS[1])
`)

// setIfTokenScript atomically replaces a lock with the cached response
// envelope iff the lock's owner token matches. Size-checks and parses
// the lock value server-side so Set is one RTT (no pre-GET).
//
// Fingerprint for the envelope is recovered from the lock encoding and
// injected into the JSON payload (ARGV[2] is the CachedResponse JSON only).
//
// Returns "OK", "LOST", or "TOO_LARGE".
//
//	KEYS[1] = key
//	ARGV[1] = token
//	ARGV[2] = response JSON (CachedResponse)
//	ARGV[3] = TTL in milliseconds
//	ARGV[4] = max stored bytes
var setIfTokenScript = goredis.NewScript(`
local cur = redis.call("GET", KEYS[1])
if cur == false then
	return "LOST"
end
if #cur > tonumber(ARGV[4]) then
	return "TOO_LARGE"
end
if string.sub(cur, 1, 5) ~= "lock:" then
	return "LOST"
end
local rest = string.sub(cur, 6)
local colon = string.find(rest, ":", 1, true)
if not colon then
	return "LOST"
end
local tok = string.sub(rest, 1, colon - 1)
if tok ~= ARGV[1] then
	return "LOST"
end
local fp_enc = string.sub(rest, colon + 1)
local fp_json
if fp_enc == "" then
	fp_json = "null"
elseif fp_enc == "=" then
	fp_json = '""'
else
	-- lock stores RawStd base64 after '='; JSON []byte needs StdEncoding (padded).
	local raw = string.sub(fp_enc, 2)
	local pad = (4 - (#raw % 4)) % 4
	fp_json = '"' .. raw .. string.rep("=", pad) .. '"'
end
local payload = '{"_m":"resp","fp":' .. fp_json .. ',"resp":' .. ARGV[2] .. '}'
if #payload > tonumber(ARGV[4]) then
	return "TOO_LARGE"
end
redis.call("SET", KEYS[1], payload, "PX", tonumber(ARGV[3]))
return "OK"
`)

// unlockIfTokenScript deletes the lock iff the owner token matches.
// Size-checks and parses server-side so Unlock is one RTT.
//
//	KEYS[1] = key
//	ARGV[1] = token
//	ARGV[2] = max stored bytes
var unlockIfTokenScript = goredis.NewScript(`
local cur = redis.call("GET", KEYS[1])
if cur == false then
	return 0
end
if #cur > tonumber(ARGV[2]) then
	return 0
end
if string.sub(cur, 1, 5) ~= "lock:" then
	return 0
end
local rest = string.sub(cur, 6)
local colon = string.find(rest, ":", 1, true)
if not colon then
	return 0
end
local tok = string.sub(rest, 1, colon - 1)
if tok ~= ARGV[1] then
	return 0
end
return redis.call("DEL", KEYS[1])
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
	// Enforce a trailing separator so prefix+key is injective across
	// store instances (prefix "idem:svcA" + key ":x" must not collide
	// with prefix "idem:svc" + key "A:x").
	if !strings.HasSuffix(prefix, ":") {
		prefix += ":"
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
	return idempotency.ValidateStorageKey(key)
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
// this envelope by setIfTokenScript.
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
// The cap is enforced server-side in getCappedScript (STRLEN before GET
// in one RTT) so a hostile or legacy writer that stored a multi-MB value
// under the same key prefix cannot force this process to transfer the
// full response body. A post-read length check still runs as defence in depth.
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
	raw, err := getCappedScript.Run(ctx, s.client, []string{redisKey}, maxStoredEntryBytes).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, false, nil
		}
		if translated := translateUnavailable(err); translated != err {
			return nil, false, translated
		}
		return nil, false, redact.WrapError("idempotencystore: get", err)
	}
	if raw == nil {
		return nil, false, nil
	}
	var data []byte
	switch v := raw.(type) {
	case string:
		if v == "TOO_LARGE" {
			return nil, false, fmt.Errorf("idempotencystore: stored entry exceeds %d bytes", maxStoredEntryBytes)
		}
		data = []byte(v)
	case []byte:
		data = v
	default:
		return nil, false, nil
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
		// Foreign/legacy non-JSON under the prefix: treat as miss (same as
		// unrecognised marker), not a hard error that poisons every Get
		// until TTL expiry. Matches doTryLock's inspect path.
		return nil, false, nil
	}
	if env.Marker != respMarker {
		// Unrecognised payload — treat as miss to avoid silently replaying
		// arbitrary bytes.
		return nil, false, nil
	}
	// Fail closed when caller supplies a fingerprint but the envelope has none.
	if fingerprint != nil && (env.Fingerprint == nil || !bytes.Equal(env.Fingerprint, fingerprint)) {
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
		return "", false, false, redact.WrapError("idempotencystore: inspect", err)
	}
	if len(existing) > maxStoredEntryBytes {
		return "", false, false, fmt.Errorf("idempotencystore: stored entry exceeds %d bytes", maxStoredEntryBytes)
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
	// One RTT: Lua size-checks the lock, verifies token ownership, recovers
	// the fingerprint from the lock encoding, and writes the envelope.
	respJSON, err := json.Marshal(resp)
	if err != nil {
		return redact.WrapError("idempotencystore: marshal cached response", err)
	}
	result, err := setIfTokenScript.Run(ctx, s.client,
		[]string{s.k(key)},
		token,
		respJSON,
		ttlMillisRoundUp(ttl),
		maxStoredEntryBytes,
	).Text()
	if err != nil {
		if translated := translateUnavailable(err); translated != err {
			return translated
		}
		return redact.WrapError("idempotencystore: set", err)
	}
	switch result {
	case "OK":
		return nil
	case "TOO_LARGE":
		return fmt.Errorf("idempotencystore: stored entry exceeds %d bytes", maxStoredEntryBytes)
	default:
		return idempotency.ErrLockLost
	}
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
	// One RTT: Lua size-checks, parses lock:token:fp, and DELs on token match.
	// Token mismatch / missing key / already-a-response are all no-ops.
	if _, err := unlockIfTokenScript.Run(ctx, s.client, []string{s.k(key)}, token, maxStoredEntryBytes).Result(); err != nil && !errors.Is(err, goredis.Nil) {
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

// tokenEqual compares owner tokens in constant time when lengths match.
func tokenEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
