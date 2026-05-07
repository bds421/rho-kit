// Package memory is a thread-safe in-process [actionlog.Store]. Use it
// for unit tests and small dev environments; for production reach for
// data/actionlog/postgres.
//
// The store keeps entries in a slice ordered by insertion plus a map
// from id to slice index for O(1) Get. List walks the slice in reverse
// (newest-first) and applies filters in-process. This is fine for the
// test-scale data volumes the store is sized for; postgres is the
// answer when the row count climbs.
package memory
