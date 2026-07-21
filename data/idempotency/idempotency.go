package idempotency

import (
	"crypto/rand"
	"errors"
	"io"
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
