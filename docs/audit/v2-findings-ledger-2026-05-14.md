# rho-kit v2.0.0 findings ledger

Date: 2026-05-14
Base commit reviewed: `8f81252`

Purpose: preserve the full review-thread finding history in a durable ledger.
Unlike `v2-line-by-line-review-2026-05-14.md`, this file intentionally keeps
open, fixed, rejected, duplicate, and needs-verification items so review context
is not lost between chats.

Status legend:

- `OPEN`: still needs code, docs, tests, or release-gate work before v2.0.0.
- `FIXED-CURRENT`: verified fixed in the current checked-out source.
- `FIXED-WORKTREE`: appears fixed by local uncommitted working-tree changes;
  still needs tests, review, and commit.
- `DOC-OPEN`: documentation/release artifact drift remains.
- `NEEDS-VERIFY`: plausible issue or design concern that needs a repro,
  targeted test, benchmark, provider test, or policy decision before scoring.
- `REJECTED`: deliberately not part of the v2.0.0 scope.
- `DUPLICATE`: covered by another ledger item.

Current worktree note: while preparing this ledger, `app/builder.go` and
`app/httpclient_module_test.go` already had uncommitted local changes that
appear to address the HTTP-client tracing-order bug. Those changes are not
owned by this file and are not treated as committed release evidence.

## Release blockers and high-priority code findings

| ID | Area | Status | Finding | Evidence / note | Action |
|---|---|---:|---|---|---|
| L-001 | `app` | FIXED-WORKTREE | Built-in HTTP client could not observe tracing because it initialized before user tracing modules. | Original evidence: `app/builder.go`, `app/builder_helpers.go`, `app/httpclient_module.go`; current working tree reorders `TracingProvider` modules before built-ins and adds a test. | Run `go test ./app/...`, review ordering side effects, commit if correct. |
| L-002 | `infra/messaging/amqpbackend/integrationtest` | OPEN | Integration tests do not compile after connection API changed. | Compile-only integration sweep fails on `conn.Close undefined`. | Update tests to current close/shutdown API; then run Docker-backed tests. |
| L-003 | `infra/messaging/natsbackend/integrationtest` | OPEN | Integration tests do not compile after connection API changed. | Compile-only integration sweep fails on `conn.Close undefined`. | Update tests to current close/shutdown API; then run Docker-backed tests. |
| L-004 | `infra/outbox/postgres` | OPEN | `FetchPending` SQL uses `FOR UPDATE SKIP LOCKED` with a window function. | PostgreSQL rejects locking clauses with window functions. | Split claim/ordering query; add integration test that executes `FetchPending`. |
| L-005 | `core/secret` | OPEN | Constant-time equality folds length mismatch into a `byte`, so lengths differing by multiples of 256 can compare equal when extra bytes are zero. | `constantTimeEqual` uses `byte(len(a) ^ len(b))`. | Use non-truncating accumulator; add tests for 256-byte length deltas. |
| L-006 | `tools/check-licenses.sh` | OPEN | License gate can pass with incomplete scan output. | `go-licenses ... || true` drops per-module scanner failures. | Fail on scanner failure and print module stderr. |
| L-007 | `crypto/passhash` | OPEN | Default verify limit allows 1 GiB Argon2 memory. | `DefaultVerifyLimits().MaxMemory` is `1 * 1024 * 1024` KiB. | Lower default or require explicit opt-in for very high memory hashes. |
| L-008 | `crypto/envelope/kekstatic` | OPEN | `Unwrap` can use a key slice after lock release while `RemoveKey`/`Close` zero the same slice. | History-derived from code review; needs race test. | Copy key bytes under lock or hold lock through AEAD setup. |
| L-009 | `crypto/encrypt` | OPEN | `FieldEncryptor.RegisterMetrics` mutates the operation hook while encryption/decryption can read it concurrently. | Shared hook is written/read without synchronization. | Make hook immutable at construction or guard with atomic/mutex. |
| L-010 | `crypto/paseto` | OPEN | `V4Local.Close` zeroes exported bytes, not necessarily live key material. | Upstream key export may return a copy. | Clarify impossibility or store key in zeroizable owned memory. |
| L-011 | `crypto/envelope/awskms` | OPEN | AWS KMS metrics register against `prometheus.DefaultRegisterer`. | No `WithRegisterer` option for the adapter metric. | Add registerer option or remove process-global metric. |
| L-012 | `crypto/envelope/gcpkms` | OPEN | Version `0` accepted where docs require positive key versions. | History-derived code review. | Reject zero version; add tests. |
| L-013 | `crypto/envelope/azurekeyvault` | OPEN | Azure Key Vault host matching can be case-sensitive for envelope KIDs. | `parseAzureKeyID` returns `u.Host`; configured vault host is lowercased. | Lowercase parsed envelope host before comparison. |
| L-014 | `infra/redis` | OPEN | FR-077 guard can be bypassed by `Fields.Redis.Options()` plus direct `Connect`. | `Fields.ValidateRedis` enforces policy; lower-level connect path accepts unsafe options. | Enforce in connect path or add an explicit unsafe opt-out. |
| L-015 | `app/redis` | FIXED-CURRENT | Earlier `WithRedis`/module path silently bypassed plaintext/passwordless guard. | Current app Redis module has FR-077 safety tests; root `WithRedis` no longer exists. | Keep regression tests. |
| L-016 | `data/lock/pgadvisory` | FIXED-CURRENT | `sessionLock.Extend` was a no-op and could allow split-brain. | Current implementation was rechecked as performing I/O; previous finding closed. | Keep lost-session test coverage. |
| L-017 | `crypto/envelope/awskms` | FIXED-CURRENT | AWS KMS unwrap accepted caller-supplied key IDs before SDK decrypt. | Current unwrap constrains configured key ID before SDK call. | Keep cross-key unwrap rejection tests. |
| L-018 | `crypto/envelope/gcpkms` | FIXED-CURRENT | GCP KMS unwrap accepted caller-supplied key IDs before SDK decrypt. | Current unwrap uses configured-key allow check before SDK call. | Keep cross-key unwrap rejection tests. |
| L-019 | `crypto/envelope/vaulttransit` | FIXED-CURRENT | HashiCorp Vault Transit support question. | Vault Transit adapter exists and is documented under `crypto/envelope/vaulttransit`. | No feature gap; keep provider docs/tests. |
| L-020 | `crypto/envelope/azurekeyvault` | FIXED-CURRENT | Azure Key Vault support request. | Azure Key Vault adapter exists and is documented. | Finish L-013 host-normalization caveat. |

