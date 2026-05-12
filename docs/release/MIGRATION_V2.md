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
    WithPostgres(cfg.Postgres).
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

Known source changes that downstream code may need to adjust. Rows are grouped
by package.

### `core/apperror`

| Area | Migration |
|---|---|
| `apperror.ConflictError.Retryable()` | Default is now `false` (was `true`). Use `apperror.NewConflictRetryable(...)` when the caller should retry. |
| `apperror.NewRateLimit` | Overload split. `NewRateLimit(msg)` is the no-`Retry-After` form; use `NewRateLimitWithRetryAfter(msg, d)` to set the hint. |
| `apperror.AsX` predicates | `AsConflict`, `AsAuthRequired`, `AsForbidden`, `AsOperationFailed`, and `AsPermanent` are removed; use the `IsX` predicates instead. `AsValidation`, `AsRateLimit`, `AsNotFound`, and `AsUnavailable` remain. |

### `core/tenant`

| Area | Migration |
|---|---|
| `tenant.WithID` | Returns `(context.Context, error)` instead of panicking. The old `WithIDChecked` has been removed. |
| `tenant.NewIDUnchecked` | Renamed to `tenant.MustNewID`. |

### `core/contextutil`, `core/validate`, `core/maputil`

| Area | Migration |
|---|---|
| `contextutil.NewID` | Renamed to `contextutil.GenerateID`. |
| `validate.New()` | Returns kit `*validate.Validator` instead of leaking `*validator.Validate`. Call site changes are mechanical: `v := validate.New(); v.Struct(x)`. |
| `core/maputil` | New package. `httpx.SetIfNotNil` has moved here (`maputil.SetIfNotNil`); update imports. |

### `crypto/signing`

| Area | Migration |
|---|---|
| `signing.Sign` / `signing.Verify` | Argument order is now `Sign(secret, body)` / `Verify(secret, body, ...)` (was `Sign(body, secret)`). The secret parameter is the named type `signing.Secret`; use `signing.NewSecret(secretBytes)` or an explicit `signing.Secret(...)` conversion so body/secret swaps fail at compile time. |
| `signing.NewStaticKeyStore` | Now returns `(*StaticKeyStore, error)`. The old panic-on-error form is `signing.MustNewStaticKeyStore`. |

### `crypto/passhash`

| Area | Migration |
|---|---|
| `passhash.Verify` | Returns `(VerifyResult, error)` (was `(ok, needsRehash bool, err error)`). Read `result.OK` and `result.NeedsRehash`. |
| `passhash.Hash` | Enforces an explicit memory cap; configure via options if defaults are too aggressive for your platform. |

### `crypto/secret`

| Area | Migration |
|---|---|
| `secret.String.Close` | Renamed to `secret.String.Zero` to match the semantics (it wipes the buffer rather than closing a resource). |

### `crypto/encrypt`

| Area | Migration |
|---|---|
| `encrypt.SealBytes` / `encrypt.OpenBytes` | Renamed to `encrypt.EncryptBytes` / `encrypt.DecryptBytes`. |

### `crypto/envelope`

| Area | Migration |
|---|---|
| `envelope.New` | Renamed to `envelope.NewEncryptor`. |
| Per-backend `New` constructors | Each KEK adapter constructor is now `NewKEK` (e.g. `awskms.NewKEK`, `gcpkms.NewKEK`). KEK adapters now confine to a single `keyID` per instance. |
| Envelope blob format | v2.0.0+ writes v3 length-prefixed AAD blobs. v2 blobs remain readable; downgrading to a pre-v2.0.0 reader will reject v3 blobs. |

### `crypto/paseto`

| Area | Migration |
|---|---|
| `paseto.NewV4Public` | Split into `paseto.NewV4PublicSigner` and `paseto.NewV4PublicVerifier`. Verifiers now reject reserved claim names at verify time. |

### `httpx`

| Area | Migration |
|---|---|
| `httpx.WriteJSON` | Signature is now `WriteJSON(w, r, status, v)`. `httpx.WriteJSONCtx` is removed. |
| `httpx.WriteValidationError` and `httpStatusToCode` | Wire format now emits `apperror.Code*` string values. 401 returns `"AUTH_REQUIRED"` (was `"UNAUTHORIZED"`), 422 returns `"PERMANENT"` (was `"UNPROCESSABLE_ENTITY"`), 429 returns `"RATE_LIMIT"` (was `"RATE_LIMITED"`), 503 returns `"UNAVAILABLE"` (was `"SERVICE_UNAVAILABLE"`). Clients that parse the `code` field in error JSON must update. |
| `httpx.SetIfNotNil` | Removed; use `core/maputil.SetIfNotNil`. |
| `httpx.NewTracingHTTPClient` | Consolidated into a single variadic-options constructor accepting `TracingClientOption`. The old `NewTracingHTTPClientWithOptions` is removed. |
| `httpx/reqsign` package | Removed. Use `httpx/sign` with `httpx/middleware/signedrequest` instead. The two formats intentionally froze separate canonical strings and key-ID header spellings. |

