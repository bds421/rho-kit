# rho-kit v2 API Freeze Hostile Review - 2026-05-13

Review source: `task.md`.

Review state:

- Workspace: `/Users/markusnissl/Developer/private/rho-kit`
- Current release-plan workspace modules: 73
- Current Go source files tracked by git: 873
- Current testable Go examples found by `rg '^func Example'`: 17
- Worktree note: this is a dirty tree. The report treats current working-tree source as evidence and does not revert or edit source code.

Commands run:

- `git status --short`
- `git diff --stat`
- `make check-dependency-allowlist` (failed)
- `make check-dependency-boundaries` (passed: 393 direct module edges)
- `make check-operational-readiness` (passed: 73 modules covered)
- `RELEASE_MODE=all make release-plan` (73 modules, 6 dependency levels)
- targeted `rg`, `nl`, `sed`, and `git diff` inspections across constructors, interfaces, stores, backends, middleware, metrics, rotation paths, docs, and release artifacts

## Prompt Coverage Checklist

| Task lens | Evidence in this report |
|---|---|
| Public API freeze | H-010, M-001, M-002, M-003, M-004, M-007 |
| API misuse resistance | M-002, M-003, M-006 |
| Secure defaults and fail-closed behavior | H-004, H-006, H-007, H-009 |
| Cross-implementation invariants | H-002, H-005, H-011, M-005 |
| Lifecycle, shutdown, and goroutines | H-006, H-008, M-001, L-001 |
| Atomicity and concurrency boundaries | H-008, H-010, Audited Clean |
| Silent data loss and pagination | H-005, M-006, M-007 |
| Resource amplification and pre-auth cost | H-002, H-003, H-005 |
| Context, cancellation, and timeouts | H-011, M-005 |
| Error contracts and retryability | H-001, H-003, Audited Clean |
| Credential and secret rotation | H-009, Audited Clean |
| Observability and metric contracts | M-004, M-008, Release Tag Blockers |
| Release, docs, legal, and supply chain | H-001, M-009, M-010, L-003 |

## CRITICAL

No code-evidenced CRITICAL issue was found in this pass. The HIGH items below are still tag blockers or API-freeze blockers unless explicitly accepted.

## HIGH

### H-001 - Dependency allowlist gate is currently red

- Severity: HIGH
- Package/file/line: `docs/audit/dependency-allowlist.txt:24`
- Affected public behavior: v2 release supply-chain evidence
- Evidence: `make check-dependency-allowlist` failed with `Stale allowlist entries no longer used as direct dependencies: github.com/lib/pq`.
- Failure scenario: the allowlist is supposed to be an exact direct-dependency review ledger. Keeping `github.com/lib/pq` approved after the repo moved away from it makes the ledger non-exact and weakens dependency review.
- Why tests/gates might miss it: normal `go test`, `lint`, and release-plan checks do not validate stale approvals.
- Minimal fix: remove `github.com/lib/pq` from `docs/audit/dependency-allowlist.txt`, or restore it as a direct dependency if still required.
- Tests to add/run: `make check-dependency-allowlist`.

### H-002 - AMQP/NATS inbound consumers bypass message and header validation

- Severity: HIGH
- Package/file/line: `infra/messaging/amqpbackend/consumer.go:297`, `infra/messaging/amqpbackend/delivery.go:65`, `infra/messaging/natsbackend/natsbackend.go:849`, `infra/messaging/natsbackend/natsbackend.go:930`, `infra/messaging/message.go:140`
- Affected public API or behavior: `messaging.Message`, AMQP/NATS `Consumer`, inbound transport contracts
- Evidence: publishers call `messaging.ValidateMessage` before sending, but AMQP `unmarshal` only JSON-decodes and then `fromAMQPDelivery` injects transport headers without calling `ValidateMessage` or `ValidateMessageHeaders`. NATS similarly unmarshals, copies headers in `deliveryHeaderMaps`, and dispatches to the handler without validating the final message. The new `MaxMessageHeaders` cap only protects validation call sites, not these inbound paths.
- Failure scenario: a foreign AMQP/NATS writer bypasses kit publishers and sends oversized or invalid message IDs/types/headers. Handlers receive metadata the shared `messaging.Message` contract says should not exist. Large header maps also defeat the just-added header-count cap because AMQP `extractStringHeaders` and NATS `deliveryHeaderMaps` allocate from the raw transport map directly.
- Why tests/gates might miss it: tests mostly round-trip messages produced by the kit publisher path, so the input already satisfies validation.
- Minimal fix: after decoding and merging transport headers, call `messaging.ValidateMessage` before invoking handlers. Apply `ValidateMessageHeaders` or equivalent while copying AMQP/NATS headers, enforcing `MaxMessageHeaders`, name/value caps, and invalid-character rejection.
- Tests to add/run: AMQP/NATS consumer tests for oversized header count, invalid header name/value, oversized message ID/type, invalid payload, and foreign writer inputs that never pass through kit publisher validation.

