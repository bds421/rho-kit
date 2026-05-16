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
| realtime-broadcast     | Recipe below   | `realtime/centrifuge`, `security/jwtutil`, `httpx/websocket` |
| api-gateway            | Recipe below   | `httpx/middleware/{ratelimit,jwt,idempotency}`, `grpcx/interceptor`, `openapigen` |
| saga-coordinator       | Recipe below   | `runtime/saga`, `infra/outbox`, `data/idempotency`, `data/lock` |

The first three ship as compileable modules in this directory.
The last three are documented as composition recipes — each
shows the exact import set, the wiring order, and the rationale
— so a coding agent can stand the pattern up against a fresh
service skeleton.

## Recipe: realtime-broadcast

Browser-facing real-time with channels, presence, and history.

```go
import (
    "github.com/bds421/rho-kit/realtime/v2/centrifuge"
    "github.com/bds421/rho-kit/security/v2/jwtutil"
)

verifier, _ := jwtutil.NewProvider( /* jwks_url, issuer, audience */ )
node, err := centrifuge.NewNode(
    centrifuge.WithJWTAuth(verifier),
    centrifuge.WithChannelClassifier(func(channel string) string {
        // Return "user" / "room" / "system" — kit projects through
        // promutil.OpaqueLabelValue as a cardinality safety net.
        return classifyChannel(channel)
    }),
    centrifuge.WithMetricsRegisterer(reg),
)
// Compose with lifecycle.Runner so Stop is invoked on shutdown.
```

`kit-doctor` flags `centrifuge.NewNode` without `WithJWTAuth`
(`centrifuge-missing-jwt-auth`, CRITICAL). The class label is
projected through `promutil.OpaqueLabelValue` so a misbehaving
classifier cannot inflate Prometheus cardinality. Dashboards
under `observability/dashboards/grafana/centrifuge.json`. Runbook
at `docs/ai/runbooks/centrifuge.md`.

## Recipe: api-gateway

Public-facing HTTP front door: rate limiting per IP and per
tenant, JWT validation, request fan-out to downstream gRPC
services with circuit breakers and retries, OpenAPI exposed.

```go
import (
    "github.com/bds421/rho-kit/app/v2"
    "github.com/bds421/rho-kit/app/ratelimit/v2"
    "github.com/bds421/rho-kit/app/jwt/v2"
    "github.com/bds421/rho-kit/grpcx/v2/interceptor"
)

builder := app.New(serviceName).
    With(jwt.Module(jwksURL, jwt.WithIssuer(iss), jwt.WithAudience(aud))).
    With(ratelimit.IP(100, time.Minute)).
    With(ratelimit.Keyed("tenant", 1000, time.Minute, tenantKeyFn)).
    With(/* downstream gRPC clients wrapped with resilience */)
// builder.Run() enforces the kit's startup validator:
// TLS configured, JWT issuer + audience set, no internal-host
// non-loopback exposure, postgres sslmode tightened, tracing
// sample rate sane.
```

For downstream gRPC fan-out, wrap the dial with
`interceptor.MaxConcurrentStreamsServer` (server-side cap),
`interceptor.StreamIdleTimeout` (client-side cleanup), and
`resilience/circuitbreaker.NewCircuitBreaker` per downstream.
`kit-doctor` flags `app.Builder.Run()` without a rate-limit
declaration (`rate-limit-omission`, HIGH). Dashboards under
`observability/dashboards/grafana/grpc-stream-limits.json`.

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
