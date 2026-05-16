// Package locktest provides a Run function that exercises every
// contract a [github.com/bds421/rho-kit/data/v2/lock.Locker]
// implementation must honour. Use it from your backend's test
// package to assert that a new Locker implementation behaves
// identically to the kit's pgadvisory, redislock, and redlock
// adapters.
//
// # Usage
//
//	package mybackend_test
//
//	import (
//	    "testing"
//
//	    "github.com/bds421/rho-kit/data/v2/lock"
//	    "github.com/bds421/rho-kit/data/v2/lock/locktest"
//	    "github.com/example/mybackend"
//	)
//
//	func TestConformance(t *testing.T) {
//	    locktest.Run(t, func(t *testing.T) lock.Locker {
//	        return mybackend.New(/* ... */)
//	    })
//	}
//
// # What the suite covers
//
//   - First Acquire on a key succeeds.
//   - Second Acquire on a still-held key returns (nil, false, nil).
//   - Release of the active holder succeeds; a subsequent Acquire
//     of the same key succeeds (fresh handle).
//   - Release-after-Release returns ErrLockLost.
//   - Extend on a held lock returns (true, nil).
//   - Extend on a released lock returns (false, nil) — NOT an error.
//   - Releasing one lock does not affect a parallel different-keyed lock.
//   - Concurrent Acquire on the same key: exactly one winner.
//
// # What the suite does NOT cover
//
//   - TTL-driven expiration (would slow the suite to seconds-per-
//     test). Per-backend integration tests inject a clock or
//     short TTLs to exercise this.
//   - Cross-process / cross-replica behavior (the harness runs
//     in one process). Each backend's own integration tests
//     prove its multi-replica properties (redlock-quorum,
//     pg session pinning, etc.).
package locktest