### H-003 - Redis read-side size caps run after the unbounded bulk allocation

- Severity: HIGH
- Package/file/line: `data/cache/rediscache/cache.go:131`, `data/cache/rediscache/cache.go:213`, `data/idempotency/redisstore/store.go:208`, `data/idempotency/redisstore/store.go:283`
- Affected behavior: Redis cache/idempotency read amplification protection
- Evidence: the recent guards check `len(val)` or `len(data)` after `GET`, `MGET`, or `Bytes()` has already materialized the full Redis bulk string in process memory.
- Failure scenario: a foreign Redis writer stores a 500 MB value under a cache/idempotency key. The cache now returns an error after allocation, but the process already paid the allocation cost and can OOM before reaching the guard. `MGet` also treats oversized values as misses, which hides data corruption differently from `Get`.
- Why tests/gates might miss it: unit tests can inject oversized but still safe values and observe the post-read error; they do not prove the guard runs before allocation.
- Minimal fix: check size before reading the value body where Redis supports it, for example `STRLEN` before `GET`, a Lua script that checks `strlen` and only returns bounded values, or bounded `GETRANGE` plus explicit oversize detection. Make `Get` and `MGet` error semantics consistent for oversized values.
- Tests to add/run: Redis integration tests using a foreign writer to store values above the configured limit; assert the method returns a typed oversize error and does not classify oversize as a cache miss. Add tests for `MGet` consistency with per-key `Get`.

### H-004 - `app/amqp.WithoutTLS` is not threaded into the backend connection

- Severity: HIGH
- Package/file/line: `app/amqp/amqp.go:67`, `app/amqp/amqp.go:106`, `app/amqp/amqp.go:147`, `infra/messaging/amqpbackend/connection.go:277`
- Affected public API: `app/amqp.Module`, `app/amqp.WithoutTLS`, `amqpbackend.Connect`
- Evidence: `WithoutTLS` sets `moduleConfig.allowPlaintext` and only disables the app-level construction check. `messagingModule` does not store that flag, and `Init` never appends `amqpbackend.WithAllowPlaintext()`. The backend still rejects `amqp://` without TLS or explicit backend opt-out.
- Failure scenario: a service intentionally configures a loopback/dev AMQP broker with `app/amqp.WithoutTLS()`. App module construction succeeds, but `Init` or the lazy reconnect path fails because the backend refuses the same plaintext URL.
- Why tests/gates might miss it: tests assert the app module can be constructed with `WithoutTLS`; they do not run `Init` through the backend URL normalization path for this opt-out.
- Minimal fix: store `allowPlaintext` on `messagingModule` and append `amqpbackend.WithAllowPlaintext()` when it is set. Add a provider-path test too, because `WithURLProvider` bypasses the construction-time URL string check.
- Tests to add/run: app/amqp `Init` tests for `amqp://localhost` with `WithoutTLS`, non-loopback plaintext rejection, and URL-provider plaintext behavior.

### H-005 - Public list/query limits are unbounded and can amplify memory/SQL work

- Severity: HIGH
- Package/file/line: `data/actionlog/actionlog.go:216`, `data/actionlog/memory/memory.go:173`, `data/actionlog/postgres/store.go:142`, `data/approval/approval.go:159`, `data/approval/memory/memory.go:128`, `data/approval/postgres/store.go:127`, `observability/auditlog/auditlog.go:512`, `observability/auditlog/memory.go:82`
- Affected public API: `actionlog.Query.Limit`, `approval.Query.Limit`, `auditlog.Logger.List(limit int)`
- Evidence: `Limit <= 0` defaults to 100, but large positive limits are accepted. Postgres stores use caller limit in SQL `LIMIT` and `make(..., 0, limit)`. Memory stores materialize matches before trimming, and auditlog exposes a raw `limit int` to stores.
- Failure scenario: an admin or API handler maps `?limit=1000000000` directly into these APIs. Postgres paths can allocate huge slices and send huge LIMITs. Memory paths scan and materialize broad result sets before returning.
- Why tests/gates might miss it: tests cover default pagination and normal page sizes, not adversarial large positive limits.
- Minimal fix: add package-level `MaxPageLimit` constants and reject or clamp `Limit > MaxPageLimit` in `Query.Validate` / `Logger.List` before store calls. Avoid preallocating cap equal to untrusted limit.
- Tests to add/run: limit-above-max tests for actionlog memory/postgres, approval memory/postgres, and auditlog `Logger.List`.

