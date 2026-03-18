// Package idempotency provides HTTP middleware for request deduplication.
//
// Requests are identified by the Idempotency-Key header. When a duplicate key
// is detected, the previously cached response is replayed without re-executing
// the handler. A processing lock prevents concurrent duplicate execution.
//
// The middleware requires a [Store] implementation for persistence. Use
// [NewMemoryStore] for testing and [redis/redisstore.New] for production
// multi-instance deployments.
package idempotency
