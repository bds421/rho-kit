# rho-kit v2 landed review round 2

Date: 2026-05-07
Branch: `main`
Scope: current landed tree of `github.com/bds421/rho-kit`.

Notes:

- This is a code review snapshot, not a status ledger. Existing comments and audit docs were treated as non-authoritative.
- Line references are to the current `main` checkout at review time and may shift after fixes land.
- Only packages with concrete findings are listed.
- No commits were made.

Verification during this round:

| Check | Result | Notes |
|---|---|---|
| `make test` | Pass | Full workspace unit test suite completed. |
| `make lint` | Fail | `errcheck` flags unchecked `Close` calls in `cmd/kit-bench-gate/main.go:49` and `cmd/kit-bench-gate/main.go:55`. |
| `git diff --check` | Pass | Markdown-only working tree diff has no whitespace errors. |

## cmd/kit-bench-gate

### HIGH - lint is red because file close errors are unchecked

Evidence: `cmd/kit-bench-gate/main.go:44-55` opens the baseline and current files and defers `baseFile.Close()` / `curFile.Close()` without checking the returned errors.

Impact: `make lint` currently fails, which blocks the documented golden-path quality gate. It also hides close/writeback errors on filesystems that can report errors at close.

Fix: close explicitly through a helper that joins close errors into the command error path, or defer a closure that records/logs the close error.

### MEDIUM - new benchmarks are underreported across metrics

Evidence: `cmd/kit-bench-gate/compare.go:40-58` creates one `seen` map outside the metric loop. The baseline-name loop marks names as seen for the first metric, and the current-only loop then suppresses those names for every later metric.

Impact: a benchmark that is present only in the current file is emitted for `ns/op` but not for later metrics such as `B/op` and `allocs/op`. The report can miss new allocation regressions.

Fix: build the seen set per metric, or compute missing/new benchmark names once and emit one diff per requested metric.

### LOW - unknown `-fail-on` metric names silently disable gates

Evidence: `cmd/kit-bench-gate/main.go:81-89` converts comma-separated input directly into `Metric` values and never rejects unknown tokens.

Impact: a typo such as `alloc/op` produces a gate that looks configured but never matches a real metric.

Fix: validate every token against the supported metric enum and exit with usage text on unknown names.

## cmd/kit-doctor

### HIGH - idempotency rule false-positives on valid middleware usage

Evidence: `cmd/kit-doctor/rules/idempotency_user_extractor.go:24-28` calls `chainHas` to find `WithUserExtractor` or `WithAllowSharedKeys`. `chainHas` in `cmd/kit-doctor/rules/helpers.go:36-55` only walks fluent selector chains. The real API takes these as variadic options to `idempotency.Middleware(...)`, not as methods on a returned builder.

Impact: correct code such as `idempotency.Middleware(store, idempotency.WithUserExtractor(fn))` is reported as critical. That makes the tool noisy exactly around a security rule operators need to trust.

Fix: inspect the call arguments for package calls named `WithUserExtractor` and `WithAllowSharedKeys`, including imported aliases.

### MEDIUM - `http.DefaultClient` detection is identifier-based, not import-based

Evidence: `cmd/kit-doctor/rules/default_http_client.go:23-28` only checks whether the selector receiver is an identifier named `http`.

Impact: `net/http` imported under an alias evades the rule, while a local variable named `http` can be falsely flagged.

Fix: resolve imports and match the selector against the `net/http` package path.

### MEDIUM - generated files are scanned unless they use two specific suffixes

Evidence: `cmd/kit-doctor/engine.go:34-37` skips only `_gen.go` and `.pb.go`, while the Go convention is the `// Code generated ... DO NOT EDIT.` header.

Impact: generated files with other names can produce findings that the user cannot or should not fix by hand.

Fix: read the file header and skip canonical generated-code files before parsing or before running rules.

## core/config

### MEDIUM - config reloaders accept nil dependencies and panic later

Evidence: `core/config/watcher.go:105-120` stores `loadFn` and `watchable` without validation. `Start` later calls `fw.loadFn` and `fw.watchable.Set` at `core/config/watcher.go:151-159`. `NewEnvReloader` similarly accepts a nil watchable at `core/config/watcher.go:223-227` and calls `r.watchable.Set` at `core/config/watcher.go:250` and `core/config/watcher.go:266`.

