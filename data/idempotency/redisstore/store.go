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

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/data/idempotency"
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
var _ idempotency.Store = (*RedisStore)(nil)

// Option configures the RedisStore.
type Option func(*RedisStore)

// WithKeyPrefix sets the key prefix for all stored entries.
// Default: "idempotency:".
func WithKeyPrefix(prefix string) Option {
	return func(s *RedisStore) { s.prefix = prefix }
}

// RedisStore implements idempotency.Store using Redis. Safe for concurrent
// use across processes.
type RedisStore struct {
	client goredis.UniversalClient
	prefix string
}

// New creates a RedisStore backed by the given Redis client. Panics if
// client is nil — a miswired store would otherwise dereference nil on
// first use.
func New(client goredis.UniversalClient, opts ...Option) *RedisStore {
	if client == nil {
		panic("redisstore: New requires a non-nil Redis client")
	}
	s := &RedisStore{
		client: client,
		prefix: "idempotency:",
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *RedisStore) k(key string) string { return s.prefix + key }

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
	Marker      string                     `json:"_m"` // "resp" — distinguishes from a lock value
	Fingerprint []byte                     `json:"fp,omitempty"`
	Response    idempotency.CachedResponse `json:"resp"`
}

const lockMarker = "lock:"
const respMarker = "resp"

// encodeLockValue produces the value stored under the key while a lock is
// held. Format: "lock:" + token + ":" + base64(fingerprint).
func encodeLockValue(token string, fingerprint []byte) string {
	return lockMarker + token + ":" + base64.RawStdEncoding.EncodeToString(fingerprint)
}

// decodeLockValue parses a lock value. Returns ok=false if the value isn't a
// lock (e.g. it's the response envelope JSON).
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
	fp, err := base64.RawStdEncoding.DecodeString(rest[idx+1:])
	if err != nil {
		return "", nil, false
	}
	return token, fp, true
}

// Get returns a cached response and applies fingerprint comparison if a
// non-nil fingerprint is supplied.
func (s *RedisStore) Get(ctx context.Context, key string, fingerprint []byte) (*idempotency.CachedResponse, bool, error) {
	data, err := s.client.Get(ctx, s.k(key)).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("idempotencystore: get %q: %w", key, err)
	}
	// Distinguish between a lock value (in-flight) and a response envelope.
	if strings.HasPrefix(string(data), lockMarker) {
		// Lock present; fingerprint check still applies so a Get-only caller
		// can detect mismatched-body reuse in the contended state.
		_, fp, ok := decodeLockValue(string(data))
		if ok && fingerprint != nil && len(fp) > 0 && !bytes.Equal(fp, fingerprint) {
			return nil, true, nil
		}
		return nil, false, nil
	}

	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, false, fmt.Errorf("idempotencystore: unmarshal %q: %w", key, err)
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
	return &resp, false, nil
}

// TryLock implements the contract from [idempotency.Store.TryLock]. Returns
// [idempotency.ErrInvalidTTL] when ttl <= 0 — Redis SET NX with EX 0 would
// otherwise create a permanent lock.
func (s *RedisStore) TryLock(ctx context.Context, key string, fingerprint []byte, ttl time.Duration) (string, bool, bool, error) {
	if ttl <= 0 {
		return "", false, false, idempotency.ErrInvalidTTL
	}
	token := idempotency.GenerateToken()
	value := encodeLockValue(token, fingerprint)

	ok, err := s.client.SetNX(ctx, s.k(key), value, ttlRoundUp(ttl)).Result()
	if err != nil {
		return "", false, false, fmt.Errorf("idempotencystore: lock %q: %w", key, err)
	}
	if ok {
		return token, false, true, nil
	}

	// SETNX failed — inspect the existing value to distinguish "same
	// fingerprint, contended" from "different fingerprint, conflict".
	existing, err := s.client.Get(ctx, s.k(key)).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			// Race: TTL expired between SETNX and GET. Caller will retry.
			return "", false, false, nil
		}
		return "", false, false, fmt.Errorf("idempotencystore: inspect %q: %w", key, err)
	}

	// Existing slot is a lock — compare fingerprints from the lock value.
	if strings.HasPrefix(string(existing), lockMarker) {
		_, fp, ok := decodeLockValue(string(existing))
		if !ok {
			return "", false, false, nil
		}
		if fingerprint != nil && len(fp) > 0 && !bytes.Equal(fp, fingerprint) {
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
func (s *RedisStore) Set(ctx context.Context, key, token string, resp idempotency.CachedResponse, ttl time.Duration) error {
	if ttl <= 0 {
		return idempotency.ErrInvalidTTL
	}
	// We need the same fingerprint that was passed at TryLock time so the
	// envelope embeds it. Recover it by reading the lock value back.
	existing, err := s.client.Get(ctx, s.k(key)).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return idempotency.ErrLockLost
		}
		return fmt.Errorf("idempotencystore: read lock %q: %w", key, err)
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
		return fmt.Errorf("idempotencystore: marshal %q: %w", key, err)
	}
	expectedLockValue := encodeLockValue(token, fp)
	result, err := setIfLockedScript.Run(ctx, s.client,
		[]string{s.k(key)},
		expectedLockValue,
		payload,
		ttlMillisRoundUp(ttl),
	).Text()
	if err != nil {
		return fmt.Errorf("idempotencystore: set %q: %w", key, err)
	}
	if result != "OK" {
		return idempotency.ErrLockLost
	}
	return nil
}

// Unlock releases the processing lock under the caller's token. Token
// mismatch is silently ignored (best-effort cleanup, e.g. on handler panic).
func (s *RedisStore) Unlock(ctx context.Context, key, token string) error {
	// We don't know the fingerprint here — but the lock value encoding is
	// "lock:token:b64fp", so we read once to recover the original value.
	existing, err := s.client.Get(ctx, s.k(key)).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil
		}
		return fmt.Errorf("idempotencystore: read lock %q: %w", key, err)
	}
	curToken, fp, ok := decodeLockValue(string(existing))
	if !ok || curToken != token {
		// Either it's already a response envelope (Set ran), or someone
		// else holds the lock now. Either way, nothing for us to do.
		return nil
	}
	expectedLockValue := encodeLockValue(token, fp)
	if _, err := unlockIfOwnerScript.Run(ctx, s.client, []string{s.k(key)}, expectedLockValue).Result(); err != nil && !errors.Is(err, goredis.Nil) {
		return fmt.Errorf("idempotencystore: unlock %q: %w", key, err)
	}
	return nil
}