### H-006 - SFTP backend `Close` is not terminal and operations can reconnect after shutdown

- Severity: HIGH
- Package/file/line: `infra/storage/sftpbackend/sftp.go:60`, `infra/storage/sftpbackend/sftp.go:344`, `infra/storage/sftpbackend/sftp.go:772`
- Affected public API: `sftpbackend.Backend.Close`, all SFTP storage operations
- Evidence: `Backend` tracks `connected` but no `closed` flag. `Close` closes handles and sets `connected=false`; later `getClient` sees disconnected and calls `connect(ctx)` again.
- Failure scenario: `storage.Manager.Close` or app shutdown closes SFTP, but a retained backend handle performs `Get`/`Put` after shutdown and opens a new SSH/SFTP connection using rotated or stale credentials. Stop returning no longer means the component owns no active network resources.
- Why tests/gates might miss it: close tests typically assert idempotent `Close`, not post-close operation behavior.
- Minimal fix: add a `closed` flag under the backend mutex. `Close` sets it; `getClient` and public operations return a stable closed error after shutdown.
- Tests to add/run: `ClosePreventsReconnect`, lazy backend close-before-first-use, and manager-close retained-handle tests.

### H-007 - `kekstatic.Close` can be bypassed by `AddKey` plus `Rotate`

- Severity: HIGH
- Package/file/line: `crypto/envelope/kekstatic/kekstatic.go:67`, `crypto/envelope/kekstatic/kekstatic.go:93`, `crypto/envelope/kekstatic/kekstatic.go:138`, `crypto/envelope/kekstatic/kekstatic.go:176`
- Affected public API: `kekstatic.KEK.Close`, `AddKey`, `Rotate`, `Wrap`
- Evidence: `Close` zeroes and deletes current key material but does not record a terminal closed state. `AddKey` can recreate the key map, `Rotate` can activate the new key, and `Wrap` can operate again.
- Failure scenario: shutdown code calls `Close` assuming the KEK is permanently retired. Another goroutine or reused object re-adds a key and resumes wrapping after key material was supposed to be gone.
- Why tests/gates might miss it: tests usually call `Wrap` immediately after `Close`; they do not attempt `AddKey -> Rotate -> Wrap`.
- Minimal fix: add a guarded `closed` flag. `Close` sets it; all mutating and cryptographic methods fail closed after close.
- Tests to add/run: `TestKEK_ClosePreventsAddRotateWrap`, idempotent close, and a race test around `Close`/`Wrap`.

### H-008 - Leader-election callback drain can block forever on shutdown or leadership loss

- Severity: HIGH
- Package/file/line: `infra/leaderelection/pgadvisory/pgadvisory.go:254`, `infra/leaderelection/pgadvisory/pgadvisory.go:327`, `infra/leaderelection/redislock/redislock.go:270`, `infra/leaderelection/redislock/redislock.go:344`
- Affected public API: `leaderelection.Callbacks.OnAcquired`, Redis and Postgres electors
- Evidence: both electors cancel the callback context on parent cancellation or lock loss, then call `awaitCallbackDrain`, which loops forever until `OnAcquired` returns. The only bounded behavior is periodic warning/metrics.
- Failure scenario: user callback ignores `ctx`. The process loses the lock or is shutting down, but the elector never returns. In strict mode this avoids same-process overlap, but it can also pin shutdown forever and leave the orchestrator to SIGKILL.
- Why tests/gates might miss it: tests use cooperative callbacks that return on context cancellation.
- Minimal fix: add a v2 API decision before freeze: `WithCallbackDrainTimeout` or an explicit stall policy (`wait forever`, `abandon after timeout`, `panic/exit`). Keep strict wait as a deliberate default only if documented in the API contract.
- Tests to add/run: callback ignoring ctx, lock-loss path, shutdown path, and metrics/log assertions for timeout/stall policy.

### H-009 - TLS/mTLS credential rotation is rolling-restart only despite rotation claims

