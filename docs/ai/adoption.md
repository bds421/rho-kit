# Adoption — Consumer-Side Onboarding

This recipe is for service authors who are adopting rho-kit v2 in a
**new downstream module**. It complements [bootstrap.md](bootstrap.md)
(which documents the kit-side Builder API) by focusing on the
`go.mod` shape, the smallest compilable program, and the mistakes the
review process keeps catching.

Snippet status: the Go program below compiles against the v2 API at
HEAD. Shell blocks are runnable from a downstream service checkout.

## 1. Module Setup

Until `v2.0.0` is tagged, downstream consumers using a local rho-kit
checkout must replace every transitively-required rho-kit module. The
v2 `app/v2` module pulls in adapter sub-modules (amqpbackend,
natsbackend, pgx, redis, etc.) so the Builder API can stay fluent
without import-time selection — see the
[forward-looking note](#7-forward-looking-v21-lazy-adapters) below
for why this is intentional and short-lived.

Minimal downstream `go.mod` for a Builder-based service that uses
Postgres + Redis + RabbitMQ + JWT:

```text
module github.com/acme/my-service

go 1.26.2

require (
    github.com/bds421/rho-kit/app/v2 v2.0.0
    github.com/bds421/rho-kit/httpx/v2 v2.0.0
    github.com/bds421/rho-kit/infra/sqldb/pgx/v2 v2.0.0
    github.com/redis/go-redis/v9 v9.18.0
)

// Until v2.0.0 ships on the module proxy, every transitively required
// rho-kit module must be replaced against a local checkout. Drop this
// block once you can resolve v2.0.0 from the proxy.
replace (
    github.com/bds421/rho-kit/app/v2                            => ../rho-kit/app
    github.com/bds421/rho-kit/authz/v2                          => ../rho-kit/authz
    github.com/bds421/rho-kit/core/v2                           => ../rho-kit/core
    github.com/bds421/rho-kit/crypto/v2                         => ../rho-kit/crypto
    github.com/bds421/rho-kit/data/v2                           => ../rho-kit/data
    github.com/bds421/rho-kit/flags/v2                          => ../rho-kit/flags
    github.com/bds421/rho-kit/grpcx/v2                          => ../rho-kit/grpcx
    github.com/bds421/rho-kit/httpx/v2                          => ../rho-kit/httpx
    github.com/bds421/rho-kit/infra/v2                          => ../rho-kit/infra
    github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2    => ../rho-kit/infra/messaging/amqpbackend
    github.com/bds421/rho-kit/infra/messaging/natsbackend/v2    => ../rho-kit/infra/messaging/natsbackend
    github.com/bds421/rho-kit/infra/messaging/redisbackend/v2   => ../rho-kit/infra/messaging/redisbackend
    github.com/bds421/rho-kit/infra/redis/v2                    => ../rho-kit/infra/redis
    github.com/bds421/rho-kit/infra/sqldb/pgx/v2                => ../rho-kit/infra/sqldb/pgx
    github.com/bds421/rho-kit/io/v2                             => ../rho-kit/io
    github.com/bds421/rho-kit/observability/v2                  => ../rho-kit/observability
    github.com/bds421/rho-kit/resilience/v2                     => ../rho-kit/resilience
    github.com/bds421/rho-kit/runtime/v2                        => ../rho-kit/runtime
    github.com/bds421/rho-kit/security/v2                       => ../rho-kit/security
)
```

The exact set of replaces a service needs depends on which `With*()`
methods it calls. Run `go mod tidy` after the first build; the toolchain
will tell you which modules are still missing. Re-run `tidy` whenever
you add a new `With*()` call.

After `v2.0.0` is tagged on the proxy:

```bash
go get github.com/bds421/rho-kit/app/v2@v2.0.0
go get github.com/bds421/rho-kit/httpx/v2@v2.0.0
go mod tidy
```

Subpackages live under the module root, e.g.
`github.com/bds421/rho-kit/httpx/v2/middleware/stack`, not
`httpx/middleware/stack/v2`. See
[MIGRATION_V2.md §1](../release/MIGRATION_V2.md#1-move-imports-to-v2-module-paths).

## 2. Minimal Working Program

A copy-pastable `main.go` that compiles against the v2 API at HEAD:

```go
package main

import (
    "log/slog"
    "net/http"
    "time"

    goredis "github.com/redis/go-redis/v9"

    "github.com/bds421/rho-kit/app/v2"
    "github.com/bds421/rho-kit/httpx/v2/middleware/stack"
    pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
)

var version = "" // set via -ldflags

func main() {
    app.Main("my-service", version, func(_ *slog.Logger) error {
        base, err := app.LoadBaseConfig(8080)
        if err != nil {
            return err
        }

        return app.New("my-service", version, base).
            WithPostgres(pgxbackend.Config{DSN: "postgres://localhost/my-service"}).
            WithRedis(&goredis.Options{Addr: "rediss://cache.internal:6379", Password: "***"}).
            WithRabbitMQ("amqps://broker.internal").
            WithJWT("https://issuer.example.com/.well-known/jwks.json").
            WithJWTAudience("my-service").
            WithIPRateLimit(100, time.Minute).
            Router(func(infra app.Infrastructure) http.Handler {
                mux := http.NewServeMux()
                // Register routes using infra.DB, infra.Publisher, etc.
                return stack.Default(mux, infra.Logger)
            }).
            Run()
    })
}
```

Things this program demonstrates:

- `app.LoadBaseConfig(8080)` is the loader for the universal config.
- `app.New(name, version, base)` returns `*app.Builder`.
- `Router(fn)` is required; `Run()` blocks until SIGINT/SIGTERM.
- `infra.Logger` inside the router is the same logger that flows from
  `app.Main` through the lifecycle runner — use it for handler logs.

For multi-tenant scaffolds and the per-capability wiring details, run
`go run github.com/bds421/rho-kit/cmd/kit-new/v2 my-service -tenant`.

## 3. Where To Find Each Capability

The package decision tree in [AGENTS.md](../../AGENTS.md#package-decision-tree)
is the canonical "I need to X, what do I import?" reference. Quick
pointers for the most common downstream wiring:

| Capability | Builder method | Infrastructure field |
|---|---|---|
| Postgres pool, readiness | `WithPostgres(pgxbackend.Config{DSN:...})` | `infra.DB` |
| Redis (TLS-required) | `WithRedis(*goredis.Options, ...kitredis.ConnOption)` | `infra.Redis` |
| RabbitMQ publisher/consumer | `WithRabbitMQ(url)` | `infra.Publisher`, `infra.Consumer` |
| NATS JetStream | `WithNATS(natsbackend.Config)` | `infra.NATS`, `infra.NATSPublisher` |
| JWT verification (JWKS) | `WithJWT(jwksURL).WithJWTAudience(aud)` | `infra.JWT` |
| Multi-tenant request scope | `WithMultiTenant(extractor, required)` | (middleware) |
| In-process rate limit | `WithIPRateLimit(n, window)` | `infra.RateLimiter` |
| Cron jobs | `WithCron()` | `infra.Cron` |
| Leader election | `WithLeaderElection(elector)` | `infra.Leader` |
| Typed HTTP handlers | `httpx.JSON[Req,Resp](logger, fn)` etc. | — |

The full list of Builder methods is in
[bootstrap.md](bootstrap.md#builder-methods).

## 4. What Is NOT Auto-Wired

The Builder is opinionated but not magical. The following capabilities
require explicit opt-in:

- **pgx pool stat collector / JWKS metrics collector.** The Builder
  registers health checks for every `With*()` it owns, but Prometheus
  collectors that require a `prometheus.Registerer` are only attached
  when the relevant package's `WithRegisterer(...)` option is passed
  (defaults to `prometheus.DefaultRegisterer` when omitted). Inspect
  each adapter's options if you use a custom registry for test
  isolation.
- **`httpx/middleware/csrf`.** Not added by `stack.Default`. Pass it
  via `stack.WithOuter(csrfMW, csrf.RequireJSONContentType)`.
- **`httpx/middleware/idempotency`.** The Builder does not mount the
  idempotency middleware on every route — wire it on the specific mux
  subtree that needs it.
- **OpenTelemetry tracing.** `WithTracing(cfg)` is opt-in; without it
  the Builder runs with no tracer provider and `infra.HTTPClient` is
  the non-tracing client.
- **Audit logger, approval store, action logger.** `WithAuditLog`,
  `WithApprovalStore`, `WithActionLogger` are explicit; the kit ships
  in-memory implementations for tests but production backends
  (Postgres) live in adapter sub-modules.
- **Migrations.** `WithMigrations(fs)` runs goose migrations from an
  `embed.FS` you supply; the Builder does not generate schemas.

## 5. Common First-Mistake Checklist

Reviews keep catching the same five mistakes in downstream services:

1. **Wrong `BaseConfig` import.**
   `app.BaseConfig` is the type. Do NOT import `core/v2/config.BaseConfig`
   — there is no such type in `core/config`. Build your service config
   struct by **embedding** `app.BaseConfig`, and populate it with
   `app.LoadBaseConfig(defaultPort)`.

2. **Calling `httpx.JSON(handler)` without the logger.**
   The signature is
   `httpx.JSON[Req,Resp](logger *slog.Logger, fn func(ctx, *http.Request, Req) (Resp, error))`.
   Pass the same `*slog.Logger` that flows in from `app.Main` /
   `infra.Logger`. The same logger argument is required by
   `JSONNoBody`, `JSONStatus`, `JSONNoBodyStatus`, and `NoContent`.

3. **Plaintext Redis URI bypassing FR-077.**
   `WithRedis(&goredis.Options{Addr: "redis://cache:6379"})` against a
   non-loopback host is rejected at `Run()` time. Set
   `Options.TLSConfig` (or use a `rediss://` URL) and a non-empty
   `Password`. Use `WithoutRedisTLS()` only on a reviewed local-dev
   boundary; there is no `KIT_ENV` escape hatch.

4. **Custom modules implementing `Close`, not `Stop`.**
   `app.Module.Stop(ctx context.Context) error` is the v2 method.
   Services migrating from v1 must rename their `Close()` to
   `Stop(ctx context.Context) error` (or embed `app.BaseModule` whose
   default `Stop` returns nil). See
   [MIGRATION_V2.md §6 `app`](../release/MIGRATION_V2.md#app).

5. **Direct `net/http.Server` or `http.DefaultClient`.**
   Both are rejected by `kit-doctor`. Use `httpx.NewServer(addr,
   handler, ...)` and `httpx.NewHTTPClient(...)` (or the Builder's
   `infra.HTTPClient`).

## 6. Programs That Outgrow The Builder

When a service needs custom transports or non-standard shutdown order,
drop down to `runtime/lifecycle.Runner` directly — the kit's
sub-packages have no upward dependency on `app/v2` and can be composed
independently. See
[bootstrap.md → Manual Wiring](bootstrap.md#manual-wiring).

## 7. Forward-Looking: v2.1 Lazy Adapters

In v2.0.0 the `app/v2` module transitively imports every adapter
sub-module (`amqpbackend`, `natsbackend`, `pgx`, `redis`,
`leaderelection`, etc.) so the Builder can keep a single fluent API
with no compile-time selection. A service that only uses HTTP + Redis
still pays the build-time cost of `amqp091`, `nats.go`, and `pgx`.

A v2.1 "lazy adapter" architecture is planned where adapter wiring
moves into per-adapter sub-packages (`github.com/bds421/rho-kit/app/postgres/v2`,
`app/amqp/v2`, `app/nats/v2`, …). Downstream services will compose
only the adapters they actually use, e.g.

```go
// Planned v2.1 shape — illustrative.
app.New("my-service", version, base).
    With(postgres.Module(pgxbackend.Config{DSN: dsn})).
    With(amqp.Module("amqps://broker.internal")).
    Router(routerFn).
    Run()
```

This is documented as a known migration cost; it is **not** a blocker
for adopting v2.0.0. Services that adopt v2.0.0 today will get a
mechanical refactor path when v2.1 lands.