Impact: startup succeeds and the service only panics when a reload event occurs. That violates the kit convention of failing fast on configuration wiring errors.

Fix: panic or return an error at construction for nil `Watchable` and nil load functions.

## data/actionlog

### HIGH - hash chain breaks across key rotation

Evidence: `data/actionlog/actionlog.go:385-405` resolves the current key and uses the current secret to compute the previous-entry hash. `VerifyChain` later resolves `prev.SignatureKeyID` and hashes the previous entry with the previous entry's key at `data/actionlog/actionlog.go:474-478`.

Impact: if the active signing key changes between two entries, the appended `PrevHash` is computed with the new key but verification recomputes it with the old key. A normal rotation can make an intact chain unverifiable.

Fix: make `PrevHash` key-independent, or compute and verify it consistently with the previous entry's key.

### HIGH - first Postgres append per tenant is not serialized

Evidence: `data/actionlog/postgres/store.go:63-74` locks the latest row with `SELECT ... FOR UPDATE`. When a tenant has no rows, there is no row to lock. Two concurrent first appends can both build sequence `1`; one then fails the unique constraint at `data/actionlog/postgres/store.go:97-99`.

Impact: callers see an append failure for a valid first event under concurrency. The `Store` contract expects `AppendChained` to serialize build and persist for a tenant.

Fix: lock an advisory key derived from tenant id, or maintain a tenant-head row that is always locked before building the next entry.

### MEDIUM - test hooks can nil out required functions

Evidence: `WithClock` and `WithIDFunc` directly assign caller functions at `data/actionlog/actionlog.go:340-347`. `Append` calls `l.clock()` at `data/actionlog/actionlog.go:381` and `l.newID()` at `data/actionlog/actionlog.go:394`.

Impact: a nil test option becomes a production panic on the first append instead of failing at construction.

Fix: reject nil functions in the options.

## data/approval and httpx/middleware/approval

### MEDIUM - nil option callbacks can create request-time panics

Evidence: middleware options assign function/logger fields directly at `httpx/middleware/approval/approval.go:71-112`; the request path calls them at `httpx/middleware/approval/approval.go:155`, `httpx/middleware/approval/approval.go:161`, `httpx/middleware/approval/approval.go:180`, `httpx/middleware/approval/approval.go:183`, `httpx/middleware/approval/approval.go:184`, and logs with `cfg.logger` at `httpx/middleware/approval/approval.go:173` and `httpx/middleware/approval/approval.go:192`. Store clock options do the same at `data/approval/memory/memory.go:33-35` and `data/approval/postgres/store.go:48-50`.

Impact: `WithTenantSource(nil)`, `WithActionExtractor(nil)`, `WithResourceExtractor(nil)`, `WithIDFunc(nil)`, `WithLogger(nil)`, or store `WithClock(nil)` all construct successfully and panic later.

Fix: reject nil callbacks/loggers at option application or normalize nil loggers/clocks back to defaults.

### LOW - expiry boundary is inclusive by accident

Evidence: memory store expiry uses `s.clock().After(r.ExpiresAt)` at `data/approval/memory/memory.go:132`; Postgres uses `now.After(r.ExpiresAt)` at `data/approval/postgres/store.go:175`.

Impact: a request is still approvable at the exact `ExpiresAt` instant, even though expiry timestamps are normally exclusive boundaries.

Fix: expire when `!now.Before(expiresAt)`.

## data/cache

### HIGH - MemoryCache reports successful writes that Ristretto rejected

Evidence: `data/cache/memory_cache.go:242-249` calls `SetWithTTL` and returns nil even when it returns false. `SetNX` then waits and records an authoritative claim at `data/cache/memory_cache.go:314-323`.

Impact: `Set` can silently drop a value. `SetNX` can return `true` while the value was never admitted, then block future `SetNX` calls until the claim expires. That violates cache and compute-once expectations.

Fix: surface admission failure as an error, or rework the backend so `Set`/`SetNX` provide deterministic semantics independent of Ristretto admission.

### MEDIUM - ComputeCache error metrics are wrong under singleflight sharing

Evidence: `data/cache/compute.go:277-284` records an error only when `shared` is false. `singleflight.Group.Do` can return `shared=true` to the leader when duplicate callers joined the same call.

Impact: compute errors during contention can be undercounted or missed entirely.

