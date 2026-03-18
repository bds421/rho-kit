// Package cache defines a generic cache interface and in-memory implementation.
//
// The MemoryCache uses Ristretto with TTL support and returns copies of values
// to keep callers from mutating cached data. It is suitable as a local L1 cache
// or for tests; production multi-instance caching should use Redis.
package cache
