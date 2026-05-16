// Package idempotencytest provides a Run function that exercises
// every contract a [github.com/bds421/rho-kit/data/v2/idempotency.Store]
// implementation must honour. Use it from your backend's test
// package to assert that the implementation behaves identically
// to the kit's MemoryStore, pgstore, and redisstore — the four
// implementations diverged dangerously in earlier kit versions
// (TTL=0 created a permanent lock in Redis, an instantly-
// expired entry in memory, and a zero-rounded entry in pg) and
// this harness exists to make sure new backends don't reintroduce
// that class of bug.
//
// # Usage
//
// In your backend's test file:
//
//	package mybackend_test
//
//	import (
//	    "testing"
//
//	    "github.com/bds421/rho-kit/data/v2/idempotency"
//	    "github.com/bds421/rho-kit/data/v2/idempotency/idempotencytest"
//	    "github.com/example/mybackend"
//	)
//
//	func TestConformance(t *testing.T) {
//	    idempotencytest.Run(t, func(t *testing.T) idempotency.Store {
//	        return mybackend.New(/* ... */)
//	    })
//	}
//
// The factory is invoked once per subtest with a fresh
// implementation so per-test state doesn't bleed between cases.
//
// # What the suite covers
//
//   - Empty / oversized key rejection.
//   - TTL <= 0 rejection on both TryLock and Set.
//   - Lock acquire / Set / Get round-trip with no fingerprint.
//   - Fingerprint match path (lock acquired, response written, Get
//     returns the cached response).
//   - Fingerprint MISMATCH path on Get (returns true/422 shape).
//   - Fingerprint MISMATCH path on TryLock (returns true/422 shape).
//   - Concurrent TryLock with same fingerprint (loser sees ok=false).
//   - Unlock with a stale token is a no-op (NOT ErrLockLost).
//   - Set after another caller stole the lock returns ErrLockLost.
//   - Cached response fields round-trip exactly (StatusCode,
//     Headers, Body — including a multi-value header).
//
// # What the suite does NOT cover
//
//   - DeleteExpired (pgstore-only — exercised by pgstore's own
//     integration tests).
//   - TTL-driven expiration over wall-clock time (would slow the
//     suite to seconds-per-test; the contract is "expired entries
//     behave like missing entries" — backends prove it via their
//     own clock-injected tests).
//   - Backend-specific tracing / metrics — those are observed at
//     a different layer.
package idempotencytest
