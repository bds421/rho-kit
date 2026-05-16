# Kit — Go Service Toolkit

**Repo:** `github.com/bds421/rho-kit` (multi-module monorepo, 77 Go modules at `/v2` path suffix)
**Go:** 1.26+ | **License:** Apache 2.0

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
make check-operational-readiness # operational-review coverage for every module
make check-publishable # pre-tag Go module release invariants
make check-dashboards # Grafana JSON + Prometheus rule validation
make release-candidate # full local pre-release quality gate
make bench         # benchmarks
make bench-baseline # capture v2 benchmark baselines for kit-bench-gate
make fmt           # goimports + gofumpt
make tidy          # go mod tidy
```

Integration tests require Docker and the `integration` build tag:
```bash
go test -tags integration ./...
```

## Golden Path

Every service follows this pattern. The snippet below is a complete
`package main` that compiles against the v2 API at HEAD; copy-paste and
fill in `cfg.JWKSURL` etc. with the env-loaded values from your service.

```go
package main

import (
    "log/slog"
    "net/http"
    "time"

    goredis "github.com/redis/go-redis/v9"

    "github.com/bds421/rho-kit/app/v2"
    "github.com/bds421/rho-kit/app/amqp/v2"
    "github.com/bds421/rho-kit/app/postgres/v2"
    "github.com/bds421/rho-kit/app/redis/v2"
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
            With(postgres.Module(pgxbackend.Config{DSN: "postgres://localhost/my-service"})).
            With(redis.Module(&goredis.Options{Addr: "rediss://cache.internal:6379", Password: "***"})).
            With(amqp.Module("amqps://broker.internal")).
            With(jwt.Module("https://issuer.example.com/.well-known/jwks.json",
                jwt.WithIssuer("https://issuer.example.com"),
                jwt.WithAudience("my-service"),
            )).
            With(ratelimit.IP(100, time.Minute)).
            Router(func(infra app.Infrastructure) http.Handler {
                mux := http.NewServeMux()
                // Register routes using postgres.Pool(infra), redis.Connection(infra),
                // amqp.Publisher(infra), etc.
                return stack.Default(mux, infra.Logger)
            }).
            Run()
    })
}
```

Notes:
- `app.LoadBaseConfig(8080)` reads `SERVER_PORT`, `SERVER_HOST`,
  `INTERNAL_PORT`, `ENVIRONMENT`, `LOG_LEVEL`, and `TLS_*` from the
  environment; pass the per-service default port.
- `BaseConfig` lives in package `app`, not `core/config`.
- `app.New(name, version, cfg)` returns a `*Builder`; methods chain.
- Adapter wiring (Postgres, Redis, RabbitMQ, NATS, OTel tracing, public
  gRPC) lives in per-adapter sub-modules under `app/`. Register each via
  `Builder.With(<adapter>.Module(...))`. Importing only `app/v2` no
  longer pulls pgx, go-redis, amqp091, nats.go, otelgrpc, or grpc-go.
- Non-loopback Redis MUST set `TLSConfig` (or a `rediss://` URL) and a
  non-empty `Password` — `redis.Module` rejects plaintext URIs (FR-077)
  unless you opt out with `redis.Module(..., redis.WithoutTLS())`
  on a reviewed boundary.
- Non-loopback AMQP MUST use `amqps://` — `amqp.Module` rejects
  plaintext `amqp://` URLs (mirrors FR-077) unless you opt out with
  `amqp.WithoutTLS()` on a reviewed boundary.

For services that outgrow the Builder (custom transports, non-standard shutdown ordering), use `lifecycle.Runner` + `config.Load` directly. See the "Manual Wiring" section in [docs/ai/bootstrap.md](docs/ai/bootstrap.md). New downstream services should also read [docs/ai/adoption.md](docs/ai/adoption.md) for the `go.mod` and common-mistakes checklist.

## Package Decision Tree

> **Import paths.** All packages are at `github.com/bds421/rho-kit/<table-path>/v2` (the `/v2` suffix is mandatory per Go module versioning).