## Security and auth semantics

| ID | Area | Status | Finding | Evidence / note | Action |
|---|---|---:|---|---|---|
| L-021 | `httpx/middleware/auth`, `grpcx/interceptor/auth` | FIXED-CURRENT | S2S auth used to allow requests when client permissions were empty. | Current tests were run with `-tags authtest`; fail-closed path is fixed. | Keep explicit empty-permission deny tests. |
| L-022 | `httpx/middleware/auth`, `grpcx/interceptor/auth` | FIXED-CURRENT | `WithTrustedS2S` was publicly reachable in production builds. | Current helpers live behind `//go:build authtest`. | Keep production build check that helper is absent. |
| L-023 | `crypto/signing` | FIXED-CURRENT | `Sign(body, secret)` and `Verify(secret, body, ...)` had inverted argument order. | Current API uses typed `Secret` and order `Sign(secret, body)`, `Verify(secret, body, ...)`. | Keep compile-time typed API. |
| L-024 | `crypto/signing` | FIXED-CURRENT | `NewStaticKeyStore` panicked while `NewStaticKeyStoreE` returned errors. | Current API has `NewStaticKeyStore(...)(..., error)` and `MustNewStaticKeyStore`. | No action. |
| L-025 | `crypto/paseto` | FIXED-CURRENT | Verify path silently dropped custom claims that collided with reserved names. | Current validate path returns `ErrReservedClaim` for reserved non-top-level claims. | Keep collision tests. |
| L-026 | `crypto/envelope` | NEEDS-VERIFY | Body AAD composition may be ambiguous without length-prefixing caller AAD/domain separator. | Claude review claim; not fully re-proven in latest pass. | Re-review format compatibility and add collision tests if real. |
| L-027 | `httpx/middleware/signedrequest` | FIXED-CURRENT | Verify buffered up to 10 MiB before MAC comparison. | Current code resolves secret before body read and spills large bodies. | Keep streaming/spooling tests. |
| L-028 | `httpx/middleware/signedrequest/redis` | OPEN | Nonce store detaches from request context, so cancelled callers may still pin Redis work. | Historical medium finding; not closed in line-by-line artifact. | Thread request context into Redis operation or cap detached work. |
| L-029 | `httpx/sign` | OPEN | Per-request signing secret is not zeroed after use. | Review-thread finding. | Use `secret.String` or explicit zeroing for copied key bytes. |
| L-030 | `security/jwtutil` | OPEN | Fully custom JWKS HTTP client can bypass TLS floor/hardened defaults. | Current API accepts custom client as-is. | Document as trust boundary or wrap/validate transport. |
| L-031 | `authz/openfga` | OPEN | Fully custom OpenFGA HTTP client can bypass kit TLS/client hardening. | Current API accepts custom client as-is. | Document as trust boundary or provide hardened client option. |
| L-032 | `security/jwtutil/revocation` | OPEN | Audit hooks can panic after a successful revocation mutation. | Line-by-line review finding. | Recover around audit hook or call before mutation with clear semantics. |
| L-033 | `httpx/middleware/csrf` | NEEDS-VERIFY | Default `SameSite=Lax` may be weaker than desired for session-bound flows. | Current code defaults Strict when session-bound and Lax otherwise; older finding partially closed. | Confirm docs match exact behavior. |
| L-034 | `httpx/middleware/csrf` | OPEN | CSRF secret rotation exists, but middleware has no zeroization/lifecycle close hook. | Review-thread finding. | Add lifecycle/zeroing support or document immutable in-memory secret lifetime. |
| L-035 | `security/asvs` | OPEN | ASVS package registry is stale/incomplete for current security/crypto/storage surface. | Line-by-line review finding. | Refresh registry and add coverage tests that fail on missing security packages. |
| L-036 | `core/apperror` | FIXED-CURRENT | `ConflictError.Retryable()` default was retryable. | Current `NewConflict` is non-retryable; retryable variant is explicit. | No action. |

## Data, cache, queue, stream, budget, and idempotency

