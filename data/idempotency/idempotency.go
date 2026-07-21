package idempotency

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strings"
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

// ErrKeyReservedPrefix is returned when a caller-supplied key uses a prefix
// reserved for kit-internal storage forms (tenant-scoped keys). Rejecting
// these at [ValidateKey] prevents a bare [Store] sharing a backend keyspace
// with a tenant-wrapped store from accepting forgeable length-prefixed
// tenant keys (review-12 / v3).
var ErrKeyReservedPrefix = errors.New("idempotency: key uses a reserved prefix")

// MaxKeyLen bounds raw idempotency keys accepted by Store implementations.
// HTTP middleware hashes client-supplied keys before storage; this cap protects
// direct Store callers and custom integrations.
const MaxKeyLen = 256

// TenantStorageKeyPrefix is the reserved prefix for storage keys produced by
// the tenant wrapper ([github.com/bds421/rho-kit/data/v2/idempotency/tenant]).
// User-supplied keys carrying this prefix are rejected by [ValidateKey].
// Storage keys look like "tns:" + 64 lowercase hex digits (SHA-256 digest).
const TenantStorageKeyPrefix = "tns:"

// reservedUserKeyPrefixes are rejected by [ValidateKey] so bare stores cannot
// accept keys that collide with kit-produced tenant storage forms.
//
//   - "tenant:" — length-prefixed form from [core/tenant.KeyFor]
//   - "tns:"    — opaque tenant-wrapper storage keys ([TenantStorageKeyPrefix])
var reservedUserKeyPrefixes = []string{
	// kit-doctor:allow tenant-key-prefix reason="this literal is rejected by validation; it never constructs a storage key"
	"tenant:",
	TenantStorageKeyPrefix,
}

// tenantStorageKeyHexLen is the hex-encoded SHA-256 length used by the tenant
// wrapper for storage keys (always 64 lowercase hex digits after "tns:").
const tenantStorageKeyHexLen = 64 // sha256.Size * 2

var tokenRandReader io.Reader = rand.Reader

// ValidateKey checks that key is safe for all Store backends as a *user*
// key (middleware Idempotency-Key, direct Store callers). It rejects empty
// keys, overlong keys, control/space runes, and reserved prefixes used by
// kit-internal storage forms ("tenant:", "tns:").
//
// Backends that receive keys already rewritten by the tenant wrapper must
// call [ValidateStorageKey] instead so the opaque "tns:"+hex form is
// accepted while still rejecting the forgeable "tenant:" length-prefixed
// shape.
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
	if hasReservedUserKeyPrefix(key) {
		return ErrKeyReservedPrefix
	}
	return nil
}

// ValidateStorageKey is the key contract for Store backend implementations.
// It accepts:
//
//   - any key that passes [ValidateKey] (ordinary user keys), and
//   - well-formed tenant-wrapper storage keys ("tns:" + 64 lowercase hex).
//
// The forgeable length-prefixed "tenant:…" form is always rejected so a bare
// store cannot be tricked into addressing another tenant's slot even when it
// shares a backend keyspace with a tenant-wrapped store.
func ValidateStorageKey(key string) error {
	if isTenantStorageKey(key) {
		return nil
	}
	return ValidateKey(key)
}

// FormatTenantStorageKey returns the canonical opaque storage key for a
// tenant-scoped form: "tns:" + lowercase hex(SHA-256(scoped)). The input is
// typically the length-prefixed string from [core/tenant.KeyFor]; callers
// must not pass user keys here without first scoping them.
//
// Exported for the tenant wrapper and tests; application code should use
// the tenant wrapper rather than constructing storage keys by hand.
func FormatTenantStorageKey(digest []byte) string {
	return TenantStorageKeyPrefix + hex.EncodeToString(digest)
}

func hasReservedUserKeyPrefix(key string) bool {
	for _, p := range reservedUserKeyPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// isTenantStorageKey reports whether key is a well-formed opaque tenant
// storage key produced by the tenant wrapper.
func isTenantStorageKey(key string) bool {
	if !strings.HasPrefix(key, TenantStorageKeyPrefix) {
		return false
	}
	hexPart := key[len(TenantStorageKeyPrefix):]
	if len(hexPart) != tenantStorageKeyHexLen {
		return false
	}
	for i := 0; i < len(hexPart); i++ {
		c := hexPart[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	// Total length is fixed and well under MaxKeyLen; no further checks.
	return true
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
