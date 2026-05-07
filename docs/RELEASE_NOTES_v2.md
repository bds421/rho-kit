# rho-kit v2.0.0 — agentic-AI service backend

The v2.0.0 release reframes the kit around **building services that AI
agents call (or that are AI agents themselves)**. The Phase 1–6 audit
shipped the security and operational guardrails that any production
service needs; v2.0.0 adds the agentic-specific primitives that every
agentic service was hand-rolling and getting wrong.

## TL;DR

| Theme | What landed | Why |
|---|---|---|
| 1 | Tenant-aware cache, idempotency, ratelimit, label cardinality guard | SaaS substrate that every other agentic primitive layers on |
| 2 | Per-tenant cost budgets (memory + Redis backends, inbound middleware, outbound RoundTripper) | The thing that prevents a misbehaving tenant's LLM bill from blowing past five figures overnight |
| 3 | Append-only signed action log + approval workflow | "What did the agent do this hour against tenant X" becomes a SQL query, not a log grep |
| 4 | MCP helpers — typed handlers as JSON-RPC tools, schema auto-generation | Expose any kit handler as an MCP tool with the kit's full middleware stack reused |
| 5 | SBOM (CycloneDX), `govulncheck` + `osv-scanner` CI, `THREAT_MODEL.md`, `SUPPLY_CHAIN.md` | "Trusted library" claim is auditable, not marketing |
| 6 | Builder integrations for every new primitive + the deferred Phase A items | The kit's golden path (`app.Builder`) reaches the new primitives without each consumer wiring middleware by hand |
| 7 | gRPC RED, DB pool, Redis, Outbox, Storage Grafana dashboards + 7 runbooks + `promtool` CI | Operations teams stop rebuilding the same panels per service |

Plus: `WithDefaultDeadline` for gRPC (closes threat-model GAP-03), and
the `examples/agentic-service` reference binary that wires the entire
v2 stack in one file.

## Breaking changes

**None significant.** v2.0.0 is additive over v1.x — every new
primitive is opt-in. The `app.Builder` methods that landed are new
(`WithPASETO`, `WithNATS`, `WithPgx`, `WithLeaderElection`,
`WithSignedRequests`, `WithMultiTenant`, `WithTenantBudget`,
`WithActionLogger`, `WithApprovalStore`); none change existing
method signatures.

The two soft incompatibilities consumers should know about:

1. **`BufferedPublisher` requires a state file in non-dev**
   environments. Set `WithBufferedStateFile(path)` or pass
   `WithEphemeralBuffer()` for an explicit opt-out backed by an
   upstream outbox. Dev environments warn but allow ephemeral
   buffering. *This was already in main pre-v2; flagged here for
   visibility.*

2. **`app.WithProductionDefaults()` enforces JWT issuer pinning**.
   Calling `WithJWT(...)` without `WithJWTIssuer(...)` (or the
   explicit `WithJWTAllowAnyIssuer()`) panics at boot in
   `KIT_ENV=production`. Migration: chain the issuer call. *Same as above
   — pre-v2.*

3. **MCP server doesn't implement JSON-RPC batch**. Single-call
   semantics keep the action-log entry per-call rather than
   per-batch (forensics is cleaner). Bodies starting with `[` are
   rejected with `-32600 Invalid request`. Document for SDK
   consumers; deferred to v2.1 if a real consumer needs it.

## New Builder methods (the migration guide)

```go
import (
    "github.com/bds421/rho-kit/app"
    "github.com/bds421/rho-kit/data/budget"
    budgetredis "github.com/bds421/rho-kit/data/budget/redis"
    "github.com/bds421/rho-kit/data/actionlog"
    actionlogpg "github.com/bds421/rho-kit/data/actionlog/postgres"
    "github.com/bds421/rho-kit/data/approval"
    approvalpg "github.com/bds421/rho-kit/data/approval/postgres"
    "github.com/bds421/rho-kit/httpx/middleware/budget"
    httpxtenant "github.com/bds421/rho-kit/httpx/middleware/tenant"
    leaderpg "github.com/bds421/rho-kit/infra/leaderelection/pgadvisory"
    natsbackend "github.com/bds421/rho-kit/infra/messaging/natsbackend"
    pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx"
)

b := app.New("my-service", "v2.0.0", cfg).
    WithProductionDefaults().

    // Auth (pick one or both — they coexist for migration)
    WithJWT(cfg.JWKSURL).WithJWTIssuer(cfg.Issuer).
    WithPASETO(pasetoProvider).

    // Multi-tenant — extracts X-Tenant-Id by default; required on
    // state-changing methods, GET/HEAD/OPTIONS pass through.
    WithMultiTenant(httpxtenant.HeaderExtractor("X-Tenant-Id"), true).

    // Per-tenant cost budgets. Default key = tenant.FromContext.
    WithTenantBudget(budgetredis.New(redisClient, 1_000_000, time.Hour)).

    // Append-only signed action log
    WithActionLogger(actionlog.New(actionlogpg.NewStore(db), secrets)).

    // Approval workflow for destructive routes
    WithApprovalStore(approvalpg.NewStore(db)).

    // pgx-native pool for LISTEN/NOTIFY + COPY (mutex with WithPostgres)
    WithPgx(pgxbackend.Config{DSN: cfg.PgxDSN}).

    // Leader election for cron jobs
    WithLeaderElection(leaderpg.New(db, "my-service", logger)).

    // Service-to-service request signing
    WithSignedRequests(keyResolver, signedrequest.NewMemoryNonceStore(time.Minute)).

    // NATS JetStream (independent of WithRabbitMQ; both can coexist)
    WithNATS(natsbackend.Config{URL: cfg.NATSURL}).

    Run(ctx)
```