| ID | Area | Status | Finding | Evidence / note | Action |
|---|---|---:|---|---|---|
| L-037 | `data/cache/compute` | FIXED-CURRENT | Foreground singleflight could survive `Close` and followers could wait forever. | Current review marked fixed. | Keep close/drain tests. |
| L-038 | `data/budget/memory` | FIXED-CURRENT | Earlier sweep/period rollover double-grant race for `Consume`. | Current `Consume` verifies bucket is still live after lock. | Keep concurrency test. |
| L-039 | `data/budget/memory` | NEEDS-VERIFY | Sweep still deletes without taking bucket mutex; non-Consume paths might have edge cases. | Original finding was closed for `Consume`; `Peek`/`Refund` were not stress-proven. | Add race/stress tests around sweep + refund/peek. |
| L-040 | `data/ratelimit/redis` | OPEN | Redis GCRA precision/config mismatch for sub-micro rates. | Line-by-line review finding. | Add lower-bound config validation or fixed-point precision docs/tests. |
| L-041 | `data/idempotency/redisstore` | OPEN | `Set`/`Unlock` read stored values without pre-read `STRLEN` cap. | `Get`/`TryLock` had cap; these paths were missing. | Apply same length guard before read. |
| L-042 | `data/idempotency/pgstore` | OPEN | Stored response rows are scanned before payload size validation. | Line-by-line review finding. | Enforce SQL-side `octet_length` cap before scan. |
| L-043 | `httpx/middleware/idempotency` | DOC-OPEN | Package docs have stale links/recipes. | Review-thread finding. | Update docs after current API check. |
| L-044 | `data/cache/rediscache` | OPEN | `MGet` can read oversized Redis values before applying max-value contract. | Line-by-line review finding. | Use length-aware Redis calls or reject oversized values before allocation. |
| L-045 | `data/cache` | NEEDS-VERIFY | ComputeCache TTL overflow caveat. | Review-thread finding; not reproduced. | Add boundary tests for very large TTL values. |
| L-046 | `data/lock/redislock` | OPEN | `Release` clears local token on ambiguous Redis backend errors. | Line-by-line review finding. | Preserve token on unknown release outcome or expose ambiguous state. |
| L-047 | `data/lock/redislock` | OPEN | Redis lock key validation is weak/missing. | Review-thread finding. | Apply shared key validation. |
| L-048 | `data/queue/redisqueue` | FIXED-CURRENT | Heartbeat permanent-fail could leave processing running and cause duplicate dispatch. | Current heartbeat loop cancels local processing after max failures. | Keep regression tests. |
| L-049 | `data/queue/redisqueue` | OPEN | `NewQueue` panic discards underlying UUID generation error. | Line-by-line review finding. | Preserve cause in panic/error path. |
| L-050 | `data/queue/riverqueue` | OPEN | ID de-duplication promise is stronger than the wrapper guarantees. | Line-by-line review finding. | Fix docs/API or enforce dedupe contract. |
| L-051 | `data/stream/redisstream` | OPEN | Producer header map is unbounded. | Line-by-line review finding. | Add header count/bytes cap. |
| L-052 | `data/stream/redisstream` | OPEN | `PublishBatch` metrics undercount partial success after pipeline errors. | Line-by-line review finding. | Record per-message result or conservative failure metrics. |
| L-053 | `data/stream/redisstream` | NEEDS-VERIFY | Foreign Redis stream entries can allocate large payload/header values before validation. | Review-thread caveat. | Add read-side size caps before full materialization where possible. |
| L-054 | `data/actionlog` | OPEN | Actionlog signing secrets are not zeroed after Sign/Verify. | Review-thread finding. | Use zeroizable secret wrappers/copies. |
| L-055 | `data/actionlog/postgres` | DOC-OPEN | Migration comments said `prev_hash` is HMAC while code used plain chain hash. | Review-thread finding; current migration now says HMAC in one place and needs source confirmation. | Align migration comments and implementation terminology. |
| L-056 | `data/approval/postgres` | OPEN | Payload is scanned before cap/validation. | Line-by-line review finding. | SQL-side `octet_length` cap before scan. |
| L-057 | `data/approval` | OPEN | Tenant wrapper has TOCTOU/cross-tenant mutation caveat at interface level. | Review-thread finding. | Recheck tenant after store mutation or narrow interface. |
| L-058 | `data/approval` | OPEN | Cursor signer lacks Close/zero lifecycle. | Review-thread finding. | Add zeroization lifecycle or document immutable lifetime. |

## HTTP, gRPC, middleware, and API ergonomics

