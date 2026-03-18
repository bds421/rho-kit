// Package ratelimit provides HTTP rate limiting middleware.
//
// Two rate limiter types are available:
//
//   - [RateLimiter] — IP-based fixed-window rate limiting using sharded LRU caches.
//   - [KeyedRateLimiter] — arbitrary-key fixed-window rate limiting, suitable for
//     API keys, user IDs, or any caller-defined key function.
//
// Both types require a cleanup goroutine started via their Run method.
package ratelimit
