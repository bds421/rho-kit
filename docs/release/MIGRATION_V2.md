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
    AllowInternalNonLoopback(). // only behind a reviewed private network boundary
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

1. Put `auth.JWT`, `RequireS2SAuth`, PASETO, or signed-request middleware
   ahead of authorization middleware on every protected route.
2. Ensure JWT/PASETO issuers include explicit permissions or scopes for user
   routes.
3. Use `httpx/authz.RequirePermission` with a policy when decisions need RBAC
   or ABAC data instead of a flat permission claim.

## 4. Adopt The v2 Builder Golden Path

Production services should converge on the Builder instead of hand-built
servers. The Builder owns middleware ordering, lifecycle, shutdown, internal
health, and production-safety validation.

Illustrative shape — adapter wiring is now factored into `app/<service>`
Modules registered with `Builder.With(...)`:

```go
return app.New("billing-api", version, cfg.BaseConfig).
    With(postgres.Module(cfg.Postgres)).
    With(redis.Module(cfg.Redis)).
    WithJWT(cfg.JWKSURL).
    WithJWTAudience("billing-api").
    MultiTenant(httpxtenant.HeaderExtractor("X-Tenant-Id")).
    TenantBudget(budgetStore).
    ActionLogger(actionLogger).
    ApprovalStore(approvalStore).
    WithSignedRequests(keyResolver, nonceStore).
    WithIPRateLimit(60, time.Minute). // affirmative rate-limit declaration is required
    Router(func(infra app.Infrastructure) http.Handler {
        mux := http.NewServeMux()
        return stack.Default(mux, logger)
    }).
    Run()
```

(Replace `postgres.Module` / `redis.Module` with the `app/<adapter>`
Modules you actually wire; see §8 for the full list of adapter packages
that replaced the removed `Builder.With<Backend>` shortcuts.)

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

#### `core/validate` migrated to JSON Schema (wave 124)

The package now wraps `github.com/google/jsonschema-go` (in-memory
schema model) plus `github.com/santhosh-tekuri/jsonschema/v6`
(compilation + validation) instead of `github.com/go-playground/validator/v10`.
Existing call sites of `validate.Struct(req)` continue to work
unchanged; the user-visible message text for the common rules
(`required`, `email`, `min`, `max`, `oneof`, `uuid`, …) is preserved.
Three migration touchpoints exist:

| Area | Migration |
|---|---|
| `validate:"..."` constraint tag | Still recognised — the kit now owns the grammar rather than the third-party library. Supported keys: `required`, `min`, `max`, `len`, `gte`, `lte`, `gt`, `lt`, `oneof`, `email`, `url`, `uuid`, `ip`, `ipv4`, `ipv6`, `cidr`, `hostname`, `alpha`, `alphanum`, `numeric`, `datetime`, `startswith=…`, `endswith=…`, `contains=…`, `excludesall=…`, `pattern=…`, `format=…`. Unknown keys are ignored; register new vocabulary via `RegisterFormat`. |
| `jsonschema:"..."` tag | New optional source for description text (the v1 `desc:"..."` tag is still honoured for backwards compatibility). The kit additionally treats the keyword `required` in the comma-separated list as a marker that the field is required, so `jsonschema:"required,Customer e-mail"` records both the requirement and the description. |
| `validate.RegisterValidation(tag, validator.Func)` removed | Replaced by `validate.RegisterFormat(name string, fn func(any) error) error`. The function is registered with the underlying santhosh-tekuri compiler as a JSON-Schema `format` validator and dispatches whenever a field's `validate:"<name>"` constraint matches. The closure shape no longer leaks any third-party validator type. |
| `validate.Func` alias removed | Use `validate.FormatFunc` (`func(any) error`) for the new format hook. |
| Empty-string handling on required string fields | The kit-built-in formats (`email`, `url`, `uuid`, `hostname`, `cidr`, `alpha`, `alphanum`, `numeric`, `startswith`/`endswith`/`contains`/`excludesall`) intentionally short-circuit on empty input so a missing required string surfaces as `is required` rather than the format-specific message — matching the v1 ordering where `required` ran before `email`. |
| `SchemaFor[T]()` / `Validator.SchemaForType(t)` | New helpers that expose the cached `*jsonschema.Schema` (jsonschema-go in-memory tree) for consumers that need to publish the schema (MCP tool catalog, future OpenAPI export, served `/schema` endpoints). The returned schema is the package's cached instance — clone before mutating. |