## What composes with what (middleware chain order)

When the Builder assembles the public mux, middleware is composed
**outermost to innermost**:

```
signedrequest → tenant → budget → recovery → logging → tracing → router
```

- `signedrequest` is outermost so unsigned requests reject before any
  tenant or budget work runs.
- `tenant` runs before `budget` because the budget middleware reads
  tenant ID from ctx.
- `recovery`, `logging`, `tracing` are part of `stack.Default` and
  unchanged from v1.x.

## New primitives (skim of what shipped)

### Multi-tenant
- `core/tenant` — type-distinct `tenant.ID`, ctx helpers, `Required`
- `data/cache/tenant.Wrap(c)` — namespaces every key with `tenant:<id>:`
- `data/idempotency/tenant.Wrap(s)` — namespaces idempotency keys (not the body fingerprint — see audit doc for rationale)
- `httpx/middleware/ratelimit/tenant` — per-tenant limit on top of IP limit (both must pass)
- `observability/promutil/labelguard` — drops + counts disallowed label values to prevent cardinality explosion

### Cost budgets
- `data/budget` — `Budget` interface with `Consume`/`Peek` plus optional `Refunder` capability
- `data/budget/memory` — single-process backend
- `data/budget/redis` — atomic Lua, cross-instance, `WithRedisTime` for clock-skew-free fairness
- `httpx/middleware/budget` — inbound charge per request, rejects with `429 + X-Budget-Remaining + Retry-After`
- `httpx/budget` — outbound `RoundTripper` with two-phase reconciliation (estimate → actual)

### Action audit + approval
- `data/actionlog` — append-only signed entries; HMAC rotation via `SignatureKeyID` carried per entry
- `data/actionlog/{memory,postgres}` — backends; postgres ships a migration
- `data/approval` — pending → approved/rejected → executed lifecycle
- `data/approval/{memory,postgres}` — backends with `SELECT FOR UPDATE` in `Decide`
- `httpx/middleware/approval` — wraps a destructive route, returns 202 + approval ID

### MCP
- `httpx/mcp` — typed `Handler[In, Out]`; auto-generates JSON-Schema from struct tags
- Reuses kit's middleware stack (auth, tenant, ratelimit, budget, approval, action log)
- `cmd/kit-new --mcp` flag scaffolds a sample tool registration

### Trust signals
- `.github/workflows/sbom.yml` — CycloneDX SBOM on tag push
- `.github/workflows/vuln.yml` — `govulncheck` + `osv-scanner` on PR / push / weekly
- `docs/audit/THREAT_MODEL.md` — 827 lines, identifies 10 GAP-01..10 follow-ups
- `docs/audit/SUPPLY_CHAIN.md` — pinning policy, signing keys, vuln SLO

### Dashboards & runbooks
- 5 new Grafana dashboards (gRPC RED, DB pool, Redis, Outbox, Storage)
- New Prometheus rules (recording, saturation, messaging)
- 7 runbooks under `docs/ai/runbooks/` matching every alert's `runbook_url`
- `promtool check rules` in CI

### gRPC hardening
- `grpcx.WithDefaultDeadline(d)` — per-RPC default deadline; closes threat-model GAP-03 (streaming-RPC exhaustion)

## What's deliberately NOT in v2.0.0

| Item | Why deferred |
|---|---|
| Cloud KMS subpackages (`kekaws`, `kekgcp`, `kekvault`) | Each needs a separate provider SDK; would bloat the dep tree of consumers who only use `kekstatic` |
| `k8slease` and `etcd` leader-election backends | Need k8s.io / etcd client libraries |
| Kafka backend | Explicit "don't do kafka" directive this wave |
| AMQP messaging dashboard | Backend uses callback hooks (`BufferedPublisherMetrics`) rather than Prometheus collectors; needs the metrics surface first |
| Per-package benchmarks for `kit-bench-gate` | Gate ships; baselines land per-area as benchmarks are written |
| Recipe entries in `docs/ai/*.md` | Separate docs sweep; the v2 primitives' package docs and `examples/agentic-service` cover the canonical wiring |
| GAP-01..10 from `THREAT_MODEL.md` (except GAP-01 cost budgets and GAP-03 gRPC deadline, which shipped) | Each is its own audit item; tracked in `docs/audit/new/` |

## Stats

- **116 commits** ahead of v1.x baseline at release.
- **5 parallel agents** + me orchestrating, plus 1 sequential agent (MCP).
- **10 new packages** under `data/`, `httpx/`, `observability/`, plus the example.
- **~6500 lines** of new docs across `THREAT_MODEL`, `SUPPLY_CHAIN`, audit `26-29`, ROADMAP, runbooks, README, this file.

## Upgrading from v1.x

1. **No code changes are required**. v1.x services keep working.
2. To adopt v2 primitives incrementally:
   - Start with `core/tenant` + the `httpx/middleware/tenant` middleware in the public mux.
   - Then add `data/cache/tenant.Wrap(...)` around any cache the service constructs.
   - Then `WithMultiTenant` on the Builder.
   - Cost budgets and action log come once tenant ID is reliably on ctx.
3. Run `kit-doctor ./...` against the upgraded service to surface any drift from the kit's secure defaults.

## Acknowledgements

This release was assembled with five concurrent subagents working in
parallel git worktrees; each owned one theme end-to-end and reported
back commits + design trade-offs that the orchestrator integrated.
The pattern worked well for genuinely-independent themes; future
releases will use the same model.