Fix: record compute errors inside `executeCompute`, or record once per group execution instead of keying metrics on the returned `shared` flag.

### MEDIUM - Redis cache accepts a nil Redis client

Evidence: `data/cache/rediscache/cache.go:98-111` stores `client` without validation. Methods dereference it starting at `data/cache/rediscache/cache.go:119` and `data/cache/rediscache/cache.go:143`.

Impact: a miswired Redis cache panics on first use instead of failing at startup.

Fix: reject nil clients in `NewRedisCache`.

## data/idempotency and httpx/middleware/idempotency

### HIGH - middleware accepts a nil store

Evidence: `httpx/middleware/idempotency/idempotency.go:250-258` validates user scoping but never validates `store`. The first request dereferences it at `httpx/middleware/idempotency/idempotency.go:313` and `httpx/middleware/idempotency/idempotency.go:337`.

Impact: a missing idempotency backend is a request-time panic, not a startup failure.

Fix: panic at middleware construction when `store` is nil.

### MEDIUM - Redis idempotency store accepts a nil Redis client

Evidence: `data/idempotency/redisstore/store.go:83-91` stores the client without validation.

Impact: the first Redis store method will panic when it dereferences `s.client`.

Fix: reject nil clients in `redisstore.New`.

### LOW - invalid header option creates confusing runtime behavior

Evidence: `WithHeader` directly assigns the name at `httpx/middleware/idempotency/idempotency.go:142-145`; requests read it at `httpx/middleware/idempotency/idempotency.go:267`.

Impact: `WithHeader("")` makes the middleware ask for an empty header name and return confusing errors on every required request.

Fix: require a non-empty valid HTTP field name.

### MEDIUM - nil logger option panics only on error paths

Evidence: `WithLogger` assigns nil at `httpx/middleware/idempotency/idempotency.go:147-150`; error paths call `cfg.logger` at `httpx/middleware/idempotency/idempotency.go:376-378`, `httpx/middleware/idempotency/idempotency.go:386-393`, and `httpx/middleware/idempotency/idempotency.go:414-423`.

Impact: backend failures or overflow paths can crash the server while normal happy-path traffic appears fine.

Fix: reject nil loggers or preserve the default when nil is passed.

## data/ratelimit

### MEDIUM - GCRA denies the exact retry boundary

Evidence: in-memory GCRA denies when `!now.After(allowAt)` at `data/ratelimit/gcra/gcra.go:159-162`. The Redis Lua path denies when `now <= allowAt` at `data/ratelimit/redis/redis.go:60-63`.

Impact: a client retrying exactly after the advertised delay can receive one more denial and another 1 ns retry value.

Fix: deny only when `now < allowAt`, and allow at equality.

### MEDIUM - nil clocks panic in all rate limiter variants

Evidence: GCRA `WithClock` assigns nil at `data/ratelimit/gcra/gcra.go:54-57` and calls it at `data/ratelimit/gcra/gcra.go:134` / `data/ratelimit/gcra/gcra.go:150`. Token bucket does the same at `data/ratelimit/tokenbucket/tokenbucket.go:55-58`, `data/ratelimit/tokenbucket/tokenbucket.go:125`, and `data/ratelimit/tokenbucket/tokenbucket.go:147`. Redis wraps the caller function without nil validation at `data/ratelimit/redis/redis.go:100-105`.

Impact: a misconfigured test hook becomes a runtime panic in production.

Fix: reject nil clock functions in all limiter option constructors.

## crypto/envelope

### MEDIUM - static KEK wrapping does not bind the key id

Evidence: `crypto/envelope/kekstatic/kekstatic.go:123` seals with nil AAD, and `crypto/envelope/kekstatic/kekstatic.go:152` opens with nil AAD.

Impact: the encrypted DEK is not cryptographically bound to the key id in the envelope. If the same master bytes are registered under multiple ids, tampering the envelope key id can still unwrap successfully under the alternate id.

Fix: include the key id as AEAD AAD in the KEK wrapper, or require the KEK interface to authenticate key-id binding.

### LOW - empty plaintext cannot be encrypted

Evidence: `crypto/envelope/envelope.go:118-121` rejects zero-length plaintext before generating a DEK.

Impact: optional encrypted fields or empty-but-present values cannot round-trip through the envelope API, even though AEAD encryption supports empty plaintext.

