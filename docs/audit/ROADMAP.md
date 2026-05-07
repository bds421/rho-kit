# Execution roadmap — current state

The original 6–10 week plan compressed into Phases 0–3 of the existing-package work. **All Phase 1 + nearly all Phase 2 has landed** (Wave 1+2+3+4+5 commits, see [CRITICAL.md](CRITICAL.md) for the per-finding ledger). What's left in the existing-package surface is documented inline below.

The new-package work (Phases 4–6) is **landed**; see per-phase status below. The v2.0.0 agentic-AI push (Phases 7–9) is also landed — tenant wrappers, cost budgets, action audit + approval, MCP helpers, trust signals (SBOM + vuln scans + threat model + supply-chain policy), and the dashboard expansion. Remaining items are the genuine SDK-bound spikes (cloud-KMS subpackages, k8slease/etcd leader-election backends, Kafka backend) plus the GAP-01..10 follow-ups identified by the threat model.

## v2.0.0 themes — ✅ landed

This wave was orchestrated as 5 parallel agents producing 7 themes:

- ✅ **Theme 1 — tenant-aware everything**: `data/cache/tenant`, `data/idempotency/tenant`, `httpx/middleware/ratelimit/tenant`, `observability/promutil/labelguard`. Builder integration via `WithMultiTenant(extractor, required)`.
- ✅ **Theme 2 — per-tenant cost budgets**: `data/budget` (interface + `Refunder` capability), `data/budget/memory`, `data/budget/redis` (atomic Lua), `httpx/middleware/budget` (inbound), `httpx/budget` (outbound `RoundTripper` with reconciliation). Builder integration via `WithTenantBudget(b, opts...)`.
- ✅ **Theme 3 — agent action audit + approval**: `data/actionlog` (HMAC-signed entries with rotation via `SignatureKeyID`), `data/actionlog/{memory,postgres}`, `data/approval` (pending → approved/rejected → executed), `data/approval/{memory,postgres}`, `httpx/middleware/approval`. Builder integration via `WithActionLogger(l)` + `WithApprovalStore(s)`.
- ✅ **Theme 4 — MCP helpers**: `httpx/mcp` exposes typed handlers as MCP tools over JSON-RPC. Schema generation from struct tags (`json` + `validate:"required"` + `desc:"..."`). Reuses the kit's middleware stack (auth, tenant, rate limit, budget, approval, action log). `cmd/kit-new --mcp` flag scaffolds a sample tool registration.
- ✅ **Theme 5 — trust signals**: SBOM (CycloneDX via Anchore) on tag push; `govulncheck` + `osv-scanner` on PR/push/weekly; `docs/audit/THREAT_MODEL.md` (827 lines, identifies 10 GAP-01..10 follow-ups); `docs/audit/SUPPLY_CHAIN.md` (609 lines, pinning + signing + vuln SLO).
- ✅ **Theme 6 — Builder integrations** (Phase A from the prior session): `WithPASETO`, `WithNATS`, `WithPgx` + mutex check, `WithLeaderElection` + cron leader gate, `WithSignedRequests`, `WriteServiceProblem`. Plus Wave 2 above.
- ✅ **Theme 7 — dashboards expansion + runbooks**: gRPC RED, DB pool, Redis, Outbox, Storage Grafana dashboards; per-area recording rules; saturation + messaging alerts; 7 runbooks under `docs/ai/runbooks/`; `promtool` validation in CI.

### v2.0.0 design choices worth knowing

