package ratelimit

import (
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"
)

// MaxKeyBytes caps keyed rate-limit keys before they reach the LRU. Longer
// values are almost always request material rather than a stable principal,
// and can turn one request into a large memory allocation.
const MaxKeyBytes = 256

// ErrInvalidKey is returned when a keyed rate-limit key is empty, oversized,
// invalid UTF-8, or contains bytes that corrupt logs and backend protocols.
var ErrInvalidKey = errors.New("ratelimit: invalid key")

// ErrInvalidLimiter is returned by error-returning methods on a nil or
// otherwise uninitialized keyed limiter.
var ErrInvalidLimiter = errors.New("ratelimit: limiter is not initialized")

// ValidateKey checks that a keyed rate-limit key is safe to store and use as
// a cache key. Use the same validator in custom middleware before calling
// lower-level keyed limiter methods.
func ValidateKey(key string) error {
	if key == "" {
		return ErrInvalidKey
	}
	if len(key) > MaxKeyBytes {
		return fmt.Errorf("%w: key exceeds maximum length", ErrInvalidKey)
	}
	if containsInvalidKeyRune(key) {
		return ErrInvalidKey
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
