// Package concurrency provides structured concurrency primitives for request
// handlers. It complements [golang.org/x/sync/errgroup] with higher-level
// fan-out helpers that handle panic recovery, bounded parallelism, and
// ordered result collection.
//
// Two flavours are provided:
//
//   - [FanOut] runs N functions concurrently and returns all results in
//     submission order. The first error cancels remaining goroutines
//     (fail-fast, like Promise.all). Only the first error is returned;
//     errors from other concurrently-running goroutines are discarded.
//
//   - [FanOutSettled] runs N functions concurrently and collects every
//     result regardless of individual errors (like Promise.allSettled).
//     Each function receives the original parent context; no derived
//     cancellation context is created.
//
// Both functions recover panics in each goroutine and convert them to errors.
// If the panic value implements the error interface, it is available via
// errors.Is / errors.As through PanicError.Unwrap.
// Use [WithMaxGoroutines] to bound parallelism.
package concurrency