- **Idempotency tenant wrapper namespaces the storage key**, not the body fingerprint — backend-layer isolation holds even if the backend bug ignores fingerprints, and a fresh request from tenant B never falsely 422s on tenant A's body.
- **Budget windows are fixed, not sliding** — LLM-cost reporting maps directly to vendor invoice lines; for adversarial smoothing, callers use `data/ratelimit/gcra`.
- **Action-log entries are HMAC-signed with rotation** — `SignatureKeyID` rides on every entry so old entries verify after rotation; `Sign`/`Verify` exposed for off-band tools.
- **Approval state machine refuses flip transitions** — approved→rejected (or vice-versa) needs a fresh request so the audit trail records the reconsideration.
- **MCP server doesn't implement JSON-RPC batch** — single-call semantics keep the action-log entry per-call rather than per-batch (forensics is cleaner).
- **MCP unauthenticated callers see "method not found", not "forbidden"** — deliberately to avoid revealing the tool catalog to the unauthenticated.
- **Builder methods refuse nil** for budget/actionlog/approval stores — silent no-op would defeat the kit's "refuse to misconfigure" stance.

## Phase 0 — Unblock — ✅ done

- ✅ gRPC bumped to v1.79.3 across all modules (`56bf04e`).
- ✅ `make lint` switched to sequential to dodge golangci-lint v2 cache lock collisions (`56bf04e`).
- ✅ Go runtime → 1.26.2 — `go.work` + 55 module `go.mod` files bumped with `toolchain` directive; `GOTOOLCHAIN=auto` fetches automatically (`5df122f`).

## Phase 1 — Stop the bleeding — ✅ done

Done (commits in parens):

- ✅ AMQP publisher mandatory + NotifyReturn (`068eeb5`).
- ✅ Local storage parent-dir fsync (`1622196`).
- ✅ Postgres sslmode safer defaults + Validate rejection (`a8fa6ed`).
- ✅ Outbox `next_retry_at` + exponential backoff + DeleteFailedBefore (`4b522b3`).
- ✅ debughttp Guard middleware (`068eeb5`).
- ✅ resilience/retry default RetryIf + Loop nil-error return (`270c901`).
- ✅ Tracing default sample rate 0.05 + Baggage opt-in (`1198dd5`).
- ✅ Cron + batchworker histogram buckets (`6a76329`).
- ✅ DecodeJSON strict trailing-data rejection (`36cf34b`).
- ✅ Idempotency `WithTTL` reject non-positive (`36cf34b`).
- ✅ ComputeCache WaitGroup race fixed (`36cf34b`).
- ✅ MemoryCache default 64 MiB cap (`36cf34b`).
- ✅ http.Server.ErrorLog defaults to slog adapter; client MaxIdleConnsPerHost raised (`36cf34b`).
- ✅ `clientip` default to loopback only; `ParseTrustedProxiesStrict` (`ab4df5c`).
- ✅ CSRF require shared secret in non-dev; SkipCheck regen bug fix (`7f0efe3`).
- ✅ CSRF rejects SameSite=None without Secure (`3784af8`).
- ✅ Timeout middleware buffer cap default 1 MiB + `WithMaxBufferSize` (`30113f9`).
- ✅ secheaders honour X-Forwarded-Proto + `WithForceHSTS` (`b324d2e`).
- ✅ Include `Timeout` in `stack.Default` (`a0b49e8`).
- ✅ gormmysql TLS registry refcount + `ReleaseTLS` (`af39f9c`).
- ✅ `stack.Default` panic-recovery middleware (`e96ffdf`). Closes original CRITICAL #2.
- ✅ `grpcx.NewServer` panic-recovery interceptors by default (`e96ffdf`). Closes original CRITICAL #3.

## Phase 2 — Tighten the contracts — mostly done

Done (commits in parens):