| ID | Area | Status | Finding | Evidence / note | Action |
|---|---|---:|---|---|---|
| L-059 | `httpx` | OPEN | Package docs claim retry behavior that `NewHTTPClient` does not provide. | Line-by-line review finding. | Split docs for plain vs resilient clients. |
| L-060 | `httpx` | OPEN | `JSONStatus` / `JSONNoBodyStatus` do not validate returned status. | Review-thread finding. | Validate 100..999 or documented HTTP status range. |
| L-061 | `httpx/problemdetails` | OPEN | `FromError` does not mirror `httpx.HTTPStatus` for `CodeStorageFull`. | Line-by-line review finding. | Add mapping + tests. |
| L-062 | `httpx/pagination` | OPEN | `HandleCursorList` lacks option validation. | Line-by-line review finding. | Validate limit/default/max at construction. |
| L-063 | `httpx/pagination` | OPEN | `BuildResult` can panic with negative limit. | Line-by-line review finding. | Reject negative limit. |
| L-064 | `httpx/pagination` | OPEN | `CursorSigner.Decode` lacks direct input length cap. | Line-by-line review finding. | Cap token length before decode/MAC work. |
| L-065 | `authz` | OPEN | `SubjectFromUntrustedHeader` is an exported production-unsafe escape hatch. | Line-by-line review finding. | Move behind test/build tag or rename to make risk explicit. |
| L-066 | `authz` | OPEN | `Logged` bypasses safe `authz.Allow` helper. | Review-thread finding. | Reuse helper or prove equivalent behavior. |
| L-067 | `authz` | OPEN | `Logged` returns nil-inner/no-decider errors without audit event. | Review-thread finding. | Emit denied/error audit event on construction/runtime failures. |
| L-068 | `authz` | OPEN | Typed nil `AuditSinkFunc` can bypass nil guard. | Review-thread finding. | Harden nil handling for function aliases. |
| L-069 | `httpx/openapi` | OPEN | `Mount` advertises error mapping it cannot perform. | Line-by-line review finding. | Fix docs or implementation. |
| L-070 | `httpx/slohttp` | NEEDS-VERIFY | Handler may return a body on `HEAD`. | Review-thread caveat. | Add HEAD tests. |
| L-071 | `httpx/middleware/auditlog` | OPEN | Middleware ignores audit sink errors. | Line-by-line review finding. | Decide fail-open vs fail-closed and expose metrics/logging. |
| L-072 | `httpx/middleware/auditlog` | DOC-OPEN | `WithTrustedProxies` docs/comment are misleading. | Review-thread finding. | Clarify trust boundary. |
| L-073 | `httpx/middleware/timeout` | OPEN | Late panics after hard timeout are swallowed from caller path. | Line-by-line review finding. | Surface via logger/metric/panic hook. |
| L-074 | `httpx/middleware/timeout` | DOC-OPEN | `ErrResponseTooLarge` comment stale. | Review-thread finding. | Update comment. |
| L-075 | `httpx/middleware/stack` | DOC-OPEN | `WithTimeout` docs say WebSocket bypass exists, but stack path does not expose that bypass. | Line-by-line review finding. | Fix docs or implement bypass. |
| L-076 | `httpx/middleware/stack` | OPEN | Default stack metrics bind to global Prometheus registerer. | Line-by-line review finding. | Add stack-level registerer option. |
| L-077 | `httpx/middleware/stack` | OPEN | `ResponseRecorder.WriteHeader` does not validate status before delegating. | Review-thread finding. | Validate status code. |
| L-078 | `httpx/middleware/secheaders` | DOC-OPEN | Docs overstate HSTS on every response. | Review-thread finding. | Align docs with TLS/proxy behavior. |
| L-079 | `httpx/middleware/tenant` | DOC-OPEN | Docs still reference removed `WithRequired` option. | Review-thread finding. | Update tenant docs. |
| L-080 | `httpx/mcp` | DOC-OPEN | Public docs/comments refer to removed boolean options. | Line-by-line review finding. | Update docs/examples. |
| L-081 | `httpx/mcp` | OPEN | Async audit stop cannot be retried after timeout. | Line-by-line review finding. | Make stop idempotent/retryable or document terminal state. |
| L-082 | `httpx/mcp` | NEEDS-VERIFY | `readBody(max+1)` overflow at extreme configured max. | Review-thread caveat. | Add guard for `max == math.MaxInt64`. |
| L-083 | `grpcx` | OPEN | Raw gRPC options can undo hardened defaults. | Line-by-line review finding. | Order hardened options last or document trust boundary. |
| L-084 | `grpcx` | OPEN | `GRPCStatus` lacks `CodeStorageFull` mapping. | Line-by-line review finding. | Add mapping + tests. |
| L-085 | `grpcx` | DOC-OPEN | Docs disagree on default interceptor order. | Review-thread finding. | Align docs with actual construction order. |
| L-086 | `core/tenant` | OPEN | `MustNewID` intentionally bypasses validation and permits empty IDs, making the name misleading. | Line-by-line review finding. | Rename to unsafe/test helper or validate. |
| L-087 | `core/tenant` | OPEN | `KeyFor` has per-part caps but no total part-count/total-length cap. | Line-by-line review finding. | Add total cap. |
| L-088 | `core/contextutil` | OPEN | Request/correlation IDs are stored without validation. | Line-by-line review finding. | Validate at set time or add explicit unsafe setters. |
| L-089 | `core/config` | OPEN | `Watchable.OnChange(nil)` accepted and later recovered/logged on every set. | Line-by-line review finding. | Reject nil callback. |
| L-090 | `core/config` | OPEN | `MustLoad` panic discards cause. | Line-by-line review finding. | Include wrapped error in panic message/value. |
| L-091 | `core/validate` | NEEDS-VERIFY | Public surface leaks third-party `*validator.Validate` and package singleton may make tests order-sensitive. | Claude high finding; not fully re-reviewed in final pass. | Decide v2 API shape before freeze. |
| L-092 | `crypto/passhash` | NEEDS-VERIFY | Triple return `(matched, needsRehash, err)` invites caller misuse. | Claude API finding. | Consider `VerifyResult` before API freeze. |
| L-093 | `core/secret` | NEEDS-VERIFY | `Close()` naming is a use-after-free trap; `Zero()` may be clearer. | Claude medium API finding. | Decide before API freeze. |

## Infrastructure, storage, messaging, databases, and leader election