Fix: allow empty plaintext unless the caller opts into a higher-level validation rule.

## crypto/paseto

### HIGH - verified custom claims are dropped

Evidence: `buildToken` writes `Claims.Custom` into the token at `crypto/paseto/paseto.go:311-313`. `validate` initializes `Custom` at `crypto/paseto/paseto.go:325-326` and reads registered claims, but never copies non-reserved claims back out before returning at `crypto/paseto/paseto.go:375`.

Impact: roles, tenant ids, scopes, or other application claims can be present in a valid token but disappear after verification.

Fix: enumerate token claims, skip registered names, and copy the rest into `Claims.Custom` with strict type handling.

### MEDIUM - issuer/audience configuration can mint tokens the verifier rejects

Evidence: `buildToken` prefers caller-provided issuer over configured issuer at `crypto/paseto/paseto.go:294-300`. The verifier then enforces configured issuer and audience at `crypto/paseto/paseto.go:349-362`.

Impact: a signer configured for a specific service can create a token that its paired verifier rejects because caller claims override the configured values.

Fix: reject mismatched caller issuer/audience at sign/seal time, or always stamp configured values when configured.

### LOW - negative clock skew tolerance is accepted

Evidence: `WithClockSkewTolerance` assigns any duration at `crypto/paseto/paseto.go:113-117`.

Impact: a negative skew tightens `exp` and `nbf` checks in non-obvious ways.

Fix: reject negative tolerances.

## security/jwtutil

### HIGH - standalone JWKS provider allows any issuer and audience by default

Evidence: `NewProvider` accepts no required issuer/audience options at `security/jwtutil/jwtutil.go:264-285`. `KeySet.Verify` only appends issuer/audience checks when non-empty at `security/jwtutil/jwtutil.go:97-102`.

Impact: direct package users can verify any correctly signed token from the JWKS authority regardless of intended issuer or audience. The app builder may enforce stricter wiring, but the package-level constructor remains unsafe by default.

Fix: require issuer and audience by default, with explicit `WithAllowAnyIssuer` / `WithAllowAnyAudience` style opt-outs.

### MEDIUM - malformed permissions claims are silently downgraded

Evidence: `security/jwtutil/jwtutil.go:134-147` logs and continues when permissions are absent or invalid. `toStringSlice` at `security/jwtutil/jwtutil.go:162-174` drops non-string array elements.

Impact: a malformed token such as `permissions: [123]` becomes an empty permission set instead of an authentication failure. That can hide issuer drift and produce confusing authorization behavior.

Fix: distinguish missing claims from malformed claims and reject malformed permissions/scopes.

### MEDIUM - replacing `http.DefaultTransport` can panic provider construction

Evidence: `defaultHTTPClient` ignores the type assertion result at `security/jwtutil/jwtutil.go:350-351` and then calls `transport.Clone()`.

Impact: any process that replaces `http.DefaultTransport` with a custom `RoundTripper` can panic when constructing a default JWKS provider.

Fix: handle a failed assertion by constructing a fresh `http.Transport` or by returning a configuration error.

## httpx

### HIGH - `NewServer` with a nil handler serves the global default mux

Evidence: `httpx/httpx.go:142-145` assigns the provided handler directly to `http.Server.Handler`. In `net/http`, a nil handler means `http.DefaultServeMux`.

Impact: a miswired service can accidentally expose handlers registered globally by dependencies or tests.

Fix: panic on nil handler, or substitute `http.NotFoundHandler()`.

### MEDIUM - kit HTTP clients allow no timeout

Evidence: `httpx.NewHTTPClient` and tracing variants assign the caller timeout directly at `httpx/httpx.go:67-75`, `httpx/httpx.go:84-88`, and `httpx/httpx.go:94-102`.

Impact: `NewHTTPClient(0, ...)` produces a client with no global timeout even though the package exists to avoid unsafe default-client behavior.

Fix: reject non-positive timeouts or default them to the kit-safe timeout.

### MEDIUM - transport cloning assumes `http.DefaultTransport` is the stdlib transport

Evidence: `newKitTransport` asserts `http.DefaultTransport.(*http.Transport)` at `httpx/httpx.go:43-53`; `NewResilientHTTPClient` does the same at `httpx/resilient.go:113`.

