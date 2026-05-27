// Package saga ships two complementary compensable-workflow primitives:
//
//   - The in-memory [Run] executor — a [Definition] is a sequence of
//     [Step]s, each with a forward action and an optional compensation.
//     When a forward action fails midway, the executor runs the
//     compensations of the already-completed steps in reverse order so
//     the system rolls back to a consistent state without manual
//     cleanup. Use for single-process workflows that complete within
//     one request's lifetime.
//
//   - The durable [DurableExecutor] — same compensable-workflow
//     semantics, but state is checkpointed to a [StateStore] at every
//     step boundary so a process crash can be recovered. Forward step
//     outputs are persisted (JSON-serialised) so a Compensate running
//     after a restart sees the data Forward produced. Use for multi-
//     step workflows where each step has external side effects (debit
//     wallet → reserve inventory → send email) that must NOT be lost
//     if the process crashes mid-flight.
//
// # When to use which
//
//   - Use [Run] when the saga must complete within one request and you
//     don't need restart recovery. ~200 LOC of state machine; no DB.
//   - Use [DurableExecutor] + [data/saga/pgstore].New when the saga
//     can span a process restart, or runs on a replica that might be
//     killed by an autoscaler, or you need an audit trail of which
//     instances completed / compensated.
//
// # Backends
//
//   - [NewMemoryStateStore]                                    — in-process
//     StateStore for tests and single-process services
//   - [github.com/bds421/rho-kit/data/saga/pgstore].New        — Postgres
//     StateStore for production (resume-on-crash, multi-replica safe)
//
// # Queue / outbox wiring (optional)
//
// The DurableExecutor runs steps in-process by default. Services that
// want each forward action to ride a queue (so a crashed worker can be
// retried by another replica before the saga even reaches Resume) can
// wrap their DurableStep.Forward in a queue-enqueue + worker pattern
// using [data/queue/redisqueue] or [data/queue/riverqueue]. Similarly
// compensations can be enqueued via [infra/outbox] for at-least-once
// guarantees across broker outages.
//
// The kit does NOT bundle queue / outbox wiring into the executor
// because the right granularity is service-specific: some sagas want
// queueing per step (each step is its own background job), others
// want the entire saga in-process. The DurableExecutor's step boundary
// is the integration point — call Run() from a queue worker rather
// than from request-handling code, and the persisted state + Resume
// gives you crash recovery either way.
//
// # When NOT to use either
//
//   - The operations are all within one database — use a single
//     transaction. Sagas trade atomicity for cross-system spans.
//   - There is no meaningful compensation (you cannot un-send an
//     email; design the workflow to be idempotent and at-least-once
//     instead).
package saga