| ID | Area | Status | Finding | Evidence / note | Action |
|---|---|---:|---|---|---|
| L-094 | `infra/sqldb` | OPEN | `Fields.Validate` allows `sslmode=require`, but pgx connection wrapper rejects it by default. | Line-by-line review finding. | Align validation and dial policy. |
| L-095 | `infra/sqldb` | OPEN | `HealthCheck(nil)` can panic when invoked. | Review-thread finding. | Reject nil DB/pool at construction. |
| L-096 | `infra/sqldb` | OPEN | `ExportPoolMetrics` lacks input validation. | Review-thread finding. | Validate nil pool/registerer behavior. |
| L-097 | `infra/sqldb/pgx` | OPEN | Raw `lastSSLMode` scan is not parser-equivalent and can misclassify `sslmode=require`. | Line-by-line review finding. | Parse DSN using pgx config and enforce after parse. |
| L-098 | `infra/storage` | OPEN | `Manager.Default`/backend access can return a closed backend after `Manager.Close`. | Line-by-line review finding. | Track closed state and return error. |
| L-099 | `infra/storage` | OPEN | `Migrate` can return nil even when object migrations failed. | Line-by-line review finding. | Aggregate per-object failures. |
| L-100 | `infra/storage` | OPEN | `ListPage` has unbounded/overflowing `MaxKeys`. | Review-thread finding. | Validate bounds. |
| L-101 | `infra/storage` | NEEDS-VERIFY | `MaxFileSize` / `limitReader` max+1 overflow caveat. | Review-thread caveat. | Guard `max == math.MaxInt64`. |
| L-102 | `infra/storage/encryption` | OPEN | Nil context can panic in semaphore path. | Review-thread finding. | Normalize nil context or reject early. |
| L-103 | `infra/storage/encryption` | OPEN | Encrypted storage reads full ciphertext before key-provider lookup. | Review-thread finding. | Read envelope header/key metadata first. |
| L-104 | `infra/storage` | OPEN | Retry/circuitbreaker decorators hide optional backend capabilities. | Line-by-line review finding. | Forward optional interfaces or document loss. |
| L-105 | `infra/storage/retry` | OPEN | List retry does not retry initial list construction correctly. | Review-thread finding. | Wrap creation and iteration consistently. |
| L-106 | `infra/storage/circuitbreaker` | OPEN | List circuit breaker counts iterator creation but not lazy iteration errors. | Review-thread finding. | Measure/record iteration errors. |
| L-107 | `infra/storage/retry` | OPEN | Non-seekable `Put` bypasses retry and maybe metadata validation. | Review-thread finding. | Validate before bypass and document retry limits. |
| L-108 | `infra/storage/localbackend` | OPEN | `List` treats arbitrary prefixes like directories, inconsistent with memory/prefix semantics. | Review-thread finding. | Align prefix semantics and add storagetest coverage. |
| L-109 | `infra/storage/localbackend` | OPEN | `List` nil-context behavior contradicts docs. | Review-thread finding. | Reject nil or normalize consistently. |
| L-110 | `infra/storage/storagehttp` | NEEDS-VERIFY | `ParseAndStore` max file size plus overhead can overflow. | Review-thread caveat. | Add guard and boundary tests. |
| L-111 | `infra/storage/storagehttp/uploadsec` | OPEN | `AllowSVG` discards sanitizer output. | Line-by-line review finding. | Persist sanitized output or rename to detection-only. |
| L-112 | `infra/storage/storagehttp/uploadsec/clamav` | FIXED-CURRENT | `removeOnEOF` data race on removed bool. | Current code uses synchronized cleanup path; earlier race closed. | Keep race test. |
| L-113 | `infra/storage/storagehttp/uploadsec/clamav` | NEEDS-VERIFY | copy-bounded max+1 overflow caveat. | Review-thread caveat. | Add `max == math.MaxInt64` guard. |
| L-114 | `infra/storage/s3backend` | FIXED-CURRENT | Expected not-found delete/exists incremented error metrics. | Reviewer finding was fixed in storage metrics normalization. | Keep metrics tests. |
| L-115 | `infra/storage/azurebackend` | FIXED-CURRENT | Expected not-found delete/exists incremented error metrics. | Reviewer finding was fixed in storage metrics normalization. | Keep metrics tests. |
| L-116 | `infra/storage/gcsbackend` | FIXED-CURRENT | Expected not-found delete/exists incremented error metrics. | Reviewer finding was fixed in storage metrics normalization. | Keep metrics tests. |
| L-117 | `infra/storage/s3backend` | OPEN | `HealthCheck(nil)` can panic. | Review-thread finding. | Reject nil client/session at construction. |
| L-118 | `infra/storage/s3backend` | DOC-OPEN | Env docs stale around credential modes and rotation. | Review-thread finding. | Update docs and config matrix. |
| L-119 | `infra/storage/s3backend` | OPEN | Rotation support is provider-level only; env-level static credentials do not rotate. | Review-thread finding. | Add provider config path or document clearly. |
| L-120 | `infra/storage/azurebackend` | OPEN | Env config only supports account-key path; token credential path is code-only. | Review-thread finding. | Add env-supported token/default credential config or document limitation. |
| L-121 | `infra/storage/gcsbackend`, `infra/storage/azurebackend` | DOC-OPEN | Listing/optional-interface support matrix missing or inaccurate. | Review-thread finding. | Add backend capability matrix. |
| L-122 | `infra/storage/sftpbackend` | OPEN | Dial/connect hides underlying cause. | Review-thread finding. | Wrap and preserve cause safely. |
| L-123 | `infra/storage/sftpbackend` | OPEN | Static key/password material has no zeroization lifecycle. | Line-by-line review finding. | Add provider/zeroizable secret support. |
| L-124 | `infra/storage/sftpbackend` | OPEN | Operations ignore cancelled context once connected. | Review-thread finding. | Thread cancellation into operation deadlines. |
| L-125 | `infra/storage/sftpbackend` | OPEN | `List(nil)` and `HealthCheck(nil)` can panic. | Review-thread finding. | Normalize/reject nil contexts. |
| L-126 | `infra/storage/storagetest` | OPEN | No arbitrary string-prefix list contract test. | Review-thread finding. | Add cross-backend prefix tests. |
| L-127 | `infra/messaging` | OPEN | `BufferedPublisher.load` silently drops invalid persisted state by default. | Line-by-line review finding. | Fail closed or expose recovery callback/metric. |
| L-128 | `infra/messaging` | OPEN | `BufferedPublisher.finalDrain` timeout is cooperative only. | Review-thread finding. | Document and add watchdog metrics. |
| L-129 | `infra/messaging/membroker` | DOC-OPEN | `Subscribe("*", "*")` docs false for multi-segment routing keys. | Review-thread finding. | Fix wildcard docs/tests. |
| L-130 | `infra/messaging` | DOC-OPEN | Base docs reference nonexistent `NewPrometheusMetrics`. | Review-thread finding. | Update docs. |
| L-131 | `infra/messaging/amqpbackend` | OPEN | Config validation accepts URL/fields that `Connect` later rejects. | Line-by-line review finding. | Share validation with connect parser. |
| L-132 | `infra/messaging/amqpbackend` | OPEN | Connection can be exposed/marked up before `onReconnect` succeeds. | Review-thread finding. | Treat hook failure as reconnect failure or expose degraded state. |
| L-133 | `infra/messaging/amqpbackend` | OPEN | URL provider timeout is cooperative only. | Review-thread finding. | Document or run provider in bounded goroutine with result select. |
| L-134 | `infra/messaging/amqpbackend` | OPEN | Inbound header copy is unbounded upfront. | Review-thread finding. | Cap header count/bytes. |
| L-135 | `infra/messaging/amqpbackend` | OPEN | Consumer handler timeout is cooperative despite stronger comments. | Review-thread finding. | Adjust docs or add watchdog. |
| L-136 | `infra/messaging/redisbackend` | DOC-OPEN | Docs overstate retry binding. | Line-by-line review finding. | Update docs. |
| L-137 | `infra/messaging/natsbackend` | OPEN | Credential-provider timeouts are cooperative only. | Line-by-line review finding. | Document or enforce hard timeout wrapper. |
| L-138 | `infra/messaging/natsbackend` | OPEN | `ExtraOptions` can override hardened defaults. | Line-by-line review finding. | Apply hardened options last or mark `ExtraOptions` trusted. |
| L-139 | `infra/messaging/natsbackend` | OPEN | Delivery header maps preallocate untrusted header count. | Review-thread finding. | Cap headers. |
| L-140 | `infra/messaging/natsbackend` | DOC-OPEN | Subject encoding comments duplicate/contradict. | Review-thread finding. | Clean docs/comments. |
| L-141 | `infra/leaderelection` | OPEN | `WithCallbackDrainTimeout` does not force `Run` to return; it retries while orphan callback may still run. | Line-by-line review finding. | Decide strict stop semantics; add callback watchdog tests. |
| L-142 | `infra/leaderelection/pgadvisory` | OPEN | `Run(nil, ...)` can poison one-shot `started` state. | Review-thread finding. | Validate context before state mutation. |
| L-143 | `infra/leaderelection` | REJECTED | Kubernetes/etcd leader election support. | User questioned whether a library should handle platform leader election. Current kit supports pg advisory and Redis lock. | Do not add for v2.0.0 unless a service requirement appears. |
| L-144 | `infra/messaging/kafka` | REJECTED | Kafka support. | User explicitly stated Kafka is unsupported, so Kafka-related gaps are not blockers. | No action. |

