# rho-kit v2 Migration Guide

Baseline: commit `bfb475f` (`chore: harden rho-kit for v2 release`).

This is the operational migration guide. The full changelog remains in
[../RELEASE_NOTES_v2.md](../RELEASE_NOTES_v2.md); this file is the sequence a
downstream service owner should follow before adopting v2.0.0.

Snippet status: shell blocks in this file are executable from a downstream
service checkout. Go blocks are illustrative migration fragments unless they
are part of a generated scaffold.

## 1. Move Imports To v2 Module Paths

Every module uses Go semantic import versioning. Import the module root with
`/v2`; subpackages come after that suffix.

```bash
go get github.com/bds421/rho-kit/app/v2@v2.0.0
go get github.com/bds421/rho-kit/httpx/v2@v2.0.0
go mod tidy
go test ./...
```

Examples:

- `github.com/bds421/rho-kit/core/v2/tenant`
- `github.com/bds421/rho-kit/httpx/v2/middleware/stack`
- `github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2`

Do not use `/v2` at the end of a subpackage path such as
`core/tenant/v2`; that is not the module layout.

## 2. Remove Development-Mode Assumptions

The kit no longer has a development mode that relaxes production-safety checks.
`BaseConfig.Environment` and `config.IsDevelopment` remain available for
application-owned behavior, but the kit's safety validator no longer uses them
as an escape hatch.

Illustrative migration fragment:

```go
// Before
b := app.New("my-service", version, cfg.BaseConfig).
    WithProductionDefaults().
    WithProductionAllowPlaintext().
    WithProductionInternalExposed().
    WithJWTAllowAnyIssuer().
    WithJWTAllowAnyAudience()

// After
b := app.New("my-service", version, cfg.BaseConfig).
    WithoutTLS().              // only when an external TLS terminator is reviewed
    WithInternalNonLoopback(). // only behind a reviewed private network boundary
    WithoutJWTIssuer().        // only for a reviewed legacy issuer migration
    WithoutJWTAudience()       // only for a reviewed legacy audience migration
```

Most services should only delete `WithProductionDefaults()` and avoid the
replacement opt-outs entirely.

## 3. Tighten Auth And Authorization

Routes using `httpx/middleware/auth.RequirePermission`,
`PermissionByMethod`, or `RequireScope` now fail closed when permissions or
scopes are absent. The only bypass is a verified mTLS service-to-service marker
installed by `RequireS2SAuth`.

Migration:

1. Put `RequireUserWithJWT`, `RequireS2SAuth`, PASETO, or signed-request
   middleware ahead of authorization middleware on every protected route.
2. Ensure JWT/PASETO issuers include explicit permissions or scopes for user
   routes.
3. Use `httpx/authz.RequirePermission` with a policy when decisions need RBAC
   or ABAC data instead of a flat permission claim.

## 4. Adopt The v2 Builder Golden Path

Production services should converge on the Builder instead of hand-built
servers. The Builder owns middleware ordering, lifecycle, shutdown, internal
health, and production-safety validation.

Illustrative shape:

```go
return app.New("billing-api", version, cfg.BaseConfig).
    WithPostgres(cfg.Database, cfg.DatabasePool).
    WithRedis(cfg.Redis).
    WithJWT(cfg.JWKSURL).
    WithJWTAudience("billing-api").
    WithMultiTenant(httpxtenant.HeaderExtractor("X-Tenant-Id"), true).
    WithTenantBudget(budgetStore).
    WithActionLogger(actionLogger).
    WithApprovalStore(approvalStore).
    WithSignedRequests(keyResolver, nonceStore).
    Router(func(infra app.Infrastructure) http.Handler {
        mux := http.NewServeMux()
        return stack.Default(mux, logger)
    }).
    Run()
```

Use `cmd/kit-new` for a buildable scaffold:

```bash
go run github.com/bds421/rho-kit/cmd/kit-new/v2@v2.0.0 billing-api \
  -module-path github.com/acme/billing-api \
  -postgres -tenant -mcp
```

