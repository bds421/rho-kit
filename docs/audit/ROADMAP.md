# Execution roadmap

Six to ten weeks of focused work to close all CRITICAL + HIGH findings and ship Tier‑1 missing primitives. The kit becomes "secure and fast by default" at the end of phase 3.

## Phase 0 — Unblock (days, not weeks)

Two things that gate the rest. Land them first or every later PR carries hidden risk.

- [existing/00] **Bump Go to 1.26.2+** across the workspace and every module; **bump `google.golang.org/grpc` to v1.79.3+**. Re-run `make vulncheck` until green.
- [existing/00] Fix `make lint` (sequential or per-module cache so parallel runners don't collide).

## Phase 1 — Stop the bleeding (1–2 weeks)

Defaults that ship insecure today. All small focused PRs, can land in parallel.

- [existing/05] Add `httpx/middleware/recover` and prepend it in `stack.Default`. → [new/01]
- [existing/06] Prepend Recovery interceptors in `grpcx.NewServer`. → [new/02]
- [existing/10] AMQP publisher: `mandatory=true` + `NotifyReturn` handling.
- [existing/12] Local storage: parent-dir fsync after rename.
- [existing/13] Postgres `sslmode`: default `require` in non-dev; reject empty/`disable` in `Validate`.
- [existing/11] Outbox: add `next_retry_at`, exponential backoff, `DeleteFailedBefore`.
- [existing/10] Gate `debughttp` behind auth + non-prod env check.
- [existing/15] `resilience/retry`: default `RetryIf` to `RetryIfNotPermanent`.
- [existing/14] `retry.Loop`: return on `nil` error (don't restart graceful workers forever).
- [existing/16] Tracing: default sample rate 0.05; Baggage opt-in only.
- [existing/16] Cron + batchworker histogram buckets sized for the workload.
- [existing/04] `httpx.DecodeJSON`: reject trailing top-level JSON via second-decode + EOF.
- [existing/05] `clientip` default to no-trusted-proxies; require explicit CIDRs (closes the IP-spoof + cross-middleware-disagreement findings together).
- [existing/05] CSRF require shared secret in non-dev (no per-process random fallback).
- [existing/08] Idempotency `WithTTL` reject non-positive; backend tests assert agreement.
- [existing/08] `ComputeCache` fix WaitGroup race (mutex around closed-check + Add).
- [existing/08] `MemoryCache` conservative default `MaxCost`; require opt-in for unbounded.

## Phase 2 — Tighten the contracts (2–3 weeks)

Interface drift and ownership-token plumbing. These touch public types so plan them as a coordinated release.

- [new/19] **Ship `app.WithProductionDefaults()`** — bundles every phase-1 hardening into one switch with startup validation. Lands after the individual fixes are in place; becomes the recommended path.
- [existing/00] Nil-dependency validation sweep across constructors that fail-late; document the convention in CLAUDE.md/AGENTS.md.
- [existing/07] Reconcile `data/lock` interface with redislock impl (per-call `Lock` value); fix Release `ErrLockLost` surfacing; fix transient-error orphan window.
- [existing/08] Reshape `data/idempotency.Store`: add owner token (fixes pgstore split-brain) and request fingerprint (fixes body-mismatch).
- [existing/05] Wire request-body fingerprint through idempotency middleware; strip identity headers from the cached response.
- [existing/05] Timeout middleware: hard-timeout mode (or rename existing as `Cooperative` and document the contract).
- [existing/07] Redis list queue: per-consumer processing list + ID-keyed in-flight hash + Lua atomic re-queue on dispatch failure.
- [existing/11] Outbox: `WithRequireTransaction` strict mode (default on for new constructions).
- [existing/03] `crypto/signing`: `New*E`/`Must*` split; `WithFutureSkew`.
- [existing/03] Remove `FieldEncryptor.Encrypt` prefix shortcut; add AAD parameter for row-binding.
- [existing/03] JWT: add `WithExpectedAudience`, `lastSuccessfulFetch` staleness gauge.
- [existing/03] `SSRFSafeTransport`/`SSRFSafeClient`: accept `*url.URL`; safe-redirect mode that re-validates each hop; default TLS 1.3.
- [existing/14] Lifecycle.Runner signal-goroutine leak fix.
- [existing/08] `ComputeCache` zero-TTL contract decision.

## Phase 3 — Observability and DX (1–2 weeks)

- [existing/16] Audit-log gormstore: composite cursor `(timestamp, id)`; LIKE-wildcard escape in Resource filter.
- [existing/16] `observability/health`: ship `Liveness()`/`Readiness(*Checker)` HTTP handlers.
- [existing/04] HTTP server: route `ErrorLog` through slog (no plain stdout); raise `MaxIdleConnsPerHost` default.
- [existing/05] `httpx/middleware/timeout`: lower per-request buffer cap; document pairing with `maxbody`.
- [existing/05] Logging middleware: shared client-IP resolver so log/ratelimit agree.
- [new/15] `/debug/pprof` + go-runtime metrics on the internal :9090 port.
- [new/16] RED-metrics middleware constructor with proper buckets.
- [new/17] RFC 7807 problem-details writer alongside `WriteError`.
- [new/22] **Observability pack** — Grafana dashboards + Prometheus alert templates that consume the metric names emitted by `redmetrics`.

## Phase 4 — Tier‑1 missing primitives (3–4 weeks)

Primitives every Go service needs sooner or later. None of these exist in the kit today.

- [new/03] `crypto/passhash` — argon2id with verify-then-rehash.
- [new/04] `crypto/envelope` — DEK/KEK split, key-version metadata, KMS providers (AWS/GCP/Vault).
- [new/05] `crypto/paseto` — safer JWT alternative for new services.
- [new/06] `security/csrf` — session-bound CSRF tokens (the existing middleware stays as a thin wrapper).
- [new/07] `core/secret` — `SecretString` type that zeroes on Close, refuses to print/marshal.
- [new/08] CSP-nonce middleware (or `httpx/middleware/secheaders` extension).

## Phase 5 — Tier‑2 infrastructure (rolling)

These are larger, mostly independent. Pick based on what the consuming services need.

- [new/09] `data/lock/pgadvisory` — Postgres advisory lock.
- [new/10] `data/ratelimit/slidingwindow` — GCRA / token bucket.
- [new/11] `infra/leaderelection` — k8s-lease / etcd / pg-advisory.
- [new/12] `infra/messaging/natsbackend` — JetStream.
- [new/13] `infra/messaging/kafkabackend` — Kafka.
- [new/14] `infra/sqldb/pgx` — `pgx`-native option for LISTEN/NOTIFY, COPY, pipelines.
- [new/20] **Multi-tenant primitives** — `core/tenant`, tenant-aware cache/idempotency/ratelimit wrappers, label allowlists for cardinality safety.
- [new/24] `httpx/middleware/signedrequest` — webhook/S2S request signing with replay cache.
- [new/25] `storagehttp/uploadsec` — MIME sniffing, AV adapter, image dimension limits, quotas.

## Phase 6 — Agent-readiness (1–2 weeks)

Tooling that makes the kit's "secure by default" promise *visible* to humans and agents.

- [new/18] `cmd/kit-doctor` — scan a service's wiring for dangerous defaults; programmatic version of this audit.
- [new/21] `cmd/kit-new` — scaffold generator (companion to `kit-doctor`).
- [new/23] `cmd/kit-bench-gate` — CI benchmark regression gate.
- AGENTS.md generator from code + `docs/ai/index.json` machine-readable surface (separate task; not a package).
- Per-package `EXAMPLES_test.go` files. Reduces "tests pass, prod doesn't" surface.

## Tracking

Each existing-package file ends with a checklist suitable to copy into a project tracker. Each new-package file ends with a definition-of-done.