- Severity: HIGH
- Package/file/line: `security/netutil/tls.go:124`, `security/netutil/tls.go:157`, `app/builder.go:1217`, `app/module.go:188`
- Affected public API: `netutil.TLSConfig.ServerTLS`, `netutil.TLSConfig.ClientTLS`, app Builder TLS wiring
- Evidence: `ServerTLS` and `ClientTLS` load cert/key/CA once and return static `tls.Config{Certificates: ..., ClientCAs/RootCAs: ...}`. There is no `GetCertificate`, `GetClientCertificate`, `GetConfigForClient`, watched CA pool, SIGHUP reload hook, or app-level rotation component.
- Failure scenario: Kubernetes or Vault Agent rotates mounted TLS files. Existing HTTP, gRPC, AMQP, Redis, Postgres, or NATS clients keep using the old cert material until process restart. The changelog currently claims broad credential rotation, but the core mTLS surface is not hot-rotatable.
- Why tests/gates might miss it: tests load certs once and assert TLS floors/redaction; they do not modify files after startup and assert new handshakes use new material.
- Minimal fix: add a hot-reloadable TLS source before v2 API freeze, or explicitly document TLS/mTLS as rolling-restart-only. For top-tier rotation, expose a `ReloadingTLSConfig`/`CertificateSource` with bounded reload errors and readiness signaling.
- Tests to add/run: rotate cert/key/CA files under a server and client; assert new handshakes use new certs without restart and bad rotations keep the previous good config while readiness/metrics expose the failure.

### H-010 - Outbox role interfaces exist but constructors still freeze the fat Store API

- Severity: HIGH
- Package/file/line: `infra/outbox/store.go:8`, `infra/outbox/store.go:98`, `infra/outbox/outbox.go:112`, `infra/outbox/outbox.go:195`, `infra/outbox/relay.go:41`, `infra/outbox/relay.go:198`
- Affected public API: `outbox.NewWriter`, `outbox.NewRelay`, `Writer.store`, `Relay.store`
- Evidence: `Store` was split into `Inserter`, `Claimer`, `Outcomer`, `Janitor`, and `Observer`, but `NewWriter(store Store, ...)` still requires the whole 10-method interface even though `Writer.Write` only calls `Insert`.
- Failure scenario: service-side transactional producers and test doubles must implement claim, heartbeat, outcome, janitor, and observer methods just to insert. That defeats the role split exactly where v2 should freeze capability-minimized APIs.
- Why tests/gates might miss it: existing fakes satisfy the full `Store`, so compile-time tests do not prove narrow interface adoption.
- Minimal fix: change `NewWriter`/`Writer` to accept `Inserter`; introduce a deliberate relay interface such as `interface { Claimer; Outcomer; Janitor }` plus `Observer` only where metrics need it.
- Tests to add/run: compile-time tests with a minimal `Inserter` fake and a minimal relay-store fake.

### H-011 - Memory budget and in-memory rate limiters ignore context while Redis implementations honor it

- Severity: HIGH
- Package/file/line: `data/budget/memory/memory.go:208`, `data/budget/memory/memory.go:271`, `data/budget/memory/memory.go:301`, `data/ratelimit/tokenbucket/tokenbucket.go:196`, `data/ratelimit/gcra/gcra.go:190`
- Affected behavior: `budget.Budget`, rate-limiter implementations
- Evidence: memory budget and in-memory rate limiters take `_ context.Context`, while Redis budget/rate-limit implementations pass context into backend calls and time-sensitive checks.
- Failure scenario: a cancelled request still acquires local locks and may consume budget/tokens in memory mode. The same service wired to Redis would observe cancellation earlier. Local and distributed implementations therefore disagree on the interface contract.
- Why tests/gates might miss it: tests usually pass `context.Background()` and assert arithmetic, not cancelled contexts.
- Minimal fix: check nil/cancelled context at method entry and before/after contended locks. Decide package-wide whether nil context is rejected or normalized.
- Tests to add/run: cancelled-context tests for memory budget `Consume/Peek/Refund`, tokenbucket `Allow`, and GCRA `Allow`.

## MEDIUM

### M-001 - Several `New*` constructors still start goroutines or do lifecycle work

- Severity: MEDIUM
- Package/file/line: `httpx/mcp/mcp.go:427`, `data/cache/memory_cache.go:163`, `data/cache/memory_cache.go:221`, `data/ratelimit/tokenbucket/tokenbucket.go:120`, `data/budget/memory/memory.go:123`
- Affected public API: `mcp.NewServer`, `cache.NewMemoryCache`, `tokenbucket.New`, `budget/memory.New`
- Evidence: these constructors start async audit workers or sweepers. Some now use weak refs, but they still allocate background runtime resources behind `New*`.
- Failure scenario: callers treat `New*` as pure construction and forget lifecycle registration. The result is hidden goroutines or non-deterministic cleanup.
- Why tests/gates might miss it: tests stop components in one process; they do not enforce the naming/lifecycle convention.
- Minimal fix: rename side-effecting constructors to `Open*`/`Start*`, split pure construction from explicit start, or add an API-freeze exception list with tests.
- Tests to add/run: lifecycle tests proving no goroutines remain after `Stop/Close`; static inventory for exported `New*` containing `go`.

### M-002 - Security and operational policy still uses positional bool options