The scaffold variants are compile-tested by `cmd/kit-new` tests.

Validation evidence for the current release-prep tree:

- The Builder methods named in this guide are present in `app/builder.go`.
- `examples/agentic-service` builds, passes tests, and has a live smoke path
  through `tools/list`, `echo`, and `/admin/budget`.
- `cmd/kit-new` scaffold variants pass their compile-oriented test suite.

## 5. Migrate Database Schemas

Use `cmd/kit-migrate` to publish/check kit-owned migrations for packages that
ship schemas, then apply service-owned SQL migrations with the service's normal
release process. In this release-prep tree, `cmd/kit-migrate` still has local
workspace replaces, so run it from a rho-kit checkout at the release tag rather
than through `go run module@version`.

```bash
git clone https://github.com/bds421/rho-kit
cd rho-kit
git checkout cmd/kit-migrate/v2.0.0

go run ./cmd/kit-migrate list
go run ./cmd/kit-migrate check idempotency postgres
go run ./cmd/kit-migrate check actionlog postgres
go run ./cmd/kit-migrate check approval postgres
```

Service-owned migrations remain explicit SQL. Do not rely on automatic schema
generation in production.

## 6. Review Renamed Or Reshaped APIs

Known source changes that downstream code may need to adjust:

| Area | Migration |
|---|---|
| `security/asvs` | Use `asvs.Catalog()` and `asvs.PackageRegistry()` accessors instead of mutable exported package variables. |
| `observability/redmetrics` | Use `redmetrics.HTTPLatencyBuckets()` and `redmetrics.BatchDurationBuckets()` accessors. |
| `security/jwtutil.Provider.Run` | Handle the returned `error` from lifecycle goroutines. |
| `httpx/middleware/ratelimit.Run` | Handle the returned `error` when manually wiring cleanup loops. |
| `infra/messaging.BufferedPublisher.Run` | Handle the returned `error` when manually wiring the drain loop. |
| `runtime/lifecycle.HTTPServer` | Supply explicit `Addr`, non-nil `Handler`, and non-zero `ReadHeaderTimeout`; zero-value `http.Server` now panics. |
| `infra/sqldb/pgx.Copy` | Use portable identifiers and chunk large imports above the documented row/column caps. |
| Storage batch/migration helpers | Chunk batch operations above `storage.MaxBatchKeys`; check `MigrateResult.ErrorsTruncated`. |
| Redis queue/stream batch helpers | Chunk above `queue.MaxBatchMessages` or `redisstream.MaxBatchMessages`. |
| `infra/redis.HealthCheck` | Treats Redis as a critical dependency by default. Use `NonCriticalHealthCheck` for cache-only or degraded-mode services. |

Validation evidence for the current release-prep tree:

- Accessors for `security/asvs`, `observability/redmetrics`, and
  `resilience/retry` exist as functions in their owning modules.
- `security/jwtutil.Provider.Run`,
  `httpx/middleware/ratelimit.RateLimiter.Run`,
  `httpx/middleware/ratelimit.KeyedRateLimiter.Run`, and
  `infra/messaging.BufferedPublisher.Run` all return `error`.
- `infra/redis.HealthCheck` and `infra/redis.NonCriticalHealthCheck` both
  exist, and integration evidence is recorded in
  [RC_CHECKLIST_V2.md](RC_CHECKLIST_V2.md).

## 7. Re-run The Release Gates In The Service

For each downstream service:

```bash
go mod tidy
go test ./...
go test -race ./...
go vet ./...
```

If the service uses kit infrastructure helpers, also run its Docker-backed
integration tests with the service's normal `integration` tag.

## 8. Things Not Migrated In v2.0.0

The following remain out of scope for v2.0.0 and should not block adoption:

- Kubernetes/etcd leader-election adapters.
- Kafka backend.
- Additional managed-KMS/Vault adapters beyond the currently frozen AWS and
  Google KMS adapter modules.
- Per-package benchmark baselines for every package.
- Provider-specific production dashboards beyond the documented runbooks and
  metrics surfaces already shipped.
