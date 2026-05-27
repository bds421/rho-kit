# Bootstrap - Service Lifecycle

Packages: `app`, `core/config`, `runtime/lifecycle`

Snippet status: Go blocks in this recipe are illustrative fragments unless
explicitly introduced as generated or executable code. Buildable golden-path
evidence lives in `cmd/kit-new` scaffold tests and `examples/agentic-service`.

## When to Use

Use `app.Main` plus `app.New(...).With*().Run()` for normal HTTP services. It centralizes structured logging, health checks, graceful shutdown, middleware setup, and the `Infrastructure` container passed to the router.

Use `runtime/lifecycle.Runner` directly only when a service has custom transports, unusual shutdown ordering, or infrastructure that does not fit the Builder.

## Quick Start

```go
import (
    "github.com/bds421/rho-kit/app/v2"
    "github.com/bds421/rho-kit/app/amqp/v2"
    "github.com/bds421/rho-kit/app/postgres/v2"
    pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
)

func main() {
    app.Main("my-service", version, func(logger *slog.Logger) error {
        base, err := app.LoadBaseConfig(8080)
        if err != nil { return err }

        return app.New("my-service", version, base).
            With(postgres.Module(pgxbackend.Config{DSN: os.Getenv("DATABASE_URL")})).
            With(amqp.Module(os.Getenv("RABBITMQ_URL"))).
            With(jwt.Module(os.Getenv("JWKS_URL"),
                jwt.WithIssuer("https://issuer.example.com"),
                jwt.WithAudience("my-service"),
            )).
            With(ratelimit.IP(100, time.Minute)).
            Router(func(infra app.Infrastructure) http.Handler {
                mux := http.NewServeMux()
                mux.Handle("GET /users", httpx.JSONNoBody[UserListResponse](
                    logger,
                    listUsers(postgres.Pool(infra).Pool()),
                ))
                return stack.Default(mux, infra.Logger)
            }).
            Run()
    })
}
```

**Lazy-adapter (v2.0.0).** Heavy adapter wiring (Postgres, Redis,
RabbitMQ, NATS, tracing, public gRPC) lives in `app/postgres`,
`app/redis`, `app/amqp`, `app/nats`, `app/tracing`, `app/grpc`. Each
sub-package exports `Module(...) app.Module` and a typed accessor
(`postgres.Pool(infra)`, `redis.Connection(infra)`, etc.) so consumer
code keeps the ergonomic shape from before the refactor. Importing
`app/v2` alone no longer pulls pgx, go-redis, amqp091, nats.go,
otelgrpc, or grpc-go.

`app.LoadBaseConfig(defaultServerPort int) (app.BaseConfig, error)` reads
`SERVER_PORT`, `SERVER_HOST`, `INTERNAL_PORT`, `ENVIRONMENT`,
`LOG_LEVEL`, and `TLS_*` from the environment. `BaseConfig` lives in
package `app`, not `core/config`. See
[adoption.md](adoption.md) for the minimum downstream `go.mod` and the
common first-mistake checklist.

## Scaffold a Service

Use `kit-new` for a buildable service skeleton. Add `-tenant` when the
service is multi-tenant; it wires Redis, strict `X-Tenant-Id`
extraction, and tenant-wrapped cache/idempotency stores.

```sh
kit-new billing-api -module-path github.com/acme/billing-api -tenant
```

## Config Loading

```go
type Config struct {
    app.BaseConfig
    sqldb.Fields
    Postgres pgxbackend.Config
    AMQPURL  string
    JWKSURL  string
}

func LoadConfig() (Config, error) {
    base, err := app.LoadBaseConfig(8080)
    if err != nil { return Config{}, err }

    db, err := sqldb.LoadFields("MYAPP", 10, 100)
    if err != nil { return Config{}, err }

    cfg := Config{
        BaseConfig: base,
        Fields:     db,
        Postgres:   pgxConfig(db.Database, db.DatabasePool),
        AMQPURL:    config.MustGetSecret("RABBITMQ_URL", ""),
        JWKSURL:    config.Get("JWKS_URL", ""),
    }
    if err := cfg.ValidateBase(); err != nil { return Config{}, err }
    if err := cfg.Fields.Validate("MYAPP"); err != nil { return Config{}, err }
    return cfg, nil
}
```