- Severity: MEDIUM
- Package/file/line: `httpx/mcp/mcp.go:313`, `httpx/mcp/mcp.go:338`, `httpx/middleware/tenant/tenant.go:96`, `httpx/middleware/csrf/csrf.go:94`, `data/queue/redisqueue/queue.go:481`
- Affected public API: `WithStrictAudit(bool)`, `WithAsyncAudit(bool)`, `WithRequired(bool)`, `WithSecure(bool)`, `WithRecoveryEnabled(bool)`
- Evidence: bare booleans flip audit strictness, async/best-effort audit, tenant requirement, CSRF cookie security, and Redis queue recovery.
- Failure scenario: `WithSecure(false)` or `WithRequired(false)` is a one-token copy-paste change that weakens production behavior without a type-level signal.
- Why tests/gates might miss it: tests intentionally cover both branches; they do not model downstream misuse.
- Minimal fix: replace with named intent options such as `WithoutSecureCookieForLocalHTTP`, `WithoutTenantRequired`, or `WithBestEffortAsyncAudit`.
- Tests to add/run: update examples/scaffolds and add kit-doctor/static checks for unsafe opt-outs in production wiring.

### M-003 - Deprecated/no-op public surfaces are still shipping in v2

- Severity: MEDIUM
- Package/file/line: `infra/storage/storagehttp/upload.go:30`, `infra/storage/storagehttp/upload.go:36`, `data/actionlog/memory/memory.go:250`, `data/actionlog/postgres/store.go:261`
- Affected public API: `storagehttp.UploadOptions.MaxMemory`, `actionlog/*Store.ListByTenantSeq`
- Evidence: `MaxMemory` is explicitly ignored and says it will be removed in v2. `ListByTenantSeq` remains exported and deprecated.
- Failure scenario: downstream code sets `MaxMemory` expecting multipart memory control, but it silently does nothing. Actionlog consumers can keep using list-all APIs that materialize long chains.
- Why tests/gates might miss it: compatibility tests preserve symbols; no gate fails on deprecated exports.
- Minimal fix: remove before v2.0.0 or make non-zero `MaxMemory` fail loudly. Remove or unexport `ListByTenantSeq` before the API freezes.
- Tests to add/run: compile docs/examples after removal and add a deprecated-export gate if any remain.

### M-004 - Prometheus metrics API shape is not consistent enough to freeze cleanly

- Severity: MEDIUM
- Package/file/line: `data/cache/rediscache/cache.go:32`, `data/cache/rediscache/cache.go:91`, `data/stream/redisstream/consumer.go:69`, `data/stream/redisstream/producer.go:31`, `grpcx/interceptor/metrics.go:28`, `infra/messaging/amqpbackend/metrics.go:66`, `infra/storage/s3backend/metrics.go:20`, `observability/redmetrics/redmetrics.go:113`
- Affected public API: metric constructors and registerer options
- Evidence: AGENTS says Prometheus metrics accept `prometheus.Registerer` via `WithRegisterer()`. The code has a mix of positional `NewMetrics(reg)`, `NewHTTP(reg, ...)`, `NewGRPCMetrics(reg)`, `WithCacheRegisterer`, `WithConsumerRegisterer`, `WithProducerRegisterer`, and `WithMetricsRegisterer`.
- Failure scenario: v2 freezes multiple metric-constructor idioms. Downstream tests and dashboards will cargo-cult different patterns, and later standardization becomes a breaking change.
- Why tests/gates might miss it: each package's metrics tests pass locally; no repo-wide convention check validates public API shape.
- Minimal fix: choose one stable pattern before v2.0.0, preferably `NewMetrics(opts ...MetricsOption)` with `WithRegisterer`, or document explicit exceptions and enforce them with a static check.
- Tests to add/run: API inventory check for exported metric constructors/options.

### M-005 - Local and memory storage backends ignore context in most operations

- Severity: MEDIUM
- Package/file/line: `infra/storage/localbackend/local.go:72`, `infra/storage/localbackend/local.go:160`, `infra/storage/localbackend/local.go:190`, `infra/storage/localbackend/local.go:214`, `infra/storage/membackend/mem.go:46`, `infra/storage/membackend/mem.go:79`, `infra/storage/membackend/mem.go:100`, `infra/storage/membackend/mem.go:158`
- Affected public API: `storage.Storage`, `storage.Lister`
- Evidence: remote backends pass context into provider SDK calls. Local/memory `Get/Delete/Exists/Copy/List` take `_ context.Context`, and local `Put` only passes context into validators, not the filesystem work.
- Failure scenario: a cancelled request can still copy memory, open files, fsync, rename, or iterate a large in-memory key set. Tests wired to memory/local do not behave like production S3/GCS/Azure/SFTP backends under cancellation.
- Why tests/gates might miss it: unit tests use background contexts and small datasets.
- Minimal fix: standardize storage context semantics. At minimum check `ctx.Err()` at method entry and in long loops; for local file reads/writes use context-aware copy helpers where possible.
- Tests to add/run: cancelled-context contract tests in `storagetest` applied to local, memory, S3/GCS/Azure, and SFTP.

