// Package idempotency defines the Store interface and types for idempotent
// request handling. The HTTP middleware implementation lives in
// [httpx/middleware/idempotency], and a Redis-backed store is in
// [redis/idempotencystore].
package idempotency
