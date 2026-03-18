# Bootstrap — Service Lifecycle

Packages: `app`, `core/config`, `core/config`, `core/config`, `runtime/lifecycle`

## When to Use

Every rho microservice uses `app.Main` + `app.New(...).With*().Run()` as its entry point. This is not optional — it provides structured logging, graceful shutdown, health checks, and the `Infrastructure` DI container.

## Quick Start

```go
func main() {
    app.Main("my-service", "v1.0.0", func(logger *slog.Logger) error {
        cfg, err := LoadConfig()
        if err != nil { return err }
        return app.New("my-service", "v1.0.0", cfg.BaseConfig).
            WithPostgres(cfg.Database, cfg.DatabasePool, &User{}, &Order{}).
            Router(func(infra app.Infrastructure) http.Handler {
                mux := http.NewServeMux()
                mux.HandleFunc("GET /users", listUsers(infra.DB))
                return stack.Default(mux, logger)
            }).
            Run()
    })
}
```

## Config Loading Pattern

### Struct-tag approach (recommended for new services):

```go
type Config struct {
    app.BaseConfig
    DBHost    string `env:"MYAPP_DATABASE_HOST,required"`
    DBPort    int    `env:"MYAPP_DATABASE_PORT" default:"5432"`
    DBPass    string `env:"MYAPP_DATABASE_PASSWORD" secret:"true"`
    AMQPURL   string `env:"RABBITMQ_URL,required" secret:"true"`
    Timeout   time.Duration `env:"REQUEST_TIMEOUT" default:"30s"`
}

func LoadConfig() (Config, error) {
    return config.Load[Config]()
}
```

### Manual approach (existing services):

```go
type Config struct {
    app.BaseConfig
    sqldb.PostgresFields
    AMQPURL       string
    TraceEndpoint string
}

func LoadConfig() (Config, error) {
    base, err := app.LoadBaseConfig(8080) // default server port
    if err != nil { return Config{}, err }

    db, err := sqldb.LoadPostgresFields("MYAPP", 10, 100) // prefix, maxIdle, maxOpen
    if err != nil { return Config{}, err }

    cfg := Config{
        BaseConfig:     base,
        PostgresFields: db,
        AMQPURL:        envutil.GetSecret("RABBITMQ_URL", ""),
        TraceEndpoint:  envutil.Get("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
    }

    if err := cfg.ValidateBase(); err != nil { return Config{}, err }
    if err := cfg.ValidatePostgres("MYAPP", cfg.Environment); err != nil { return Config{}, err }
    return cfg, nil
}
```

## Builder Methods Reference

| Method | What it enables | Requires |
|---|---|---|
| `WithMariaDB(cfg, pool, models...)` | GORM MariaDB + auto-migrate + health check | — |
| `WithPostgres(cfg, pool, models...)` | GORM PostgreSQL + auto-migrate + health check | — |
| `WithDBMetrics()` | Prometheus pool metrics every 15s | DB configured |
| `WithRedis(opts, connOpts...)` | Redis connection + health check + pool metrics | — |
| `WithRabbitMQ(url)` | Lazy AMQP connection + pre-wired Publisher + Consumer | — |
| `WithCriticalBroker()` | Broker health = HTTP 503 (not just degraded) | `WithRabbitMQ` |
| `WithJWT(jwksURL)` | Background JWKS key cache | — |
| `WithIPRateLimit(n, window)` | Per-IP sliding-window rate limiter | — |
| `WithKeyedRateLimit(name, n, window)` | Named keyed rate limiter (per-user, per-tenant) | — |
| `WithStorage(backend, checks...)` | Single unnamed storage backend | — |
| `WithNamedStorage(name, backend, checks...)` | Named backend in storage.Manager | — |
| `WithMigrations(dir)` | Goose SQL migrations on startup (fail-fast) | DB configured |
| `WithCron(opts...)` | Cron scheduler (lifecycle-managed) | — |
| `WithAuditLog(store, opts...)` | Audit event logger | — |
| `WithSeed(fn)` | `--seed <path>` CLI support | DB configured |
| `WithTracing(cfg)` | OpenTelemetry OTLP/gRPC | — |
| `WithServerOption(opt)` | Custom httpx.ServerOption | — |
| `AddHealthCheck(check)` | Custom readiness check | — |
| `WithCustomReadiness(handler)` | Override /ready handler | — |
| `Background(name, fn)` | Managed goroutine (starts before router) | — |
| `OnShutdown(fn)` | Hook on SIGINT/SIGTERM (before drain) | — |
| `Router(fn)` | Set the HTTP handler builder | — |