- ✅ data/lock interface refit + ErrLockLost (`2408d15`).
- ✅ Idempotency Store reshape + pgstore owner_token migration (`1f06b5e`).
- ✅ Idempotency middleware body-fingerprint plumbing (`1f06b5e`).
- ✅ Redis queue per-consumer processing list + ID-keyed remove (`f4a0a95`).
- ✅ FieldEncryptor prefix-shortcut removal + AAD support (`99917ac`).
- ✅ JWT WithExpectedAudience (`c502dd2`).
- ✅ SSRF safe-redirect + TLS 1.3 default (`b6a4a9a`, `c502dd2`).
- ✅ Auth middleware VerifiedChains check (`c502dd2`).
- ✅ atomicfile mode preservation + EXDEV fallback (`c502dd2`).
- ✅ Auditlog cursor signing + LIKE escape + composite cursor (`98f05e4`, `1198dd5`).
- ✅ CSRF Origin allowlist (`409cdbb`).
- ✅ Lifecycle.Runner signal-goroutine leak + joined start+stop errors (`6a76329`).

Still open:

- ✅ [existing/00] Nil-dependency validation sweep across constructors. (`6ba1e7d`)
- ✅ [existing/08] ComputeCache zero-TTL contract decision. (`6ba1e7d`)
- ✅ [existing/08] Idempotency backends reject non-positive TTL with `ErrInvalidTTL`. (`a01fad7`)
- ✅ [existing/03] SSRF `*FromURL` constructors. (`a649495`)
- ✅ [existing/03] JWT mandatory expected issuer in non-dev. (`659babb`)
- ✅ [existing/05] Idempotency identity-header strip + mandatory user extractor. (`83da31b`)
- ✅ [existing/05] CSRF SameSite=None+Secure validation. (`3784af8`)
- ✅ [existing/07] redislock Acquire surfaces transient-SET orphans (probe via GET). (`432f001`)
- ✅ [existing/11] Outbox `Writer.WithRequireTransaction()`. (`5cfa5c9`, default off)
- 🔴 [existing/03] crypto/signing `New*E` / `Must*` split; `WithFutureSkew`.
- 🔴 [existing/03] JWT staleness metric + max-stale rejection.
- 🔴 [existing/05] Timeout middleware hard-timeout mode.
- 🔴 [existing/05] CSRF session-bound HMAC (depends on [new/06]).
- 🔴 [existing/07] Bounded recoverProcessing interleaved with new-message reads.
- 🔴 [existing/09] Per-stream consumer ID (or panic on multi-stream Consume).
- 🔴 [existing/10] AMQP consumer ctx semantics on shutdown (`WithoutCancel`).
- 🔴 [existing/10] Dead-letter publish failure cap.
- 🔴 [existing/10] BufferedPublisher state-file mandatory in prod; surface persistence errors; restrictive umask.
- 🔴 [existing/12] S3 SSE defaults + presigned PUT enforcement; storagehttp MaxMemory + UUIDKeyFunc fallback; encryption Put concurrency cap.
- ✅ [new/19] Production-safe defaults — superseded by the unconditional Builder validator (see Phase 6 entry below).

## Phase 3 — Polish — small items, mostly Phase 3 quality

All Phase 3 polish items across `existing/02`–`existing/17` have landed. Each per-package audit file now includes a "Recently Landed (Phase 3)" section recording what shipped and a closed migration checklist. Highlights:

- ✅ [existing/02] core/config: `GetSecret(string, error)` split; `EnvReloader.WithImmediateLoad`; required-env rejects explicit-empty; `NewSecureID`.
- ✅ [existing/03] crypto+security: JWKS staleness budget; static keystore error API; signing future-skew option.
- ✅ [existing/04] httpx server+client: signed cursors; `IdleConnTimeout`; `httpxtest.DoRealServer`.
- ✅ [existing/05] httpx middleware: timeout `WithHard`; logging client-IP resolver; tracing hijack 101.
- ✅ [existing/07] data lock+queue: bounded `recoverProcessing` interleaved with BLMove.
- ✅ [existing/08] data cache+idem: `BulkCache` (MGet/MSet/SetNX); compute cache surfaces backend errors; bounded sweeper.
- ✅ [existing/10] infra messaging: `WithoutCancel` shutdown ctx; DLQ failure cap; `Connection.WaitForConnection`; membroker `Unsubscribe`.
- ✅ [existing/11] infra outbox: `WithMaxConcurrentPublishes` worker pool; SQLite multi-relay process-local guard.
- ✅ [existing/12] infra storage: SSE defaults; safer key extension; encryption concurrency cap; sftp generation cleanup; URL templating.
- ✅ [existing/13] infra sqldb+redis: `IsTLSEnabled` covers Postgres/MySQL; `ConnectUniversal` for Sentinel/Cluster; redistest `FlushDB`.
- ✅ [existing/14] runtime: cron per-job timeout + ctx sync; eventbus `Unsubscribe` + `OnFull` policy; FanOut default cap.
- ✅ [existing/15] resilience: `CircuitBreaker.ExecuteCtx`; nil-receiver semantics documented.
- ✅ [existing/16] observability: Health Liveness/Readiness handlers; logattr Secret/Email; tracing Init timeout + fallback; auditlog memory IPAddress; promutil `Register` API; SLO `LatencyLabelFilter`.
- ✅ [existing/17] io: progressReader concurrency doc + `WithThrottle` / `WithMinDelta`.

## Phase 4 — Tier‑1 missing primitives — ✅ done

