// Package ratelimit provides HTTP rate limiting middleware.
//
// Two rate limiter types are available:
//
//   - [RateLimiter] — IP-based fixed-window rate limiting using sharded LRU caches.
//   - [KeyedRateLimiter] — arbitrary-key fixed-window rate limiting, suitable for
//     API keys, user IDs, or any caller-defined key function.
//
// Both types require a cleanup goroutine started via their Run method.
//
// # Degradation Support
//
// Both rate limiter types support graceful degradation via [WithDegradation]
// and [WithKeyedDegradation]. When configured with a [HealthIndicator] and
// [DegradationHandler], the middleware checks dependency health before
// enforcing rate limits. This is designed to work with [redis.Connection]
// and [redis.DegradationPolicy] without importing infra/redis directly.
package ratelimit
