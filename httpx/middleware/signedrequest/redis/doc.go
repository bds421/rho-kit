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
// Keys are written as `signedrequest:nonce:<nonce>`. Nonces in the
// kit are 16 random bytes base64-encoded, so collisions across
// callers are statistically impossible — there is no need to
// partition by key id.
//
// # Failure mode
//
// A Redis outage surfaces as an error from SeenOrStore, which the
// middleware translates into a 500 response. The package does NOT
// silently fail open — admitting a possibly-replayed request is
// strictly worse than rejecting a possibly-fresh one for audit and
// idempotency contracts that depend on signed-request guarantees.
package redis