- ✅ [new/03] `crypto/passhash` — argon2id with verify-then-rehash; PHC string format; `Verify` returns `(matched, needsRehash, err)` so callers can transparently upgrade params on next login (`8089439`).
- ✅ [new/04] `crypto/envelope` — DEK/KEK split with self-describing blob (magic+ver+keyID+wrappedDEK+nonce+ct), AAD bound via SHA-256 of header, online `Rewrap` for rotation; ships `kekstatic` for tests/dev (`8089439`).
- ✅ [new/05] `crypto/paseto` — V4Public (Ed25519) + V4Local (XChaCha20-Poly1305); mandatory expected-issuer/audience or explicit `WithAllowAnyIssuer` opt-out; clock-skew tolerance applied in our `validate()` (bypassing the library's default `NotExpired` rule) (`8089439`).
- ✅ [new/06] `security/csrf` — `Issuer.Issue/Verify` with `prefix(8) || iat(8) || nonce(16) || hmac(32)` length-prefixing the sessionID; `OriginAllowlist` for Origin/Referer checks (`ca3f5aa`). Existing httpx CSRF middleware refit to use this primitive remains TODO under [existing/05].
- ✅ [new/07] `core/secret` — `String` type with explicit `Reveal()`/`RevealString()`; `String()`, `GoString()`, `MarshalJSON`, `MarshalText`, `LogValue`, `Format` all emit `<redacted>` (`f3b7611`).
- ✅ [new/08] `httpx/middleware/cspnonce` — per-request CSP nonce via `crypto/rand` injected into `script-src` and `style-src`; `FromContext` accessor + `HTMLAttr` template helper (`06386f1`).

## Phase 5 — Tier‑2 infrastructure — ✅ done (with explicit deferrals)

Done:

- ✅ [new/09] `data/lock/pgadvisory` — `Locker.Acquire` + `AcquireTx`; FNV-1a hash maps string key to int64; honours data/lock interface (`7253ecb`).
- ✅ [new/10] `data/ratelimit` — `Limiter` interface plus `tokenbucket`, `gcra`, and `redis` (cross-instance, atomic Lua) implementations.
- ✅ [new/11] `infra/leaderelection` — `Elector` interface with `Run(ctx, Callbacks)` and `IsLeader()`; `pgadvisory` and `redislock` backends ship.
- ✅ [new/12] `infra/messaging/natsbackend` — JetStream Publisher/Consumer with explicit ack, durable consumers, redeliver-on-nack, Term-on-malformed.
- ✅ [new/14] `infra/sqldb/pgx` — pgx-native pool with LISTEN/NOTIFY + COPY + TLS-required-in-prod sslmode enforcement.
- ✅ [new/20] Multi-tenant primitives — `core/tenant` (type-distinct `ID`, `WithID/FromContext/Required`) + `httpx/middleware/tenant` (default header extractor, `WithRequired`, safe-method passthrough).
- ✅ [new/24] `httpx/middleware/signedrequest` + `httpx/sign` — HMAC-SHA256 with timestamp+nonce+body-hash binding (`35aad31`).
- ✅ [new/25] `storagehttp/uploadsec` — `Validator` chain with MIME sniffing, extension cross-check, image-decode-config bomb defence (`35aad31`).
- ✅ [new/05] PASETO `Provider` with periodic refresh — atomic key swap, previous-set-on-failure semantics, `WithOnRefreshError` callback for telemetry.

Deferred (genuinely out of scope, require separate effort):

- 🔴 [new/04] Cloud-KMS subpackages (`kekaws`, `kekgcp`, `kekvault`) — only `kekstatic` ships; cloud variants need provider SDKs.
- 🔴 [new/11] `k8slease`, `etcd` leader-election backends — need k8s.io / etcd SDKs.
- 🔴 [new/13] `infra/messaging/kafkabackend` — Kafka backend skipped per "don't do kafka" directive.
- 🔴 [new/20] cache + idempotency tenant wrappers, per-tenant rate-limit middleware, `promutil/labelguard`, Builder `WithMultiTenant` — primitives ship; integrations are follow-up audit items.

## Phase 6 — Agent-readiness — ✅ done (with explicit deferrals)

Done:

- ✅ [new/15] `observability/pprof` + `observability/runtimemetrics` — `Mount(mux)` for net/http/pprof; curated Prometheus collector (`35aad31`).
- ✅ [new/16] `observability/redmetrics` — `HTTPMetrics` (Requests/Errors/Duration/InFlight) + `BatchMetrics` (`06386f1`).
- ✅ [new/17] `httpx/problemdetails` — RFC 7807 writer with `Extensions` inlined via custom MarshalJSON (`06386f1`).
- ✅ [new/18] `cmd/kit-doctor` — CLI with rule scaffold + 4 initial rules (jwt-missing-claims, idempotency-user-extractor, default-http-client, http-server-error-log); `-strict` floor + JSON output.
- ✅ [new/19] Production-safe defaults — JWT issuer/audience pin, Postgres TLS-required, tracing SampleRate capped, internal-host loopback. Originally landed as `app.WithProductionDefaults()` (`35aad31`, `4d04fe1`); subsequently made unconditional by removing development mode (`c113451`). Per-relaxation `Without*()` opt-outs (`WithoutTLS`, `WithInternalNonLoopback`, `WithoutJWTIssuer`, `WithoutJWTAudience`) replace the meta switch.
- ✅ [new/21] `cmd/kit-new` — scaffold generator with embedded templates; generated tree builds + vets clean (verified by self-test in CI).
- ✅ [new/22] `observability/dashboards` — HTTP RED + Go runtime + service overview Grafana JSON; recording rules + availability/latency alerts; SLO multi-burn-rate templates.
- ✅ [new/23] `cmd/kit-bench-gate` — `go test -bench` text parser + diff/regression engine; GOMAXPROCS-suffix stripping for cross-runner stability; `-fail-on` ratchet.

Deferred (large surfaces; ship per-area as the surface stabilises):

- 🔴 [new/22] gRPC, DB, Redis, messaging, storage, outbox, ratelimit dashboards — only HTTP+runtime+overview ship in this wave.
- 🔴 [new/21] `kit-new --modules` / `--tenant` / `--token` flags — base scaffold ships; Builder-aware module wiring follows the corresponding Builder integration items.
- 🔴 [new/23] Per-package benchmarks for the surface listed in the audit — gate ships; benchmarks land per-package as audit identifies hot paths.

## Tracking

Each existing-package file's `## Landed` block lists the commit that closed each finding. The `## Open` block + migration checklist is the live to-do list.