### M-006 - Actionlog and approval cursors are forgeable base64 positions

- Severity: MEDIUM
- Package/file/line: `data/actionlog/cursor.go:23`, `data/actionlog/cursor.go:35`, `data/approval/cursor.go:19`, `data/approval/cursor.go:31`
- Affected public API: `actionlog.Query.Cursor`, `approval.Query.Cursor`
- Evidence: cursors are public base64url encodings of timestamp and ID. Length and syntax are checked, but there is no HMAC/signature. By contrast, `observability/auditlog` and `httpx/pagination` have signed cursor helpers.
- Failure scenario: a client can forge a cursor to skip pending approvals or audit/action entries in a UI/API. It may not cross tenant boundaries, but it violates the "opaque cursor" expectation and makes pagination state mutable by clients.
- Why tests/gates might miss it: tests verify round trips and malformed input, not tamper resistance.
- Minimal fix: sign cursors with a configured cursor key or explicitly rename/document them as transparent keyset positions. For sensitive admin/audit APIs, prefer the existing signed-cursor pattern.
- Tests to add/run: forged cursor, different-key cursor, malformed cursor, and cross-backend cursor compatibility tests.

### M-007 - `storage.Lister` truncates on `MaxKeys` without returning a next cursor or truncation signal

- Severity: MEDIUM
- Package/file/line: `infra/storage/list.go:26`, `infra/storage/list.go:45`, `infra/storage/s3backend/list.go:52`, `infra/storage/localbackend/list.go:118`, `infra/storage/sftpbackend/list.go:162`
- Affected public API: `storage.Lister.List`, `storage.ListOptions`
- Evidence: `ListOptions` has `MaxKeys` and `StartAfter`, but `Lister.List` returns only an iterator of objects/errors. Backends stop once `MaxKeys` is reached and do not return `NextStartAfter`, `IsTruncated`, or equivalent.
- Failure scenario: a caller uses `MaxKeys` to page storage keys and has to infer continuation from the last yielded key. If the iterator stops early due to consumer stop, backend error, or exact page boundary, the API does not tell the caller whether more objects exist.
- Why tests/gates might miss it: tests can assert bounded results but not that a caller can robustly resume every listing.
- Minimal fix: add a paged API before freeze, for example `ListPage(ctx, prefix, opts) (Page, error)` with `NextStartAfter` and `Truncated`, or document iterator paging as caller-managed and add helpers.
- Tests to add/run: backend contract tests for exact page size, more-than-page size, consumer early stop, and resume across all lister implementations.

### M-008 - AMQP/NATS publish metrics freeze high-cardinality route labels

- Severity: MEDIUM
- Package/file/line: `infra/messaging/route.go:38`, `infra/messaging/amqpbackend/metrics.go:71`, `infra/messaging/amqpbackend/metrics.go:120`, `infra/messaging/natsbackend/metrics.go:53`, `infra/messaging/natsbackend/metrics.go:84`
- Affected metric contract: `amqp_published_total`, `amqp_publish_duration_seconds`, `nats_published_total`, `nats_publish_duration_seconds`
- Evidence: route validation bounds length and invalid characters, but metrics label every publish with raw `exchange` and `routing_key`. There is no route registry or label guard.
- Failure scenario: a service accidentally includes tenant/customer/resource IDs in routing keys. Prometheus series cardinality explodes and the label contract becomes hard to change after v2.
- Why tests/gates might miss it: tests use static route names and assert collectors register; they do not exercise dynamic route names.
- Minimal fix: require static route registration for metrics, use `promutil.OpaqueLabelValue`, or add an opt-in label guard/route registry before freezing the metric contract.
- Tests to add/run: dynamic route cardinality guard tests and dashboard contract tests for allowed route labels.

### M-009 - Release evidence is stale after the workspace grew to 73 modules

- Severity: MEDIUM
- Package/file/line: `docs/release/RC_CHECKLIST_V2.md:22`, `docs/release/RC_CHECKLIST_V2.md:27`, `docs/release/RC_CHECKLIST_V2.md:34`, `docs/release/TAGGING_PLAN_V2.md:159`
- Affected release behavior: pre-tag checklist, tagging plan, release proof
- Evidence: live commands report 73 modules and 393 direct module edges. Release docs still cite 67 modules, 348 edges, 59 direct external deps, and a green allowlist.
- Failure scenario: release managers either distrust correct live output or treat stale checklist evidence as current.
- Why tests/gates might miss it: release docs are manually maintained; the checks validate live state, not stale prose.
- Minimal fix: regenerate release docs and rehearsal evidence after final source changes, and mark the older 67-module rehearsals superseded.
- Tests to add/run: `RELEASE_MODE=all make release-plan`, `make check-operational-readiness`, `make check-dependency-boundaries`, `make check-dependency-allowlist`, final rehearsal.

