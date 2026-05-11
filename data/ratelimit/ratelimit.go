// Package ratelimit defines the algorithm-agnostic [Limiter] interface
// used across the kit's rate-limit primitives.
//
// Implementations live in subpackages so consumers only depend on what
// they need:
//
//   - data/ratelimit/tokenbucket — in-memory token bucket. Fast,
//     single-instance, allows bursts up to the bucket capacity.
//   - data/ratelimit/gcra — in-memory GCRA (Generic Cell Rate
//     Algorithm). Smooth — no edge bursts. Same interface, different
//     trade-offs.
//   - data/ratelimit/redis — Redis-backed GCRA via Lua, atomic in a
//     single round trip. Use when limits must be enforced across
//     replicas.
//
// All implementations satisfy [Limiter] so the HTTP middleware,
// gRPC interceptors, and message-consumer guards can swap algorithms
// without rewiring the call sites.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxKeyLen caps raw limiter keys across all backends. Redis can accept much
// larger keys, but long dynamic keys waste memory and usually mean the caller
// is using request-specific data instead of a stable tenant/user/route scope.
const MaxKeyLen = 256

// ErrInvalidKey is returned by limiters when key is empty, oversized, invalid
// UTF-8, or contains control bytes that corrupt logs or backend protocol
// framing. An invalid key collapses or explodes buckets in ways that are
// almost certainly bugs rather than intent.
var ErrInvalidKey = errors.New("ratelimit: key is invalid")

// ErrInvalidLimiter is returned when a limiter method is invoked on a
// nil or otherwise uninitialized limiter implementation.
var ErrInvalidLimiter = errors.New("ratelimit: limiter is not initialized")

// Limiter decides whether an event identified by key should be allowed.
type Limiter interface {
	// Allow reports whether the event is allowed at the current time.
	//
	// Returns:
	//   - allowed=true: the event is allowed; retryAfter is 0.
	//   - allowed=false: the event is throttled; retryAfter is the
	//     suggested wait before retrying. Zero retryAfter means "the
	//     limiter has no opinion" (e.g. token-bucket implementations
	//     that only know "denied").
	//   - err: backend or argument error. allowed must be false.
	Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration, err error)
}

// ValidateKey checks that a rate-limit key is safe for all limiter backends.
func ValidateKey(key string) error {
	if key == "" {
		return ErrInvalidKey
	}
	if len(key) > MaxKeyLen {
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
