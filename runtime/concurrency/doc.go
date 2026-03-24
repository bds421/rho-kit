// Package concurrency provides structured concurrency primitives for request
// handlers. It complements [golang.org/x/sync/errgroup] with higher-level
// fan-out helpers that handle panic recovery, bounded parallelism, and
// ordered result collection.
//
// Two flavours are provided:
//
//   - [FanOut] runs N functions concurrently and returns all results in
//     submission order. The first error cancels remaining goroutines
//     (fail-fast, like Promise.all).
//
//   - [FanOutSettled] runs N functions concurrently and collects every
//     result regardless of individual errors (like Promise.allSettled).
//
// Both functions recover panics in each goroutine and convert them to errors.
// Use [WithMaxGoroutines] to bound parallelism.
package concurrency
