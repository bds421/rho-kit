// Package idempotency defines the Store interface and types for idempotent
// request handling. The HTTP middleware implementation lives in
// [httpx/middleware/idempotency], with backend-specific stores in
// [data/idempotency/redisstore] and [data/idempotency/pgstore]
// (also importable as [redisstore] / [pgstore] when those packages are
// already in scope).
//
// asvs: V11.1.2
package idempotency