## Infrastructure Struct

Available inside `RouterFunc`:

```go
type Infrastructure struct {
    Logger         *slog.Logger         // always set
    ClientTLS      *tls.Config          // nil without TLS env vars
    ServerTLS      *tls.Config          // nil without TLS env vars
    DB             *gorm.DB             // nil without WithMariaDB/WithPostgres
    Broker         messaging.Connector        // nil without WithRabbitMQ
    Publisher      messaging.MessagePublisher // nil without WithRabbitMQ (pre-wired)
    Consumer       messaging.MessageConsumer  // nil without WithRabbitMQ (pre-wired)
    JWT            *jwtutil.Provider    // nil without WithJWT
    RateLimiter    *ratelimit.RateLimiter          // nil without WithIPRateLimit
    KeyedLimiters  map[string]*ratelimit.KeyedRateLimiter // empty map if none
    Redis          *redis.Connection    // nil without WithRedis
    Storage        storage.Storage      // nil without WithStorage
    StorageManager *storage.Manager     // nil without WithNamedStorage
    Cron           *cron.Scheduler      // nil without WithCron
    AuditLog       *auditlog.Logger     // nil without WithAuditLog
    EventBus       *eventbus.Bus        // always non-nil; in-process domain event dispatch
    HTTPClient     *http.Client         // always set; tracing-aware if WithTracing
    Config         app.BaseConfig // always set

    // Callable inside RouterFunc only:
    Background(name string, fn func(ctx context.Context) error)
    SetCustomReadiness(h http.Handler)
    AddHealthCheck(check health.DependencyCheck)
}
```

**Pre-wired fields:** `Publisher` and `Consumer` are ready to use when `WithRabbitMQ` is called — no need to manually create `amqpbackend.NewPublisher` or `amqpbackend.NewConsumer`. Use `infra.Publisher.Publish(...)` and pass `infra.Consumer` to `messaging.StartConsumers` directly.

## Lifecycle Order

`Run()` executes: validate → tracing → TLS → Redis → DB + schema migration (AutoMigrate in dev, goose in prod) → (seed → exit if `--seed`) → RabbitMQ → JWT refresh → rate limiter cleanup → DB metrics → Redis metrics → audit log → cron scheduler → early background goroutines → RouterFunc → internal server (:9090) → public server → wait SIGINT/SIGTERM → OnShutdown hooks → drain workers (10s) → close connections.

## Environment Variables

| Variable | Default | Purpose |
|---|---|---|
| `LOG_LEVEL` | `info` | debug, info, warn, error |
| `SERVER_HOST` | `0.0.0.0` | Public server bind address |
| `SERVER_PORT` | service-specific | Public server port |
| `INTERNAL_PORT` | `9090` | Health/metrics port |
| `ENVIRONMENT` | `production` | `"development"` enables dev mode |
| `TLS_CA_CERT` | — | CA cert path (all 3 required for TLS) |
| `TLS_CERT` | — | Server/client cert path |
| `TLS_KEY` | — | Private key path |
| `DATABASE_URL` | — | PostgreSQL/MySQL connection URI (takes precedence over individual DB_* vars) |

Database and RabbitMQ support both URL and individual field configuration. URL takes precedence when set. See `database/config.go` and `messaging/amqpbackend/config.go` for details.

## Seeding

```go
app.New(...).
    WithPostgres(cfg.Database, cfg.DatabasePool, &User{}).
    WithSeed(func(db *gorm.DB, path string, log *slog.Logger) error {
        var users []User
        if err := app.LoadSeedJSON(path, &users); err != nil { return err }
        return db.Create(&users).Error
    }).
    Run()
```

Run with: `./service --seed ./seeds/data.json` — migrates, seeds, exits (no HTTP server).