## Runtime, observability, tools, benchmarks, and operations

| ID | Area | Status | Finding | Evidence / note | Action |
|---|---|---:|---|---|---|
| L-145 | `runtime/eventbus` | FIXED-CURRENT | Shutdown-window publish could return success instead of stopped. | Current pool returns/surfaces `ErrStopped`. | Keep shutdown tests. |
| L-146 | `runtime/eventbus` | OPEN | `Publish(nil)` can panic on saturated `OnFullBlock` path. | Line-by-line review finding. | Reject nil event before queue path. |
| L-147 | `runtime/lifecycle` | OPEN | Second-signal force cancellation does not interrupt salvage-budget stops. | Review-thread finding. | Tighten stop context model/docs. |
| L-148 | `runtime/lifecycle` | DOC-OPEN | Stop timeout docs overstate total shutdown bound. | Line-by-line review finding. | Update docs. |
| L-149 | `runtime/batchworker` | DOC-OPEN | Timeout/Stop are cooperative but docs imply stronger cancellation. | Review-thread finding. | Clarify docs/metrics. |
| L-150 | `runtime/cron` | DOC-OPEN | Job timeout/default timeout are cooperative; docs overpromise. | Review-thread finding. | Clarify docs/metrics. |
| L-151 | `runtime/temporal` | OPEN | `Connect` hides dial cause. | Line-by-line review finding. | Wrap cause while preserving redaction. |
| L-152 | `runtime/temporal` | OPEN | `Worker.Start` can return nil on ctx cancellation without waiting for worker error. | Review-thread finding. | Return joined/cause-aware error. |
| L-153 | `runtime/temporal` | OPEN | `Worker.Stop(ctx)` ignores `ctx`. | Line-by-line review finding. | Honor ctx or document SDK limitation. |
| L-154 | `runtime/temporal` | NEEDS-VERIFY | `github.com/nexus-rpc/sdk-go` appears only as an indirect Temporal SDK dependency. | `runtime/temporal/go.mod` does not require Nexus directly; `go.sum` contains it. | Decide whether Temporal module is acceptable despite indirect Nexus; no direct kit API found. |
| L-155 | `observability/slo` | OPEN | Config validation is thin and gather errors are discarded. | Line-by-line review finding. | Validate thresholds and surface gather errors. |
| L-156 | `observability/tracing` | OPEN | `EnableBaggage` can be ignored in noop/fallback paths; init docs overpromise collector reachability checks. | Line-by-line review finding. | Align behavior/docs and tests. |
| L-157 | `observability/promutil` | DOC-OPEN | Register docs stale around `ok` behavior. | Review-thread finding. | Update docs. |
| L-158 | `observability/promutil/labelguard` | OPEN | `WithRegisterer(nil)` behavior inconsistent with other packages. | Review-thread finding. | Normalize nil registerer convention. |
| L-159 | `observability/auditlog` | OPEN | Signed cursor decode lacks direct input length cap. | Review-thread finding. | Add token length cap. |
| L-160 | `observability/health` | OPEN | Dependency timeout is cooperative. | Review-thread finding. | Clarify docs and metric timeout/degraded behavior. |
| L-161 | `observability/dashboards` | OPEN | Dashboards/rules assume namespace/service labels that many collectors do not emit themselves. | Line-by-line review finding. | Add descriptor-level tests or explicit relabel/wrapper docs. |
| L-162 | `observability/dashboards` | OPEN | Recording rules group by labels that may be omitted; dashboard variables can be empty. | Review-thread finding. | Add dashboard query tests with fixture metrics. |
| L-163 | `observability/dashboards` | DOC-OPEN | DB pool dashboard README/text references stale GORM/MySQL or `NewPoolMetrics`. | Review-thread finding. | Update dashboard docs. |
| L-164 | `tools/check-dashboard-metrics.sh` | OPEN | Dashboard metric gate checks source-string names, not Prometheus descriptors, labels, help, or registerer behavior. | Live gate passes but is weaker than stable contract claim. | Add descriptor-level metric contract tests. |
| L-165 | `cmd/kit-bench-gate` | OPEN | `-count=N` benchmark samples collapse to the last sample. | Parser/index overwrites duplicate benchmark names. | Aggregate samples statistically; add duplicate-name tests. |
| L-166 | `docs/release/benchmarks` | OPEN | Baseline coverage is narrow and current baselines are historical/preliminary. | Manifest marks preliminary; earlier review noted only small subset covered. | Regenerate clean canonical baselines after blockers fixed; expand module coverage. |
| L-167 | benchmarks | OPEN | New benchmark functions for packages without obvious hot paths remain incomplete. | User requested top-tier benchmark baselines; final pass did not prove broad benchmark coverage. | Add representative benchmarks for adapters, signing/PASETO, Redis, storage, messaging, outbox. |
| L-168 | dashboards | NEEDS-VERIFY | Provider-specific production dashboards exist for several providers, but need live-contract validation. | Grafana JSON/promtool pass; descriptor/label validation not done. | Add dashboard fixture tests and runbooks per provider. |
| L-169 | `tools/check-publishable.sh` | OPEN | Does not enforce `toolchain` directives despite supply-chain policy requiring them. | Supply-chain policy requires `toolchain`; modules lack it. | Either remove policy or enforce/add directives. |
| L-170 | `tools/rehearse-v2-release.sh` | OPEN | Rehearsal copies working tree and `git add .`, so untracked artifacts can enter evidence. | Review-thread finding. | Require clean tree or copy tracked files only. |
| L-171 | `tools/capture-benchmark-baselines.sh` | OPEN | Dirty tree is recorded but not rejected for canonical baselines. | Review-thread finding. | Fail dirty by default; add explicit allow-dirty flag. |
| L-172 | `.github/CODEOWNERS` | FIXED-CURRENT | Concern that `@bds421/security` might be decorative. | `make check-release-team` passed and verified team/protection. | Keep release gate. |
| L-173 | `.github/workflows/vuln.yml` / `Makefile` | FIXED-CURRENT | `govulncheck` version drift. | Current Makefile and workflow both pin v1.1.4. | No action. |
| L-174 | `SECURITY.md` | FIXED-CURRENT | Missing root security policy. | Root `SECURITY.md` exists and points to GHSA private advisories. | No action. |
| L-175 | worktree hygiene | OPEN | Vim swap file exists for the previous review artifact. | `docs/audit/.v2-line-by-line-review-2026-05-14.md.swp` is untracked. | Remove only after confirming no editor session needs recovery. |

