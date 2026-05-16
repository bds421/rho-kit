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
| **saga-coordinator**   | Implemented    | `runtime/saga`, `data/idempotency`, per-key exclusive section (in-memory; pgadvisory in production) |

All six patterns now ship as compileable modules in this
directory. Each has a smoke test that exercises the composition
without external infrastructure, plus a README that documents
both the demonstrated wiring and the production-wiring
substitutions (Redis / Postgres backends, real IDP, real
broker).

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