Impact: processes that install a custom default transport can panic when constructing kit clients.

Fix: handle non-`*http.Transport` defaults by building a new `http.Transport` or by returning an explicit error.

## httpx/reqsign, httpx/sign, and httpx/middleware/signedrequest

### MEDIUM - direct reqsign body limits are never enforced

Evidence: `httpx/reqsign/reqsign.go:48-59` defines max body fields and options populate them at `httpx/reqsign/reqsign.go:99-117`. `SignRequest` and `VerifyRequest` apply options at `httpx/reqsign/reqsign.go:145-151` and `httpx/reqsign/reqsign.go:223-230`, but neither checks `len(body)` before signing/verifying.

Impact: callers that rely on `WithSignMaxBodySize` or `WithVerifyMaxBodySize` for direct API use can sign or verify oversized bodies.

Fix: enforce the configured body limit in `SignRequest` and `VerifyRequest`, or remove the option from the direct APIs.

### MEDIUM - outbound signing options can nil out required functions

Evidence: `httpx/sign.WithClock` and `WithNonceFn` assign caller functions directly at `httpx/sign/sign.go:67-75`; `RoundTrip` calls them at `httpx/sign/sign.go:117-118`.

Impact: a nil test hook panics on first outbound request.

Fix: reject nil functions in both options.

### LOW - signed request verifier accepts empty required header names

Evidence: `WithRequiredHeaders` appends lower-cased names directly at `httpx/middleware/signedrequest/signedrequest.go:107-112`; verification checks them at `httpx/middleware/signedrequest/signedrequest.go:175-178`.

Impact: `WithRequiredHeaders("")` makes every request fail with a missing empty header.

Fix: validate each header name with the same rules used for HTTP field names.

## httpx/mcp

### HIGH - JSON-RPC requests without `"jsonrpc": "2.0"` are accepted

Evidence: `httpx/mcp/server.go:91-95` rejects only when `req.JSONRPC` is non-empty and not `2.0`.

Impact: malformed or non-2.0 clients are accepted even though the endpoint claims JSON-RPC 2.0 semantics.

Fix: require `req.JSONRPC == "2.0"`.

### HIGH - JSON-RPC notifications receive responses

Evidence: `jsonRPCRequest.ID` is optional at `httpx/mcp/server.go:28-34`; `normaliseID` converts an absent id to `null` at `httpx/mcp/server.go:472-480`; the response path always writes a response.

Impact: JSON-RPC notifications are not supposed to receive responses. Clients that use notification semantics can deadlock or treat the server as non-compliant.

Fix: track whether the id member was present and suppress responses for notifications, except parse errors where the spec permits `id: null`.

### HIGH - strict action-log append can hang without a deadline

Evidence: synchronous audit uses `context.WithoutCancel(ctx)` at `httpx/mcp/actionlog.go:129-134`; `appendActionLog` calls `s.cfg.actionLogger.Append` directly at `httpx/mcp/actionlog.go:163-171`.

Impact: in strict mode, a hung audit store can hang the tool-call goroutine after the tool already completed side effects.

Fix: add a bounded audit append timeout and document the strict-mode failure behavior.

### HIGH - async audit enqueue can send on a closed channel

Evidence: `enqueueAuditJob` checks `auditStopped` at `httpx/mcp/actionlog.go:141-149` and then sends to `s.auditQueue` at `httpx/mcp/actionlog.go:150-151`. `Stop` sets `auditStopped` and closes the channel at `httpx/mcp/mcp.go:435-438`.

Impact: a concurrent request can observe `auditStopped == 0`, race with `Stop`, and panic with send-on-closed-channel.

Fix: protect stop/enqueue with a mutex or avoid closing the request-facing channel until no enqueuers can run.

### MEDIUM - schema permits unknown fields that runtime rejects

Evidence: runtime decoding uses `DisallowUnknownFields` at `httpx/mcp/server.go:279-282`, but generated object schemas at `httpx/mcp/schema.go:242-249` omit `additionalProperties: false`.

Impact: clients generated from the schema can send calls that look schema-valid but fail at runtime.

Fix: add `additionalProperties: false` for generated struct object schemas.

## grpcx

### HIGH - gRPC JWT auth accepts non-UUID subjects that HTTP auth rejects