## Documentation, release artifacts, and stale audit files

| ID | Area | Status | Finding | Evidence / note | Action |
|---|---|---:|---|---|---|
| L-176 | `README.md`, root golden path | DOC-OPEN | Examples pass `rediss://...` as `go-redis Options.Addr`, but go-redis expects `host:port` unless URL parser path is used. | Current README lines were inspected. | Use `infra/redis.ParseURL`/fields path or host:port. |
| L-177 | `README.md`, `docs/ai/redis.md` | DOC-OPEN | Docs use removed `rediscache.NewRedisCache`; current constructor is `NewCache`. | Verified by `rg`; code has `NewCache`. | Update examples. |
| L-178 | `AGENTS.md` | DOC-OPEN | Decision tree points to nonexistent `redis/queue.DepthCheck`. | `git ls-files` has no `redis/queue`. | Remove or replace with current health check API. |
| L-179 | `AGENTS.md` | DOC-OPEN | Mentions Builder-created RabbitMQ/NATS message-size methods that no longer exist. | Root builder uses adapter modules. | Update convention text. |
| L-180 | `docs/ai/*` | DOC-OPEN | Multiple recipe files still show removed Builder APIs (`WithPostgres`, `WithRedis`, `WithRabbitMQ`, `WithNATS`, `WithTracing`, `WithMaxMessageBytes`, `WithRouteMaxMessageBytes`, `app.WithRabbitMQURLProvider`). | Verified by `rg`. | Replace with `Builder.With(<adapter>.Module(...))`. |
| L-181 | `docs/ai/*`, release notes | DOC-OPEN | Old `WithMultiTenant(extractor, true)` signature remains in docs. | Current API is `WithMultiTenant(extractor)` / `WithMultiTenantOptional`. | Update snippets. |
| L-182 | `docs/ai/http.md` | DOC-OPEN | Outbound budget example wraps `http.DefaultTransport`, conflicting with anti-pattern. | Verified by `rg`. | Use `httpx.NewHTTPClient` or cloned safe transport. |
| L-183 | `docs/ai/utilities.md`, `core/apperror/doc.go` | DOC-OPEN | Error code docs omit `CodeStorageFull` and stale count wording remains. | Review-thread finding. | Update code list and count. |
| L-184 | `docs/ai/utilities.md` | DOC-OPEN | `httpx.WriteJSON(w, 200, result)` signature is stale. | Current API requires request argument. | Update snippet. |
| L-185 | `docs/ai/storage.md` | DOC-OPEN | Docs claim wrappers forward optional interfaces, but retry/circuitbreaker hide optional capabilities. | Review-thread finding. | Update docs or fix wrappers. |
| L-186 | `docs/ai/storage.md` | DOC-OPEN | Docs say `storage.Migrate` caps/returns errors consistently, but implementation can return nil on object failures. | Covered by L-099. | Update after fix. |
| L-187 | `docs/ai/security.md` | FIXED-CURRENT | Earlier signing example had wrong signature/return values. | Current docs show typed `Secret`/current signature. | No action. |
| L-188 | `docs/ai/credential-rotation.md` | OPEN | Rotation matrix overstates uniform support; static/env paths for S3/Azure/SFTP and some app surfaces remain weaker. | Review-thread finding. | Rewrite as capability matrix with static/provider/reload columns. |
| L-189 | `docs/ai/runbooks/*` | DOC-OPEN | Some runbooks use generic metric names or stale DB wording (`lib/pq`, old pool docs). | Review-thread finding. | Align with actual dashboards/collectors. |
| L-190 | `docs/audit/SUPPLY_CHAIN.md` | DOC-OPEN | Requires `toolchain go1.26.2` in every module, but modules do not have toolchain directives. | Verified. | Either add directives or revise policy. |
| L-191 | `docs/audit/SUPPLY_CHAIN.md` | DOC-OPEN | Still calls the kit proprietary while root license is Apache-2.0. | Verified lines 642/677 vs `LICENSE.md`. | Update supply-chain license section. |
| L-192 | `docs/audit/SUPPLY_CHAIN.md` | DOC-OPEN | References Dependabot config as landed/next, but no `.github/dependabot.yml` was found. | `git ls-files` inventory. | Add config or remove claim. |
| L-193 | `docs/audit/SUPPLY_CHAIN.md` | DOC-OPEN | Contains stale v1 tag/path examples. | Review-thread finding. | Refresh examples. |
| L-194 | `docs/audit/THREAT_MODEL.md` | DOC-OPEN | Still references removed Builder APIs and old `infra/outbox/gormstore`. | Verified by `rg`. | Update threat model examples/control table. |
| L-195 | `docs/audit/README.md`, dated audit files | DOC-OPEN | Audit README says completed audit reports are not kept, but stale dated review artifacts remain. | Verified directory contents. | Remove stale artifacts or change retention policy. |
| L-196 | `docs/release/API_FREEZE_V2.md` | DOC-OPEN | Freezes removed Builder names (`WithNATS`, `WithPostgres`, `WithRedis`). | Verified. | Freeze adapter module APIs instead. |
| L-197 | `docs/release/FINAL_RELEASE_RUNBOOK_V2.md` | DOC-OPEN | Contains stale 73-module/tag count and misses `make check-api-freeze-coverage` in RC gates. | Review-thread finding. | Update counts/gates. |
| L-198 | `docs/release/MIGRATION_V2.md` | DOC-OPEN | Early golden-path snippet still uses removed Builder APIs although later mapping is closer to current API. | Verified. | Make all snippets consistent. |
| L-199 | `docs/release/MIGRATION_V2.md` | DOC-OPEN | Lazy-adapter section contradicts itself: v2.0 shipped vs v2.1 planned. | Review-thread finding. | Remove stale v2.1 wording. |
| L-200 | `docs/release/MIGRATION_V2.md` | NEEDS-VERIFY | `cmd/kit-migrate` checklist may omit newer migration surfaces (`auditlog/postgres`, `infra/outbox/postgres`). | Review-thread finding. | Compare migrator commands against module migrations. |
| L-201 | `docs/release/RC_CHECKLIST_V2.md` | DOC-OPEN | Mixed stale historical 73-module evidence with current 77-module state. | Review-thread finding. | Refresh or clearly mark historical rows. |
| L-202 | `docs/release/TAGGING_PLAN_V2.md` | DOC-OPEN | Expected boundary edge count was stale in prose relative to live 413 edges. | Live `make check-dependency-boundaries` reports 413. | Update plan. |
| L-203 | `docs/RELEASE_NOTES_v2.md` | DOC-OPEN | Contradicts itself on lazy-adapter architecture. | Verified. | Keep only current adapter-module story. |
| L-204 | `docs/RELEASE_NOTES_v2.md` | DOC-OPEN | Documents removed Builder methods and old middleware order. | Verified by `rg`. | Rewrite Builder/middleware sections. |
| L-205 | `docs/RELEASE_NOTES_v2.md` | DOC-OPEN | Says "No code changes are required" for v1 upgrades, contradicting v2 import/API migration. | Verified. | Replace with accurate migration guidance. |
| L-206 | `docs/RELEASE_NOTES_v2.md` | DOC-OPEN | Says no open in-kit threat gaps while threat model still lists open low follow-ups. | Review-thread finding. | Align release notes and threat model. |
| L-207 | `docs/RELEASE_NOTES_v2.md` | DOC-OPEN | Includes internal process details ("parallel agents", commit counts) not relevant to package consumers. | Review-thread finding. | Remove from public release notes. |
| L-208 | `cmd/kit-verify/go.mod` | DOC-OPEN | Package comment claims HSTS verification, but `kit-verify` does not probe HSTS. | Verified. | Add HSTS probe or remove claim. |
| L-209 | `cmd/kit-migrate/CHANGES.md`, package `CHANGES.md` files | DOC-OPEN | Many stale v1-era changelogs mention removed GORM modules and obsolete APIs. | Review-thread finding. | Remove stale package changelogs or regenerate current ones. |