`sqldb.LoadFields` returns validated PostgreSQL fields and pool settings. Convert those fields into `pgxbackend.Config` in service code, or load a `DATABASE_URL` directly into `pgxbackend.Config{DSN: ...}` when that is simpler.

## Builder Methods

The Builder hosts the HTTP-level cross-cutting primitives. Adapter wiring
lives in per-adapter sub-modules under `app/` and is registered via
[`Builder.With`](#adapter-modules).

### Adapter modules

| Sub-package | Module constructor | Getter | Notes |
|---|---|---|---|
| `app/postgres/v2` | `postgres.Module(cfg, opts…)` | `postgres.Pool(infra)` | `postgres.WithMigrations(fs)` runs goose SQL migrations on startup |
| `app/redis/v2` | `redis.Module(cfg, opts…)` | `redis.Connection(infra)` | `redis.Module(cfg, redis.WithoutTLS())` opts out of FR-077; `redis.WithConn(kitredis.WithX())` passes connection-level options |
| `app/amqp/v2` | `amqp.Module(url, opts…)` | `amqp.Connection/Publisher/Consumer(infra)` | Non-loopback `amqp://` panics; use `amqps://` or `amqp.WithoutTLS()`. `amqp.WithURLProvider(fn)` rotates credentials; `amqp.WithCriticalBroker()` flips health to 503 |
| `app/nats/v2` | `nats.Module(cfg, opts…)` | `nats.Connection/Publisher(infra)` | `nats.WithMessageSizeLimiter(...)` caps publisher payloads |
| `app/tracing/v2` | `tracing.Module(cfg)` | (auto-wires the HTTP client) | `cfg.SampleRate > 0.1` panics at construction |
| `app/grpc/v2` | `grpc.Module(reg, addr, opts…)` | `grpc.Server(infra)` | Auto-wires kit server TLS, adds internal gRPC health over h2c, `grpc.WithPublicHealth()` exposes public health |

### Builder methods (HTTP + cross-cutting)

| Method | What it enables | Requires |
|---|---|---|
| `With(m)` | Register an adapter module returned by a sub-package's `Module(...)` constructor (postgres, redis, amqp, nats, jwt, paseto, flags, cron, leader, slo, ratelimit, signedrequest, http, grpc, tracing, storage, auditlog, actionlog, approval, authz, tenant, budget, …) | - |
| `WithoutRateLimit()` | Explicit opt-out of the always-on rate-limit gate | - |
| `AddHealthCheck(check)` | Custom readiness dependency | - |
| `Background(name, fn)` | Managed goroutine | - |
| `OnShutdown(fn)` | Shutdown hook before close/drain | - |
| `Logger(l)` | Override the kit-built logger | - |
| `StartupTimeout(d)` | Cap on module Init() time | - |
| `Router(fn)` | HTTP handler builder | required |

Concept-specific configuration moves through the corresponding bridge
module — register via `b.With(<bridge>.Module(...))`:

| Concept | Wiring | Accessor |
|---|---|---|
| JWT verification | `With(jwt.Module(jwksURL, jwt.WithIssuer(iss), jwt.WithAudience(aud)))` | `jwt.Provider(infra)` |
| PASETO provider | `With(paseto.Module(provider))` | `paseto.Provider(infra)` |
| Signed requests | `With(signedrequest.Module(resolver, store, …))` | _(middleware)_ |
| Feature flags | `With(flags.Module(provider))` | `flags.Client(infra)` |
| Per-IP rate limit | `With(ratelimit.IP(n, window))` | `ratelimit.IPLimiter(infra)` |
| Keyed rate limit | `With(ratelimit.Keyed(name, n, window))` | `ratelimit.KeyedLimiter(infra, name)` |
| Cron scheduler | `With(cron.Module(opts...))` | `cron.Scheduler(infra)` |
| Leader election | `With(leader.Module(elector))` | `leader.Elector(infra)` |
| SLO checker | `With(slo.Module(slos...))` | _(internal /slo handler auto-wires)_ |
| HTTP server config | `With(http.Module(http.WithoutTLS(), http.WithReloadingTLS(), …))` | _(consumed by Builder)_ |
| Tenant extraction | `With(tenant.Module(extractor, tenant.WithoutTenantRequired()))` | _(middleware; read via `coretenant.FromContext`)_ |
| Tenant budget | `With(budget.Module(store, opts...))` | `budget.Store(infra)` |
| Object storage | `With(storage.Module(backend, storage.WithNamed("uploads", b2)))` | `storage.Backend(infra)`, `storage.Manager(infra)` |
| Audit logger | `With(auditlog.Module(store, opts...))` | `auditlog.Logger(infra)` |
| Action logger | `With(actionlog.Module(logger))` | `actionlog.Logger(infra)` |
| Approval store | `With(approval.Module(store))` | `approval.Store(infra)` |
| Authorization decider | `With(authz.Module(decider))` | `authz.Decider(infra)` |
| In-process event bus | `With(eventbus.Module(eventbus.WithPoolSize(n)))` | `eventbus.Bus(infra)` |

## Infrastructure

Available inside `RouterFunc`:

```go
type Infrastructure struct {
    Logger        *slog.Logger
    ServerTLS     *tls.Config
    TLSCertSource netutil.CertificateSource // populated when http.WithReloadingTLS is configured
    Config        app.BaseConfig

    Background(name string, fn func(ctx context.Context) error)
    SetCustomReadiness(h http.Handler)
    AddHealthCheck(check health.DependencyCheck)
    Resource(key string) (any, bool)
    SetResource(key string, value any)
}
```

Every other service resource (Postgres pool, Redis, JWT, PASETO,
flags, cron scheduler, leader elector, SLO checker, rate limiters,
AMQP/NATS connections, event bus, the outbound HTTP client, …)
lives in the bridge resource map and is reached via per-bridge
lookup functions: `postgres.Pool(infra)`, `jwt.Provider(infra)`,
`ratelimit.IPLimiter(infra)`, `app.HTTPClient(infra)`,
`eventbus.Bus(infra)`, etc. The callback fields are valid only
during the synchronous `RouterFunc` call.

## Lifecycle Order

`Run()` validates config, initializes enabled modules, builds `Infrastructure`, calls `RouterFunc`, starts the internal and public servers, waits for SIGINT/SIGTERM, runs shutdown hooks, drains managed goroutines, then closes initialized resources. The internal listener serves HTTP `/ready` and the gRPC Health Checking Protocol over h2c on the same address. Public gRPC health is disabled unless `WithPublicGRPCHealth()` is called.

Use `RunContext(ctx)` instead of `Run()` when the service is embedded in a test, CLI, worker supervisor, or parent process that already owns cancellation. Cancelling `ctx` triggers the same graceful drain as SIGINT/SIGTERM without relying on process-global signals.

Builder-managed module cleanup, tracing shutdown, and internal-server shutdown
use bounded detached cleanup contexts. Cleanup survives parent cancellation while
preserving context values such as tenant, trace, and logger metadata for modules
and instrumentation wrappers.

Migrations are goose SQL migrations. They run whenever `postgres.Module` is constructed with `postgres.WithMigrations(fs)`, regardless of environment.

## Environment Variables

| Variable | Default | Purpose |
|---|---|---|
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `SERVER_HOST` | `0.0.0.0` | Public server bind address |
| `SERVER_PORT` | service-specific | Public server port |
| `INTERNAL_HOST` | loopback | Internal health/metrics/gRPC-health bind address |
| `INTERNAL_PORT` | `9090` | Internal health/metrics/gRPC-health port |
| `ENVIRONMENT` | `production` | App environment string |
| `TLS_CA_CERT` | - | CA cert path |
| `TLS_CERT` | - | Server/client cert path |
| `TLS_KEY` | - | Private key path |
| `DATABASE_URL` | - | PostgreSQL URI; takes precedence over individual DB vars |

PostgreSQL field configuration comes from `sqldb.LoadFields`. RabbitMQ URL handling is in `infra/messaging/amqpbackend`.

## Manual Wiring

```go
func main() {
    app.Main("my-service", version, func(logger *slog.Logger) error {
        cfg := config.MustLoad[MyConfig]()

        db, err := pgxbackend.Connect(context.Background(), cfg.Postgres)
        if err != nil { return err }
        defer db.Close()

        redisConn, err := redis.Connect(cfg.RedisOpts, redis.Logger(logger))
        if err != nil { return err }
        defer redisConn.Close()

        mux := http.NewServeMux()
        mux.Handle("GET /users", httpx.JSONNoBody(logger, listUsers(db.Pool())))
        handler := stack.Default(mux, logger)

        runner := lifecycle.NewRunner(logger)
        serverLog := slog.NewLogLogger(logger.Handler(), slog.LevelWarn)
        runner.Add("http", lifecycle.NewHTTPServer(httpx.NewServer(":8080", handler, httpx.WithErrorLog(serverLog))))
        runner.AddFunc("worker", func(ctx context.Context) error {
            return myWorker(ctx, redisConn)
        })
        return runner.Run(context.Background())
    })
}
```

## Database Migrations

```go
//go:embed migrations/*.sql
var migrationsFS embed.FS

app.New("my-svc", version, cfg.BaseConfig).
    With(postgres.Module(cfg.Postgres, postgres.WithMigrations(migrationsFS))).
    Router(routerFn).
    Run()
```

Workflow for schema changes:

1. Write or update SQL in `migrations/`.
2. Include reversible `-- +goose Up` and `-- +goose Down` sections.
3. Test the migration against a real PostgreSQL container.
4. Keep application query code and SQL migrations in the same change.

## Cron Jobs

```go
app.New("my-svc", version, cfg.BaseConfig).
    With(postgres.Module(cfg.Postgres)).
    With(cron.Module()).
    Router(func(infra app.Infrastructure) http.Handler {
        cron.Scheduler(infra).Add("cleanup", "0 2 * * *", func(ctx context.Context) error {
            return cleanupOldRecords(ctx, postgres.Pool(infra).Pool())
        })
        return buildRouter(infra)
    }).
    Run()
```

## Audit Log

```go
// Production: use the kit's postgres-backed Store (apply schema with
//   kit-migrate publish --to=./migrations auditlog
// then bind it to the same pgx pool the service already uses).
//
//   pool, _ := pgxpool.New(ctx, dsn)
//   auditStore := auditlogpg.New(pool)
//
// Dev / tests / local demos: in-memory store has zero deps.
auditStore := auditlog.NewMemoryStore()

app.New("my-svc", version, cfg.BaseConfig).
    With(appauditlog.Module(auditStore)).
    Router(func(infra app.Infrastructure) http.Handler {
        alog := appauditlog.Logger(infra)
        mux := http.NewServeMux()
        mux.HandleFunc("POST /orders/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
            if err := alog.LogE(r.Context(), auditlog.Event{
                Actor:    "user-1",
                Action:   "delete",
                Resource: "orders/" + r.PathValue("id"),
                Status:   "success",
            }); err != nil {
                http.Error(w, "audit unavailable", http.StatusServiceUnavailable)
                return
            }
            w.WriteHeader(http.StatusNoContent)
        })
        return mux
    }).
    Run()
```

## Anti-Patterns

- Do not call `Builder.Run()` without `Router()`.
- Do not use `Infrastructure` fields outside the `RouterFunc` closure.
- Do not ignore nil checks on optional `Infrastructure` fields.
- Do not disable PostgreSQL TLS except through the loopback-only test opt-out.
- Do not put Docker-backed test helpers in base service modules.
