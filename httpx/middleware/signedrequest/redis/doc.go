// Package redis provides a Redis-backed [signedrequest.NonceStore]
// suitable for multi-replica HTTP services.
//
// The in-memory store shipped with the kit
// ([signedrequest.MemoryNonceStore]) only sees nonces handled by the
// process holding the map. A request signed once and replayed against
// a different replica is admitted because that replica's map has no
// record of the first attempt. This package closes that gap by
// keeping the seen-set in Redis: every replica reads and writes the
// same key space, so a nonce that any replica observed is rejected
// by every replica.
//
// # When to use
//
//   - Two or more replicas of the same service, each running
//     [signedrequest.Middleware] against the same audience.
//   - Any deployment where the round-trip latency to Redis (typically
//     0.5–2ms) is acceptable on the request hot path.
//
// Use [signedrequest.MemoryNonceStore] only for single-instance
// deployments where the entire signed-request audience is served by
// one process.
//
// # Mechanics
//
// SeenOrStore is a single SET NX EX call. NX makes the write
// conditional — the second replica to see the same nonce gets the
// "key already exists" path and SeenOrStore returns false. EX sets
// the TTL to the kit's configured replay window so Redis evicts
// stale nonces without a sweep job.
//
// # Key format
//
// Keys are written as `signedrequest:nonce:<nonce>`. The keyspace is
// global: nonces are NOT partitioned by key id. The kit's
// [signedrequest.NonceStore] contract only hands SeenOrStore the
// nonce, never the resolving key id, so per-key-id scoping is not
// expressible at this layer.
//
// Global scoping is safe against accidental cross-caller collisions:
// kit nonces are 16 random bytes base64-encoded, so two honest
// callers independently minting the same nonce is statistically
// impossible.
//
// It is NOT a defense against an adversarial co-tenant. Any holder of
// a different valid key who learns another caller's nonce before that
// caller's request is delivered (e.g. via a header-logging proxy or
// shared telemetry) can sign their own request with the stolen nonce
// and burn it first, causing the victim's request to be rejected as a
// replay (401). The preconditions are narrow — nonce disclosure to a
// distinct key holder ahead of delivery — but if your threat model
// includes mutually distrusting tenants behind a shared Redis, give
// each tenant its own [WithKeyPrefix] namespace so a nonce burned
// under one prefix cannot reject a fresh request under another.
//
// # Failure mode
//
// A Redis outage surfaces as an error from SeenOrStore, which the
// middleware translates into a 500 response. The package does NOT
// silently fail open — admitting a possibly-replayed request is
// strictly worse than rejecting a possibly-fresh one for audit and
// idempotency contracts that depend on signed-request guarantees.
package redis