### M-010 - Security docs still contain compile-broken API examples

- Severity: MEDIUM
- Package/file/line: `docs/ai/security.md:123`, `docs/audit/THREAT_MODEL.md:790`
- Affected docs/API contract: signing and signedrequest examples
- Evidence: `crypto/signing.Verify` returns `error`, but docs show `ok, verifyErr := signing.Verify(...)`. The threat model references `signedrequest.Verify`, while the middleware package exposes `Middleware(...)`.
- Failure scenario: downstream services copy examples during migration and hit compile failures or nonexistent APIs.
- Why tests/gates might miss it: markdown examples are not all compiled.
- Minimal fix: update snippets or convert the important docs into testable `Example*` functions.
- Tests to add/run: snippet compile gate for executable Go blocks and package-level examples.

### M-011 - Temporal dependency risk is isolated but not explicitly accepted

- Severity: MEDIUM
- Package/file/line: `runtime/temporal/go.mod:16`, `runtime/temporal/go.mod:27`, `runtime/temporal/temporal.go:1`, `docs/release/RC_CHECKLIST_V2.md:299`
- Affected supply-chain/API decision: `runtime/temporal/v2`
- Evidence: the repo does not call Nexus RPC APIs directly. `github.com/nexus-rpc/sdk-go` appears only as an indirect dependency of `go.temporal.io/sdk`. The Temporal wrapper is isolated in its own module and exposes only Temporal SDK client/worker surfaces.
- Failure scenario: v2 freezes a Temporal adapter and accepts the Temporal SDK's indirect dependency tree, including Nexus RPC, without a clear release decision. If the team later decides the dependency is not acceptable, removing `runtime/temporal/v2` or changing its SDK basis becomes a v2 breaking/API-support issue.
- Why tests/gates might miss it: allowlist/boundary checks look at direct deps and module isolation; they do not encode ecosystem confidence or indirect dependency acceptance.
- Minimal fix: explicitly accept Temporal SDK's indirect dependency tree in supply-chain docs, or defer `runtime/temporal/v2` from the v2 stable surface until the dependency posture is settled.
- Tests to add/run: direct/indirect dependency report for `runtime/temporal`, plus a release checklist row that records the decision.

## LOW

### L-001 - MemoryCache lifecycle docs still overstate forgotten-Close leakage

- Severity: LOW
- Package/file/line: `data/cache/memory_cache.go:218`, `data/cache/memory_cache.go:468`
- Affected docs: `MemoryCache.Close`
- Evidence: the sweeper uses a weak pointer so forgotten `Close` no longer pins the cache forever, but the Close docs still say forgetting Close leaks goroutines.
- Failure scenario: reviewers and operators misunderstand current behavior. `Close` is still required for deterministic shutdown, but the parent-retention claim is stale.
- Why tests/gates might miss it: tests validate behavior, not comments.
- Minimal fix: revise docs to distinguish deterministic cleanup from weak-ref safety net.
- Tests to add/run: none; docs-only.

### L-002 - Client IP docs still say private proxies are trusted by default

- Severity: LOW
- Package/file/line: `httpx/middleware/clientip/clientip.go:50`
- Affected docs/API contract: `clientip.ClientIP`
- Evidence: code now defaults to loopback-only trusted proxies, but the `ClientIP` comment says headers are trusted when `RemoteAddr` comes from "private/loopback ranges."
- Failure scenario: downstream services assume RFC1918/ULA proxies are trusted by default and do not configure ingress CIDRs explicitly.
- Why tests/gates might miss it: tests assert code behavior, not stale wording.
- Minimal fix: update comment to "configured trusted proxy ranges, default loopback-only."
- Tests to add/run: none; docs-only.

### L-003 - AGENTS/release metadata still conflict with the current module count

- Severity: LOW
- Package/file/line: `AGENTS.md:3`, `docs/release/RC_CHECKLIST_V2.md:34`, `docs/release/FINAL_RELEASE_RUNBOOK_V2.md:139`
- Affected docs/release narrative: repo size and release plan
- Evidence: `AGENTS.md` and release docs still describe 67 modules while live `RELEASE_MODE=all make release-plan` reports 73 modules.
- Failure scenario: reviewers and agents start from stale module-count assumptions and may skip the six modules added after the older release evidence.
- Why tests/gates might miss it: live release-plan can pass while guidance docs remain stale.
- Minimal fix: update AGENTS and release docs to the current module count after the final source changes.
- Tests to add/run: docs consistency grep for "67 modules" and stale evidence counts.

