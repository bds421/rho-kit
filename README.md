# rho-kit

`rho-kit` is the standard Go service toolkit. It centralizes the
infrastructure patterns every service needs so teams can focus on domain logic
while staying consistent, secure, and observable.

**Why it exists**
- Provide a single, opinionated "golden path" for service startup.
- Eliminate repeated boilerplate around logging, tracing, health, and config.
- Ship hardened primitives for data stores, messaging, and security.

**Design principles**
- Secure by default and fail fast on misconfiguration.
- Small, composable packages with explicit boundaries.
- Observability is never optional (logs, metrics, traces are first‑class).
- Safe defaults that scale in production without surprises.

## Golden path: `app`

`app` is the standard service entry point. It wires logging, health
endpoints, lifecycle management, and optional infrastructure like databases,
RabbitMQ, and JWT verification.

```go
app.Main("backend", handler.Version, func(logger *slog.Logger) error {
    cfg, err := config.Load()
    if err != nil {
        return err
    }

    return app.New("backend", handler.Version, cfg.BaseConfig).
        WithMariaDB(cfg.Database, cfg.DatabasePool, &model.User{}).
        WithRabbitMQ(cfg.RabbitMQ.URL).
        WithJWT(cfg.JWKSURL).
        WithIPRateLimit(100, time.Minute).
        Router(func(infra app.Infrastructure) http.Handler {
            return router.New(infra, logger)
        }).
        Run()
})
```

For PostgreSQL, swap `WithMariaDB` with `WithPostgres` and use
`sqldb.PostgresConfig` / `sqldb.PoolConfig` (or `sqldb.LoadPostgresFields`).
See `examples/app` for a full example.

## HTTP stack (recommended)

```go
handler := stack.Default(router, logger,
    stack.WithOuter(csrf.RequireJSONContentType, csrf.RequireCSRF),
)
```

## Redis (example)

```go
conn, err := redis.Connect(&goredis.Options{Addr: "localhost:6379"}, redis.WithInstance("cache"))
if err != nil {
    log.Fatal(err)
}

c, err := cache.NewRedisCache(conn.Client(), "api-cache")
if err != nil {
    log.Fatal(err)
}

_ = c.Set(ctx, "user:123", []byte("..."), time.Minute)
```

## Usage

```bash
go get github.com/bds421/rho-kit
```

## Package map

- `app` – service bootstrap, infrastructure wiring, graceful shutdown.
- `core/config` / `security/netutil` – configuration, env parsing, mTLS helpers.
- `observability/logging` / `observability/tracing` – structured logging and OpenTelemetry setup.
- `observability/health` – readiness/liveness types and dependency checks.
- `httpx` – HTTP server, JSON helpers, traced HTTP clients.
- `httpx/middleware/*` – request ID, auth, CSRF, metrics, rate limit, timeout, client IP, tracing.
- `httpx/middleware/stack` – canonical middleware ordering helper.
- `httpx/healthhttp` – readiness/liveness/metrics HTTP handler.
- `httpx/authz` – route-level authorization.
- `httpx/pagination` – cursor-based pagination.
- `infra/sqldb` / `infra/sqldb/gormdb` – DB config, DSN helpers, GORM setup.
- `infra/redis` + subpackages – resilient Redis connection, cache, stream, queue, locks.
- `infra/messaging` – message types, outbox publisher, consumer framework.
- `messaging/amqpbackend` – RabbitMQ connections, topology, consumers, publishers.
- `messaging/redisbackend` – Redis Streams messaging backend.
- `infra/storage` + backends – file storage with S3, Azure, GCS, SFTP, local backends.
- `core/cache` – backend‑agnostic cache interface + memory cache.
- `resilience/retry` / `resilience/circuitbreaker` – resilience patterns for transient failures.
- `crypto/encrypt` / `crypto/signing` / `crypto/masking` – crypto helpers and safe masking.
- `security/jwtutil` – JWKS-based JWT verification.
- `core/idempotency` – idempotent request store interface.
- `security/netutil` / `io/atomicfile` / `core/validate` – focused utilities.
- `testutil/*` – testcontainers helpers for storage backends.

## Conventions and notes

- `RequireUserWithJWT` panics if the provider is nil to prevent accidental auth bypass.
- Some packages intentionally **panic** on programmer errors to fail fast.
- `httpx` intentionally avoids the stdlib `net/http/httputil` name collision.
- Resource names are used as Prometheus labels; keep them small and static.

## Common env vars

- `ENVIRONMENT` – set to `development` to enable dev-only behavior.
- `LOG_LEVEL` – `debug`, `info`, `warn`, `error`.
- `SERVER_HOST`, `SERVER_PORT`, `INTERNAL_PORT` – HTTP bind settings.
- `TLS_CA_CERT`, `TLS_CERT`, `TLS_KEY` – enable mTLS when all are set.
- `DB_HOST`, `DB_PORT`, `<PREFIX>_DB_USER/_DB_PASSWORD/_DB_NAME` – DB config.
- `DB_SSL_MODE` – PostgreSQL SSL mode (disable, allow, prefer, require, verify-ca, verify-full).
- `RABBITMQ_URL` – AMQP URL (Docker secrets supported via `_FILE`).
