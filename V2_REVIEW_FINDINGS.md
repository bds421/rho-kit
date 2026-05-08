# rho-kit v2 Review Findings

Comprehensive review across 95 modules / ~58k LOC, 9 parallel domain reviewers.
Total findings: **~324** (12 CRITICAL · 92 HIGH · 145 MEDIUM · 75 LOW).

Reviewed: 2026-05-08 against `main` (commits up to `7a13f6a`).

## Status (this PR)

Fixes applied in the same review pass:

| Finding | Status | Notes |
|---|---|---|
| C1 — `slo` default error filter `code=5..` → `status=5..` | ✅ fixed | `observability/slo/slo.go:309-313`. Tests updated. |
| C2 — `redmetrics` no panic recovery | ✅ fixed | `observability/redmetrics/redmetrics.go:160-202`. Re-panics so outer recover can still log. |
| C3 — JWKS accepts HMAC keys (alg confusion) | ✅ fixed | `security/jwtutil/jwtutil.go:71-118`. Filter `kty=oct`. |
| C4 — `FieldEncryptor.Decrypt` plaintext passthrough | ✅ fixed | `crypto/encrypt/encrypt.go`. Strict by default; explicit `WithPlaintextRead()` opt-in for migrations. |
| C5 — HMAC signing: no canonical context binding | ✅ fixed | `crypto/signing/signing.go`. Added `CanonicalContext`, `SignContext`, `VerifyContext`. |
| C7 — redislock SETNX-error probe budget | 🟡 mitigated | Probe budget reduced to 250ms; deeper redesign deferred to v2 backlog. |
| C9 — `pgadvisory` FNV→SHA-256 | ✅ fixed | `data/lock/pgadvisory/pgadvisory.go`. SHA-256 truncated to int64. |
| C10 — `httpx/deadline_transport` body cancellation | ✅ fixed | `httpx/deadline_transport.go`. Cancel only on body close (cancelOnCloseBody wrapper). |
| C11 — AGENTS.md / docs golden path drift | ✅ fixed | AGENTS.md, docs/ai/bootstrap.md, docs/ai/sqldb.md, docs/ai/utilities.md. |
| C12 — `kit-migrate` empty-arg crash | ✅ fixed | `cmd/kit-migrate/main.go:73`. |
| HIGH — `passhash` no max password length DoS | ✅ fixed | `crypto/passhash/passhash.go`. `MaxPasswordLen=1024`. |
| HIGH F-5 — JWKS HTTPS enforcement, content-type, body cap | ✅ fixed | `security/jwtutil/jwtutil.go`. `WithAllowInsecureURL` opt-out for tests. |
| HIGH — kekstatic `RemoveKey` panic on active key | ✅ fixed | Returns error now. |
| HIGH — kekstatic `Wrap` defensive copy | ✅ fixed | GCM constructed under RLock. |
| HIGH F-23 — SFTP InsecureIgnoreHostKey explicit WARN | ✅ fixed | Loud startup log. |
| HIGH — `runtime/cron` no double-Start guard | ✅ fixed | `cron.go`. `started` flag rejects re-entry. |
| HIGH — `resilience/retry` no pre-attempt ctx check | ✅ fixed | `retry.go`. |
| HIGH — `resilience/retry` jitter clamp | ✅ fixed | `WithJitter` clamps to [0, 1]. |
| HIGH — `resilience/circuitbreaker` `context.Canceled` as failure | ✅ fixed | Default `IsSuccessful` ignores canceled (caller-side). |
| HIGH — `data/cache.MemoryCache.nxClaims` unbounded | ✅ fixed | Background sweeper, stopped via Close. |
| HIGH — `redis-queue` heartbeat masking transient errors | ✅ fixed | Tracks consecutive failures, exits loop after threshold. |
| HIGH — `io/atomicfile` symlink hazard | ✅ fixed | Refuses to write through symlink at destination. |
| HIGH F-24 — TLS 1.2 floor on `*tls.Config` entrypoints | 🟡 partial | `httpx.NewHTTPClient` and gormpostgres enforce TLS 1.2. Other entrypoints (httpx server, AMQP, MySQL) deferred. |
| HIGH F-18 — AGENTS.md decision tree omits modules | ✅ fixed | 24 missing modules added. |
| HIGH F-19 — `examples/app/main.go` doesn't compile | ✅ fixed | Rewritten against current `app.Builder` + `sqldb.Fields`. |
| LOW F-21 — stale TODOs in package docs | ✅ fixed | Removed obsolete TODOs in ratelimit, leaderelection, kekstatic. |
| HIGH — `infra/redis` health loop after dead | ✅ fixed | Loop returns when `dead` channel closes. |
| HIGH — `storagehttp` always set `nosniff` on serve | ✅ fixed | `serve.go`. Forces browsers to honor Content-Type. |
| HIGH — `gcsbackend` aborted writer leaks resumable session | ✅ fixed | `CloseWithError` on copy failure. |
| HIGH — `lifecycle.FuncComponent` no double-Start guard | ✅ fixed | `started` flag rejects re-entry. |
| MEDIUM — `batchworker` symmetric jitter | ✅ fixed | Centered on interval; ±maxJitter. |
| HIGH — `actionlog/memory.tenantMu` unbounded | ✅ fixed | sync.Map + `PruneTenants` helper. |
| HIGH — `leaderelection/redislock` Release unbounded ctx | ✅ fixed | 5s timeout + warn on non-ErrLockLost release errors. |
| HIGH — `leaderelection/pgadvisory` Release unbounded ctx | ✅ fixed | Same pattern as redislock. |

## Known deferred to v2 backlog

These were considered but not fixed in this pass — most are large API breaking changes that need their own focused PRs and consumer-coordination:

- **C6, C8** — Redis Lua atomicity rewrites in queue/idempotency (need careful Lua + tests).
- **F-12** — delete `infra/sqldb/deprecated.go` (breaks gormdb/* and dbtest/*; needs coordinated update).
- **F-13** — delete `infra/messaging/buffered_publisher_compat.go` (need to confirm no external consumers).
- **F-14** — delete deprecated `csrf.RequireCSRF`, `authz.SubjectFromHeader`, `httpx.RequestID`/`SetRequestID`.
- **F-1, F-4, F-6** — naming and clock unification across modules (mechanical sweep + breaking).
- **`core/contextutil.Key[T]` collision-proof construction** — breaking change.
- **All "Builder auto-applies stack.Default"** — substantial API redesign of `app.Builder`.
- **gRPC-server TLS auto-wiring from `serverTLS`** — Builder change.
- **Observability log-key alignment to OTel semantic conventions** — touches every package.
- **Many MEDIUM observability/health/runtime concerns** flagged by agents.

---

## Triage summary

| Severity | Count | What it means |
|---|---|---|
| CRITICAL | 12 | Correctness/security bugs exploitable now or causes silent data loss. Fix before tagging v2. |
| HIGH | ~92 | Real bugs, security gaps, API ergonomics that v2 should fix while breaking changes are free. |
| MEDIUM | ~145 | Polish, consistency, missing options, docs drift. Fix opportunistically. |
| LOW | ~75 | Cosmetic, defensive coding suggestions. Tracker-only. |

---

## CRITICAL findings (fix before v2 tag)

| # | Module | Issue | Location |
|---|---|---|---|
| C1 | `observability/slo` | Default error filter `code=5..` doesn't match what `redmetrics` emits (`status`). Default SLO silently always-healthy. | `slo.go:306` |
| C2 | `observability/redmetrics` | Middleware does not recover panics; panicked requests are invisible to RED metrics. | `redmetrics.go:160-184` |
| C3 | `security/jwtutil` | `ParseKeySet` accepts `oct`/HMAC keys from JWKS — classic alg-confusion. | `jwtutil.go:71-80` |
| C4 | `crypto/encrypt` | `Decrypt` passthrough on missing prefix → encryption can be silently bypassed. | `encrypt.go:117-150` |
| C5 | `crypto/signing` | HMAC signs `<ts>.<body>` only; no method/path/recipient binding → cross-endpoint replay. | `signing.go:69-80` |
| C6 | `data/queue/redisqueue` | Same-ID retry race in Lua tombstone — `removeByID` can target the wrong copy. | `helpers.go:35-47` |
| C7 | `data/lock/redislock` | SETNX-error probe path reuses token across retries; can falsely report "we already hold it". | `lock.go:417-440` |
| C8 | `data/idempotency/redisstore` | `Set` does GET-then-Lua; TOCTOU window can confuse fingerprints. | `store.go:253-294` |
| C9 | `data/lock/pgadvisory` | Key→int64 uses FNV-1a → adversarial collisions break mutual exclusion. | `pgadvisory.go:130-138` |
| C10 | `httpx/deadline_transport` | `defer cancel()` cancels response body reads on HTTP/2. | `deadline_transport.go:69-87` |
| C11 | docs / AGENTS.md | Golden-path uses `WithMariaDB(cfg, pool, models...)` but actual API is `WithMySQL(cfg, pool)` (no variadic models, no auto-migrate). Code AI agents copy will not compile. | `AGENTS.md:81`, `docs/ai/sqldb.md`, `docs/ai/bootstrap.md` |
| C12 | `cmd/kit-migrate` | Empty-string argument crashes via `arg[0]`. | `cmd/kit-migrate/main.go:73` |

---

## Findings by domain

Each section below lists HIGH/MEDIUM findings. Full per-finding details with line-numbers and suggested fixes are preserved in agent transcripts; this index links them by location.

### Core (`core/*`) — 28 findings

| Sev | Module | Issue | Location |
|---|---|---|---|
| HIGH | `apperror` | `FieldError.Code` is `string`, not typed `Code` | `errors.go:32` |
| HIGH | `apperror` | `NotFoundError.EntityID any` leaks formatting variance | `errors.go:54-58` |
| HIGH | `apperror` | `UnavailableError.RetryAfter` exists but no constructor | `errors.go:156-166` |
| HIGH | `contextutil` | `Key[T]{}` zero-value identity allows silent collisions across packages | `contextutil.go:33` |
| MEDIUM | `apperror` | `ConflictError` hardcoded `Retryable=true` | `errors.go:84-95` |
| MEDIUM | `apperror` | Type-based identity vs `tenant`'s sentinel pattern → inconsistency | `errors.go:60-166` |
| MEDIUM | `config` | `WithSignalChannel(ch)` doesn't wire signal.Notify for external channels | `watcher.go:39-49` |
| MEDIUM | `config` | `setField` lacks `float32`/full integer width parity | `load.go:225-289` |
| MEDIUM | `config` | `[]string` parsing silently drops empty + trims; no escape | `load.go:265-275` |
| MEDIUM | `config` | `Watchable.Set` notification ordering can diverge from store ordering | `watchable.go:46-67` |
| MEDIUM | `contextutil` | `NewID` silently degrades on `crypto/rand` failure (no error to caller) | `generate.go:24-31, 49-58` |
| MEDIUM | `safecast` | Saturating casts silently — no `(value, ok)` variant; missing common conversions | `safecast.go:8-37` |
| MEDIUM | `secret` | `RevealString` defeats zeroize-on-close guarantee — undocumented | `secret.go:108-115` |
| MEDIUM | `tenant` | `NewIDUnchecked` is a public escape hatch around the colon-separator check | `tenant.go:128-136` |
| MEDIUM | `validate` | Singleton validator races with init-time registration across packages | `validate.go:50-78` |

(Plus 13 LOW items — `randstr` per-rune syscall cost; `secret` two construction modes; missing tests for `ForbiddenError`, `validate` boundaries, `secret` Close race.)

### Crypto + Security — 30 findings

| Sev | Module | Issue | Location |
|---|---|---|---|
| HIGH | `crypto/passhash` | No max password length → Argon2 memory DoS | `passhash.go:152-183, 203-230` |
| HIGH | `security/jwtutil` | JWKS URL accepts `http://` — silent integrity loss | `jwtutil.go:548-579` |
| HIGH | `security/jwtutil` | `sub` claim accepted in any shape; documented "UUID identity" not enforced | `jwtutil.go:36-38, 145-148` |
| HIGH | `security/jwtutil` | JWKS endpoint accepts non-JSON 200; no `Accept` header | `jwtutil.go:548-579` |
| HIGH | `crypto/envelope/kekstatic` | `Wrap` uses key slice without defensive copy — fragile | `kekstatic.go:119-142` |
| HIGH | `crypto/paseto` | Sequential key try in `V4Public.Verify` is a timing oracle for kid identification | `paseto.go:196-213` |
| HIGH | `security/csrf` | Issuer accepts a single secret; no rotation support → operators don't rotate | `csrf.go:59-103` |
| HIGH | `security/csrf` | Token format leaks 8-byte hashed sessionID prefix → cross-site linkability | `csrf.go:135-147, 209-212` |
| HIGH | `security/csrf` | `OriginAllowlist.matches` no normalization; `Origin: null` falls through to Referer | `csrf.go:235-266` |
| HIGH | `security/netutil` | `SSRFSafeTransport` returned bare → callers wrap into a redirect-following client | `ssrf.go:175-201` |
| HIGH | `security/netutil` | `SSRFSafeDynamicTransport` keeps connections (no `DisableKeepAlives`) | `ssrf.go:295-318` |
| HIGH | `security/netutil` | mTLS default `VerifyClientCertIfGiven` is unsafe-default | `tls.go:72-98` |
| MEDIUM | `security/jwtutil` | 1MiB JWKS body limit + no key-count cap → parse-time amplification | `jwtutil.go:564` |
| MEDIUM | `crypto/envelope/kekstatic` | `RemoveKey` panics on active-key removal; no error to caller | `kekstatic.go:89-96` |
| MEDIUM | `crypto/paseto` | Provider lacks `MaxStale` like jwtutil | `provider.go:119-152` |
| MEDIUM | `crypto/encrypt` | `EncryptOptional` returns plaintext for nil encryptor → hides config bugs | `encrypt.go:108-114` |
| MEDIUM | `crypto/paseto` | `Claims.Custom` unbounded copy → DoS amplification on key compromise | `paseto.go:340-398` |
| MEDIUM | `crypto/envelope` | `parseBlob` accepts `kL=0` | `envelope.go:275-302` |
| MEDIUM | `crypto/signing` | Early-return paths leak length oracle on `ErrEmptySecret` | `signing.go:124-130` |
| MEDIUM | `security/csrf` | `iat` int64 overflow on adversarial timestamps | `csrf.go:165-188` |
| MEDIUM | `crypto/passhash` | No dummy-hash helper to equalize unknown-user verify timing | `passhash.go:241-279` |

(Plus 9 LOW items — masking length leakage, doc gaps, `KeyUnsafe` being public, etc.)

### HTTPx — 45 findings (1 CRITICAL above)

| Sev | Module | Issue | Location |
|---|---|---|---|
| HIGH | `httpx/middleware/auth` | mTLS match doesn't validate cert ExtKeyUsage / IsCA | `auth.go:215-236` |
| HIGH | `httpx/middleware/idempotency` | `preserveHeaders` lookup key-canonicalisation inconsistency | `idempotency.go:432-440` |
| HIGH | `httpx/middleware/idempotency` | Replay loses outer-middleware headers (e.g. `X-Request-Id`) | `idempotency.go:540-549` |
| HIGH | `httpx/middleware/timeout` | Hard-mode handler may still write via `http.ResponseController.Unwrap()` | `timeout.go:99-133` |
| HIGH | `httpx/middleware/timeout` | Per-request 1MiB buffer × N concurrent → memory amplification | `timeout.go:99-104` |
| HIGH | `httpx/middleware/timeout` | `Header()` returns unprotected map → race-detector trip | `writer.go:55-57` |
| HIGH | `httpx/middleware/csrf` | `normaliseOrigin` drops port; default-port mismatch not handled | `csrf.go:444-447` |
| HIGH | `httpx/middleware/csrf` | Secret stored without defensive copy | `csrf.go:233` |
| HIGH | `httpx/middleware/signedrequest` | No host validation; proxy path-rewrites silently break sigs | `signedrequest.go:288-306` |
| HIGH | `httpx/middleware/signedrequest` | Nonce-store error returns 500 post-auth — confusing | `signedrequest.go:243-245` |
| MEDIUM | `httpx/middleware/signedrequest` | Skew check overflows with adversarial timestamp | `signedrequest.go:204-209` |
| MEDIUM | `httpx/middleware/cspnonce` | Nonce injection naive against `'unsafe-inline'` precedence | `cspnonce.go:142-172` |
| MEDIUM | `httpx/middleware/clientip` | Returns RemoteAddr literal as "client IP" for non-IP listeners | `clientip.go:55` |
| MEDIUM | `httpx/middleware/secheaders` | Trusted-proxy CIDR list not unified with clientip/ratelimit/logging | `secheaders.go:81-91` |
| MEDIUM | `httpx/middleware/maxbody` | 413 implicit not surfaced through wrapping writers | `maxbody.go:16` |
| MEDIUM | `httpx/typed` | `JSON`/`JSONNoBody`/`JSONStatus`/`JSONNoBodyStatus` API balloon | `typed.go:19-37` |
| MEDIUM | `httpx` | `NewHTTPClient` / `NewTracingHTTPClient` / `NewTracingHTTPClientWithOptions` triple-constructor | `httpx.go:92-137` |
| MEDIUM | `httpx` | `WriteJSON` vs `WriteJSONCtx` — collapse to one ctx-leading variant | `httpx.go:218,224` |
| MEDIUM | `httpx/middleware/idempotency` | `WithAllowSharedKeys` + multi-tenant route → cross-user cache | `idempotency.go:286-292` |
| MEDIUM | `httpx/middleware/idempotency` | Hijack-mid-write produces non-faithful replay | `idempotency.go:419-430` |
| MEDIUM | `httpx/middleware/csrf` | `HasBearerToken` skip + cookie auth fallback → CSRF bypass | `csrf.go:483-486` |
| MEDIUM | `httpx/middleware/auditlog` | Hardcoded 5s timeout for store writes; no `WithTimeout` | `auditlog.go:151-177` |
| MEDIUM | `httpx/middleware/auditlog` | `statusRecorder` doesn't implement Hijack/Flush — breaks WebSocket | `auditlog.go:142-144` |
| MEDIUM | `httpx/error_handler` | `OperationFailedError.Error()` may leak wrapped DB driver text | `error_handler.go:96-104` |
| MEDIUM | `httpx/error_handler` | `WriteValidationError` may leak per-field constraint hints | `error_handler.go:140` |
| MEDIUM | `httpx/middleware/stack` | `Outer` middleware sits OUTSIDE recover — panic uncaught | `stack.go:44` |
| MEDIUM | `httpx/middleware/idempotency` | `ReadFrom` TeeReader defeats sendfile optimization (doc misleading) | `idempotency.go:611-623` |
| MEDIUM | `httpx/reqsign` | `RoundTrip` clone exhausts caller's body | `reqsign/transport.go:50-75` |
| MEDIUM | `httpx/sign` | `readBody` mutates input request — RoundTripper contract violation | `sign/sign.go:142-157` |

(Plus ~13 LOW items — `extractBearerToken` `>=7` boundary; dead code; missing SSRF guard in HTTPCheck; pagination cursor length unbounded; etc.)

### Data — 40 findings (4 CRITICAL above)

| Sev | Module | Issue | Location |
|---|---|---|---|
| HIGH | `data/queue/redisqueue` | Reaper SCAN not cluster-safe; no hash tags → CROSSSLOT | `helpers.go:356-407` |
| HIGH | `data/lock/redislock` | `Locker.Acquire` has no max-elapsed bound | `lock.go:148-164` |
| HIGH | `data/cache` | `nxClaims` map grows unbounded — no sweeper | `memory_cache.go:40,296-328` |
| HIGH | `data/actionlog/memory` | `tenantMu` map grows unbounded | `memory.go:30-51` |
| HIGH | `data/queue/redisqueue` | Retry RPUSH + removeByID not atomic → duplicate enqueue on cancel | `helpers.go:198-228` |
| HIGH | `data/stream/redisstream` | `removeConsumer` PEL-empty check + DELCONSUMER not atomic | `consumer.go:410-449` |
| HIGH | `data/idempotency/pgstore` | TOCTOU between failed UPDATE and SELECT | `store.go:182-228` |
| HIGH | `data/queue/redisqueue` | Heartbeat-loop transient errors → silent TTL lapse | `queue.go:602-613` |
| HIGH | `data/queue/redisqueue` | `recoverProcessing` O(N²) per-item LRANGE | `helpers.go:496-521` |
| HIGH | `data/cache` | Foreground `ComputeCache` has no compute timeout | `compute.go:267-301` |
| HIGH | `data/stream/redisstream` | `getDeliveryCount` returns 1 on error → masks max-delivery | `helpers.go:207-230` |
| MEDIUM | `data/ratelimit/gcra` | retryAfter `+ 1ns` skews precise sleeps | `gcra.go:163-164` |
| MEDIUM | `data/ratelimit/tokenbucket` | Float64 drift over millions of Allow calls | `tokenbucket.go:146-175` |
| MEDIUM | `data/budget/redis` | `peekScript` doesn't refresh TTL → window can lapse mid-flight | `redis.go:58-91` |
| MEDIUM | `data/lock/redislock` | `WithLock` swallows non-ErrLockLost release errors | `lock.go:192-210` |
| MEDIUM | `data/stream/redisstream` | `parseMessage` silently drops headers on JSON error | `helpers.go:232-260` |
| MEDIUM | `data/cache` | `detachCancelKeepDeadline` semantics ambiguous on no-deadline parent | `compute.go:446-452` |
| MEDIUM | `data/queue`, `data/stream` | Abstract interfaces don't match concrete shapes | `queue.go`, `stream.go` |
| MEDIUM | `data/queue/redisqueue` | `EnqueueBatch` partial-success not surfaced to caller | `queue.go:529-544` |
| MEDIUM | `data/queue/redisqueue` | Tombstone sentinel can collide cross-invocation | `helpers.go:35-47` |
| MEDIUM | `data/cache/tenant`, `data/idempotency/tenant` | Panics on missing tenant ID — should be error | `tenant.go:87-94, 76-83` |
| MEDIUM | `data/idempotency/pgstore` | Caller ctx for `Set` UPDATE → cache write skipped on tight deadline | `store.go:155-168` |
| MEDIUM | `data/stream/redisstream` | DLQ XACK-failure increments DLQ counter (double-count next time) | `helpers.go:17-72` |
| MEDIUM | `data/queue/redisqueue` | `LRem 1` for malformed message removal — duplicate-payload race | `helpers.go:107-114` |

(Plus 12 LOW items including missing tests, abstract interface mismatch, `recordError` double-count.)

### Infra — 48 findings

| Sev | Module | Issue | Location |
|---|---|---|---|
| HIGH | `infra/messaging/amqpbackend` | Per-publish channel open/close in confirm mode → high overhead | `publisher.go:124-169` |
| HIGH | `infra/messaging/amqpbackend` | Non-blocking returnCh select can miss basic.return | `publisher.go:144-167` |
| HIGH | `infra/messaging/amqpbackend` | Topology has no `Args`/`Internal`/`AutoDelete` knobs | `topology.go:14-41,61-137` |
| HIGH | `infra/messaging/amqpbackend` | Single-threaded `handleDelivery` per consumer; no `WithConcurrency` | `consumer.go:259-262` |
| HIGH | `infra/messaging/natsbackend` | In-flight handlers run with cancelled ctx, no WaitGroup on Stop | `natsbackend.go:376-389` |
| HIGH | `infra/messaging/redisbackend` | Ignores `Binding.Retry` — silent semantic divergence vs AMQP | `consumer.go:36-43` |
| HIGH | `infra/leaderelection/redislock` | Release uses `context.Background()` unbounded | `redislock.go:159` |
| HIGH | `infra/outbox` | `MarkPublished` after Publish creates duplicate-publish window | `relay.go:341-388` |
| HIGH | `infra/outbox/gormstore` | Tx-commit failure on FetchPending → rows re-claimed → double-publish | `gormstore.go:152-202` |
| HIGH | `infra/redis` | Three-goroutine reconnect callback fan-out is fragile | `connection.go:323-383` |
| HIGH | `infra/sqldb/pgx` | `Listen` returns connection to pool with subscriptions intact | `pgx.go:232-264` |
| HIGH | `infra/sqldb/gormdb/gormpostgres` | Deprecated `New` skips strict-TLS rejection — bypasses hardening | `postgres.go:43-147` |
| HIGH | `infra/storage/sftpbackend` | Caller-Close required; no finalizer-based misuse detection | `sftp.go:392-425` |
| HIGH | `infra/storage/s3backend` | `Put` trusts caller's `Content-Type` — no sniff | `s3.go:200-221` |
| HIGH | `infra/storage/s3backend` | Multipart-upload interface gap — `MultipartUploader` not implementable | `s3.go:33-47` |
| HIGH | `infra/storage/gcsbackend` | Aborted writer Close() leaks resumable session — should `Cancel()` | `gcs.go:127-141` |
| HIGH | `infra/storage/storagehttp` | `storePart` trusts caller's `Content-Type` — XSS via image+html polyglot | `upload.go:166-213` |
| HIGH | `infra/storage/storagehttp` | `MaxFileSize` validator may not enforce hard byte cap pre-multipart | `upload.go:185-212` |
| HIGH | `infra/storage/encryption` | 256MiB plaintext × NumCPU concurrent → easy OOM | `encryption.go:226-249` |
| MEDIUM | `infra/messaging/amqpbackend` | Stale watchConnection generations not explicitly cleaned | `connection.go:230-253` |
| MEDIUM | `infra/messaging/amqpbackend` | Reconnect logs partial-topology failures opaquely | `connection.go:333-348` |
| MEDIUM | `infra/messaging/amqpbackend` | `ReplySender` single-mutex serializes RPC replies | `rpc_reply.go:73-85` |
| MEDIUM | `infra/messaging/natsbackend` | All handler errors → Nak unconditional; ignores `apperror.IsPermanent` | `natsbackend.go:445-454` |
| MEDIUM | `infra/messaging/natsbackend` | Panic recovery → Nak burns redelivery budget | `natsbackend.go:399-408` |
| MEDIUM | `infra/leaderelection/redislock` | `renewInterval >= TTL/2` not validated at construction | `redislock.go:62-68,183-202` |
| MEDIUM | `infra/leaderelection/pgadvisory` | Session-scoped advisory lock breaks under PgBouncer txn pool | `pgadvisory.go:177-181` |
| MEDIUM | `infra/outbox` | Stacked heartbeats if previous heartbeat lags | `relay.go:398-427` |
| MEDIUM | `infra/outbox/gormstore` | SQLite multi-process limitation only documented, not enforced | `gormstore.go:113-128` |
| MEDIUM | `infra/redis` | `pingTimeout` hardcoded 5s, no `WithConnectTimeout` | `connection.go:138-189` |
| MEDIUM | `infra/sqldb/gormdb/gormmysql` | TLS fingerprint omits MinVersion/Ciphers/ClientAuth | `mysql.go:31-78` |
| MEDIUM | `infra/sqldb/gormdb` | `WithTx` commit-failure semantics not documented | `tx.go:18-41` |
| MEDIUM | `infra/storage/sftpbackend` | Unbounded cleanup-goroutine queue under reconnect storm | `sftp.go:213-244` |
| MEDIUM | `infra/storage/s3backend` | Presigned PUT requires header echo; not surfaced | `presign.go:50-78` |
| MEDIUM | `infra/storage/s3backend` | `New` uses `context.Background()` for SDK init | `s3.go:107-110` |
| MEDIUM | `infra/storage/azurebackend` | No ctx for `New`; no `WithHealthTimeout` | `azure.go:69-99` |
| MEDIUM | `infra/storage/gcsbackend` | No `PresignedStore` impl despite GCS supporting v4 SignedURL | `gcs.go:53-81` |
| MEDIUM | `infra/storage/storagehttp` | `ServeFile` doesn't always set `X-Content-Type-Options: nosniff` | `serve.go:142-145` |
| MEDIUM | `infra/storage/storagehttp/uploadsec` | Two validator systems (storage.Validator + uploadsec.Validator) | `uploadsec.go:78-94` |
| MEDIUM | `infra/messaging` | `BufferedPublisher.drain` ignores save errors | `buffered_publisher.go:431-448` |
| MEDIUM | `infra/messaging/amqpbackend/debughttp` | Default-allow when allowlist nil → dangerous | `debug.go:117-170` |
| MEDIUM | `infra/storage` | `Manager.Default()/Disk()` panic — no `MaybeDisk` accessor | `manager.go:91-115` |
| MEDIUM | `infra/storage/encryption` | `Copy` = Get + Put (no fast path; doubles memory + RTT) | `encryption.go:371-394` |

(Plus 7 LOW items including missing integration tests for OOM/streaming-reject paths, dead-stop cycle in Redis health loop, etc.)

### Observability — 33 findings (2 CRITICAL above)

| Sev | Module | Issue | Location |
|---|---|---|---|
| HIGH | `observability/redmetrics` | `requests_total{status}` vs `errors_total{status_class}` inconsistent | `redmetrics.go:177-181` |
| HIGH | `observability/auditlog` | `Logger.Log` swallows store errors → silent compliance event drop | `auditlog.go:85-103` |
| HIGH | `observability/auditlog/gormstore` | No length validation on `event.ID`; PK conflict opaque | `store.go:115` |
| HIGH | `observability/health` | Mutable `Version`/`CacheTTL`/`Checks` fields read across goroutines | `health.go:107-238` |
| HIGH | `observability/runtimemetrics` | `runtime.ThreadCreateProfile(nil)` per scrape locks runtime | `runtimemetrics.go:81` |
| HIGH | `observability/logging` | Uses `service`/`version`/`environment` keys; OTel uses `service.name` etc. | `logging.go:51-59` |
| HIGH | `observability/logattr` | `StatusCode` key `status` collides with audit string `Status` | `logattr.go:55-58` |
| HIGH | `observability/promutil` | `RegisterCollector` returns existing collector; new one held by caller still appears | `register.go:23-27` |
| HIGH | `observability/promutil/labelguard` | Per-vec goroutine + unbounded cache map | `labelguard.go:212-229` |
| HIGH | `observability/health` | Hard-coded 3s per-check timeout; not configurable per dependency | `health.go:268-290` |
| HIGH | `observability/auditlog/gormstore` | Lazy cursor MAC secret has no Prom signal — silent multi-replica break | `store.go:96-111` |
| MEDIUM | `observability/redmetrics` | No labelguard wired for `route` label by default | `redmetrics.go:170-177` |
| MEDIUM | `observability/tracing` | OTLP exporter no Headers/TLSConfig/Compression options | `tracing.go:114-119` |
| MEDIUM | `observability/tracing` | `ParentBased` without remote-parent ratio → upstream forces 100% | `tracing.go:151` |
| MEDIUM | `observability/health` | Handlers `WriteHeader` before encode — partial body on error | `handlers.go:51-64` |
| MEDIUM | `observability/auditlog/memory` | O(N) per page; cursor missing → silent restart | `memory.go:42-67` |
| MEDIUM | `observability/slo` | `histogramPercentile` sums cumulative counts — wrong if buckets diverge | `slo.go:336-391` |
| MEDIUM | `observability/slo` | +Inf bucket returns last finite bound — silently underreports | `slo.go:386-390` |
| MEDIUM | `observability/slo` | Error rate is lifetime, not Window-bounded — fundamental SLO error | `slo.go:307-314` |
| MEDIUM | `observability/pprof` | `Mount` accepts raw mux; no auth/ratelimit/loopback option | `pprof.go:51-57` |
| MEDIUM | `observability/health` | `RunHealthCheck` hardcodes URL/path | `healthcheck_cli.go:16-41` |
| MEDIUM | `observability/auditlog/gormstore` | No mandatory-filter hook → easy authz bypass via `Filter` | `store.go:194-259` |
| MEDIUM | `observability/logging` | `traceHandler.Handle` mutates record without Clone | `logging.go:108-117` |
| MEDIUM | `observability/redmetrics` | Per-request alloc + 2-3 WithLabelValues lookups in hot path | `redmetrics.go:165-181` |
| MEDIUM | `observability/auditlog/gormstore` | Tests run only against memdb — not real Postgres | `store_test.go` |
| MEDIUM | `observability/auditlog` | `Event.Metadata` and `IPAddress` not validated at boundary | `auditlog.go:14-24` |

(Plus 8 LOW items including counter naming, key conventions in health logs, missing batch tuning options.)

### Runtime + Resilience + IO — 38 findings

| Sev | Module | Issue | Location |
|---|---|---|---|
| HIGH | `runtime/eventbus` | recover-on-closed-send + sync.Pool double-release race window | `pool.go:79-106` |
| HIGH | `runtime/eventbus` | `OnFullDrop` indistinguishable from "drop after Stop"; same metric | `pool.go:128-146` |
| HIGH | `runtime/eventbus` | `Bus.Stop` doesn't await `Bus.Start` return | `pool.go:163-170` |
| HIGH | `runtime/eventbus` | `dispatchAsync` may double-Put on closed-channel send | `eventbus.go:425-431` |
| HIGH | `runtime/eventbus` | Drain handlers run on cancelled-publisher ctx; Stop deadline can't propagate | `pool.go:185-209` |
| HIGH | `runtime/cron` | No `started` guard → double Start corrupts state | `cron.go:135-145` |
| HIGH | `runtime/cron` | No DST/missed-fire handling; not documented | `cron.go:82-94` |
| HIGH | `resilience/retry` | No ctx check before first attempt; cancelled ctx still invokes fn | `retry.go:249-292` |
| HIGH | `resilience/retry` | `Policy.Delay(N)` non-deterministic with jitter; doc claims otherwise | `retry.go:230-247` |
| HIGH | `resilience/circuitbreaker` | `ExecuteCtx` counts `context.Canceled` as failure → trip on caller cancel | `circuitbreaker.go:134-148` |
| HIGH | `runtime/lifecycle` | Stop runs before Start goroutines complete; race on per-component | `runner.go:188-198` |
| HIGH | `io/atomicfile` | No `O_NOFOLLOW`/symlink check on dir + path | `atomicfile.go:46-49` |
| HIGH | `io/atomicfile` | Directory fsync on EXDEV path syncs wrong directory | `atomicfile.go:108-116` |
| HIGH | `runtime/concurrency` | `FanOutSettled` doesn't interrupt in-flight tasks on parent cancel | `fanout.go:174-216` |
| MEDIUM | `runtime/cron` | `SetJobTimeout` post-Add race; doc says `WithJobTimeout` | `cron.go:104-111` |
| MEDIUM | `runtime/cron` | Leader-flap can allow overlapping invocations | `cron.go:172-226` |
| MEDIUM | `resilience/retry` | No Retry-After header support | `retry.go:73-91` |
| MEDIUM | `resilience/retry` | No `MaxElapsedTime` option | `retry.go:96-121` |
| MEDIUM | `resilience/retry` | Jitter validation missing for >1.0 / negative | `retry.go:38-40` |
| MEDIUM | `resilience/circuitbreaker` | No error-rate window; only consecutive-failures | `circuitbreaker.go:78-98` |
| MEDIUM | `resilience/circuitbreaker` | Hardcoded `MaxRequests:1` in half-open | `circuitbreaker.go:84-90` |
| MEDIUM | `runtime/lifecycle` | Shared stopTimeout lets last component starve earlier ones | `runner.go:208-235` |
| MEDIUM | `runtime/lifecycle` | `FuncComponent` no double-Start guard | `component.go:62-73` |
| MEDIUM | `runtime/lifecycle` | `httpServerComponent.Start` ignores ctx | `component.go:33-38` |
| MEDIUM | `io/atomicfile` | Permission read via Stat (follows symlinks); ownership not preserved | `atomicfile.go:50-56` |
| MEDIUM | `io/atomicfile` | `Load[T]` returns zero on missing — ambiguous with zero-value JSON | `atomicfile.go:14-30` |
| MEDIUM | `runtime/concurrency` | `FanOut` discards successes on first error | `fanout.go:99-100` |
| MEDIUM | `runtime/batchworker` | `started`/`cancel` not jointly observed — Start/Stop race | `batchworker.go:175-192` |
| MEDIUM | `runtime/batchworker` | Jitter is one-sided (interval + [0, max]); not symmetric ±X% | `batchworker.go:229-242` |
| MEDIUM | `io/progress` | `throttledReader.Read` truncates without informing caller | `throttle.go:74-131` |
| MEDIUM | `io/progress` | Non-ctx constructor uses uninterruptable Sleep | `throttle.go:111-128` |
| MEDIUM | `runtime/eventbus` | `WithRegisterer` vs `cron.WithRegistry`/`batchworker.WithRegistry` | `eventbus.go:99-106` |

(Plus ~6 LOW items: flaky tests, double-Ctrl-C race, asymmetric Subscribe/Unsubscribe, etc.)

### App + cmd + grpcx — 36 findings (1 CRITICAL above)

| Sev | Module | Issue | Location |
|---|---|---|---|
| HIGH | `app` | Builder does NOT auto-apply `stack.Default` — services silently ship without recover/secheaders/metrics/log/timeout | `builder.go:961-982` |
| HIGH | `app` | `WithIPRateLimit` builds limiter but never applies middleware to public mux | `builder.go:836-853` |
| HIGH | `app` | Module Close runs in `defer` not lifecycle; workers race with DB shutdown | `builder.go:889-893,1118-1119` |
| HIGH | `app` | Shutdown hooks run *concurrent* with component Stop — infra may already be closed | `builder.go:1067-1096` |
| HIGH | `app` | Module Init resource leak on panic mid-Init (before module-level Close) | `builder.go:74-79` |
| HIGH | `app` / `grpcx` | gRPC server gets no TLS from Builder despite `serverTLS` configured | `grpc_module.go:62,124` |
| HIGH | `grpcx` | `NewServer` doesn't auto-apply logging/metrics/correlation; no MaxConcurrentStreams | `server.go:157-216` |
| HIGH | `cmd/kit-new` | Generated `make doctor` runs `kit-doctor ./...` (kit-doctor expects a path) | `templates/Makefile.tmpl:17` |
| HIGH | `cmd/kit-new` | Generated `Run()` doesn't use `app.Builder`, no TLS, no `stack.Default` | `templates/wire.go.tmpl:62-85` |
| HIGH | `examples/agentic-service` | Hard-coded HMAC secret in committed file | `internal/app/app.go:71-73` |
| MEDIUM | `grpcx` | `WithDefaultDeadline` opt-in; httpx default is 30s | `server.go:139-144` |
| MEDIUM | `grpcx/interceptor` | `extractBearerToken` parser permissive | `interceptor/auth.go:168-171` |
| MEDIUM | `app` | No `app.LoadDatabaseConfig` etc. helpers — services hand-roll envvars | `config.go:55-79` |
| MEDIUM | `app` | `SERVER_HOST` defaults to `0.0.0.0` w/o validator opt-out | `config.go:67` |
| MEDIUM | `cmd/kit-migrate` | Tool *publishes* migrations; name implies running them; no checksum drift check | `cmd/kit-migrate/main.go:66-134` |
| MEDIUM | `cmd/kit-new` | Generated `main.go` doesn't use `app.Main` (no version, no health) | `templates/main.go.tmpl:18-29` |
| MEDIUM | `cmd/kit-new` | Hardcoded `go 1.26.2` in template | `templates/go.mod.tmpl:3` |
| MEDIUM | `app` | `WithJWTAudience("")` accepts empty silently; `WithJWTIssuer` panics | `builder.go:397-400` |
| MEDIUM | `app` | `WithJWTIssuer`/`WithoutJWTIssuer` "last call wins" footgun | `builder.go:386-393` |
| MEDIUM | `grpcx` | `Health.Check` returns SERVING for `StatusConnecting` | `health.go:40-54` |
| MEDIUM | `examples/agentic-service` | Doesn't demonstrate `app.Builder` path | `internal/app/app.go:79-104` |
| MEDIUM | `app` | Lifecycle test relies on `syscall.Kill` + 200ms sleeps — flaky | `builder_test.go:226-369` |

(Plus ~14 LOW items including registry sort, no `--seed` integration test, panic-vs-overwrite asymmetry across `With*` methods.)

### Cross-cutting — 26 findings (1 CRITICAL above)

| Sev | Issue | Location |
|---|---|---|
| HIGH | F-8: 13+ go.mod files have `v0.0.0` placeholder requires — blocks any release pipeline | repo-wide |
| HIGH | F-9: Empty root CHANGELOG, per-module CHANGES last updated 2026-04-06 (296 commits since) | `CHANGELOG.md` etc. |
| HIGH | F-10: 4 breaking commits not yet released; verify NX bumps to v2 | git log |
| HIGH | F-11: dev-mode removal undocumented for service authors | `app/`, `docs/ai/bootstrap.md` |
| HIGH | F-12: `infra/sqldb/deprecated.go` (167-line wall) — delete in v2 | `infra/sqldb/deprecated.go` |
| HIGH | F-14: AGENTS.md golden path uses deprecated `csrf.RequireCSRF` | `AGENTS.md` |
| HIGH | F-16: `docs/ai/bootstrap.md` references non-existent `envutil.Get`/`GetSecret` | `docs/ai/bootstrap.md:67-68` |
| HIGH | F-17: `docs/ai/utilities.md` references `core/cache` (actual: `data/cache`) | `docs/ai/utilities.md:3` |
| HIGH | F-18: AGENTS.md decision tree omits ~24 modules introduced post-v1 | `AGENTS.md:54-105` |
| HIGH | F-19: `examples/app/main.go` doesn't compile (deprecated APIs, build:ignore tag hides drift) | `examples/app/main.go` |
| MEDIUM | F-1: `Option` vs `<Type>Option` naming inconsistent | repo-wide |
| MEDIUM | F-4: `now func() time.Time` duplicated 13+ times — no shared `core/clock` | repo-wide |
| MEDIUM | F-6: `redislock` exposes both stateful and stateless APIs | `data/lock/redislock/lock.go` |
| MEDIUM | F-13: `infra/messaging/buffered_publisher_compat.go` — drop in v2 | file |
| MEDIUM | F-23: `crypto/rand` usage clean, but SFTP `InsecureIgnoreHostKey` opt-in path needs verification + WARN | `infra/storage/sftpbackend/sftp.go` |
| MEDIUM | F-24: TLS MinVersion floor not enforced in entrypoints accepting `*tls.Config` | repo-wide |
| LOW | F-2: `Logger` clash across 3 modules (struct vs interface) | various |
| LOW | F-3: `Store` interface name overloaded across 5 domains | various |
| LOW | F-5: `MemBackend` vs `MemoryBackend` naming inconsistency | `infra/storage/membackend` |
| LOW | F-7: `infra/messaging/redisbackend` requires go-redis/v9 as indirect | `go.mod` |
| LOW | F-20: 235 same-package vs 45 `_test` package — undocumented rule | repo-wide |
| LOW | F-21: Stale TODOs in package docs — some now implemented | various |
| LOW | F-25: `core/randstr` lacks bias-freedom doc/test | `core/randstr` |
| LOW | F-26: `examples/agentic-service` not in NX affected build target (verify) | `nx.json` |

---

## Recommended fix order

**Wave 1 (this PR — fast wins, no API breaks):**
1. C12 — `kit-migrate` empty-arg crash (1-line fix)
2. C9 — pgadvisory FNV → SHA-256 (small, isolated)
3. C1 — `slo` default error filter `code=5..` → `status=5..`
4. C2 — `redmetrics` panic recovery
5. C11 — fix AGENTS.md / docs golden path
6. F-16, F-17 — docs path corrections (`envutil.Get`, `core/cache`)
7. C10 — `httpx/deadline_transport` body cancellation
8. F-19 — fix or delete `examples/app/main.go`

**Wave 2 (this PR — security hardening, no public API breaks):**
9. C3 — JWKS reject `oct`/HMAC keys
10. C4 — `FieldEncryptor.Decrypt` strict by default (with opt-in passthrough)
11. C7 — redislock token regenerated on every retry
12. F-24 — enforce TLS 1.2 floor on `*tls.Config` entrypoints
13. SFTP InsecureIgnoreHostKey explicit-WARN

**Wave 3 (separate PR or future — breaking API changes for v2):**
- C5 — HMAC signing canonical context (new API)
- C6, C8 — Lua atomicity in queue/idempotency (Lua rewrites)
- F-12 — delete `infra/sqldb/deprecated.go`
- F-13 — delete `buffered_publisher_compat.go`
- F-14 — delete deprecated `csrf.RequireCSRF`/`authz.SubjectFromHeader`/`httpx.RequestID`
- F-1, F-4, F-6 — naming and clock unification
- core/contextutil `Key[T]` collision-proof construction
- All "auto-apply stack.Default" / "auto-TLS gRPC" Builder fixes
- ~all observability log-key alignment to OTel semantic conventions

**Wave 4 (release process):**
- F-8 — resolve `v0.0.0` placeholder requires
- F-9, F-10, F-11 — release notes for breaking changes
- F-18 — fill in decision tree

---

## What this review did not cover

- **External integration tests** — only unit tests verified green; agents didn't exercise testcontainers.
- **Performance benchmarks** — no profiling done.
- **Public-API churn analysis** — `apidiff` not run vs last tagged version.
- **Threat model review** — no STRIDE/LINDDUN modeling.
- **Documentation rendering** — no godoc.org / pkg.go.dev preview check.
