// Package idempotency provides HTTP middleware for request deduplication.
//
// Requests are identified by the Idempotency-Key header. When a duplicate key
// is detected, the previously cached response is replayed without re-executing
// the handler. A processing lock prevents concurrent duplicate execution.
//
// The middleware requires a [github.com/bds421/rho-kit/data/v2/idempotency.Store]
// implementation for persistence. Use
// [github.com/bds421/rho-kit/data/v2/idempotency.NewMemoryStore] for single-
// instance testing and
// [github.com/bds421/rho-kit/data/idempotency/redisstore/v2.New] or
// [github.com/bds421/rho-kit/data/idempotency/pgstore/v2.New] for production
// multi-instance deployments. Never use an in-memory store behind a multi-
// replica service.
//
// asvs: V11.1.2
package idempotency
