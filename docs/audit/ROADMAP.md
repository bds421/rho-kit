# Execution roadmap — current state

Phases 0–6 of the original v1→v2 audit are landed. The v2.0.0
agentic-AI push (Phases 7–9) is also landed — tenant wrappers,
cost budgets, action audit + approval, MCP helpers, trust signals
(SBOM + vuln scans + threat model + supply-chain policy), and the
dashboard expansion. What remains is explicitly-deferred, non-gap work
such as SDK-bound backend spikes and additional benchmarks.

## v2.0.0 themes — landed

This wave was orchestrated as 5 parallel agents producing 7 themes:

- **Theme 1 — tenant-aware everything**: `data/cache/tenant`,
  `data/idempotency/tenant`, `httpx/middleware/ratelimit/tenant`,
  `observability/promutil/labelguard`. Builder integration via
  `WithMultiTenant(extractor, required)`.
- **Theme 2 — per-tenant cost budgets**: `data/budget` (interface +
  `Refunder` capability), `data/budget/memory`, `data/budget/redis`
  (atomic Lua), `httpx/middleware/budget` (inbound), `httpx/budget`
  (outbound `RoundTripper` with reconciliation). Builder integration
  via `WithTenantBudget(b, opts...)`.
- **Theme 3 — agent action audit + approval**: `data/actionlog`
  (HMAC-signed entries with rotation via `SignatureKeyID`),
  `data/actionlog/{memory,postgres}`, `data/approval` (pending →
  approved/rejected → executed), `data/approval/{memory,postgres}`,
  `httpx/middleware/approval`. Builder integration via
  `WithActionLogger(l)` + `WithApprovalStore(s)`.
- **Theme 4 — MCP helpers**: `httpx/mcp` exposes typed handlers as
  MCP tools over JSON-RPC. Schema generation from struct tags
  (`json` + `validate:"required"` + `desc:"..."`). Reuses the kit's
  middleware stack (auth, tenant, rate limit, budget, approval,
  action log). `cmd/kit-new --mcp` flag scaffolds a sample tool
  registration.
- **Theme 5 — trust signals**: SBOM (CycloneDX via Anchore) on tag
  push; `govulncheck` + `osv-scanner` on PR/push/weekly;
  exact direct-dependency source allowlist in CI;
  [`THREAT_MODEL.md`](THREAT_MODEL.md) (tracks shipped mitigations and
  currently has no open in-kit mitigation gaps);
  [`SUPPLY_CHAIN.md`](SUPPLY_CHAIN.md) (pinning + signing + vuln SLO).
- **Theme 6 — Builder integrations**: `WithPASETO`, `WithNATS`,
  `WithPostgres` pgx readiness, `WithLeaderElection` + cron leader gate,
  `WithSignedRequests`, `WriteServiceProblem`. Plus Wave 2 above.
- **Theme 7 — dashboards expansion + runbooks**: gRPC RED, DB pool,
  Redis, Outbox, Storage Grafana dashboards; per-area recording
  rules; saturation + messaging alerts; 7 runbooks under
  `docs/ai/runbooks/`; `promtool` validation in CI.

### v2.0.0 design choices worth knowing

- **Idempotency tenant wrapper namespaces the storage key**, not the
  body fingerprint — backend-layer isolation holds even if the backend
  bug ignores fingerprints, and a fresh request from tenant B never
  falsely 422s on tenant A's body.
- **Budget windows are fixed, not sliding** — LLM-cost reporting maps
  directly to vendor invoice lines; for adversarial smoothing, callers
  use `data/ratelimit/gcra`.
- **Action-log entries are HMAC-signed with rotation** —
  `SignatureKeyID` rides on every entry so old entries verify after
  rotation; `Sign`/`Verify` exposed for off-band tools.
- **Approval state machine refuses flip transitions** —
  approved→rejected (or vice-versa) needs a fresh request so the
  audit trail records the reconsideration.
- **MCP server doesn't implement JSON-RPC batch** — single-call
  semantics keep the action-log entry per-call rather than per-batch
  (forensics is cleaner).
- **MCP unauthenticated callers see "method not found", not
  "forbidden"** — deliberately to avoid revealing the tool catalog
  to the unauthenticated.
- **Builder methods refuse nil** for budget/actionlog/approval stores
  — silent no-op would defeat the kit's "refuse to misconfigure"
  stance.

## Phase 0–6 — landed

Phases 0 (unblock) through 6 (agent-readiness) closed every CRITICAL
plus the operational-footgun HIGH cluster. The per-finding ledger
lives in [CRITICAL.md](CRITICAL.md). Highlights:

- gRPC v1.79.3 + Go 1.26.2 toolchain bump.
- `stack.Default` panic-recovery middleware (closes original CRITICAL
  #2); `grpcx.NewServer` Recovery interceptors by default (closes
  original CRITICAL #3).
- AMQP publisher mandatory + NotifyReturn; `debughttp` Guard
  middleware.
- Outbox `next_retry_at` + exponential backoff + self-managed
  published/failed retention cleanup; `Writer.WithRequireTransaction()`.
- Postgres `sslmode` safer defaults; gormmysql TLS registry refcount.
- Idempotency `Store` reshape with body-fingerprint plumbing;
  pgstore `owner_token` migration; backends reject non-positive TTL.
- Redis queue per-consumer processing list + ID-keyed remove.
- `data/lock` Locker refit with per-call `Lock` handle and
  `ErrLockLost`.
- CSRF Origin allowlist; mandatory shared secret in non-dev;
  `WithDevSecret` opt-in; SameSite=None+Secure validation;
  session-bound HMAC primitive (`security/csrf`).
- `clientip` default to loopback only; `ParseTrustedProxiesStrict`.
- Timeout middleware buffer cap default 1 MiB; hard-timeout mode.
- secheaders `WithTrustedProxiesForProto` + `WithForceHSTS`.
- JWT `WithExpectedAudience` + mandatory issuer; `WithMaxStale`.
- `crypto/passhash`, `crypto/envelope` (with `kekstatic`),
  `crypto/paseto`, `core/secret`, `httpx/middleware/cspnonce`.
- `data/lock/pgadvisory`, `data/ratelimit` (token bucket + GCRA +
  Redis), `infra/leaderelection` (pgadvisory + redislock),
  `infra/messaging/natsbackend`, `infra/sqldb/pgx`.
- Cross-backend message-size limits via `messaging.MessageSizeLimiter`
  with Builder `WithMaxMessageBytes` / `WithRouteMaxMessageBytes`.
- `core/tenant` + httpx middleware; cache + idempotency tenant
  wrappers; per-tenant rate-limit middleware; `promutil/labelguard`.
- `httpx/middleware/signedrequest` + `httpx/sign`;
  `storagehttp/uploadsec` (MIME sniff + image-bomb defence + ClamAV
  scanner adapter).
- `observability/pprof`, `observability/runtimemetrics`,
  `observability/redmetrics`, `httpx/problemdetails`,
  `observability/dashboards`.
- `cmd/kit-doctor`, `cmd/kit-new`, `cmd/kit-bench-gate`.
- Production-safe defaults: originally landed as
  `app.WithProductionDefaults()`, then made unconditional by removing
  development mode (`c113451`). Per-relaxation `Without*()` opt-outs
  (`WithoutTLS`, `WithInternalNonLoopback`, `WithoutJWTIssuer`,
  `WithoutJWTAudience`) replace the meta switch.

## Deferred to v2.1+

Items that were genuinely out of scope or require separate effort.
Each is tracked here until shipped; pickup is opt-in per item.

### Cloud / SDK-bound spikes

- 🔴 **`crypto/envelope` cloud KMS subpackages** (`kekaws`, `kekgcp`,
  `kekvault`) — only `kekstatic` ships; cloud variants need the
  respective provider SDKs and would bloat the dep tree of consumers
  who don't use them.
- 🔴 **`infra/leaderelection/k8slease` and `.../etcd` backends** —
  need `k8s.io/client-go` / `etcd` client SDKs respectively.
- 🔴 **`infra/messaging/kafkabackend`** — explicit "don't do kafka"
  directive this wave; revisit when there's a concrete consumer ask.

### Dashboards + scaffolding follow-ups

- 🔴 **gRPC, DB, Redis, messaging, storage, outbox, ratelimit
  dashboards** — only HTTP RED + Go runtime + service overview
  shipped in this wave; remaining dashboards land per-area as the
  metric surface stabilises.
- 🔴 **`kit-new --modules` / `--token` flags** — base scaffold and
  `--tenant` wrapper wiring ship; remaining module-token wiring follows
  the corresponding Builder integration items.
- 🔴 **Per-package benchmarks for `kit-bench-gate`** — gate ships;
  benchmarks land per-package as audit identifies hot paths.
### Threat-model gaps

GAP-01 (cost budgets), GAP-02 (safe redirects), GAP-03 (gRPC
default deadline), GAP-04 (internal gRPC health), GAP-05 (tenant
scaffold), GAP-06 (JWT revocation), GAP-07 (message-size overrides),
GAP-08 (ClamAV upload scanning), GAP-09 (outbox retention cleanup),
and GAP-10 (direct dependency source allowlist plus heavy SDK boundary
gate) shipped in v2.0.0.
[THREAT_MODEL.md](THREAT_MODEL.md) §8 currently has no open in-kit
mitigation gaps.

## Tracking

Per-finding status from the v1→v2 audit lives in
[CRITICAL.md](CRITICAL.md). Forward-going threats and gaps live in
[THREAT_MODEL.md](THREAT_MODEL.md). New work uses conventional commits
and per-PR descriptions; the per-package implementation-plan files
(`existing/00-17`, `new/01-29`) that this directory used to host have
been retired now that everything they tracked has shipped.
