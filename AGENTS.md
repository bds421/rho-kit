# Kit — Go Service Toolkit

**Repo:** `github.com/bds421/rho-kit` (multi-module monorepo, 65 Go modules at `/v2` path suffix)
**Go:** 1.26+ | **License:** Proprietary

Shared infrastructure library for rho platform microservices. Provides secure-by-default, composable packages so services focus on domain logic.

Snippet status: command blocks are executable from the repository root unless
noted otherwise. The Go block below is an illustrative golden-path shape; the
buildable paths are `cmd/kit-new` scaffolds and `examples/agentic-service`.

## Commands

```bash
make test          # unit tests
make test-race     # race detector
make test-integration # Docker-backed integration tests
make test-cover    # coverage report
make lint          # golangci-lint v2
make vulncheck     # govulncheck
make check-dependency-allowlist # direct external Go dependency policy
make check-dependency-boundaries # keep heavy SDKs behind adapters/test helpers
make check-publishable # pre-tag Go module release invariants
make release-candidate # full local pre-release quality gate
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
        WithPostgres(cfg.Postgres).
        WithRedis(&redis.Options{Addr: cfg.RedisAddr}).
        WithRabbitMQ(cfg.AMQPURL).
        WithJWTAudience("my-service").
        WithJWT(cfg.JWKSURL).
        WithIPRateLimit(100, time.Minute).
        WithTracing(tracingCfg).
        Router(func(infra app.Infrastructure) http.Handler {
            // Auto-migrate / register repositories using infra.DB here.
            mux := http.NewServeMux()
            // register routes using infra.DB, infra.Publisher, etc.
            csrfMW := csrf.New(csrf.WithSecret(cfg.CSRFSecret))
            return stack.Default(mux, logger,
                stack.WithOuter(csrfMW, csrf.RequireJSONContentType),
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
| Fan-out N tasks concurrently | `runtime/concurrency` (FanOut, FanOutSettled) | [utilities](docs/ai/utilities.md) |
| Composable lifecycle | `runtime/lifecycle` (Runner, Component) | [utilities](docs/ai/utilities.md) |
| Typed context keys | `core/contextutil` | [utilities](docs/ai/utilities.md) |
| Struct-tag config loading | `core/config` | [bootstrap](docs/ai/bootstrap.md) |
| Explicit middleware chains | `httpx/middleware/stack` (Chain) | [http](docs/ai/http.md) |
| Store/retrieve files | `infra/storage` + backend (s3/azure/gcs/sftp/local) | [storage](docs/ai/storage.md) |
| Multi-disk file storage | `storage.Manager` | [storage](docs/ai/storage.md) |
| Encrypt files at rest | `storage/encryption` | [storage](docs/ai/storage.md) |
| Scan uploaded files for malware | `storagehttp/uploadsec` + `storagehttp/uploadsec/clamav` | [storage](docs/ai/storage.md) |
| Publish/consume AMQP messages | `messaging/amqpbackend` (Publisher, Consumer) | [messaging](docs/ai/messaging.md) |
| Publish/consume Redis Streams | `messaging/redisbackend` (Publisher, Consumer) | [messaging](docs/ai/messaging.md) |
| Buffered message delivery | `messaging.BufferedPublisher` | [messaging](docs/ai/messaging.md) |
| Bound message size per route | `messaging.MessageSizeLimiter`, Builder `WithMaxMessageBytes` / `WithRouteMaxMessageBytes` | [messaging](docs/ai/messaging.md) |
| Cache data (single instance) | `cache.MemoryCache` | [utilities](docs/ai/utilities.md) |
| Cache data (shared/distributed) | `data/cache/rediscache` | [redis](docs/ai/redis.md) |
| Event streaming (fan-out) | `data/stream/redisstream` | [redis](docs/ai/redis.md) |
| Task queue (single consumer) | `data/queue/redisqueue` | [redis](docs/ai/redis.md) |
| Cross-service messaging | `infra/messaging` interfaces + backend | [messaging](docs/ai/messaging.md) |
| Connect to PostgreSQL | `infra/sqldb`, `infra/sqldb/pgx` | [database](docs/ai/sqldb.md) |
| Retry transient failures | `resilience/retry` | [resilience](docs/ai/resilience.md) |
| Protect against cascading failure | `resilience/circuitbreaker` | [resilience](docs/ai/resilience.md) |
| Encrypt DB fields | `crypto/encrypt.FieldEncryptor` | [security](docs/ai/security.md) |
| Sign/verify webhooks (HMAC) | `crypto/signing` | [security](docs/ai/security.md) |
| Verify JWTs (JWKS) | `security/jwtutil` | [security](docs/ai/security.md) |
| Revoke JWTs after logout | `security/jwtutil/revocation` | [security](docs/ai/security.md) |
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
| Write integration tests (DB) | `infra/sqldb/dbtest/v2` | [testing](docs/ai/testing.md) |
| Write integration tests (Redis) | `infra/redis/redistest/v2` | [testing](docs/ai/testing.md) |
| Write integration tests (RabbitMQ) | `infra/messaging/amqpbackend/integrationtest/v2/rabbitmqtest` | [testing](docs/ai/testing.md) |
| Test storage backends | `infra/storage/storagetest/v2` | [testing](docs/ai/testing.md) |
| In-memory broker for unit tests | `messaging/membroker` | [testing](docs/ai/testing.md) |
| Safe integer cast (no silent overflow) | `core/safecast` | [utilities](docs/ai/utilities.md) |
| Cryptographically random strings (OTPs, tokens) | `core/randstr` | [utilities](docs/ai/utilities.md) |
| Zeroizable secret type | `core/secret` | [utilities](docs/ai/utilities.md) |
| Tenant-aware identity and scoped keys | `core/tenant` | [utilities](docs/ai/utilities.md) |
| Per-tenant cost / spend ledger | `data/budget` (+ `memory`/`redis` backends) | [redis](docs/ai/redis.md) |
| Async approval workflows | `data/approval` (+ `memory`/`postgres`) | [utilities](docs/ai/utilities.md) |
| Append-only chained action log | `data/actionlog` (+ `memory`/`postgres`) | [utilities](docs/ai/utilities.md) |
| In-memory rate limiter (token bucket) | `data/ratelimit/tokenbucket` | [http](docs/ai/http.md) |
| In-memory rate limiter (smooth, GCRA) | `data/ratelimit/gcra` | [http](docs/ai/http.md) |
| Distributed rate limiter (Redis GCRA) | `data/ratelimit/redis` | [redis](docs/ai/redis.md) |
| Per-tenant cache scoping | `data/cache/tenant` | [redis](docs/ai/redis.md) |
| Per-tenant idempotency scoping | `data/idempotency/tenant` | [redis](docs/ai/redis.md) |
| Postgres advisory lock | `data/lock/pgadvisory` | [database](docs/ai/sqldb.md) |
| MCP-compatible HTTP handlers | `httpx/mcp` | [http](docs/ai/http.md) |
| HMAC request signing | `httpx/sign`, `httpx/middleware/signedrequest` | [security](docs/ai/security.md) |
| Safe URL helpers and redirects | `httpx/urlutil`, `httpx.SafeRedirect` | [http](docs/ai/http.md) |
| HTTP request budget enforcement | `httpx/budget`, `httpx/middleware/budget` | [http](docs/ai/http.md) |
| Postgres-backed idempotency | `data/idempotency/pgstore` | [database](docs/ai/sqldb.md) |
| PASETO v4 token issuance / verification | `crypto/paseto` | [security](docs/ai/security.md) |
| Argon2id password hashing | `crypto/passhash` | [security](docs/ai/security.md) |
| Envelope encryption (DEK + KEK) | `crypto/envelope`, `crypto/envelope/kekstatic` | [security](docs/ai/security.md) |
| RED metrics for HTTP/gRPC handlers | `observability/redmetrics` | [observability](docs/ai/observability.md) |
| Go runtime metrics | `observability/runtimemetrics` | [observability](docs/ai/observability.md) |
| SLO checker (latency, error/success rate) | `observability/slo` | [observability](docs/ai/observability.md) |
| pprof profiling endpoint (internal port only) | `observability/pprof` | [observability](docs/ai/observability.md) |
| Service health check binary | `cmd/kit-doctor`, `observability/health.RunHealthCheck` | [observability](docs/ai/observability.md) |
| Scaffold a new service | `cmd/kit-new` (`-tenant` for tenant-aware Redis/cache/idempotency) | — |
| Performance regression gate | `cmd/kit-bench-gate` | — |
| NATS JetStream messaging | `infra/messaging/natsbackend` | [messaging](docs/ai/messaging.md) |
| Leader election | `infra/leaderelection` (`pgadvisory`/`redislock`) | [redis](docs/ai/redis.md) |

## Key Conventions

- **Env vars**: `UPPER_SNAKE_CASE`. Secrets use `{PREFIX}_` prefix and support `_FILE` suffix for mounted secrets.
- **Error handling**: Return typed `core/apperror` errors using `apperror.Code` enum (`CodeNotFound`, `CodeValidation`, `CodeConflict`, `CodeAuthRequired`, `CodeForbidden`, `CodeRateLimit`, `CodeOperationFailed`, `CodePermanent`, `CodeUnavailable`). `httpx.WriteServiceError` maps them to HTTP status codes automatically via `httpx.HTTPStatus()`. Every error type implements `Retryable() bool` — use `apperror.ShouldRetry` as a predicate for retry middleware (e.g. `retry.WithRetryIf(apperror.ShouldRetry)`). Error codes are transport-agnostic; HTTP mapping lives in `httpx`, not in `core/apperror`.
- **Metrics**: All Prometheus metrics accept `prometheus.Registerer` via `WithRegisterer()` options. Defaults to `prometheus.DefaultRegisterer` for zero-config usage. Use custom registerers for test isolation.
- **Permanent errors**: Wrap with `apperror.NewPermanent()` to skip retries in consumers.
- **Operation errors**: Use `apperror.NewOperationFailed()` for non-retryable operation failures that should be logged and typed; HTTP adapters return a generic `internal error` body for both operation failures and untyped errors.
- **Unavailable errors**: Use `apperror.NewUnavailable()` when the service itself is not ready, or `apperror.NewDependencyUnavailable("redis", msg, cause)` when an upstream dependency is down. Both are retryable. HTTP status mapping (502 vs 503) is handled by `httpx.HTTPStatus()`.
- **Fail fast**: Configuration errors panic at startup (nil backends, empty names). Runtime errors return `error`.
- **Health checks**: Internal port `:9090` serves `/ready` (dependency health), `/metrics` (Prometheus), and gRPC health over h2c. Public gRPC health requires `WithPublicGRPCHealth()`.
- **TLS**: Set `TLS_CA_CERT`, `TLS_CERT`, `TLS_KEY` together to enable mTLS globally.
- **Middleware order**: Always use `stack.Default()` — it enforces: outer → metrics → requestID → tracing → logging → inner → handler.
- **Scaffolds**: Use `kit-new -tenant` for new multi-tenant services so Redis cache and idempotency start behind the tenant wrappers.
- **Dependencies**: New direct external Go modules must be added to `docs/audit/dependency-allowlist.txt` in the same change; `make check-dependency-allowlist` rejects unreviewed or stale direct dependencies.
- **Messaging size**: Builder-created RabbitMQ and NATS publishers enforce `messaging.DefaultMaxMessageBytes`; use `WithRouteMaxMessageBytes` for explicitly large routes instead of disabling the cap globally.

## Anti-Patterns

- **Never** create or serve with `net/http` server entrypoints (`http.Server`, `http.ListenAndServe`, `http.Serve`) directly — use `httpx.NewServer` (safe timeouts, header limits).
- **Never** use `http.DefaultClient` — use `httpx.NewHTTPClient` or `infra.HTTPClient`.
- **Never** embed user IDs or request IDs in Redis/Prometheus metric names — causes cardinality explosion.
- **Never** use raw client filenames as storage keys — use `storagehttp.UUIDKeyFunc`.
- **Never** store user uploads when malware scanning fails or returns inconclusive — treat `uploadsec.ErrScannerUnavailable` as fail-closed.
- **Never** skip `validate.Struct()` on user input — it returns `apperror.ValidationError` with field details.
- **Never** import Docker-backed test helpers from base packages — keep Testcontainers helpers in split modules such as `infra/sqldb/dbtest/v2`.
- **Never** ACK messages on transient errors — return the error so retry/DLX handles it.
- **Never** disable message-size limits globally just to support one large event type — add a route-specific override with `WithRouteMaxMessageBytes`.
- **Never** use `idempotency.NewMemoryStore()` in production — it only works on a single instance. Use `data/idempotency/redisstore.New()` or `data/idempotency/pgstore.New()` for multi-instance deployments.
- **Never** hand-roll tenant-scoped Redis/cache/idempotency keys with a literal `tenant:` prefix — use `core/tenant.Key` or the tenant wrappers.
- **Never** introduce a direct external Go dependency without updating `docs/audit/dependency-allowlist.txt` and passing `make check-dependency-allowlist`.
- **Never** store `SSRFSafeTransport` long-term — the resolved IP may go stale. Create a new transport per request.
- **Never** call `Lock.Acquire` twice without `Release` — the second acquire will return an error.
- **Never** pass zero/negative values to `NewRateLimiter`, `NewKeyedRateLimiter`, `Timeout`, or `MaxBodySize` — they panic on misconfiguration.

## Keeping Docs in Sync

When adding new `With*()` methods to the Builder, new middleware, or new storage backends, update the corresponding `docs/ai/*.md` recipe file and the decision tree above.
