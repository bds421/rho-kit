# AGENTS.md — `runtime/saga`

## When to use this package

- Multi-step workflow where each step has a compensating action and ALL steps must either succeed or compensate ("saga pattern").
- Steps are short-lived and run in-process (this is NOT a workflow orchestrator like Temporal).
- The kit's existing primitives (outbox, idempotency, action audit) already wire the durability story; saga adds the compensable-step structure.

## When to use something else

- **Long-running workflows that span processes / restarts:** Temporal, Cadence, or a custom outbox-driven state machine. The kit's saga is in-process only.
- **Single transactional unit (no compensation needed):** use the database transaction directly via `infra/sqldb/pgx`.
- **Distributed two-phase commit:** the saga pattern is the better choice (2PC has known availability issues); but the kit cannot drive 2PC for you.

## Key APIs

- `Step` — a struct with `Name`, `Forward(ctx, state)`, and `Compensate(ctx, state)` (Compensate may be nil for non-compensable steps).
- `NewDefinition(steps ...Step) (*Definition, error)` — validates the ordered step list (non-empty, every step has a `Name` and a `Forward`). `MustDefinition(...)` is the panicking variant for package-level vars.
- `Run(ctx, def, state) error` — package-level function (not a method). Runs forward steps in order; on failure runs `Compensate` for every step that succeeded, in reverse order. `state` is the shared value handed to every step's `Forward`/`Compensate`.

## Common mistakes

- **`Forward` panics in the middle of a step** — the kit's panic recovery wraps the step but the partial state may already exist in the downstream. `Compensate` must be idempotent and handle "the forward action partially completed" correctly.
- **`Compensate` that depends on `Forward`'s return value (not stored in state)** — the kit passes only `ctx` and the shared `state` value; `Forward`'s return error is not threaded into `Compensate`. Save anything you need to compensate into the `state` object.
- **Treating saga as a queue** — it's an in-process composition. For "fire and forget compensable steps across instances", use outbox + messaging + a state-machine approach.

## Observability

- No built-in tracing or metrics yet — wave 145 added panic-recovery, but observability spans are a v2.x follow-up.
