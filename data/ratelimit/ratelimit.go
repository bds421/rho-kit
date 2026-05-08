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
	"time"
)

// ErrInvalidKey is returned by limiters when key is empty.
// (An empty key collapses every caller into a single bucket — almost
// certainly a bug rather than the intent.)
var ErrInvalidKey = errors.New("ratelimit: key must not be empty")

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