| I need to... | Use | Recipe |
|---|---|---|
| Bootstrap a service | `app` (Main, Builder, Infrastructure) | [bootstrap](docs/ai/bootstrap.md) |
| Serve HTTP with middleware | `httpx`, `httpx/middleware/stack` | [http](docs/ai/http.md) |
| Authenticate requests (JWT) | `httpx/middleware/auth`, `security/jwtutil` | [http](docs/ai/http.md) |
| Rate-limit requests | `httpx/middleware/ratelimit` | [http](docs/ai/http.md) |
| Typed HTTP handlers (reduce boilerplate) | `httpx.JSON[Req,Resp](logger, func(ctx, *http.Request, Req) (Resp, error))`; siblings `JSONNoBody[Resp](logger, func(ctx, *http.Request) (Resp, error))`, `JSONStatus[Req,Resp](logger, func(ctx, *http.Request, Req) (int, Resp, error))`, `JSONNoBodyStatus[Resp](logger, func(ctx, *http.Request) (int, Resp, error))`, `NoContent(logger, func(ctx, *http.Request) error)`. Mux-bound wrappers: `httpx.Handle/HandleNoBody/HandleStatus/HandleNoBodyStatus`. | [http](docs/ai/http.md) |
| Idempotent HTTP requests | `httpx/middleware/idempotency` | [http](docs/ai/http.md) |
| Distributed locking | `data/lock/redislock` | [redis](docs/ai/redis.md) |
| Fan-out N tasks concurrently | `runtime/concurrency` (FanOut, FanOutSettled) | [utilities](docs/ai/utilities.md) |
| Composable lifecycle | `runtime/lifecycle` (Runner, Component) | [utilities](docs/ai/utilities.md) |
| Typed context keys | `core/contextutil` | [utilities](docs/ai/utilities.md) |
| Struct-tag config loading | `core/config` | [bootstrap](docs/ai/bootstrap.md) |
| Explicit middleware chains | `httpx/middleware/stack` (Chain) | [http](docs/ai/http.md) |
| Store/retrieve files | `infra/storage` + backend (s3/azure/gcs/sftp/local) | [storage](docs/ai/storage.md) |
| Multi-disk file storage | `infra/storage` (Manager) | [storage](docs/ai/storage.md) |
| Encrypt files at rest | `infra/storage/encryption` | [storage](docs/ai/storage.md) |
| Scan uploaded files for malware | `infra/storage/storagehttp/uploadsec` + `infra/storage/storagehttp/uploadsec/clamav` | [storage](docs/ai/storage.md) |
| Publish/consume AMQP messages | `infra/messaging/amqpbackend` (Publisher, Consumer) | [messaging](docs/ai/messaging.md) |
| Publish/consume Redis Streams | `infra/messaging/redisbackend` (Publisher, Consumer) | [messaging](docs/ai/messaging.md) |
| Buffered message delivery | `messaging.BufferedPublisher` | [messaging](docs/ai/messaging.md) |
| Bound message size per route | `messaging.MessageSizeLimiter`, `amqp.WithMessageSizeLimiter` / `nats.WithMessageSizeLimiter` | [messaging](docs/ai/messaging.md) |
| Cache data (single instance) | `data/cache` (MemoryCache) | [utilities](docs/ai/utilities.md) |
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
| Outbound HTTP client (mTLS-aware, no resilience) | `httpx.NewHTTPClient` / `httpx.NewTracingHTTPClient` | [http](docs/ai/http.md) |
| Resilient outbound HTTP calls | `httpx.NewResilientHTTPClient` | [http](docs/ai/http.md) |
| Request-scoped logging | `httpx/middleware/logging.WithRequestLogger`, `httpx.Logger` | [http](docs/ai/http.md) |
| Test HTTP handlers | `httpx/httpxtest` | [testing](docs/ai/testing.md) |
| Redis-backed idempotency | `data/idempotency/redisstore` | [redis](docs/ai/redis.md) |
| Queue depth health check | `data/queue/redisqueue.Queue.DepthCheck` | [redis](docs/ai/redis.md) |
| Write integration tests (DB) | `infra/sqldb/dbtest/v2` | [testing](docs/ai/testing.md) |
| Write integration tests (Redis) | `infra/redis/redistest/v2` | [testing](docs/ai/testing.md) |
| Write integration tests (RabbitMQ) | `infra/messaging/amqpbackend/integrationtest/v2/rabbitmqtest` | [testing](docs/ai/testing.md) |
| Test storage backends | `infra/storage/storagetest/v2` | [testing](docs/ai/testing.md) |
| In-memory broker for unit tests | `infra/messaging/membroker` | [testing](docs/ai/testing.md) |
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
| MCP-compatible HTTP handlers | `httpx/mcp` (NewServer, Register[In,Out]) | [http](docs/ai/http.md) |
| HMAC request signing | `httpx/sign`, `httpx/middleware/signedrequest` | [security](docs/ai/security.md) |
| Safe URL helpers and redirects | `httpx/urlutil`, `httpx.SafeRedirect` | [http](docs/ai/http.md) |
| HTTP request budget enforcement | `httpx/budget`, `httpx/middleware/budget` | [http](docs/ai/http.md) |
| Postgres-backed idempotency | `data/idempotency/pgstore` | [database](docs/ai/sqldb.md) |
| PASETO v4 token issuance / verification | `crypto/paseto` | [security](docs/ai/security.md) |
| Argon2id password hashing | `crypto/passhash` | [security](docs/ai/security.md) |
| Envelope encryption (DEK + KEK) | `crypto/envelope`, `crypto/envelope/kekstatic` | [security](docs/ai/security.md) |
| Wrap envelope DEKs with managed KMS / Vault | `crypto/envelope/awskms`, `crypto/envelope/azurekeyvault`, `crypto/envelope/gcpkms`, `crypto/envelope/vaulttransit` | [security](docs/ai/security.md) |
| RED metrics for HTTP/gRPC handlers | `observability/redmetrics` | [observability](docs/ai/observability.md) |
| Go runtime metrics | `observability/runtimemetrics` | [observability](docs/ai/observability.md) |
| SLO checker (latency, error/success rate) | `observability/slo` | [observability](docs/ai/observability.md) |
| pprof profiling endpoint (internal port only) | `observability/pprof` | [observability](docs/ai/observability.md) |
| Service health check binary | `app.Main` `--health` flag (invokes `observability/health.RunHealthCheck`) | [observability](docs/ai/observability.md) |
| Per-request health endpoints | `httpx/healthhttp` | [observability](docs/ai/observability.md) |
| Tamper-evident audit log | `observability/auditlog` (in-process `MemoryStore`) → `observability/auditlog/postgres` (durable, schema via `cmd/kit-migrate auditlog`) | [observability](docs/ai/observability.md) |
| Transactional outbox (at-least-once messaging, DB + broker) | `infra/outbox` (Relay) + `infra/outbox/postgres` (durable Store; schema via `cmd/kit-migrate outbox`; `WithTx`/`RequireTx` for caller-tx atomicity) | [messaging](docs/ai/messaging.md) |
| Distributed tracing helpers | `observability/tracing` | [observability](docs/ai/observability.md) |
| RFC 7807 problem-details responses | `httpx/problemdetails` | [http](docs/ai/http.md) |
| OpenAPI helpers | `httpx/openapi` | [http](docs/ai/http.md) |
| Run a gRPC service | `grpcx` (Server, RegisterServices) | [http](docs/ai/http.md) |
| Scaffold a new service | `cmd/kit-new` (`-tenant` for tenant-aware Redis/cache/idempotency) | — |
| Audit a service for security regressions | `cmd/kit-doctor` | [observability](docs/ai/observability.md) |
| Verify a running service's ASVS controls | `cmd/kit-verify` | [security](docs/ai/security.md) |
| Performance regression gate | `cmd/kit-bench-gate` | — |
| NATS JetStream messaging | `infra/messaging/natsbackend` | [messaging](docs/ai/messaging.md) |
| Postgres-backed durable job queue | `data/queue/riverqueue` | [database](docs/ai/sqldb.md) |
| Leader election | `infra/leaderelection` (`pgadvisory`/`redislock`) | [redis](docs/ai/redis.md) |
| Run scheduled tasks (cron) | `runtime/cron` (Scheduler) | [utilities](docs/ai/utilities.md) |
| Batched background workers | `runtime/batchworker` (Worker) | [utilities](docs/ai/utilities.md) |
| In-process event bus (domain events) | `runtime/eventbus` (Bus, Publish, Subscribe) | [utilities](docs/ai/utilities.md) |
| Feature flags / runtime toggles | `flags` (register via `app/flags`) | [utilities](docs/ai/utilities.md) |
| Atomic file writes | `io/atomicfile` | [utilities](docs/ai/utilities.md) |
| Progress-reporting readers | `io/progress` | [utilities](docs/ai/utilities.md) |
| Mask PII for logs/exports (field/value masking) | `crypto/masking` | [security](docs/ai/security.md) |
| ASVS control catalog | `security/asvs` | [security](docs/ai/security.md) |
| mTLS identity (peer cert claims) | `security/mtlsidentity` | [security](docs/ai/security.md) |
| CSRF helpers (token mint/verify) | `security/csrf` | [security](docs/ai/security.md) |
| Rotate infrastructure credentials | `pgxbackend.PasswordProvider`, Redis credential providers, AMQP/NATS auth providers, storage provider credentials, CSRF/signed-request key rings | [credential rotation](docs/ai/credential-rotation.md) |