### `httpx/middleware/auth`

| Area | Migration |
|---|---|
| `auth.RequireUserWithJWT` | Renamed to `auth.JWT`. |
| `auth.WithUserID`, `auth.WithPermissions`, `auth.WithTrustedS2S`, `grpcx/interceptor.WithTrustedS2S` | Now live behind `//go:build authtest` with no default-build stubs. Production builds cannot compile direct auth-context injection; integration tests must add the `authtest` build tag to use the helpers. |

### `httpx/middleware/ratelimit`

| Area | Migration |
|---|---|
| `RateLimiter.Run` | Renamed to `Start`; pair with new `Stop` method. The limiter satisfies `lifecycle.Component`. |
| `RateLimiter.Middleware` | Now a free function. |
| `KeyedRateLimitMiddleware` | Renamed to `KeyedMiddleware`. |
| `ratelimit.NewMetrics` | Functional-options constructor; pass `MetricsOption` values instead of positional fields. |

### `app`

| Area | Migration |
|---|---|
| `app.Module.Close` | Renamed to `app.Module.Stop`. Any service implementing custom modules must rename the method. |
| `app.Builder.WithRedis` | Production safety enforces TLS on Redis URIs. Opt out per-builder via `WithoutRedisTLS` only on a reviewed private boundary. |

### `infra/messaging`

| Area | Migration |
|---|---|
| `MessagePublisher` / `MessageConsumer` | Renamed to `Publisher` / `Consumer`. Update all interface references and embeds. |
| `Connector.Close` | Renamed to `Connector.Stop(ctx) error`. The new signature takes a `context.Context` so shutdown can be bounded. |
| `BufferedPublisher` options | Destuttered. `WithBufferedMaxSize` → `WithMaxSize`, `WithBufferedFlushInterval` → `WithFlushInterval`, and so on. |

### `infra/storage`

| Area | Migration |
|---|---|
| `storage.Manager.Disk` | Renamed to `storage.Manager.Backend` and now returns `(Storage, error)` with `apperror.NewNotFound` instead of panicking on a missing entry. |
| Storage / messaging sentinel errors | Now return `apperror` codes (e.g. `apperror.NewNotFound`) rather than package-local sentinels. Use `apperror.IsNotFound` etc. to inspect. |

### `data/queue/redisqueue`

| Area | Migration |
|---|---|
| Heartbeat cancellation | Heartbeat failures now cancel the in-flight `Process` callback's context so handlers can observe revocation and bail. |
| Per-consumer processing list layout | Layout changed. In-flight messages on v1 keys are NOT picked up by v2 consumers — drain v1 queues before upgrading, or run a one-off migration script that moves entries from the legacy processing list to per-consumer lists. |

### `data/stream/redisstream`

| Area | Migration |
|---|---|
| `redisstream.WithHeader` | Now returns `error` so invalid header names/values fail at configure time. |

### `observability/tracing`

| Area | Migration |
|---|---|
| `tracing.Provider.Shutdown` | Renamed to `tracing.Provider.Stop`. |

### `runtime/cron`, `runtime/batchworker`

| Area | Migration |
|---|---|
| `cron.WithRegistry` / `batchworker.WithRegistry` | Renamed to `WithRegisterer` to match the Prometheus interface. |

### `runtime/lifecycle`

| Area | Migration |
|---|---|
| `lifecycle.FuncComponent` | The exported `StartFn` field is removed. Construct via `lifecycle.NewFuncComponent(fn)`. |
| `runtime/lifecycle.HTTPServer` | Supply explicit `Addr`, non-nil `Handler`, and non-zero `ReadHeaderTimeout`; zero-value `http.Server` now panics. |

### Other

| Area | Migration |
|---|---|
| `security/asvs` | Use `asvs.Catalog()` and `asvs.PackageRegistry()` accessors instead of mutable exported package variables. |
| `observability/redmetrics` | Use `redmetrics.HTTPLatencyBuckets()` and `redmetrics.BatchDurationBuckets()` accessors. |
| `security/jwtutil.Provider.Run` | Handle the returned `error` from lifecycle goroutines. |
| `infra/messaging.BufferedPublisher.Run` | Handle the returned `error` when manually wiring the drain loop. |
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
- Additional managed-KMS adapters beyond the currently frozen AWS KMS, Azure
  Key Vault, Google Cloud KMS, and HashiCorp Vault Transit adapter modules.