Evidence: gRPC auth stores `claims.Subject` directly at `grpcx/interceptor/auth.go:144-151`. HTTP auth rejects non-UUID subjects at `httpx/middleware/auth/auth.go:147-150`.

Impact: HTTP and gRPC routes disagree on the identity contract. A malformed or foreign subject can pass gRPC auth and flow into authorization or business logic as a user id.

Fix: apply the same subject validation in gRPC auth, or centralize claim-to-context conversion.

## infra/messaging

### HIGH - AMQP consumer accepts nil critical dependencies

Evidence: `infra/messaging/amqpbackend/consumer.go:104-115` stores `conn`, `logger`, and later `handler` without validation. `ConsumeOnce` logs with `c.logger` at `infra/messaging/amqpbackend/consumer.go:172-178`, opens `c.conn.Channel()` at `infra/messaging/amqpbackend/consumer.go:181`, and invokes the handler at `infra/messaging/amqpbackend/consumer.go:299`.

Impact: a miswired consumer can panic during startup or on first delivery instead of failing immediately.

Fix: validate connector and logger in `NewConsumer`, default nil logger to `slog.Default()`, and reject nil handlers in `ConsumeOnce`.

### MEDIUM - NATS close has no deadline despite the contract

Evidence: `infra/messaging/natsbackend/natsbackend.go:140-147` says drain is best-effort with a 5s deadline, but the implementation only calls `c.nc.Drain()`.

Impact: shutdown can stall longer than operators expect when the NATS connection is unhealthy or has pending work.

Fix: use the NATS drain timeout API or wrap drain with a bounded context/timer.

### MEDIUM - durable buffered publisher starts empty after state-load failure

Evidence: `infra/messaging/buffered_publisher.go:182-185` logs `load` failure and continues with an empty pending queue even when a state file was configured.

Impact: corrupt or unreadable state can silently drop buffered messages after a restart.

Fix: fail startup on state-load errors unless an explicit lossy recovery option is set.

## infra/outbox

### HIGH - retry requeue can resurrect rows from the wrong state

Evidence: `MarkPublished` and `MarkFailed` guard on `status = processing` and classify zero rows at `infra/outbox/gormstore/gormstore.go:210-244`. `IncrementAttempts` updates by id only at `infra/outbox/gormstore/gormstore.go:289-302` and never checks `RowsAffected`.

Impact: a late publish failure can reset a row to pending even if another worker or stale-recovery path already moved it to published, failed, or another state.

Fix: guard `IncrementAttempts` with `status = processing`, check `RowsAffected`, and return `ErrStaleState` / `ErrNotFound` consistently.

### MEDIUM - store contract does not define stale handling for retry requeue

Evidence: `infra/outbox/store.go:43-49` says `IncrementAttempts` resets to pending but does not require status guarding or stale-state errors.

Impact: backend implementations can diverge on a high-concurrency failure path, making relay behavior backend-dependent.

Fix: extend the interface contract to match `MarkPublished` / `MarkFailed` semantics.

## infra/sqldb/gormdb

### MEDIUM - direct driver callers can nil the logger and panic

Evidence: MySQL driver logs through `logger` at `infra/sqldb/gormdb/gormmysql/driver.go:59`, `infra/sqldb/gormdb/gormmysql/driver.go:68`, `infra/sqldb/gormdb/gormmysql/driver.go:87`, and `infra/sqldb/gormdb/gormmysql/driver.go:97`. Postgres does the same at `infra/sqldb/gormdb/gormpostgres/driver.go:44`, `infra/sqldb/gormdb/gormpostgres/driver.go:72`, and `infra/sqldb/gormdb/gormpostgres/driver.go:81`. Replica registration logs at `infra/sqldb/gormdb/replica.go:53`.

Impact: direct users outside the app builder can crash database initialization by passing a nil logger.

Fix: normalize nil logger to `slog.Default()` at driver entry points.

### MEDIUM - Postgres TLS merge can undo verify-full hostname verification

Evidence: TLS mode is escalated to `verify-full` when a client TLS bundle is present at `infra/sqldb/gormdb/gormpostgres/driver.go:95-103`, but `mergeTLS` copies `clientTLS.InsecureSkipVerify` into the driver config at `infra/sqldb/gormdb/gormpostgres/driver.go:146-148`.

Impact: a caller-provided TLS config with `InsecureSkipVerify` defeats the strict verification implied by `verify-full`.

