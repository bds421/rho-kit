// Package queue defines portable interfaces for FIFO job queues with
// reliable delivery: [Message], [Handler], [Publisher], and [Consumer].
//
// # Implementations
//
//   - [github.com/bds421/rho-kit/data/queue/riverqueue/v2] implements
//     [Publisher] (Postgres-backed durable enqueue). Consume is owned by
//     River's worker runtime rather than [Consumer].
//   - [github.com/bds421/rho-kit/data/queue/redisqueue/v2] is a Redis
//     LIST-based queue with its own Message/Handler types and API; it
//     does not implement [Publisher] or [Consumer] directly. Callers
//     that need Redis LIST semantics should import redisqueue, not code
//     against these interfaces expecting a drop-in backend.
//
// [Consumer] is exported for external adapters. No in-tree backend
// currently implements it; prefer the concrete backend's consume path
// until an adapter ships.
package queue
