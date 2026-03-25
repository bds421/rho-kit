# Kit — Go Service Toolkit

**Module:** `github.com/bds421/rho-kit`
**Go:** 1.26+ | **License:** Proprietary

Shared infrastructure library for rho platform microservices. Provides secure-by-default, composable packages so services focus on domain logic.

## Commands

```bash
make test          # unit tests
make test-race     # race detector
make test-cover    # coverage report
make lint          # golangci-lint v2
make vulncheck     # govulncheck
make bench         # benchmarks
make fmt           # goimports + gofumpt
make tidy          # go mod tidy
```

Integration tests require Docker and the `integration` build tag:
```bash
go test -tags integration ./...
```

## Golden Path

Every service follows this pattern:

```go
app.Main("my-service", version, func(logger *slog.Logger) error {
    cfg, err := LoadConfig()
    if err != nil { return err }
    return app.New("my-service", version, cfg.BaseConfig).
        WithPostgres(cfg.Database, cfg.DatabasePool, &Model{}).
        WithRedis(&redis.Options{Addr: cfg.RedisAddr}).
        WithRabbitMQ(cfg.AMQPURL).
        WithJWT(cfg.JWKSURL).
        WithIPRateLimit(100, time.Minute).
        WithTracing(tracingCfg).
        Router(func(infra app.Infrastructure) http.Handler {
            mux := http.NewServeMux()
            // register routes using infra.DB, infra.Publisher, etc.
            return stack.Default(mux, logger,
                stack.WithOuter(csrf.RequireCSRF, csrf.RequireJSONContentType),
            )
        }).
        Run()
})
```

For services that outgrow the Builder (custom transports, non-standard shutdown ordering), use `lifecycle.Runner` + `config.Load` directly. See the "Manual Wiring" section in [docs/ai/bootstrap.md](docs/ai/bootstrap.md).

## Package Decision Tree

| I need to... | Use | Recipe |
|---|---|---|
| Bootstrap a service | `app` (Main, Builder, Infrastructure) | [bootstrap](docs/ai/bootstrap.md) |
| Serve HTTP with middleware | `httpx`, `httpx/middleware/stack` | [http](docs/ai/http.md) |
| Authenticate requests (JWT) | `httpx/middleware/auth`, `security/jwtutil` | [http](docs/ai/http.md) |
| Rate-limit requests | `httpx/middleware/ratelimit` | [http](docs/ai/http.md) |
| Typed HTTP handlers (reduce boilerplate) | `httpx` (JSON, JSONNoBody, JSONStatus, NoContent) | [http](docs/ai/http.md) |
| Idempotent HTTP requests | `httpx/middleware/idempotency` | [http](docs/ai/http.md) |
| Distributed locking | `data/lock/redislock` | [redis](docs/ai/redis.md) |
| Composable lifecycle | `runtime/lifecycle` (Runner, Component) | [utilities](docs/ai/utilities.md) |
| Typed context keys | `core/contextutil` | [utilities](docs/ai/utilities.md) |
| Struct-tag config loading | `core/config` | [bootstrap](docs/ai/bootstrap.md) |
| Explicit middleware chains | `httpx/middleware/stack` (Chain) | [http](docs/ai/http.md) |
| Store/retrieve files | `infra/storage` + backend (s3/azure/gcs/sftp/local) | [storage](docs/ai/storage.md) |
| Multi-disk file storage | `storage.Manager` | [storage](docs/ai/storage.md) |
| Encrypt files at rest | `storage/encryption` | [storage](docs/ai/storage.md) |
| Publish/consume AMQP messages | `messaging/amqpbackend` (Publisher, Consumer) | [messaging](docs/ai/messaging.md) |
| Publish/consume Redis Streams | `messaging/redisbackend` (Publisher, Consumer) | [messaging](docs/ai/messaging.md) |
| Buffered message delivery | `messaging.BufferedPublisher` | [messaging](docs/ai/messaging.md) |
| Cache data (single instance) | `cache.MemoryCache` | [utilities](docs/ai/utilities.md) |
| Cache data (shared/distributed) | `data/cache/rediscache` | [redis](docs/ai/redis.md) |
| Event streaming (fan-out) | `data/stream/redisstream` | [redis](docs/ai/redis.md) |
| Task queue (single consumer) | `data/queue/redisqueue` | [redis](docs/ai/redis.md) |
| Cross-service messaging | `infra/messaging` interfaces + backend | [messaging](docs/ai/messaging.md) |
| Connect to MariaDB/PostgreSQL | `infra/sqldb`, `infra/sqldb/gormdb` | [database](docs/ai/sqldb.md) |
| Retry transient failures | `resilience/retry` | [resilience](docs/ai/resilience.md) |
| Protect against cascading failure | `resilience/circuitbreaker` | [resilience](docs/ai/resilience.md) |
| Encrypt DB fields | `crypto/encrypt.FieldEncryptor` | [security](docs/ai/security.md) |
| Sign/verify webhooks (HMAC) | `crypto/signing` | [security](docs/ai/security.md) |
| Verify JWTs (JWKS) | `security/jwtutil` | [security](docs/ai/security.md) |
| mTLS between services | `security/netutil` | [security](docs/ai/security.md) |
| Prevent SSRF | `security/netutil` | [security](docs/ai/security.md) |
| Validate structs | `core/validate` | [utilities](docs/ai/utilities.md) |
| Cursor pagination | `httpx/pagination` | [utilities](docs/ai/utilities.md) |
| Typed application errors | `core/apperror` | [utilities](docs/ai/utilities.md) |
| Authorize requests (RBAC/ABAC) | `authz` | [http](docs/ai/http.md) |
| Consistent structured logging | `observability/logattr` | [utilities](docs/ai/utilities.md) |
| Resilient outbound HTTP calls | `httpx.NewResilientHTTPClient` | [http](docs/ai/http.md) |
| Request-scoped logging | `httpx/middleware/logging.WithRequestLogger`, `httpx.Logger` | [http](docs/ai/http.md) |
| Test HTTP handlers | `httpx/httpxtest` | [testing](docs/ai/testing.md) |
| Redis-backed idempotency | `data/idempotency/redisstore` | [redis](docs/ai/redis.md) |
| Queue depth health check | `redis/queue.DepthCheck` | [redis](docs/ai/redis.md) |
| Write integration tests (DB) | `infra/sqldb/dbtest` | [testing](docs/ai/testing.md) |
| Write integration tests (Redis) | `infra/redis/redistest` | [testing](docs/ai/testing.md) |
| Write integration tests (RabbitMQ) | `messaging/amqpbackend/rabbitmqtest` | [testing](docs/ai/testing.md) |
| Test storage backends | `testutil/storagetest` | [testing](docs/ai/testing.md) |
| In-memory DB for unit tests | `testutil/memdb` | [testing](docs/ai/testing.md) |
| In-memory broker for unit tests | `messaging/membroker` | [testing](docs/ai/testing.md) |