Fix: reject `InsecureSkipVerify` for Postgres client TLS, or ignore it when strict TLS is selected.

### LOW - direct Postgres driver default can still fall back to plaintext

Evidence: `buildPostgresDSN` defaults `sslmode` to `prefer` at `infra/sqldb/gormdb/gormpostgres/driver.go:90-97` when no higher-level validation is involved.

Impact: standalone direct users can silently negotiate plaintext against a server that declines TLS.

Fix: make direct driver construction require an explicit TLS mode, or default direct construction to `require` outside test/dev helpers.

## infra/storage

### MEDIUM - storage HTTP helpers accept nil backends

Evidence: `ParseAndStore` validates options but not `backend` at `infra/storage/storagehttp/upload.go:90-107`; the part writer calls `backend.Put` at `infra/storage/storagehttp/upload.go:200-202`. `ServeFile` calls `backend.Get` at `infra/storage/storagehttp/serve.go:48-54`.

Impact: route wiring with a nil storage backend panics during a request.

Fix: return a clear setup/request error when backend is nil.

### MEDIUM - SFTP logger option can nil out the default logger

Evidence: `WithLogger` assigns the provided pointer directly at `infra/storage/sftpbackend/sftp.go:106-110`; connection and health paths log through it at `infra/storage/sftpbackend/sftp.go:245` and `infra/storage/sftpbackend/sftp.go:512-513`.

Impact: `WithLogger(nil)` panics only on connect or health failure paths.

Fix: reject nil logger or preserve `slog.Default()`.

## runtime/lifecycle, runtime/batchworker, and runtime/cron

### MEDIUM - lifecycle runner accepts nil components and nil functions

Evidence: `Runner.Add` appends components without validation at `runtime/lifecycle/runner.go:69-71`; `AddFunc` wraps any function at `runtime/lifecycle/runner.go:76-78`; `Run` later calls `nc.component.Start` at `runtime/lifecycle/runner.go:158`.

Impact: invalid lifecycle wiring starts and fails inside a component goroutine instead of failing at registration.

Fix: panic on empty names, nil components, and nil functions in the add methods.

### MEDIUM - batch worker can block forever when stopped before start with a non-canceling context

Evidence: `New` creates `done` at `runtime/batchworker/batchworker.go:112-117`. `Stop` waits on `w.done` at `runtime/batchworker/batchworker.go:168-173`; `done` is only closed from `Start` at `runtime/batchworker/batchworker.go:140-144`.

Impact: `Stop(context.Background())` before `Start` blocks forever.

Fix: track started state and make pre-start `Stop` close or treat `done` as already complete.

### MEDIUM - batch worker logger option can nil out the default

Evidence: `WithLogger` assigns directly at `runtime/batchworker/batchworker.go:73-76`; `Start` and the run path log through `w.logger` at `runtime/batchworker/batchworker.go:131`, `runtime/batchworker/batchworker.go:186`, and `runtime/batchworker/batchworker.go:195`.

Impact: `WithLogger(nil)` creates a latent panic on start or panic-recovery paths.

Fix: reject nil loggers or preserve the default.

### MEDIUM - cron accepts nil jobs

Evidence: `Scheduler.Add` wraps any job function at `runtime/cron/cron.go:117-123`; the wrapper calls `fn(ctx)` at `runtime/cron/cron.go:210`.

Impact: a nil job is accepted at registration and panics only when the schedule fires.

Fix: panic at registration when `fn` is nil.

## observability

### MEDIUM - readiness handler accepts a nil checker

Evidence: `observability/health/handlers.go:47-49` closes over `checker` and calls `checker.Evaluate` on every request.

Impact: a miswired readiness endpoint panics instead of reporting unhealthy.

Fix: reject nil checkers or return a handler that consistently emits unhealthy.

### MEDIUM - audit retention batching is not guaranteed across GORM dialects

Evidence: `observability/auditlog/gormstore/store.go:267-286` relies on `Limit(retentionBatchSize).Delete(&auditEvent{})`.

Impact: SQL dialects such as PostgreSQL do not support a simple `DELETE ... LIMIT` form; GORM may ignore or rewrite the limit. The retention job can delete far more than one batch in a single transaction, defeating the batching intent.

Fix: select a batch of primary keys first, then delete by id list inside each loop.
