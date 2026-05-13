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
func main() {
    app.Main("my-service", version, func(logger *slog.Logger) error {
        cfg, err := LoadConfig()
        if err != nil { return err }

        return app.New("my-service", version, cfg.BaseConfig).
            WithPostgres(cfg.Postgres).
            WithRabbitMQ(cfg.AMQPURL).
            WithJWT(cfg.JWKSURL).
            WithIPRateLimit(100, time.Minute).
            Router(func(infra app.Infrastructure) http.Handler {
                mux := http.NewServeMux()
                mux.Handle("GET /users", httpx.JSONNoBody(logger, listUsers(infra.DB.Pool())))
                return stack.Default(mux, logger)
            }).
            Run()
    })
}
```

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

| Method | What it enables | Requires |
|---|---|---|
| `WithPostgres(cfg)` | pgx-backed PostgreSQL pool and readiness check | `cfg.DSN` |
| `WithMigrations(dir)` | Goose SQL migrations on startup | `WithPostgres` |
| `WithRedis(opts, connOpts...)` | Redis connection and pool metrics | - |
| `WithRabbitMQ(url)` | Lazy AMQP connection plus pre-wired Publisher and Consumer | - |
| `WithCriticalBroker()` | Broker health failure returns HTTP 503 | `WithRabbitMQ` |
| `WithNATS(cfg)` | NATS connection plus default JetStream publisher and publish metrics | - |
| `WithMaxMessageBytes(n)` / `WithRouteMaxMessageBytes(exchange, routingKey, n)` | Serialized message-size limits for Builder-created RabbitMQ and NATS publishers | `WithRabbitMQ` / `WithNATS` |
| `WithJWT(jwksURL)` | Background JWKS key cache | - |
| `WithJWTAudience(aud)` | Required JWT audience | `WithJWT` |
| `WithPASETO(provider)` | PASETO token provider | - |
| `WithSignedRequests(...)` | HMAC signed request verifier | - |
| `WithMultiTenant(extractor, required)` | Tenant extraction middleware | - |
| `WithTenantBudget(budget, opts...)` | Tenant request budget middleware | `WithMultiTenant(..., required=true)` |
| `WithActionLogger(logger)` | Action logger in infrastructure | - |
| `WithApprovalStore(store)` | Approval store in infrastructure | - |
| `WithAuthz(decider)` | Authorization decider in infrastructure | - |
| `WithFeatureFlags(provider)` | Feature flag client in infrastructure | - |
| `WithIPRateLimit(n, window)` | Per-IP rate limiter | - |
| `WithKeyedRateLimit(name, n, window)` | Named keyed rate limiter | - |
| `WithStorage(backend, checks...)` | Single unnamed storage backend | - |
| `WithNamedStorage(name, backend, checks...)` | Named backend in storage manager | - |
| `WithAuditLog(store, opts...)` | Audit logger | - |
| `WithCron(opts...)` | Lifecycle-managed cron scheduler | - |
| `WithLeaderElection(elector)` | Leader election handle | - |
| `WithTracing(cfg)` | OpenTelemetry tracing; endpoint must be `host[:port]`, service name required when exporting | - |
| `WithServerOption(opt)` | Custom public server option | - |
| `WithStackOptions(opts...)` | Extra default-stack options | - |
| `WithoutDefaultStack()` | Use router handler without `stack.Default` wrapping | - |
| `AddHealthCheck(check)` | Custom readiness dependency | - |
| `WithCustomReadiness(handler)` | Override `/ready` handler | - |
| `Background(name, fn)` | Managed goroutine | - |
| `OnShutdown(fn)` | Shutdown hook before close/drain | - |
| `WithModule(module)` | Custom lifecycle module | - |
| `Router(fn)` | HTTP handler builder | required |

## Infrastructure

Available inside `RouterFunc`:

```go
type Infrastructure struct {
    Logger    *slog.Logger
    ClientTLS *tls.Config
    ServerTLS *tls.Config

    DB            *pgxbackend.Pool
    Broker        messaging.Connector
    Publisher     messaging.MessagePublisher
    Consumer      messaging.MessageConsumer
    NATS          *natsbackend.Connection
    NATSPublisher *natsbackend.Publisher

    JWT    *jwtutil.Provider
    PASETO *paseto.Provider

    Leader        leaderelection.Elector
    TenantBudget  budget.Budget
    ActionLog     actionlog.Logger
    ApprovalStore approval.Store
    Authz         authz.Decider
    Flags         *flags.Client

    RateLimiter   *ratelimit.RateLimiter
    KeyedLimiters map[string]*ratelimit.KeyedRateLimiter
    Redis         *redis.Connection

    Storage        storage.Storage
    StorageManager *storage.Manager
    Cron           *cron.Scheduler
    AuditLog       *auditlog.Logger
    EventBus       *eventbus.Bus
    GRPCServer     *grpc.Server

    HTTPClient *http.Client
    Config     app.BaseConfig

    Background(name string, fn func(ctx context.Context) error)
    SetCustomReadiness(h http.Handler)
    AddHealthCheck(check health.DependencyCheck)
}
```

Nil fields mean the matching `With*()` method was not called. The callback fields are valid only during the synchronous `RouterFunc` call.

## Lifecycle Order

`Run()` validates config, initializes enabled modules, builds `Infrastructure`, calls `RouterFunc`, starts the internal and public servers, waits for SIGINT/SIGTERM, runs shutdown hooks, drains managed goroutines, then closes initialized resources. The internal listener serves HTTP `/ready` and the gRPC Health Checking Protocol over h2c on the same address. Public gRPC health is disabled unless `WithPublicGRPCHealth()` is called.

Use `RunContext(ctx)` instead of `Run()` when the service is embedded in a test, CLI, worker supervisor, or parent process that already owns cancellation. Cancelling `ctx` triggers the same graceful drain as SIGINT/SIGTERM without relying on process-global signals.

Builder-managed module cleanup, tracing shutdown, and internal-server shutdown
use bounded detached cleanup contexts. Cleanup survives parent cancellation while
preserving context values such as tenant, trace, and logger metadata for modules
and instrumentation wrappers.

Migrations are goose SQL migrations. They run whenever `WithMigrations` is configured, regardless of environment.

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

        redisConn, err := redis.Connect(cfg.RedisOpts, redis.WithLogger(logger))
        if err != nil { return err }
        defer redisConn.Close()

        mux := http.NewServeMux()
        mux.Handle("GET /users", httpx.JSONNoBody(logger, listUsers(db.Pool())))
        handler := stack.Default(mux, logger)

        runner := lifecycle.NewRunner(logger)
        serverLog := slog.NewLogLogger(logger.Handler(), slog.LevelWarn)
        runner.Add("http", lifecycle.HTTPServer(httpx.NewServer(":8080", handler, httpx.WithErrorLog(serverLog))))
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
    WithPostgres(cfg.Postgres).
    WithMigrations(migrationsFS).
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
    WithCron().
    Router(func(infra app.Infrastructure) http.Handler {
        infra.Cron.Add("cleanup", "0 2 * * *", func(ctx context.Context) error {
            return cleanupOldRecords(ctx, infra.DB.Pool())
        })
        return buildRouter(infra)
    }).
    Run()
```

## Audit Log

```go
auditStore := auditlog.NewMemoryStore() // replace with durable storage in production

app.New("my-svc", version, cfg.BaseConfig).
    WithAuditLog(auditStore).
    Router(func(infra app.Infrastructure) http.Handler {
        mux := http.NewServeMux()
        mux.HandleFunc("POST /orders/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
            if err := infra.AuditLog.LogE(r.Context(), auditlog.Event{
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