### `crypto/signing`

| Area | Migration |
|---|---|
| `signing.Sign` / `signing.Verify` | Argument order is now `Sign(secret, body)` / `Verify(secret, body, ...)` (was `Sign(body, secret)`). The secret parameter is the named type `signing.Secret`; use `signing.NewSecret(secretBytes)` or an explicit `signing.Secret(...)` conversion so body/secret swaps fail at compile time. |
| `signing.NewStaticKeyStore` | Now returns `(*StaticKeyStore, error)`. The old panic-on-error form is `signing.MustNewStaticKeyStore`. |
| `signing.ErrExpiredSignature` | Split into `signing.ErrSignatureExpired` (timestamp older than `maxAge`) and `signing.ErrSignatureClockSkew` (timestamp too far in the future of the verifier's clock). Callers that branched on the single sentinel must now check both — or use `errors.Is(err, signing.ErrSignatureExpired) \|\| errors.Is(err, signing.ErrSignatureClockSkew)` for the merged behaviour. Splitting them lets dashboards alert separately on "consumer is slow / replay window missed" vs "producer's clock is wrong". |

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
| `paseto.Provider.Stop` | Renamed to `Provider.Close() error`. `Provider` is not a lifecycle Component; it pairs with `Run(ctx)` for cache refresh and only releases its own background resources. |

### `httpx`

| Area | Migration |
|---|---|
| `httpx.WriteJSON` | Signature is now `WriteJSON(w, r, status, v)`. `httpx.WriteJSONCtx` is removed. |
| `httpx.WriteValidationError` and `httpStatusToCode` | Wire format now emits `apperror.Code*` string values. 401 returns `"AUTH_REQUIRED"` (was `"UNAUTHORIZED"`), 422 returns `"PERMANENT"` (was `"UNPROCESSABLE_ENTITY"`), 429 returns `"RATE_LIMIT"` (was `"RATE_LIMITED"`), 503 returns `"UNAVAILABLE"` (was `"SERVICE_UNAVAILABLE"`). Clients that parse the `code` field in error JSON must update. |
| `apperror.CodeStorageFull` / HTTP 507 | New in v2: storage backends that exhaust capacity (S3 `EntityTooLarge`, GCS quota 507/413, Azure `RequestBodyTooLarge`/`InsufficientStorage`, SFTP/local `ENOSPC`) return [`storage.ErrInsufficientCapacity`](../../infra/storage/storage.go) which carries `apperror.CodeStorageFull`. `httpx.WriteServiceError`, `httpx.HTTPStatus`, and `httpStatusToCode` map this to HTTP 507 Insufficient Storage with wire code `"STORAGE_FULL"`. Clients should treat this status as retryable after operator action. |
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

### `httpx/mcp` migrated to `modelcontextprotocol/go-sdk` (wave 121)

The kit's hand-rolled JSON-RPC + schema-generation + dispatch
implementation was replaced with the official Go SDK at
`github.com/modelcontextprotocol/go-sdk` v1.6.0. The kit retains its
typed-handler facade and audit/destructive-gate value-add as a thin
wrapper around the SDK's `Server.AddTool` and
`NewStreamableHTTPHandler` primitives.

| Area | Migration |
|---|---|
| Wire format | Now spec-compliant **Streamable HTTP**. Clients MUST send `Content-Type: application/json` AND `Accept: application/json, text/event-stream` on every request. The kit's `Server.HTTP()` returns the SDK's `StreamableHTTPHandler` configured with `Stateless:true, JSONResponse:true` — sessionless and application/json-replied, matching the pre-SDK kit's "one call per request" contract. Clients without `Accept: application/json, text/event-stream` get `400 Bad Request` from the SDK before reaching the kit. |
| Shorthand invocation removed | The pre-SDK kit accepted `method: "<tool-name>"` as a shortcut for `method: "tools/call", params: {name: "<tool-name>", ...}`. The SDK transport only routes the spec methods (`initialize`, `tools/list`, `tools/call`, `ping`, ...); clients must use `tools/call`. |
| Tool error envelope | Application-level handler errors (validation failures, gate refusals, internal errors) are returned as `CallToolResult{IsError: true, Content: [{type:"text", text:"<message>"}]}` per the MCP spec, NOT as JSON-RPC `error` objects with `-32603/-32602/...` codes. Only true transport problems (malformed JSON-RPC envelope, unknown protocol method) still surface as JSON-RPC `error`. Clients that branched on `response.error.code` must read `response.result.isError` and `response.result.content[0].text` instead. |
| `Server.HTTP()` body cap removed | `WithMaxRequestBytes(n)` is removed — the SDK does not surface a body-cap knob. Pre-process requests with an `http.MaxBytesReader` middleware in front of the MCP handler if the kit-shipped default no longer fits. |
| JSON-RPC notifications, batch requests, error-text guarantees | Notifications (`{...,"method":"...","params":{...}}` with no `id`) are accepted by the SDK transport; batch requests are decoded by the SDK and the kit's "fail on `[`" check is gone. The kit's strict "do not echo decoder text" invariant is enforced at the kit-owned wrapper, but the SDK's `unknown tool "<name>"` error message (returned when an unregistered tool name is invoked) does include the caller-supplied name — accept this as the v2.0.0 trade-off. |
| `GenerateSchema` / `ErrCyclicSchema` / `ErrUnsupportedType` | The reflection-based `mcp.GenerateSchema` helper is removed. Schemas are inferred from the typed In/Out struct via `core/v2/validate.SchemaFor[T]()` (jsonschema-go-backed). The two sentinel errors are preserved and remain `errors.Is`-comparable for callers that branch on registration failures. |
| Actor extractor receives a synthetic `*http.Request` | The SDK's `CallToolRequest` does not expose the full inbound HTTP request. The kit synthesises a minimal `*http.Request` whose `Header` field is `req.Extra.Header` and whose `Context` is the request context. Custom `WithActorExtractor(fn func(*http.Request) string)` implementations that depended on `URL`, `RemoteAddr`, `Method`, etc. must be reshaped to read `Header` / `Context` only. The bundled `WithActorFromHeader` / `WithActorFromContext` extractors are unchanged. |
| `Server.HTTP()` return type | Was a `http.HandlerFunc`; now an `*sdkmcp.StreamableHTTPHandler` wrapped in the kit's same `http.Handler` interface. External callers see the same `http.Handler` contract; tests that type-asserted on the concrete return type will need to drop the assertion. |
| Initialization negotiation | The SDK negotiates the protocol version on `initialize` and returns its own `protocolVersion` (currently `2025-11-25`). The kit no longer carries an explicit allow-list of versions or a `supportedProtocolVersion` constant. |
| New direct dependency | `github.com/modelcontextprotocol/go-sdk` v1.6.0 (already listed in `docs/audit/dependency-allowlist.txt` since wave 120). |

### `app`

| Area | Migration |
|---|---|
| `app.Module.Close` | Renamed to `app.Module.Stop`. Any service implementing custom modules must rename the method. |
| `app.Builder.WithRedis` | Production safety enforces TLS on Redis URIs. Opt out per-builder via `WithoutRedisTLS` only on a reviewed private boundary. |
| `app.Builder.Background` | Renamed to `app.Builder.Background` to match the `With*` registration convention used by every other Builder method (`WithJWT`, `Storage`, `AuditLog`, ...). Service wiring: replace `b.Background(name, fn)` with `b.Background(name, fn)`. The runtime callback `Infrastructure.Background` (registered late from inside `RouterFunc`) is unchanged. |
| `app.Builder` rate-limit gate | `Builder.Run()` now requires an affirmative rate-limit declaration. Chain exactly one of `WithIPRateLimit(n, window)`, `WithKeyedRateLimit(name, n, window)`, or the explicit `WithoutRateLimit()` opt-out. Pre-v2.0.0 a Builder with no rate-limit option silently defaulted to "no limit"; that contradicted the fail-loud contract every other Builder safety control (TLS, JWT issuer / audience, internal-host loopback) already obeys. `kit-doctor` flags the omission pre-build via the `rate-limit-omission` rule for editor integration. |

### `infra/messaging`

| Area | Migration |
|---|---|
| `MessagePublisher` / `MessageConsumer` | Renamed to `Publisher` / `Consumer`. Update all interface references and embeds. |
| `Connector.Close` | Renamed to `Connector.Stop(ctx) error`. The new signature takes a `context.Context` so shutdown can be bounded. |
| `BufferedPublisher` options | Destuttered. `WithBufferedMaxSize` → `WithMaxSize`, `WithBufferedStateFile` → `WithStateFile`, `WithBufferedMaxMessageBytes` → `WithMaxMessageBytes`, `WithBufferedFinalDrainTimeout` → `WithFinalDrainTimeout`. |

### `infra/redis`

| Area | Migration |
|---|---|
| `redis.RedisConfig` | Renamed to `redis.Config`. The kit no longer stutters the package name in exported type names. Field shape and JSON tags are unchanged. |
| `redis.RedisFields` | Renamed to `redis.Fields`. Service configs that embed `Redis RedisConfig` now embed `Redis Config`; the env-loader contract is unchanged. |
| `redis.LoadRedisFields` | Renamed to `redis.LoadFields`. The `kit-new` scaffold's `wire.go.tmpl` is updated to the new name; downstream wirings should follow. |
| `redis.RedisMetrics` / `redis.NewRedisMetrics` | Renamed to `redis.Metrics` / `redis.NewMetrics`. Consumers passing the metrics struct to `redis.WithPoolMetrics(...)` only need the type rename; behaviour is unchanged. |

### `io/progress`

| Area | Migration |
|---|---|
| `progress.NewProgressReader` | Renamed to `progress.NewReader`. The kit-level `storage.NewProgressReader` wrapper keeps its name (multiple backend packages dot-import side-by-side) but now delegates to `progress.NewReader`. |

### `infra/storage`

| Area | Migration |
|---|---|
| `storage.Manager.Disk` | Renamed to `storage.Manager.Backend` and now returns `(Storage, error)` with `apperror.NewNotFound` instead of panicking on a missing entry. Use `storage.Manager.MustBackend(name)` for the panic-on-miss form. |
| Storage / messaging sentinel errors | Now return `apperror` codes (e.g. `apperror.NewNotFound`) rather than package-local sentinels. Use `apperror.IsNotFound` etc. to inspect. |

### `infra/leaderelection/pgadvisory`, `infra/leaderelection/redislock`

| Area | Migration |
|---|---|
| `WithCallbackDrainTimeout` | Option removed. `holdLeadership` now blocks until `Callbacks.OnAcquired` returns, so the same process cannot run two overlapping leadership terms. The cancelled `OnAcquired` context is the only signal — your callback MUST observe `ctx.Done()` and return promptly. If you need a hard upper bound, wrap the callback body with your own `context.WithTimeout`. See `TestHoldLeadership_LossDoesNotReturnUntilCallbackDrains` in each adapter. |
| Callback-drain visibility | Both adapters now expose `NewMetrics`, `WithMetrics`, and `WithCallbackDrainWarnInterval` so a stalled `OnAcquired` callback is operator-visible. The watchdog logs a warn and increments `leaderelection_callback_drain_warn_total{key}` every 30 seconds (override via `WithCallbackDrainWarnInterval`), and the terminal drain duration lands in `leaderelection_callback_drain_seconds{state="drained",key=...}` with `state="pending"` snapshots on each warn tick. Buckets: `[1, 5, 10, 30, 60, 120, 300]` seconds. |

### `data/budget/memory`, `data/ratelimit/gcra`, `data/ratelimit/tokenbucket`

| Area | Migration |
|---|---|
| `Budget.Stop`, `gcra.Limiter.Stop`, `tokenbucket.Limiter.Stop` | All three renamed to `Close() error`. These types own background ticker goroutines only — they do not satisfy `lifecycle.Component` and should be released via `defer x.Close()` from the owning constructor's caller. |

### `data/ratelimit/tokenbucket` now wraps `golang.org/x/time/rate`

| Area | Migration |
|---|---|
| Per-key locking | Each bucket now carries its own mutex (inside the wrapped `*rate.Limiter`); the limiter-wide mutex is held only for the bucket-map lookup. High-cardinality keysets no longer serialise through a single lock — a real throughput win for contended deployments. No callsite changes required. |
| `retryAfter` granularity | The deny-path delay is now `rate.Reservation.DelayFrom(now)`, rounded to the nearest `time.Duration`. Off-by-one-nanosecond differences from the previous float arithmetic are expected at edge cases and within the limiter's contract. |
| `refillPerSec = math.MaxFloat64` | `rate.Limit(math.MaxFloat64) == rate.Inf`; the limiter is permissive and every request is allowed. Previously this edge returned a 1-nanosecond `retryAfter` after the first denial. Use a large-but-finite refill (e.g. `1e15`) if you need the old shape. |
| Fractional capacity in `(0, 1)` | `rate.NewLimiter` takes an integer burst, so `int(0.5) == 0` produces a bucket that can never satisfy a 1-token reservation. `Allow` now returns `ratelimit.ErrInvalidLimiter` for these buckets so the misconfiguration surfaces immediately. Round capacity up before constructing the limiter. |

### `data/lock/redislock` now wraps `github.com/go-redsync/redsync/v4`

Wave 126 swapped the kit's in-house SET-NX + Lua release/extend scripts
for `go-redsync/redsync/v4` in single-pool mode. The kit's
`Locker`/`Lock` interface, `WithLock`, `LockerWithValue`, `WithTTL`,
`WithRetry`, `WithMaxWait`, `MaxLockKeyLen`, `validateLockKey`, and the
`DegradedLocker` outage fallback all keep their public shapes and
semantics; redsync becomes an internal implementation detail driving the
acquire/release/extend primitives.

| Area | Migration |
|---|---|
| Backoff shape | Retry delay is now `±25%` jittered around the `WithRetry` interval (redsync's `WithRetryDelayFunc`). The previous fixed-interval poll caused synchronised retry spikes when many callers contended for the same key; the jitter spreads waiters across the interval. Callers timing themselves against the legacy "exactly N polls at exactly interval Δ" cadence should widen tolerance windows by ~25%. |
| Orphan-window probe removed | Pre-wave-126 `tryAcquire` recovered from a TCP-RST mid-SETNX with a follow-up `GET` to confirm whether the SET landed. Redsync handles the same failure mode probabilistically through retry-with-jitter, so the probe was dropped — two layers of contention recovery were redundant and harder to reason about. Callers who genuinely need the "did my SETNX land?" guarantee should layer it at the application level; the kit no longer offers it. |
| Release on token mismatch | When the key still exists but holds a foreign token (TTL expired and another holder claimed it), the kit continues to return `lock.ErrLockLost`. Redsync surfaces this as `redsync.ErrNodeTaken` internally; the kit folds both `ErrNodeTaken` and `ErrLockAlreadyExpired` into `ErrLockLost` so callers using `errors.Is` keep working. |
| Extend on lost lock | `handle.Extend` keeps returning `(false, nil)` when the lock is no longer owned — redsync's `ExtendContext` returns `(false, err)` with an `ErrNodeTaken`-shaped error in that case, which the kit translates back to the kit's contract so heartbeat loops can branch on the boolean. |
| New transitive dependency | Adds `github.com/go-redsync/redsync/v4` plus `github.com/hashicorp/go-multierror` (indirect). Reviewed and listed in `docs/audit/dependency-allowlist.txt`. Redsync's Redlock multi-master quorum mode is NOT used — single-pool keeps the kit on the same operational contract as before. A future `redlock` sub-package can adopt the quorum mode without disturbing this package. |

### `data/queue/redisqueue`

The v2 implementation wraps [hibiken/asynq](https://github.com/hibiken/asynq).
The kit's `Queue` interface, `Message` envelope, and metric series are
preserved; the LIST+heartbeat machinery underneath is replaced.

| Area | Migration |
|---|---|
| Wire envelope | In-flight tasks from a pre-v2 kit are NOT readable by v2. The v2 envelope rides inside an `asynq.Task` of type `rho.envelope`; the on-Redis layout uses asynq's `asynq:{<queue>}:` key prefix. **Operators must drain pre-v2 queues before upgrading**, or write a one-off migration script that re-publishes pending messages through the v2 `Enqueue` API. |
| Heartbeat-based recovery | Removed. Stuck-task recovery is now governed by asynq's invisibility timeout (configurable via `WithInvisibilityTimeout`, default ~30s). A worker that crashes mid-handler leaks its task only for the invisibility window instead of one heartbeat interval. |
| `WithHeartbeatTTL`, `WithHeartbeatInterval`, `WithoutRecovery` | Removed. Asynq manages claim+release internally; the kit no longer surfaces heartbeat tunables. The equivalent operator dial is `WithInvisibilityTimeout`. |
| `WithBlockTimeout` | Removed. Asynq's server polls Redis on its own schedule; there is no kit-level BLMOVE timeout to configure. |
| `WithProcessingQueue`, `WithDeadLetterQueue` | Removed. Asynq names processing and archive (DLQ) keys per its own scheme; the kit's `:processing:<id>` and `:dead` suffixes no longer exist. |
| `WithDeadLetterMaxLen` | Removed. Asynq archive entries are capped by `Retention` (configurable via the new `WithRetention` option) and operators can run `asynq.Inspector.DeleteAllArchivedTasks(queue)` to bulk-clean. |
| `WithConcurrency` (new) | Asynq's worker pool is goroutine-bounded. The kit defaults to `Concurrency=1` so pre-v2 single-consumer-per-queue behaviour is preserved; opt in to parallel dispatch with `WithConcurrency(n)`. |
| `WithInvisibilityTimeout` (new) | Sets the per-task asynq `ShutdownTimeout`/visibility-timeout knob; replaces the pre-v2 heartbeat scheme as the only stuck-task recovery dial. |
| `WithShutdownTimeout` (new) | Graceful-shutdown window the asynq server gives in-flight handlers when the parent context is cancelled. Default 30s. |
| `WithRetention` (new) | Per-task retention TTL for completed tasks so the asynq Inspector / web UI can show recent completions for operator triage. Zero (default) disables retention. |
| `Queue.Close` (new) | Releases the asynq client and inspector. The supplied `redis.UniversalClient` is NOT closed — asynq's contract requires the caller to own the Redis connection lifecycle. |
| Periodic/scheduled tasks | Use asynq's `*asynq.Scheduler` directly with the same Redis client; the kit's `Queue` does not surface scheduling. See asynq's wiki page on periodic tasks. |
| `removeByID` / `recoverProcessing` / Lua scripts | All removed. The kit no longer ships any Lua; asynq's bundled scripts handle claim/ack/retry. |
| Metric semantics | `redis_queue_processing_depth` now reports asynq's `Active` count (claimed-by-worker), `redis_queue_queue_depth` reports asynq's `Pending`, `redis_queue_dlq_depth` reports asynq's `Archived`. Label set (`{queue}`) unchanged. `redis_queue_ack_not_found_total` is removed — asynq does not have an analogous failure mode. |
| `data/queue/redisqueue/integrationtest` | Tests retargeted at asynq semantics (Inspector-based depth assertions, archive-based dead-letter assertions). Operator runbooks that grep on the kit's pre-v2 Redis key names (`<queue>:processing:*`, `<queue>:dead`) must update to asynq's `asynq:{<queue>}:active`, `asynq:{<queue>}:pending`, `asynq:{<queue>}:archived`. |

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
| `resilience/circuitbreaker.ErrCircuitOpen` | Sentinel message text changed from `"circuit breaker is open"` to `"circuitbreaker: circuit is open"` to match the kit-wide `pkg:` prefix convention. Use `errors.Is(err, circuitbreaker.ErrCircuitOpen)` instead of string-matching the message; the HTTP middleware's `{"error":"circuit breaker is open"}` JSON wire format is unchanged for downstream HTTP clients. |

Validation evidence for the current release-prep tree:

- Accessors for `security/asvs`, `observability/redmetrics`, and
  `resilience/retry` exist as functions in their owning modules.
- `security/jwtutil.Provider.Run` and
  `infra/messaging.BufferedPublisher.Run` return `error`.
- `httpx/middleware/ratelimit.Limiter` and
  `httpx/middleware/ratelimit.KeyedLimiter` expose `Start(ctx) error` /
  `Stop(ctx) error` (renamed from `Run`) and satisfy `lifecycle.Component`.
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

## 8. Adopter Onboarding And The Lazy-Adapter Architecture (shipped in v2.0.0)

The downstream onboarding flow (minimum `go.mod`, the smallest compilable
program, common first-mistake checklist) lives in
[../ai/adoption.md](../ai/adoption.md). New services should start there
before touching individual `With*()` methods.

**v2.0.0 ships the lazy-adapter architecture.** Heavy adapter wiring lives
in per-adapter sub-modules under `app/`:

- `github.com/bds421/rho-kit/app/postgres/v2` — pgx-native Postgres pool
- `github.com/bds421/rho-kit/app/redis/v2` — go-redis connection
- `github.com/bds421/rho-kit/app/amqp/v2` — RabbitMQ connection (with AMQP TLS enforcement)
- `github.com/bds421/rho-kit/app/nats/v2` — NATS JetStream connection
- `github.com/bds421/rho-kit/app/tracing/v2` — OpenTelemetry TracerProvider
- `github.com/bds421/rho-kit/app/grpc/v2` — public gRPC server + internal gRPC health

Importing `app/v2` alone no longer pulls pgx, go-redis, amqp091, nats.go,
otelgrpc, or grpc-go into the binary. Services declare each adapter they
actually need via `Builder.With`:

### Before (v1 / pre-refactor v2)

```go
import (
    "github.com/bds421/rho-kit/app/v2"
    pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
    goredis "github.com/redis/go-redis/v9"
)

app.New("svc", "v1", cfg).
    WithPostgres(pgxbackend.Config{DSN: dsn}).
    WithMigrations(migrationsFS).
    WithRedis(&goredis.Options{Addr: addr}).
    WithRabbitMQ(brokerURL).
    WithNATS(natsCfg).
    WithTracing(tracingCfg).
    WithModule(app.NewGRPCModule(register, ":50051")).
    Router(func(infra app.Infrastructure) http.Handler {
        _ = infra.DB         // *pgxbackend.Pool
        _ = infra.Redis      // *kitredis.Connection
        _ = infra.Publisher  // amqp publisher
        return mux
    }).
    Run()
```

### After (v2.0.0)

```go
import (
    "github.com/bds421/rho-kit/app/v2"
    "github.com/bds421/rho-kit/app/postgres/v2"
    "github.com/bds421/rho-kit/app/redis/v2"
    "github.com/bds421/rho-kit/app/amqp/v2"
    "github.com/bds421/rho-kit/app/nats/v2"
    "github.com/bds421/rho-kit/app/tracing/v2"
    "github.com/bds421/rho-kit/app/grpc/v2"
    pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
    natsbackend "github.com/bds421/rho-kit/infra/messaging/natsbackend/v2"
    goredis "github.com/redis/go-redis/v9"
)

app.New("svc", "v1", cfg).
    With(postgres.Module(pgxbackend.Config{DSN: dsn}, postgres.WithMigrations(migrationsFS))).
    With(redis.Module(&goredis.Options{Addr: addr})).
    With(amqp.Module(brokerURL)).
    With(nats.Module(natsbackend.Config{URL: natsURL})).
    With(tracing.Module(tracingCfg)).
    With(grpc.Module(register, ":50051")).
    Router(func(infra app.Infrastructure) http.Handler {
        _ = postgres.Pool(infra)        // *pgxbackend.Pool
        _ = redis.Connection(infra)     // *kitredis.Connection
        _ = amqp.Publisher(infra)       // messaging.Publisher
        return mux
    }).
    Run()
```

### Mechanical mapping

| Removed Builder method            | New adapter Module                                       |
| --------------------------------- | -------------------------------------------------------- |
| `WithPostgres(cfg)`               | `With(postgres.Module(cfg))`                             |
| `WithMigrations(fs)`              | `postgres.WithMigrations(fs)` option on the module       |
| `WithRedis(opts, …)`              | `With(redis.Module(opts, …))`                            |
| `WithoutRedisTLS()`               | `redis.Module(opts, redis.WithoutTLS())`                 |
| `WithRabbitMQ(url)`               | `With(amqp.Module(url))`                                 |
| `WithRabbitMQURLProvider(fn)`     | `With(amqp.Module("", amqp.WithURLProvider(fn)))`        |
| `WithCriticalBroker()`            | `amqp.WithCriticalBroker()` option                       |
| `WithMaxMessageBytes(n)`          | `amqp.WithMessageSizeLimiter(...)` / `nats.WithMessageSizeLimiter(...)` |
| `WithRouteMaxMessageBytes(…)`     | combine on the limiter passed to the adapter option      |
| `WithNATS(cfg)`                   | `With(nats.Module(cfg))`                                 |
| `WithTracing(cfg)`                | `With(tracing.Module(cfg))`                              |
| `app.NewGRPCModule(reg, addr, …)` | `grpc.Module(reg, addr, …)`                              |
| `WithPublicGRPCHealth()`          | `grpc.WithPublicHealth()` option                         |
| `infra.DB`                        | `postgres.Pool(infra)`                                   |
| `infra.Redis`                     | `redis.Connection(infra)`                                |
| `infra.Broker` / `.Publisher` / `.Consumer` | `amqp.Connection(infra)` / `amqp.Publisher(infra)` / `amqp.Consumer(infra)` |
| `infra.NATS` / `.NATSPublisher`   | `nats.Connection(infra)` / `nats.Publisher(infra)`       |
| `infra.GRPCServer`                | `grpc.Server(infra)`                                     |

### New: AMQP TLS enforcement

`amqp.Module` panics at construction time if the URL scheme is `amqp://`
and the host is non-loopback. This mirrors the existing Redis transport-
safety check. Use `amqps://` for production brokers, or opt out for
local-dev fixtures with `amqp.WithoutTLS()`.

## 9. Things Not Migrated In v2.0.0

The following remain out of scope for v2.0.0 and should not block adoption:

- Kubernetes/etcd leader-election adapters.
- Kafka backend.
- Additional managed-KMS adapters beyond the currently frozen AWS KMS, Azure
  Key Vault, Google Cloud KMS, and HashiCorp Vault Transit adapter modules.
