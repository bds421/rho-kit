# AGENTS.md — `runtime/saga`

## When to use this package

- Multi-step workflow where each step has a compensating action and ALL steps must either succeed or compensate ("saga pattern").
- Steps execute in-process; `DurableExecutor` persists run/step state so
  incomplete work can resume after a process restart.
- The kit's existing primitives (outbox, idempotency, action audit) already wire the durability story; saga adds the compensable-step structure.

## When to use something else

- **Human-scale or externally scheduled workflows:** Temporal, Cadence, or a
  dedicated workflow engine. The kit resumes durable saga state but does not
  provide timers, signals, a workflow UI, or remote activity workers.
- **Single transactional unit (no compensation needed):** use the database transaction directly via `infra/sqldb/pgx`.
- **Distributed two-phase commit:** the saga pattern is the better choice (2PC has known availability issues); but the kit cannot drive 2PC for you.

## Key APIs

- `Step` — a struct with `Name`, `Forward(ctx, state)`, and `Compensate(ctx, state)` (Compensate may be nil for non-compensable steps).
- `NewDefinition(steps ...Step) (*Definition, error)` — validates the ordered step list (non-empty, every step has a `Name` and a `Forward`). `MustDefinition(...)` is the panicking variant for package-level vars.
- `Run(ctx, def, state) error` — package-level function (not a method). Runs forward steps in order; on failure runs `Compensate` for every step that succeeded, in reverse order. `state` is the shared value handed to every step's `Forward`/`Compensate`.
- `NewDurableExecutor(store, opts...)` — crash-recoverable execution backed
  by a `StateStore`; production uses `data/saga/pgstore` and calls `Resume`.

## Common mistakes

- **`Forward` panics in the middle of a step** — the kit's panic recovery wraps the step but the partial state may already exist in the downstream. `Compensate` must be idempotent and handle "the forward action partially completed" correctly.
- **`Compensate` that depends on `Forward`'s return value (not stored in state)** — the kit passes only `ctx` and the shared `state` value; `Forward`'s return error is not threaded into `Compensate`. Save anything you need to compensate into the `state` object.
- **Treating saga as a queue** — execution still happens in the calling
  process. Use outbox + messaging for durable cross-service dispatch.

## Observability

- The saga package emits structured errors but no built-in Prometheus metrics
  or tracing spans; instrument step functions at the service boundary.