## Manual Wiring (Advanced)

For services that outgrow the Builder pattern (e.g., custom shutdown ordering, non-HTTP transports, or infrastructure not supported by Builder), use `lifecycle.Runner` + `config.Load` directly:

```go
func main() {
    app.Main("my-service", "v1.0.0", func(logger *slog.Logger) error {
        cfg := config.MustLoad[MyConfig]()

        // Init components directly.
        db, err := gormdb.New(cfg.DB, cfg.Pool, logger)
        if err != nil { return err }
        defer closeDB(db)

        redisConn, err := redis.Connect(cfg.RedisOpts, redis.WithLogger(logger))
        if err != nil { return err }
        defer redisConn.Close()

        // Build HTTP handler.
        mux := http.NewServeMux()
        mux.Handle("GET /users", httpx.JSONNoBody(logger, listUsers(db)))
        handler := stack.Default(mux, logger)

        // Compose lifecycle.
        runner := lifecycle.NewRunner(logger)
        runner.Add("http", lifecycle.HTTPServer(httpx.NewServer(":8080", handler)))
        runner.AddFunc("worker", func(ctx context.Context) error {
            return myWorker(ctx, redisConn)
        })
        return runner.Run(context.Background())
    })
}
```

**When to use this pattern:**
- Non-standard infrastructure (gRPC, NATS, custom transports)
- Custom shutdown ordering between components
- Services that don't fit the With*() builder model
- Integration tests that need fine-grained control

The Builder remains recommended for typical HTTP+DB+MQ services where it eliminates boilerplate.

## Database Migrations (GORM + Goose)

Pass both GORM models and goose migrations — the kit auto-selects based on `ENVIRONMENT`:
- **development**: GORM AutoMigrate (fast iteration, no SQL files needed)
- **production/staging**: goose SQL migrations only (controlled, reviewable, reversible)

```go
//go:embed migrations/*.sql
var migrationsFS embed.FS

app.New("my-svc", "v1.0.0", cfg).
    WithPostgres(dbCfg, poolCfg, &User{}, &Order{}).
    WithMigrations(migrationsFS).
    Router(routerFn).
    Run()
```

No if/else needed — a single configuration works in all environments.

**Workflow for schema changes:**
1. Modify the GORM struct (add field, index, etc.)
2. Create a numbered SQL file: `migrations/00003_add_user_email.sql`
3. Write Up and Down SQL matching the GORM change
4. Test in dev with AutoMigrate, deploy to prod with goose

See `migrate/doc.go` for GORM tag → SQL reference and migration file format.

## Cron Jobs

```go
app.New("my-svc", "v1.0.0", cfg).
    WithCron().
    Router(func(infra app.Infrastructure) http.Handler {
        infra.Cron.Add("cleanup", "0 2 * * *", func(ctx context.Context) error {
            return cleanupOldRecords(ctx, infra.DB)
        })
        return buildRouter(infra)
    }).
    Run()
```

## Audit Log

```go
store := gormstore.New(infra.DB)
store.AutoMigrate()

app.New("my-svc", "v1.0.0", cfg).
    WithPostgres(dbCfg, poolCfg).
    WithAuditLog(store).
    Router(func(infra app.Infrastructure) http.Handler {
        // Programmatic logging:
        infra.AuditLog.LogAction(ctx, "user-1", "delete", "orders/123", "success")

        // Or HTTP middleware for automatic capture:
        handler := auditlog.Middleware(infra.AuditLog,
            auditlog.WithActorExtractor(extractUserID),
        )(router)
        return handler
    }).
    Run()
```

## Anti-Patterns

- **Don't** call `Builder.Run()` without `Router()` — there's no default handler.
- **Don't** use `Infrastructure` fields outside the `RouterFunc` closure — they may not be initialized yet.
- **Don't** ignore nil checks on optional `Infrastructure` fields (DB, Publisher, etc.).
- **Don't** use `WithMariaDB` and `WithPostgres` together — mutually exclusive, panics at validation.
- **Don't** worry about passing both models and migrations — the kit auto-selects AutoMigrate (dev) or goose (prod) based on `ENVIRONMENT`.