## Key Conventions

- **Env vars**: `UPPER_SNAKE_CASE`. Secrets use `{PREFIX}_` prefix and support `_FILE` suffix for mounted secrets.
- **Error handling**: Return typed `core/apperror` errors using `apperror.Code` enum (`CodeNotFound`, `CodeValidation`, `CodeConflict`, `CodeAuthRequired`, `CodeForbidden`, `CodeRateLimit`, `CodeOperationFailed`, `CodePermanent`, `CodeUnavailable`). `httpx.WriteServiceError` maps them to HTTP status codes automatically via `httpx.HTTPStatus()`. Every error type implements `Retryable() bool` — use `apperror.ShouldRetry` as a predicate for retry middleware (e.g. `retry.WithRetryIf(apperror.ShouldRetry)`). Error codes are transport-agnostic; HTTP mapping lives in `httpx`, not in `core/apperror`.
- **Metrics**: All Prometheus metric constructors expose `NewMetrics(opts ...MetricsOption)` (positional `NewMetrics(reg)` is gone) and default to `prometheus.DefaultRegisterer` for zero-config usage. Naming follows two stable v2 conventions:
  - `WithRegisterer(MetricsOption)` is the canonical inner option, used by every single-purpose metrics constructor (e.g. `infra/outbox`, `infra/redis`, `infra/storage/{azure,gcs,s3,sftp}backend`, `infra/leaderelection/{pgadvisory,redislock}`, `httpx/middleware/signedrequest`, `data/queue/redisqueue`, `data/cache/rediscache`).
  - `WithMetricsRegisterer(Option)` is the canonical outer option, used only when a top-level constructor (`ConnOption` / `CacheOption` / backend `Option` / `ServerOption`) threads a registerer through to its inner metrics builder — see `infra/redis.WithMetricsRegisterer` (ConnOption), `data/cache/rediscache.WithMetricsRegisterer` (CacheOption), `infra/storage/{azure,gcs,s3,sftp}backend.WithMetricsRegisterer` (backend Option), `data/queue/redisqueue.WithMetricsRegisterer` (queue Option), `grpcx.WithMetricsRegisterer` (ServerOption).
  - `WithHTTPRegisterer` / `WithBatchRegisterer` (in `observability/redmetrics`) and `WithProducerMetricsRegisterer` / `WithConsumerMetricsRegisterer` (in `data/stream/redisstream`) are the prefix-by-constructor variant: each package exposes multiple distinct metric sets in one package, so the registerer option carries the discriminator prefix.
  Wave 64 finished the canonicalisation: every inner `MetricsOption` is now `WithRegisterer`, every outer-Option spelling is `WithMetricsRegisterer`. The pre-wave `MetricsWithRegisterer` outlier is gone. Use custom registerers for test isolation.
