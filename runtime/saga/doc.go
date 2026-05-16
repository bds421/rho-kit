// Package saga defines a minimal compensable-workflow primitive:
// a [Definition] is a sequence of [Step]s, each with a forward
// action and a compensation. When a forward action fails midway
// through, the executor runs the compensations of the already-
// completed steps in reverse order so the system rolls back to a
// consistent state without manual cleanup.
//
// Wave 150 ships the in-memory executor + the Definition / Step
// types. The kit's intended production wiring is:
//
//   - Each forward action is enqueued via redisqueue (asynq) so
//     it survives a process restart;
//   - Compensations are emitted via the outbox so they ride the
//     same transactional guarantees as ordinary domain events;
//   - State (which step is current, which compensations have
//     run) lives in a kit DB table that the executor reads to
//     resume after a crash.
//
// Those persistence pieces are deliberately not in this commit —
// the in-memory executor + the type vocabulary are the
// foundational seam every higher-level implementation has to
// agree on. Future waves layer the queue/outbox/table wiring on
// top of [Run].
//
// # When to use this
//
//   - A workflow has more than one external side effect (debit
//     wallet → reserve inventory → send email) and any of them
//     might fail. Without sagas the kit's repository pattern
//     leaves a partial commit behind on failure.
//   - The compensating action is well-defined (refund the debit,
//     un-reserve the inventory) and a kit-grade workflow
//     primitive saves every service from re-inventing rollback.
//
// # When NOT to use this
//
//   - The operations are all within one database — use a single
//     transaction.
//   - There is no meaningful compensation (you cannot un-send
//     an email; design the workflow to be idempotent and at-
//     least-once instead).
package saga
