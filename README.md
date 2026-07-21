# rho-kit

**License:** Apache 2.0

`rho-kit` is the standard Go service toolkit. It centralizes the
infrastructure patterns every service needs so teams can focus on domain logic
while staying consistent, secure, and observable.

## Release

- [docs/RELEASE_NOTES_v2.md](docs/RELEASE_NOTES_v2.md) — historical v2.0.0
  breaking-changes enumeration.
- [CHANGELOG.md](CHANGELOG.md) — per-release summary.

### How to publish a release

The kit is a Go multi-module workspace; releases use `go.work` as the
sole cross-module-resolution mechanism (no `replace` directives in
`go.mod` files). The one-time `replace`-drop was done at v2.0.0;
subsequent releases just bump version numbers and tag.

1. **Validate locally.** Run `make release-candidate`. It includes the
   policy and tidy checks, lint, race tests, builds, vulnerability analysis,
   integration tests, coverage, and kit-doctor. GitHub does not duplicate
   this exhaustive release-owner gate.
2. **Rehearse locally (safe, no real origin touched).**
   ```bash
   RELEASE_VERSION=v2.x.y bash tools/rehearse-v2-release.sh
   ```
   Runs the entire dance against a temp bare repo. Must reach
   "Rehearsal passed." before touching origin.
   The manual `Release Rehearsal` GitHub workflow exists only for validating
   release machinery changes; it is not required for every release.
3. **Temporarily disable PR-review branch protection** (the dance
   pushes ~7 commits + tag batches directly to main; main is
   normally PR-only):
   ```bash
   gh api -X DELETE repos/<owner>/<repo>/branches/main/protection/required_pull_request_reviews
   ```
4. **Run the real release.**
   ```bash
   RELEASE_VERSION=v2.x.y bash tools/release-version.sh
   ```
   Per-level: bumps internal kit requires to the target version,
   tidies, commits, tags every module in the level, pushes tags
   atomically. Mechanical release commits contain `[skip ci]` so the
   dependency levels do not launch duplicate GitHub jobs. After all levels:
   pushes coordination tag `release/v2.x.y`.
5. **Restore branch protection immediately.** Capture the live protection
   response before step 3 and restore those exact values. The current policy
   is:
   ```bash
   gh api -X PATCH repos/<owner>/<repo>/branches/main/protection/required_pull_request_reviews \
     --input - <<EOF
   {"dismiss_stale_reviews": true, "require_code_owner_reviews": true,
    "require_last_push_approval": false, "required_approving_review_count": 0}
   EOF
   ```
6. **Smoke-test downstream resolution.**
   ```bash
   tmpdir=$(mktemp -d); cd "$tmpdir"
   go mod init verify
   go get github.com/bds421/rho-kit/app/v2@v2.x.y
   go list -m all | grep rho-kit   # all should show v2.x.y
   ```
7. **Publish the GitHub Release.** Use the version entry in `CHANGELOG.md`,
   call out breaking changes, and link the comparison from the previous
   coordination tag. Do not reuse the historical v2.0.0 notes.

## Adoption

New downstream services should start with
[docs/ai/adoption.md](docs/ai/adoption.md): minimum `go.mod` (the v2.x tags
are published, so `require` the versioned modules directly — no `replace`
block needed), the smallest compilable `main.go`, where each capability lives
in the decision tree, and the common first-mistake checklist. The package
decision tree in [AGENTS.md](AGENTS.md#package-decision-tree) is the
canonical "I need to X, what do I import?" reference.

Snippet status: Go blocks in this README are illustrative fragments. The shell
commands are executable from a downstream module. Golden-path evidence lives in
`examples/agentic-service` and the `cmd/kit-new` scaffold tests.

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
    base, err := app.LoadBaseConfig(8080)
    if err != nil {
        return err
    }

    return app.New("backend", handler.Version, base).
        With(postgres.Module(pgxbackend.Config{DSN: os.Getenv("DATABASE_URL")})).
        With(redis.Module(&goredis.Options{Addr: "cache.internal:6379", Password: "***", TLSConfig: &tls.Config{ServerName: "cache.internal"}})).
        With(amqp.Module(os.Getenv("RABBITMQ_URL"))).
        With(jwt.Module(os.Getenv("JWKS_URL"),
            jwt.WithIssuer("https://issuer.example.com"),
            jwt.WithAudience("backend"),
        )).
        With(ratelimit.IP(100, time.Minute)).
        Router(func(infra app.Infrastructure) http.Handler {
            return router.New(infra, logger)
        }).
        Run()
})
```

Use `app.LoadBaseConfig`, `sqldb.LoadFields`, and package-specific loaders for
env-backed settings. Pass a hardened `pgxbackend.Config` to `postgres.Module`.

**v2.0.0 lazy-adapter architecture.** Heavy adapter wiring (Postgres, Redis,
RabbitMQ, NATS, OTel tracing, public gRPC) lives in per-adapter sub-modules
under `app/`: `app/postgres`, `app/redis`, `app/amqp`, `app/nats`,
`app/tracing`, `app/grpc`. Importing `app/v2` alone no longer pulls pgx,
go-redis, amqp091, nats.go, otelgrpc, or grpc-go.

For credential rotation, prefer provider hooks over static secrets: pgx
`PasswordProvider`, go-redis credential providers, AMQP/NATS auth providers,
cloud SDK default credentials, CSRF `WithSecrets`, and signed-request
`WrapKeyStore`. See [docs/ai/credential-rotation.md](docs/ai/credential-rotation.md).
See `examples/agentic-service` for a full example.

## HTTP stack (recommended)

```go
csrfMW := csrf.New(
    csrf.WithSecrets(cfg.CSRFSecret, cfg.PreviousCSRFSecrets...),
    csrf.WithAllowedOrigins(cfg.PublicOrigin),
)
handler := stack.Default(router, logger,
    stack.WithOuter(csrfMW, csrf.RequireJSONContentType),
)
```

## Redis (example)

```go
conn, err := kitredis.Connect(&goredis.Options{Addr: "localhost:6379"}, kitredis.WithInstance("cache"))
if err != nil {
    log.Fatal(err)
}

