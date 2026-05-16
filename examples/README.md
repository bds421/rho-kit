# examples

Reference services that demonstrate canonical rho-kit v2.0.0
composition patterns. Each example is a small, self-contained
Go module under `examples/<name>/` with a `cmd/<name>/main.go`
entry point, an `internal/app` package containing the wiring,
and a smoke test that exercises the composition.

## When to look here

You are a coding agent picking the kit and you want to see a
working composition of the primitives — not a single-package
demo — before deciding which to use.

Each example focuses on ONE pattern and demonstrates the load-
bearing order of the kit packages it composes. The intent is
pedagogical: copy the wiring shape into a real service, then
swap the in-memory backends for the production equivalents the
README points at.

## Pattern catalog

| Pattern                | Status         | Composes |
|------------------------|----------------|----------|
| **agentic-service**    | Implemented    | `httpx/mcp`, `data/{actionlog,approval,budget}`, `tenant` middleware |
| **webhook-receiver**   | Implemented    | `signedrequest`, `idempotency` middleware, typed handler |
| **background-worker**  | Implemented    | `messaging.TypedSubscription`, `resilience/{retry,circuitbreaker}` |
| **api-gateway**        | Implemented    | `httpx/middleware/ratelimit`, stubbed JWT auth, `resilience/{retry,circuitbreaker}` for downstream fan-out |
| **realtime-broadcast** | Implemented    | `realtime/centrifuge`, `security/jwtutil`, `httpx` |
| saga-coordinator       | Recipe below   | `runtime/saga`, `infra/outbox`, `data/idempotency`, `data/lock` |

The first five ship as compileable modules in this directory.
The last one is documented as a composition recipe — it shows
the exact import set, the wiring order, and the rationale — so
a coding agent can stand the pattern up against a fresh service
skeleton.

## Recipe: saga-coordinator

Multi-step transactions across multiple downstream APIs with
compensation. Crash-safe via outbox.

```go
import (
    "github.com/bds421/rho-kit/runtime/v2/saga"
    "github.com/bds421/rho-kit/infra/v2/outbox"
    "github.com/bds421/rho-kit/data/v2/lock/pgadvisory"
    idem "github.com/bds421/rho-kit/data/v2/idempotency/pgstore"
)

definition := saga.Definition{
    Steps: []saga.Step{
        {Name: "reserve-inventory", Forward: reserveInv, Compensate: releaseInv},
        {Name: "charge-card",       Forward: charge,     Compensate: refund},
        {Name: "ship",              Forward: ship,       Compensate: cancelShip},
    },
}
// Idempotency around the entire saga handle so retries are safe.
// Advisory lock around the saga key so concurrent retries don't race.
// outbox.Publish enqueues the "saga started" / "step compensated"
// events crash-safely; the relay drains to messaging.
```

The kit's `runtime/saga.Run` is in-memory in v2.0.0 — durability
is provided externally by combining outbox + idempotency +
advisory lock as shown. See the `runtime/saga` package preamble
for the planned `Run` extension that absorbs this wiring directly.

## Conventions

Every example follows the same shape so a coding agent can read
one and apply the others:

- `cmd/<name>/main.go` — entry point with signal handling.
- `internal/app/app.go` — the wiring, with a load-bearing
  comment block at the top explaining the composition order.
- `internal/app/app_test.go` — at least one end-to-end smoke
  test that exercises the composition without external infra.
- `go.mod` — module path
  `github.com/bds421/rho-kit/examples/<name>/v2`, with
  `replace` directives pointing every kit module at the local
  workspace. Lives in `go.work`.
- `README.md` — "when to use this pattern", "what's wired",
  "what's NOT here", and the smoke-test command.

When a recipe above graduates to a real example, it adopts this
same shape.
