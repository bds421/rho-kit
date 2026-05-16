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
| `validate:"..."` struct tag | **Removed in v2.0.0** (wave 142). The kit now reads constraints exclusively from `jsonschema:"..."`. Wave 124 had migrated the engine off `go-playground/validator/v10` but kept the `validate:` grammar as a backward-compat parser; wave 142 deletes that parser so the kit has a single tag system. Migration is mechanical: rename every `validate:"…"` to `jsonschema:"…"` and the existing grammar continues to work unchanged (`required`, `min`, `max`, `len`, `gte`, `lte`, `gt`, `lt`, `oneof`, `email`, `url`, `uuid`, `ip`, `ipv4`, `ipv6`, `cidr`, `hostname`, `alpha`, `alphanum`, `numeric`, `datetime`, `startswith=…`, `endswith=…`, `contains=…`, `excludesall=…`, `pattern=…`, `format=…`, `unique`). Custom `RegisterFormat` names are now invoked via explicit `jsonschema:"format=<name>"` rather than the bare-keyword shorthand. |
| `validate.RegisterFormat` | Returns `error` instead of panicking (the rest of the kit's option-style helpers panic on programmer error at construction time). The error return is preserved deliberately: `RegisterFormat` can be invoked after a Validator has already served traffic, so a panic there would be a runtime crash rather than a startup crash; existing callers branch on the error to surface "duplicate format" / "validator already frozen" through ops dashboards. Do not file this as an inconsistency. |
| `jsonschema:"..."` tag | The single constraint/description tag from v2.0.0. Tokens whose key matches a known constraint keyword (see above) are parsed as JSON-Schema constraints; every other token is concatenated as the property's description, so `jsonschema:"required,Customer e-mail"` records both the requirement and the description. |
| `validate.RegisterValidation(tag, validator.Func)` removed | Replaced by `validate.RegisterFormat(name string, fn func(any) error) error`. The function is registered with the underlying santhosh-tekuri compiler as a JSON-Schema `format` validator and dispatches whenever a field's `jsonschema:"format=<name>"` constraint matches. The closure shape no longer leaks any third-party validator type. |
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

#### New: OpenAPI 3.1 spec generation (wave 128)

The new `httpx/openapigen` package builds an OpenAPI 3.1 document
from kit-typed handlers and serves it via an opt-in `http.Handler`.
Every typed handler already carries a statically known request +
response type; `core/v2/validate.SchemaFor[T]` (wave 124) turns those
into JSON Schemas; OpenAPI 3.1's JSON Schema 2020-12 alignment lets
the kit embed those schemas directly into operations without a
third-party openapi dep.

| Area | Addition |
|---|---|
| `openapigen.NewSpec(title, version)` | Constructs an empty `Spec`. |
| `openapigen.Handle[Req, Resp](mux, spec, method, path, logger, fn, opts...)` | Mux + spec registration in one call — uses `httpx.JSON[Req, Resp]` for the runtime handler. Sibling helpers cover the `JSONStatus` / `JSONNoBody` / `JSONNoBodyStatus` / `NoContent` shapes. |
| `Spec.Register(method, path, opts...)` | Lower-level entry point for callers that wire the runtime handler separately. |
| `Spec.Handler()` | Returns an `http.Handler` that serves the rendered document as `application/json` on GET / HEAD. Mount it explicitly via `mux.Handle("/openapi.json", spec.Handler())` — the kit does not pick the URL for you. |
| `WithRequestType[T]() / WithResponseType[T](status)` | Generic options that pull the schema from `validate.SchemaFor[T]`. `WithRequestSchema` / `WithResponseSchema` accept an explicit `*jsonschema.Schema` for callers that want bespoke shapes. |
| `WithParameter` | Attach query / path / header / cookie parameters. The kit does NOT auto-discover them from `net/http` pattern wildcards because the stdlib grammar does not expose typed parameter metadata. |
| `WithSecurity` / `Spec.AddSecurityScheme` / `Spec.SetGlobalSecurity` | Per-operation + document-level security requirements; security schemes follow the OAS 3.1 `securitySchemes` object. |
| Scope limits | The initial wave deliberately ships a narrow surface: single response per status code per route, no auto-discovered parameters, no per-operation example payloads, no tag-object emission (per-operation `tags` strings only). These are not architectural limits and will extend in later waves without breaking the present API. |
| Public API stability | `httpx.JSON` / `httpx.Handle` and all sibling helpers retain identical signatures — the OpenAPI integration is additive and opt-in. Services already on the v2 typed handlers can adopt the spec generator by switching `httpx.Handle` for `openapigen.Handle` at registration time. |
| `Operation.Security` shape (wave 131) | Field is `*[]map[string][]string` (pointer-to-slice) so an explicit `WithSecurity()` with no requirements emits the JSON shape `"security": []` to opt the operation out of the document-level requirement. The pre-wave-131 plain-slice shape collided with Go's `json:",omitempty"` rule: a caller that passed an empty slice silently re-enabled the document-level security on every wire round-trip. Callers constructing `Operation` literally now pass `&[]map[string][]string{}` for the anonymous-override case; the `WithSecurity` helper handles this for them. |
| Schema-error scrubbing vs `httpx/mcp` (wave 131) | When a registered handler's request type fails schema generation, `openapigen` surfaces the underlying error chain (including `validate.ErrCyclicSchema` / `validate.ErrUnsupportedType` via `errors.Is`); the parallel `httpx/mcp` registration path scrubs the inner type name from the error message. The asymmetry is intentional: `openapigen` errors surface at service-boot time and the caller is the programmer who declared the bad type, so the type name is triage-useful. `httpx/mcp` errors can surface during `tools/list` at runtime where the response goes to a remote MCP client; the kit refuses to echo internal type names in that path. |

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
| `WithCallbackDrainTimeout` | **Retained.** Default behaviour (no option) is wait-forever: `holdLeadership` blocks until `Callbacks.OnAcquired` returns, so the same process cannot run two overlapping leadership terms. The cancelled `OnAcquired` context is the only signal — your callback MUST observe `ctx.Done()` and return promptly. Passing a positive duration enables fail-fast shutdown: when the timeout fires the elector records a `drainStateTimeout` metric observation and returns `Run` wrapping `ErrCallbackDrainTimeout`. The orphan goroutine is left to finish (Go has no goroutine kill); the orchestrator MUST treat the timeout as a fatal signal and exit/restart rather than retry in-process. See `TestHoldLeadership_LossDoesNotReturnUntilCallbackDrains` in each adapter for the wait-forever path. |
| Callback-drain visibility | Both adapters now expose `NewMetrics`, `WithMetrics`, and `WithCallbackDrainWarnInterval` so a stalled `OnAcquired` callback is operator-visible. The watchdog logs a warn and increments `leaderelection_callback_drain_warn_total{key}` every 30 seconds (override via `WithCallbackDrainWarnInterval`), and the terminal drain duration lands in `leaderelection_callback_drain_seconds{state="drained",key=...}` with `state="pending"` snapshots on each warn tick. Buckets: `[1, 5, 10, 30, 60, 120, 300]` seconds. |

### `infra/leaderelection/k8slease` (new)

| Area | Migration |
|---|---|
| New adapter | `infra/leaderelection/k8slease` (wave 127) implements `leaderelection.Elector` on top of `k8s.io/client-go/tools/leaderelection`. It competes for a Kubernetes `coordination.k8s.io/v1` Lease object so kubectl shows who currently leads — recommended when the service already runs on Kubernetes and the operator wants leader-election state to live in the same control plane as the workload. |
| Construction | `k8slease.New(client kubernetes.Interface, namespace, name, identity string, opts ...Option)`. Identity MUST be unique per replica (typically `POD_NAME`). Options: `WithLeaseDuration` / `WithRenewDeadline` / `WithRetryPeriod` (defaults `15s / 10s / 2s` mirror client-go upstream), plus the same `WithLogger`, `WithMetrics`, `WithCallbackDrainWarnInterval`, and `WithCallbackDrainTimeout` shapes as the pgadvisory / redislock adapters. |
| Heavy-SDK boundary | This is the only place inside the kit that depends on `k8s.io/client-go`. Consumers that do not run on Kubernetes never import this package and never pull the dep transitively — `make check-dependency-boundaries` enforces the same isolation that holds for the AMQP, NATS, pgx, and cloud-storage adapters. |
| Run semantics | Unlike pgadvisory / redislock which loop the acquire path, client-go's `LeaderElector` already owns the acquire / renew / retry loop and is one-shot. `Run` delegates to it and returns once leadership ends (either ctx cancel or the Lease was taken by a peer). Callers that want continuous re-election should wrap `Run` in `lifecycle.Runner` (its restart policy handles this naturally). `ReleaseOnCancel` is enabled so an orderly shutdown hands the Lease back to peers immediately rather than forcing them to wait out the full lease duration. |
| Metrics | `leaderelection_callback_drain_seconds{namespace,name,state}` and `leaderelection_callback_drain_warn_total{namespace,name}` — the Lease coordinates (namespace, name) replace `key` because they match the operator's mental model for Kubernetes objects. |

### `infra/leaderelection/etcd` (new)

| Area | Migration |
|---|---|
| New adapter | `infra/leaderelection/etcd` (wave 160) implements `leaderelection.Elector` on top of `go.etcd.io/etcd/client/v3/concurrency`. Recommended for bare-metal / VM deployments that already run etcd for service-discovery or configuration; pairs naturally with services where `kubectl` is not available but `etcdctl` is. |
| Construction | `etcd.New(client *clientv3.Client, electionKey, identity string, opts ...Option)`. Election key MUST begin with `/`, must not exceed 256 bytes, and must not contain control bytes. Identity MUST be unique per replica. Options: `WithLeaseTTLSeconds` (default 15), `WithReacquireBackoff` (default 2 s), `WithLogger`, `WithMetrics`, `WithCallbackDrainWarnInterval`, `WithCallbackDrainTimeout` — same shape as k8slease / pgadvisory / redislock. |
| Heavy-SDK boundary | This is the only place inside the kit that depends on `go.etcd.io/etcd/client/v3`. Consumers that do not run on etcd never import this package and never pull the dep transitively. `make check-dependency-boundaries` enforces the same isolation that holds for `k8s.io/client-go`, the AMQP/NATS/Kafka backends, and the cloud-storage adapters. |
| Run semantics | Unlike k8slease's one-shot `LeaderElector.Run`, the etcd adapter loops internally: it Campaigns, holds the term while the session is healthy, drains `OnAcquired`, runs `OnLost`, and re-Campaigns. `Run` returns only when the caller ctx is cancelled or `WithCallbackDrainTimeout` fires. The session is closed and the election is explicitly Resigned on planned shutdown so peers do not wait out the lease TTL. |
| Metrics | `leaderelection_callback_drain_seconds{election,state}` and `leaderelection_callback_drain_warn_total{election}` — the `election` label is the operator-configured key prefix, validated via `promutil.ValidateStaticLabelValue` so a misconfigured caller cannot inflate cardinality. |

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
| New transitive dependency | Adds `github.com/go-redsync/redsync/v4` plus `github.com/hashicorp/go-multierror` (indirect). Reviewed and listed in `docs/audit/dependency-allowlist.txt`. The single-pool `redislock.Locker` keeps the kit on the same operational contract as before; the multi-master quorum variant ships in the `redislock/redlock` sub-package (wave 159) for deployments that need single-instance failure tolerance. |

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
| Handler `context.Context` is not the queue shutdown ctx | The asynq server is wired with `BaseContext: func() context.Context { return context.Background() }`. The ctx your handler receives carries asynq's per-task deadline (driven by `WithInvisibilityTimeout`) but does NOT inherit the parent ctx passed to `Process`. This matches asynq's own contract — operators rely on the per-task deadline for "kill stuck handlers" and `Process`'s shutdown is bounded by `WithShutdownTimeout` instead. Long-running handlers that need to observe pre-shutdown cancellation should subscribe to their own cancellation source (e.g. a shared `context.Context` held by the service) rather than relying on the handler argument. |
| `Process` start-error visibility | If the underlying `asynq.Server.Start` fails (e.g. invalid Redis credentials, broker unreachable at boot), `Process` logs the error via the configured `slog.Logger` and returns silently — there is no error return on the `Process` signature because it is invoked from `StartProcessors` inside a goroutine. Operators MUST monitor the `redisqueue` log stream during service start; a missing `redisqueue: asynq server failed to start` line is the only positive signal. The same shape applies to `redisstream.Consumer.Consume`. |

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
| `data/cache/rediscache`, `data/lock/redislock`, `data/stream/redisstream`, `data/budget/redis`, `data/idempotency/redisstore`, `data/queue/redisqueue` returned errors | Wave 136: backend driver errors are now wrapped via `redact.WrapError` / `redact.WrapSentinel` rather than `fmt.Errorf("...: %w", err)`. `errors.Is` / `errors.As` against the returned error still walks the original chain, but `err.Error()` renders `<prefix>: <redacted error: T>` instead of the driver's verbatim text. Downstream code that relied on substring-matching the driver's message (e.g. `strings.Contains(err.Error(), "READONLY")`) must switch to `errors.Is` against the kit sentinel; code that propagates `err.Error()` into HTTP response bodies or untrusted log sinks is now safe by default. |

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

## 6.X New v2.0.0 Primitives (Wave 150+)

### `messaging.BindingSpec.Queue` renamed to `ConsumerGroup` (Wave 155)

The `Queue` field on `messaging.BindingSpec` and `messaging.Binding`
has been renamed to `ConsumerGroup`. The old name was an AMQP-ism —
in AMQP a queue and a consumer group are 1:1, but Kafka and Redis
Streams have an explicit `(topic|stream, consumer-group)` split and
the kit's wrappers already interpreted `Queue` as "consumer group
identity" for those backends. The rename makes the cross-backend
invariant explicit.

```go
// Before (v1.x / pre-v2):
spec := messaging.BindingSpec{
    Exchange:     "orders",
    ExchangeType: messaging.ExchangeTopic,
    Queue:        "billing-worker",
    RoutingKey:   "order.paid",
}

// After (v2.0.0):
spec := messaging.BindingSpec{
    Exchange:      "orders",
    ExchangeType:  messaging.ExchangeTopic,
    ConsumerGroup: "billing-worker",
    RoutingKey:    "order.paid",
}
```

`Binding.RetryQueue` and `Binding.DeadQueue` are unchanged — those
fields represent AMQP topology artifacts that legitimately model
queues, not consumer groups, and are populated only by the AMQP
backend.

`ValidateBindingSpecs` now returns `"consumer group must not be
empty"` instead of `"queue name must not be empty"`; downstream
assertions that match on that substring must be updated.



### Saga compensable workflows (`runtime/saga`)

Wave 150 ships `runtime/saga` — an in-memory orchestrator for
multi-step workflows where each forward action has a compensation.
Use when:

- A workflow has more than one external side effect (debit wallet →
  reserve inventory → send email) and any of them might fail.
- The compensating action is well-defined (refund the debit, un-
  reserve inventory) and you want the kit to drive rollback in
  reverse order rather than re-implementing it per service.

```go
def := saga.MustDefinition(
    saga.Step{
        Name: "debit-wallet",
        Forward:    debitWallet,
        Compensate: refundWallet,
    },
    saga.Step{
        Name: "reserve-inventory",
        Forward:    reserveInventory,
        Compensate: releaseReservation,
    },
)
if err := saga.Run(ctx, def, &orderState{}); err != nil {
    // saga.ForwardError + (optional) saga.CompensateError joined.
}
```

The package preamble documents the planned redisqueue + outbox + DB
table wiring that future waves layer on top of `Run` for crash-safe
sagas. v2.0.0 ships only the in-memory executor and the type
vocabulary (Step / Definition / Run / ForwardError / CompensateError)
so downstream services can adopt the API now and inherit the
persistence wiring when it lands.

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

### New: jwtutil.SigningProvider

`security/jwtutil` was historically verify-only — services consumed
tokens minted by external IdPs and the kit's mandate stopped at
JWKS-backed verification. v2.0 keeps that posture by default (the
`Verifier` / `Provider` surface is unchanged) and adds
`jwtutil.SigningProvider` so services that need to issue short-lived
JWTs can do so without rolling a parallel issuance path against the
same `lestrrat-go/jwx/v3` dependency the verifier already pulls in.

Mirrors `crypto/paseto.SigningProvider` for lifecycle parity:

- Construct via `jwtutil.NewSigningProvider(ctx, rotator, opts...)`.
- `rotator` is a `KeyRotator func(ctx) (crypto.PrivateKey, error)` —
  invoked once synchronously at startup and again on every
  `WithSigningRotationInterval` tick.
- `Sign(claims)` / `SignContext(ctx, claims)` emit a signed JWT using
  the current epoch's key.
- `Close()` terminates the refresh goroutine and drops the cached key
  reference.

Required options (mirrors the verify-side `NewProvider` guardrails):

- `WithSigningRotationInterval(d)`.
- Either `WithSigningExpectedIssuer(s)` or
  `WithSigningAllowAnyIssuer()`.
- Either `WithSigningExpectedAudience(s)` or
  `WithSigningAllowAnyAudience()`.

Optional knobs:

- `WithSigningMethod(jwa.SignatureAlgorithm)` — default `ES256`.
  Symmetric `HS*` algorithms and `none` are rejected.
- `WithSigningDefaultLifetime(d)` — exp default when
  `Claims.ExpiresAt` is zero.
- `WithSigningMaxStale(d)` / `WithoutSigningMaxStaleLimit()` — bound
  how long Sign keeps using the previous key after rotator failures.
- `WithSigningFetchTimeout(d)` — per-refresh deadline.
- `WithOnSigningRefreshError(fn)` — wire a metric/alert; refresh
  failures stay silent otherwise.
- `WithIssuedJTIRecorder(rec)` — forward every issued jti to a
  caller-owned ledger so a verifier-side revocation store can later
  mark the jti revoked. The recorder is consulted AFTER signing and
  BEFORE Sign returns; a recorder error fails the Sign call.

Behavioural notes (deviations from `crypto/paseto.SigningProvider` are
called out where the JWT spec or jwx's API forces a different shape):

- **Key rotation.** SigningProvider holds a single in-memory key
  reference and swaps it atomically on each successful rotation. The
  previous key is released to the GC — there is no grace window on
  the issuance side. The verifier-side JWKS is the grace-window
  owner; pick a rotation interval substantially shorter than the
  verifier-side overlap window. (Same as paseto.)
- **jti tracking.** Sign mints a 128-bit random jti per token unless
  `Claims.ID` is set. Optional `WithIssuedJTIRecorder` lets the
  caller register issued jtis in their own ledger — the kit's
  `security/jwtutil/revocation.Store` does NOT track issued jtis
  itself; wire a custom `IssuedJTIRecorder` if you need that
  mapping. (paseto leaves jti tracking to the application.)
- **Audience.** A single audience is pinned at construction; the
  verifier-side confused-deputy guardrail (RFC 7519 §4.1.3) is
  mirrored on the issuance side. Multi-audience tokens require
  multiple SigningProvider instances — Sign rejects caller-provided
  audience overrides to keep the posture auditable. (Same as
  paseto.)
- **Kid.** Each rotated signing key is tagged with its RFC 7638
  thumbprint as the `kid` header so verifier-side JWKS lookups work
  by construction. paseto carries no kid because v4.public verifies
  by trying every public key — JWT-side multi-key JWKS need the kid
  hint. Operators that pin a custom kid can stamp it on the
  `crypto.PrivateKey` before returning it from the rotator… or
  publish a JWKS that lists the thumbprint kid; either pairing
  works.
- **Algorithm.** `ES256` default mirrors the kit's verify-side
  default. `RS256`/`PS256`/`PS384`/`PS512` and `EdDSA` are supported
  for adopters wiring against JWKS-backed IdPs that publish those
  algorithms.

### New: kafkabackend

`infra/messaging/kafkabackend` adapts Apache Kafka (via
`github.com/segmentio/kafka-go`) to the kit's `messaging.Publisher` and
`messaging.Consumer` contracts. It joins `amqpbackend`, `natsbackend`,
`redisbackend`, and `membroker` as a first-class backend; no Builder
adapter module is provided yet — services wire `kafkabackend.NewPublisher`
and `kafkabackend.NewSubscriber` directly.

Mapping notes (full detail in the package doc):

- `exchange` → Kafka topic.
- `routingKey` → record key (drives partition assignment under the
  default `kafka.Hash` balancer) and an `X-Routing-Key` record header.
- `messaging.Message` → JSON-encoded record `Value`.
- `messaging.Binding.ConsumerGroup` → must match the subscriber's
  consumer group when non-empty (mirrors `redisbackend` FR-064).
- Ack semantics: handler `nil` → `Reader.CommitMessages`. Handler
  error → offset NOT advanced; redelivered after rebalance/restart.
  `apperror.IsPermanent` → offset committed (poison-pill discard).
- Retry / dead-letter (`Binding.Retry`) is NOT honoured — Kafka has no
  native per-message redelivery. Wrap handlers in
  `resilience/retry` or implement a dead-letter topic at the producer
  level. The subscriber logs a WARN when `Binding.Retry` is set so
  the surprise surfaces at startup.
- Offset reset: `kafkabackend.WithStartOffset(kafka.FirstOffset)`
  (default) or `kafka.LastOffset` controls where new consumer groups
  begin. Existing groups always honour their committed offsets.
- Defaults: `kafka.Snappy` compression, `kafka.RequireAll` durability,
  10ms `BatchTimeout`, single-message `BatchSize` (synchronous publish
  latency). Override with `kafkabackend.WithCompression`,
  `WithRequiredAcks`, `WithBatchTimeout`, `WithBatchSize`, etc.

Integration tests live in
`infra/messaging/kafkabackend/integrationtest` and spin up a Confluent
Local broker via Testcontainers (`-tags integration`).

Operator notes:

- **At-least-once redelivery on shutdown.** `Subscriber.commitWithOutcome`
  uses the still-cancelled parent ctx when the subscriber is shutting
  down. If a handler completes successfully but the parent ctx has
  already been cancelled by the orchestrator, the commit may fail and
  the next consumer to join the group will see the same message
  redelivered. This is the intended at-least-once shape — kafkabackend
  prefers redelivery over silently advancing the offset on a
  cancelled commit. Handlers MUST be idempotent.
- **SASL OAUTHBEARER is not supported.** The current kafka-go SASL
  surface covers PLAIN and SCRAM. Cloud-managed Kafka brokers that
  mandate OAUTHBEARER (Confluent Cloud, AWS MSK with IAM) cannot wire
  through kafkabackend today; tracked as a follow-up wave.
- **Metrics label cardinality.** Both publisher-side and consumer-side
  metrics default to `WithOpaqueRouteLabels` / `WithOpaqueConsumeLabels`
  in v2 (wave 140), projecting `(topic, group)` and `(topic, routing_key)`
  through `promutil.OpaqueLabelValue` so per-tenant naming cannot blow
  up Prometheus cardinality. Deployments with audited static topology
  can opt out via `WithRawRouteLabels` / `WithRawConsumeLabels`.

## Messaging metric label cardinality (resolved in wave 140)

`infra/messaging/natsbackend` and `infra/messaging/kafkabackend` project
consume-side `(stream, durable)` (NATS) and `(topic, group)` (Kafka)
labels through `promutil.OpaqueLabelValue` by default as of wave 140.
This matches the kit-wide HTTP-route convention and AMQP publisher-side
labels (wave 36). Deployments with audited static stream/group naming
can opt out per backend with `WithRawConsumeLabels`.

## 9. Things Not Migrated In v2.0.0

The following remain out of scope for v2.0.0 and should not block adoption:

- Additional managed-KMS adapters beyond the currently frozen AWS KMS, Azure
  Key Vault, Google Cloud KMS, and HashiCorp Vault Transit adapter modules.

Kubernetes/coordination.k8s.io leader-election shipped in wave 127 (kept).
etcd leader-election is now in scope and tracked as a v2.0.0 wave.