## Key Conventions

- **Env vars**: `UPPER_SNAKE_CASE`. Secrets use `{PREFIX}_` prefix and support `_FILE` suffix for mounted secrets.
- **Error handling**: Return typed `core/apperror` errors using `apperror.Code` enum (`CodeNotFound`, `CodeValidation`, `CodeConflict`, `CodeAuthRequired`, `CodeForbidden`, `CodeRateLimit`, `CodeOperationFailed`, `CodePermanent`, `CodeUnavailable`). `httpx.WriteServiceError` maps them to HTTP status codes automatically via `apperror.HTTPStatus()`. Every error type implements `Retryable() bool` — use `apperror.ShouldRetry` as a predicate for retry middleware (e.g. `retry.WithRetryIf(apperror.ShouldRetry)`). Error codes are transport-agnostic and map cleanly to both HTTP and gRPC status codes.
- **Metrics**: All Prometheus metrics accept `prometheus.Registerer` via `WithRegisterer()` options. Defaults to `prometheus.DefaultRegisterer` for zero-config usage. Use custom registerers for test isolation.
- **Permanent errors**: Wrap with `apperror.NewPermanent()` to skip retries in consumers.
- **Operation errors**: Use `apperror.NewOperationFailed()` for known failures with client-safe messages (vs untyped errors which get generic "internal error").
- **Unavailable errors**: Use `apperror.NewUnavailable()` when the service itself is not ready (maps to 503), or `apperror.NewDependencyUnavailable("redis", msg, cause)` when an upstream dependency is down (maps to 502). Both are retryable.
- **Fail fast**: Configuration errors panic at startup (nil backends, empty names). Runtime errors return `error`.
- **Health checks**: Internal port `:9090` serves `/ready` (dependency health) and `/metrics` (Prometheus).
- **TLS**: Set `TLS_CA_CERT`, `TLS_CERT`, `TLS_KEY` together to enable mTLS globally.
- **Middleware order**: Always use `stack.Default()` — it enforces: outer → metrics → requestID → tracing → logging → inner → handler.

## Anti-Patterns

- **Never** create `http.Server` directly — use `httpx.NewServer` (safe timeouts, header limits).
- **Never** use `http.DefaultClient` — use `httpx.NewHTTPClient` or `infra.HTTPClient`.
- **Never** embed user IDs or request IDs in Redis/Prometheus metric names — causes cardinality explosion.
- **Never** use raw client filenames as storage keys — use `storagehttp.UUIDKeyFunc`.
- **Never** skip `validate.Struct()` on user input — it returns `apperror.ValidationError` with field details.
- **Never** call `WithMariaDB` and `WithPostgres` together — they are mutually exclusive.
- **Never** ACK messages on transient errors — return the error so retry/DLX handles it.
- **Never** use `idempotency.NewMemoryStore()` in production — it only works on a single instance. Use `redis/redisstore.New()` for multi-instance deployments.
- **Never** store `SSRFSafeTransport` long-term — the resolved IP may go stale. Create a new transport per request.
- **Never** call `Lock.Acquire` twice without `Release` — the second acquire will return an error.
- **Never** pass zero/negative values to `NewRateLimiter`, `NewKeyedRateLimiter`, `Timeout`, or `MaxBodySize` — they panic on misconfiguration.

## Keeping Docs in Sync

When adding new `With*()` methods to the Builder, new middleware, or new storage backends, update the corresponding `docs/ai/*.md` recipe file and the decision tree above.
