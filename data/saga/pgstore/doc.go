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
// # Replica safety
//
// Put uses an optimistic-concurrency check on the row's updated_at
// column so two replicas calling DurableExecutor.Run for the same
// instance never both advance CurrentStep. The losing replica's Put
// returns [ErrConcurrentUpdate]; the executor's loop treats it as a
// retryable signal and re-reads the instance state.
package pgstore
