// Package cachetest provides a Run function that exercises every
// contract a [github.com/bds421/rho-kit/data/v2/cache.Cache]
// implementation must honour. Use it from your backend's test
// package to assert that a new Cache implementation behaves
// identically to MemoryCache and rediscache.
//
// # Usage
//
//	package mybackend_test
//
//	import (
//	    "testing"
//
//	    "github.com/bds421/rho-kit/data/v2/cache"
//	    "github.com/bds421/rho-kit/data/v2/cache/cachetest"
//	    "github.com/example/mybackend"
//	)
//
//	func TestConformance(t *testing.T) {
//	    cachetest.Run(t, func(t *testing.T) cache.Cache {
//	        return mybackend.New(/* ... */)
//	    })
//	}
//
// # What the suite covers
//
//   - Set/Get round-trip preserves bytes exactly.
//   - Get on a missing key returns ErrCacheMiss.
//   - Delete is idempotent (no error on missing key).
//   - Set overwrites an existing key with new bytes.
//   - Exists returns true / false correctly.
//   - Empty key is rejected on every method.
//   - Concurrent Set/Get/Delete leaves the cache in a consistent
//     state.
//
// # What the suite does NOT cover
//
//   - TTL-driven expiration (would slow the suite). Backends
//     exercise this with clock-injected unit tests.
//   - BulkCache (MGet/MSet/SetNX) — a separate sub-suite when
//     enough backends implement them.
package cachetest