- **Permanent errors**: Wrap with `apperror.NewPermanent()` to skip retries in consumers.
- **Operation errors**: Use `apperror.NewOperationFailed()` for non-retryable operation failures that should be logged and typed; HTTP adapters return a generic `internal error` body for both operation failures and untyped errors.
- **Unavailable errors**: Use `apperror.NewUnavailable()` when the service itself is not ready, or `apperror.NewDependencyUnavailable("redis", msg, cause)` when an upstream dependency is down. Both are retryable. HTTP status mapping (502 vs 503) is handled by `httpx.HTTPStatus()`.
- **Fail fast**: Configuration errors panic at startup (nil backends, empty names). Runtime errors return `error`.
- **Health checks**: Internal port `:9090` serves `/ready` (dependency health), `/metrics` (Prometheus), and gRPC health over h2c. Public gRPC health requires `WithPublicGRPCHealth()`.
- **TLS**: Set `TLS_CA_CERT`, `TLS_CERT`, `TLS_KEY` together to enable mTLS globally.
- **Middleware order**: Always use `stack.Default()` — it enforces: outer → metrics → requestID → tracing → logging → inner → handler.
- **Scaffolds**: Use `kit-new -tenant` for new multi-tenant services so Redis cache and idempotency start behind the tenant wrappers.
- **Dependencies**: New direct external Go modules must be added to `docs/audit/dependency-allowlist.txt` in the same change; `make check-dependency-allowlist` rejects unreviewed or stale direct dependencies.
- **Messaging size**: `infra/messaging` publishers default to `messaging.DefaultMaxMessageBytes` (1 MiB); `infra/outbox.WithRouteMaxMessageBytes` overrides for one route, `infra/messaging/buffered_publisher.WithMaxMessageBytes` overrides the buffered-publisher default. Adapter modules (`app/amqp`, `app/nats`) wire these limits through to the underlying backend.
- **Credential rotation**: Prefer provider-backed credentials over static secrets when the upstream SDK supports it. Use DB `PasswordProvider`, go-redis credential providers, AMQP URL providers, NATS auth providers, cloud SDK default credentials, CSRF `WithSecrets`, and signed-request key stores for zero-downtime rotation.
- **Benchmark baselines**: `make bench-baseline` writes raw `go test -bench` outputs under `docs/release/benchmarks/$(RELEASE_VERSION)/`; a clean current release-candidate capture is the canonical `kit-bench-gate -baseline` input for the release branch. If the directory manifest marks the files historical/preliminary, treat them as comparison evidence only and rerun the baseline before tagging.

## Anti-Patterns

- **Never** create or serve with `net/http` server entrypoints (`http.Server`, `http.ListenAndServe`, `http.Serve`) directly — use `httpx.NewServer` (safe timeouts, header limits).
- **Never** use `http.DefaultClient` — use `httpx.NewHTTPClient` or `app.HTTPClient(infra)`.
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
- Always pair `Locker.Acquire` with `handle.Release` (or use `Locker.WithLock` to defer release). Re-Acquiring the same key returns a fresh handle; failing to release leaks the lock until TTL.
- **Never** pass zero/negative values to `NewLimiter`, `NewKeyedLimiter`, `Timeout`, or `MaxBodySize` — they panic on misconfiguration.

## Keeping Docs in Sync

When adding new `With*()` methods to the Builder, new middleware, or new storage backends, update the corresponding `docs/ai/*.md` recipe file and the decision tree above.