## Audited Clean

- KMS key confinement: AWS, GCP, Azure Key Vault, and Vault Transit adapters constrain key IDs before provider decrypt/unwrap calls.
- S2S auth fail-closed: HTTP and gRPC auth paths require explicit trusted-S2S context or concrete permissions/scopes; missing permissions are no longer treated as allow.
- Retry cancellation: `resilience/retry` preserves the business error with `errors.Join` when context cancellation races a returned error.
- Memory budget sweep race: current memory budget verifies the live bucket under lock before mutating, avoiding the prior double-grant orphan bucket bug.
- Redis queue heartbeat: permanent heartbeat failure cancels local processing, preventing peer reaper duplicate dispatch.
- Runtime eventbus shutdown: closed-channel publish races now surface `ErrStopped` instead of success.
- Signed request verification: key resolution happens before body streaming, and oversized/tampered bodies do not reach the handler before signature checks.
- JWT/PASETO verification: JWKS/PASETO providers have stale-key windows and fail closed after max stale unless explicitly disabled.
- CSRF: session-bound issuer supports current+previous secrets and now caps token length before base64 decode.
- Storage not-found metrics: S3, GCS, and Azure normalize expected not-found outcomes before recording operation errors.
- Postgres credential rotation: pgx supports `PasswordProvider` for new physical connections and exposes `Pool.Reset` to force fresh auth after a rotation event.
- NATS credential rotation: NATS config supports `UsernamePasswordProvider` and `TokenProvider`, delegated to nats.go reauthentication.
- Release gates: `make check-operational-readiness` passes on 73 modules; `make check-dependency-boundaries` passes on 393 direct module edges.

## Missed-Review-Lens Summary

- H-002 and H-003 were missed because the earlier review trusted newly added "bounds" comments instead of checking whether the cap executes before allocation and whether inbound paths call the validator.
- H-004 was missed because the review checked the app-level construction guard but did not follow the option into backend `Connect`.
- H-005, M-006, and M-007 were missed because pagination was reviewed for existence of cursors, not for adversarial limit size, cursor tampering, and continuation semantics.
- H-006, H-007, and H-008 were missed because lifecycle review stopped at idempotent `Close`/warning behavior instead of requiring post-close fail-closed and bounded drain semantics.
- H-009 was missed because rotation review focused on provider-backed app credentials and did not include TLS/mTLS cert files as a first-class credential class.
- H-010 was missed because interface role-splitting was noticed but constructor call sites were not checked for whether the role split actually became usable.
- H-011 and M-005 were missed because memory/local implementations were treated as cheap/test implementations instead of contract participants that must match Redis/cloud backends.
- M-004 and M-008 were missed because metrics were checked for registration and dashboards, not as stable public API contracts with naming, option-shape, and cardinality obligations.
- M-009 through L-003 were missed because release docs were not rechecked after the latest module growth.

## v2 API Freeze Blockers

- Decide and implement inbound messaging validation semantics before freezing AMQP/NATS consumer contracts.
- Make `rediscache` and `redisstore` read-size enforcement real pre-allocation guards, and standardize oversized-value errors.
- Fix `app/amqp.WithoutTLS` so the documented option affects backend connection behavior.
- Add max page-limit contracts to actionlog, approval, and auditlog list APIs.
- Decide whether actionlog/approval cursors must be signed, and change the API now if they do.
- Make SFTP `Close` terminal and define post-close behavior across storage backends.
- Make `kekstatic.Close` terminal.
- Add a leader-election callback stall policy before the elector API freezes.
- Decide whether TLS/mTLS rotation is hot-supported or rolling-restart-only; expose the hot-rotation API now if top-tier rotation is required.
- Narrow outbox constructors to role interfaces before the fat `Store` shape freezes.
- Standardize Prometheus metric constructor/registerer API shape.
- Decide whether `storage.Lister` needs a paged return type with truncation/next cursor before v2.
- Replace positional bool safety options or explicitly accept them as stable v2 footguns.
- Remove/no-op reject deprecated exported surfaces marked for v2 removal.

## Release Tag Blockers

- `make check-dependency-allowlist` must pass.
- Release docs must be refreshed for 73 modules, 393 direct module edges, current dependency counts, and the dirty-tree/current-HEAD rehearsal.
- Final release rehearsal must be rerun after all source/API fixes.
- AGENTS/release docs must stop contradicting the current module count.
