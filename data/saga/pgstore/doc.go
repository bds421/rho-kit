// Package pgstore is the Postgres-backed [saga.StateStore] backend.
//
// Schema: a single saga_instances table holds one row per instance
// with the state machine fields plus JSONB columns for the per-step
// outputs and the compensated-index list. Apply the migration shipped
// in `migrations/` via the kit-migrate runner.
//
// # Use this package when
//
//   - You run saga.DurableExecutor in production and want resume-on-
//     crash + multi-replica safety.
//
// # Do NOT use this package for
//
//   - Tests / single-process services — use
//     [saga.NewMemoryStateStore] instead.
//
// # Write semantics
//
// Put's first write of a fresh instance (UpdatedAt zero) uses
// INSERT … ON CONFLICT (id) DO NOTHING and never overwrites an existing
// row; a collision returns [ErrConcurrentUpdate]. Subsequent writes
// (UpdatedAt non-zero) UPDATE the row in place by ID, overwriting its
// mutable columns to match the "writes (or overwrites)" contract of
// saga.StateStore.Put. The update does not gate on updated_at because
// DurableExecutor reads an instance once and then Puts repeatedly
// without re-reading, leaving its in-memory UpdatedAt stale after the
// first write. A vanished row (concurrent Delete) returns
// [ErrConcurrentUpdate].
package pgstore