## Explicitly closed, rejected, or not-current items from earlier reviews

| ID | Area | Status | Finding / decision | Evidence / note | Action |
|---|---|---:|---|---|---|
| L-210 | storage metrics | FIXED-CURRENT | Azure/GCS/S3 not-found operations used to increment error metrics. | Current review marked fixed. | Keep not-found metric tests. |
| L-211 | retry | FIXED-CURRENT | Retry used to swallow function error on simultaneous context cancellation. | Current review marked fixed. | Keep joined/preferred error test. |
| L-212 | clamav | FIXED-CURRENT | `removeOnEOF` race. | Current code has synchronized cleanup. | Keep race test. |
| L-213 | eventbus | FIXED-CURRENT | Closed pool publish could hide stopped state. | Current code returns `ErrStopped`. | Keep test. |
| L-214 | redisqueue | FIXED-CURRENT | Permanent heartbeat failure left processing running. | Current code cancels process context. | Keep test. |
| L-215 | license root | FIXED-CURRENT | Root `LICENSE.md` used to be unfilled Apache template / AGENTS proprietary contradiction. | Current root license is filled Apache-2.0; stale proprietary wording remains only in docs/audit. | Fix L-191. |
| L-216 | `CODEOWNERS` | FIXED-CURRENT | Concern team may not exist. | `make check-release-team` passed. | No action. |
| L-217 | Kubernetes/etcd leader election | REJECTED | Do not add just because it was mentioned. | Platform-native leader election is usually handled by Kubernetes/controller tooling; kit already has pg/redis options. | Revisit only with concrete consumer need. |
| L-218 | Kafka | REJECTED | Kafka support gaps are not blockers. | User explicitly said Kafka is unsupported. | No action for v2.0.0. |
| L-219 | `httpx/reqsign` | NEEDS-VERIFY | Deprecated package ships in v2; either remove or undeprecate. | Claude medium item; not fully rechecked in final pass. | Decide before API freeze. |
| L-220 | internal package surface | NEEDS-VERIFY | Only a few `internal/` dirs means helpers become public freeze surface. | Claude medium item; broad refactor risk. | Review public helpers before tag; avoid churn unless harmful surface found. |

## Verification still required after fixes

- Run full `make test-race`.
- Run full Docker-backed `make test-integration` after L-002/L-003 are fixed.
- Re-run clean canonical benchmark baseline capture from a clean tree.
- Add descriptor-level Prometheus contract tests and dashboard fixture tests.
- Run targeted fuzzers for cursors, signed requests, redirects, pagination,
  URL handling, and config loaders beyond the smoke fuzz files.
- Run live/provider integration checks for AWS KMS, GCP KMS, Azure Key Vault,
  HashiCorp Vault Transit, and object storage only if credentials/environments
  are available.
- Run `tools/rehearse-v2-release.sh` after stale docs and release blockers are
  fixed.