c, err := rediscache.NewCache(conn.Client(), "api-cache")
if err != nil {
    log.Fatal(err)
}

tenantCache := tenantcache.Wrap(c)
_ = tenantCache.Set(ctx, "profile:123", []byte("..."), time.Minute)
```

## Usage

```bash
# Each module ships independently and uses Go's /v2 path suffix.
go get github.com/bds421/rho-kit/app/v2
go get github.com/bds421/rho-kit/httpx/v2
```

## Package map

- `app` – service bootstrap, infrastructure wiring, lifecycle, and graceful shutdown.
- `core/config`, `core/apperror`, `core/validate`, `core/secret`, `core/tenant` – configuration, typed errors, validation, and focused primitives.
- `httpx`, `httpx/middleware/*`, `httpx/healthhttp`, `httpx/pagination`, `httpx/mcp` – hardened HTTP servers, JSON helpers, middleware, health endpoints, pagination, and MCP handlers.
- `authz`, `authz/openfga`, `httpx/authz` – authorization interfaces, OpenFGA adapter, and HTTP bridge.
- `security/jwtutil`, `security/netutil`, `security/csrf`, `security/asvs` – JWT verification, mTLS/SSRF-safe networking, CSRF helpers, and ASVS scanning metadata.
- `crypto/encrypt`, `crypto/envelope`, `crypto/paseto`, `crypto/passhash`, `crypto/signing` – encryption, token, password, and request-signing primitives.
- `infra/sqldb`, `infra/sqldb/pgx`, `infra/sqldb/dbtest` – SQL contracts, pgx backend, migrations, and Docker-backed DB test helper module.
- `infra/redis`, `infra/redis/redistest` – resilient Redis connection management plus the split Docker-backed Redis test helper module.
- `data/cache`, `data/cache/rediscache`, `data/idempotency`, `data/lock`, `data/queue`, `data/stream`, `data/ratelimit` – data interfaces, memory implementations, and optional Redis/Postgres adapters.
- `infra/messaging`, `infra/messaging/amqpbackend`, `infra/messaging/redisbackend`, `infra/messaging/natsbackend` – message contracts, buffered delivery, RabbitMQ, Redis Streams, and NATS JetStream adapters.
- `infra/storage` plus `s3backend`, `azurebackend`, `gcsbackend`, `sftpbackend`, `storagehttp/uploadsec`, `storagehttp/uploadsec/clamav`, `storagetest` – storage interfaces, cloud/SFTP/local backends, upload validation/scanning, and backend compliance tests.
- `observability/health`, `observability/logging`, `observability/logattr`, `observability/redmetrics`, `observability/runtimemetrics`, `observability/slo`, `observability/pprof`, `observability/tracing` – health, logs, metrics, profiling, SLOs, and tracing.
- `runtime/lifecycle`, `runtime/concurrency`, `runtime/eventbus`, `runtime/cron`, `runtime/batchworker` – lifecycle orchestration, worker patterns, eventing, and scheduling.
- `resilience/retry`, `resilience/circuitbreaker`, `io/atomicfile`, `io/progress`, `flags` – retries, circuit breakers, safe file writes, progress tracking, and feature flags.

## Conventions and notes

- `auth.JWT` panics if the provider is nil to prevent accidental auth bypass.
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
