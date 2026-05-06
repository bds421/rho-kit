# Execution roadmap — current state

The original 6–10 week plan compressed into Phases 0–3 of the existing-package work. **All Phase 1 + nearly all Phase 2 has landed** (Wave 1+2+3+4+5 commits, see [CRITICAL.md](CRITICAL.md) for the per-finding ledger). What's left in the existing-package surface is documented inline below.

The new-package work (Phases 4–6) is unstarted; those proposals live in `new/01-25`.

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
- 🔴 [new/19] Ship `app.WithProductionDefaults()` after the per-finding fixes above complete.

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

## Phase 4 — Tier‑1 missing primitives (unstarted)

- [new/03] `crypto/passhash` — argon2id with verify-then-rehash.
- [new/04] `crypto/envelope` — DEK/KEK split, key-version metadata, KMS providers (AWS/GCP/Vault).
- [new/05] `crypto/paseto` — safer JWT alternative for new services.
- [new/06] `security/csrf` — session-bound CSRF tokens (existing middleware stays as a wrapper).
- [new/07] `core/secret` — `SecretString` type that zeroes on Close, refuses to print/marshal.
- [new/08] CSP-nonce middleware.

## Phase 5 — Tier‑2 infrastructure (unstarted)

- [new/09] `data/lock/pgadvisory` — Postgres advisory lock.
- [new/10] `data/ratelimit/slidingwindow` — GCRA / token bucket.
- [new/11] `infra/leaderelection` — k8s-lease / etcd / pg-advisory.
- [new/12] `infra/messaging/natsbackend` — JetStream.
- [new/13] `infra/messaging/kafkabackend` — Kafka.
- [new/14] `infra/sqldb/pgx` — `pgx`-native option for LISTEN/NOTIFY, COPY, pipelines.
- [new/20] Multi-tenant primitives.
- [new/24] `httpx/middleware/signedrequest`.
- [new/25] `storagehttp/uploadsec`.

## Phase 6 — Agent-readiness (unstarted)

- [new/15] `/debug/pprof` + go-runtime metrics on internal port.
- [new/16] RED-metrics middleware with proper buckets.
- [new/17] RFC 7807 problem-details writer.
- [new/18] `cmd/kit-doctor`.
- [new/21] `cmd/kit-new`.
- [new/22] Observability pack (Grafana + alert templates).
- [new/23] `cmd/kit-bench-gate`.

## Tracking

Each existing-package file's `## Landed` block lists the commit that closed each finding. The `## Open` block + migration checklist is the live to-do list.
