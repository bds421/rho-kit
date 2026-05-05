# NEW: data/ratelimit (sliding window / GCRA / token bucket)

**Phase**: 5 (Tier‑2 infrastructure)
**Module path**: `github.com/bds421/rho-kit/data/ratelimit`

## Why

The current `httpx/middleware/ratelimit` is fixed-window only — bursty at window edges, no smoothing. Production deployments often need a smoother shape (token bucket / GCRA) and need it shared across instances (Redis-backed).

Splitting the limiter primitive out of the HTTP middleware lets non-HTTP consumers (gRPC interceptors, message-consumer dispatch limits, outbound-call quotas) reuse it.

## Public API

```go
package ratelimit

// Limiter is the algorithm-agnostic interface.
type Limiter interface {
    // Allow returns (allowed, retryAfter, err).
    Allow(ctx context.Context, key string) (bool, time.Duration, error)
}
```

### Subpackages

```
data/ratelimit/tokenbucket   -- in-memory token bucket (fast, single instance)
data/ratelimit/gcra          -- in-memory GCRA (smooth)
data/ratelimit/redis         -- Redis-backed GCRA (cross-instance), via Lua script
```

The `redis` subpackage uses the standard "GCRA in Redis" Lua script (single round trip, atomic). It conforms to the same `Limiter` interface so the middleware can swap implementations.

### HTTP middleware integration

```go
// httpx/middleware/ratelimit gains a constructor that accepts a Limiter:
func MiddlewareWithLimiter(l ratelimit.Limiter, keyFunc func(*http.Request) string) func(http.Handler) http.Handler
```

The existing fixed-window limiter stays as a default for simple cases; consumers needing cross-instance limits switch to the Redis-backed Limiter.

## Definition of done

- [ ] Top-level `Limiter` interface.
- [ ] In-memory token bucket + GCRA implementations.
- [ ] Redis-backed GCRA via Lua.
- [ ] HTTP middleware accepts any `Limiter`.
- [ ] Tests: round-trip; cross-process limit-sharing via test Redis container.
- [ ] Recipe entry in `docs/ai/http.md`.
