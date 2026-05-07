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

**Two runtime behaviour changes**: the `httpx/middleware/auth` fail-closed
fix below, and the removal of development mode (next section). Everything
else in v2.0.0 is additive over v1.x; new `app.Builder` methods
(`WithPASETO`, `WithNATS`, `WithPgx`, `WithLeaderElection`,
`WithSignedRequests`, `WithMultiTenant`, `WithTenantBudget`,
`WithActionLogger`, `WithApprovalStore`) don't change existing signatures.

## Breaking change: no development mode

The kit no longer has a development mode. Production-safe defaults are
the only mode, and the `app.Builder` runs the production-safety validator
unconditionally at `Build()` time. There is no `KIT_ENV` (or `APP_ENV`)
escape hatch in any kit code path — the runtime no longer reads those
env vars to weaken safety checks.

### Removed: `WithProductionDefaults()`

`Builder.WithProductionDefaults()` is gone. Its checks (JWT issuer/
audience pinning, TLS-required, internal-host loopback, postgres sslmode,
tracing sample-rate cap, `WithTenantBudget` + `WithMultiTenant` pairing)
now run unconditionally in `Builder.Build()`. Migration is to delete
the `WithProductionDefaults()` call from your chain.

### Renamed: opt-out methods

The four explicit opt-outs lost their `Production` prefix and now read
as deliberate "I know what I'm doing" declarations. Behaviour is
identical, only names changed:

| Old | New |
|---|---|
| `WithProductionAllowPlaintext` | `WithoutTLS` |
| `WithProductionInternalExposed` | `WithInternalNonLoopback` |
| `WithJWTAllowAnyIssuer` | `WithoutJWTIssuer` |
| `WithJWTAllowAnyAudience` | `WithoutJWTAudience` |

Each opt-out remains documented as a per-relaxation declaration the
operator must apply consciously (network isolation confirmed,
external TLS terminator in place, etc.).

### Removed: `KIT_ENV` runtime checks

The kit no longer reads `KIT_ENV` (or `APP_ENV`) to decide whether to
enforce a check. The following call-site checks went unconditional:

- `app/jwt_module.go` — issuer enforcement was previously gated on
  `KIT_ENV != development`; the Builder validator now rejects missing
  issuer upstream.
- `security/jwtutil.NewProvider` — used to panic on missing issuer
  outside development; now accepts any configuration the caller hands
  it (Builder validates upstream; standalone callers should still
  pair `WithExpectedIssuer` or `WithAllowAnyIssuer`).
- `infra/sqldb/pgx.Connect` — TLS check is unconditional; tests against
  testcontainers can pass `Config{AllowPlaintext: true}` to opt out.
- `infra/messaging.NewBufferedPublisher` — state-file requirement is
  unconditional; the existing `WithEphemeralBuffer()` is the only
  opt-out.
- `httpx/middleware/csrf.New` — HMAC secret requirement is
  unconditional; the existing `WithDevSecret()` is the only opt-out.

The `examples/agentic-service` reference binary's "refuse to run when
KIT_ENV looks like production" guard was removed — the example is now
documented as illustrative-only via package comments, and consumers
that want production wiring use `app.Builder` whose always-on validator
catches missing TLS/auth/sslmode at startup.

`core/config.IsDevelopment` and `BaseConfig.Environment` are
preserved for downstream consumers' own logic; the kit's core path
simply stops calling them.

### Migration

```go
// Before
b := app.New("my-service", "v2.0.0", cfg).
    WithProductionDefaults().
    WithProductionAllowPlaintext().
    WithProductionInternalExposed().
    WithJWTAllowAnyIssuer().
    WithJWTAllowAnyAudience().
    Run(ctx)

// After
b := app.New("my-service", "v2.0.0", cfg).
    // The validator runs automatically; remove WithProductionDefaults().
    WithoutTLS().                  // was WithProductionAllowPlaintext
    WithInternalNonLoopback().     // was WithProductionInternalExposed
    WithoutJWTIssuer().            // was WithJWTAllowAnyIssuer
    WithoutJWTAudience().          // was WithJWTAllowAnyAudience
    Run(ctx)
```

Most services don't pass any opt-outs; the migration in that case is
just to delete `WithProductionDefaults()`.

### Auth middleware now fails closed (security fix)

`auth.RequirePermission`, `auth.PermissionByMethod`, and
`auth.RequireScope` previously fell through to the next handler when
the request carried no permissions/scopes claim — the rationale being
that mTLS-authenticated internal services don't carry one. The
implementation hooked that bypass to *absence of the claim* rather
than to the verified-mTLS path, so a route mounted without any auth
middleware in front silently granted full access, and a JWT issued
without the permissions claim implicitly bypassed RBAC.

**v2.0.0**: `RequireS2SAuth`'s mTLS branch now stamps a trusted-S2S
marker onto the context; the three middlewares bypass ONLY when that
marker is present. Any other condition (no claim, no marker, no auth
middleware) returns 403.

Migration for downstream services that relied on the implicit bypass:

1. Wire `RequireS2SAuth` properly so verified-mTLS callers get the
   marker (preferred — preserves the original intent).
2. Issue JWTs with explicit permissions claims for the routes that
   need them.
3. Use `httpx/authz.RequirePermission` with a `Policy` if the bypass
   condition is more nuanced than "verified internal cert".

`RequireScopeStrict` is unchanged — it intentionally does NOT honor
the marker.

### Other soft incompatibilities

1. **`BufferedPublisher` requires a state file in non-dev**
   environments. Set `WithBufferedStateFile(path)` or pass
   `WithEphemeralBuffer()` for an explicit opt-out backed by an
   upstream outbox. Dev environments warn but allow ephemeral
   buffering. *This was already in main pre-v2; flagged here for
   visibility.*

2. **JWT issuer pinning is enforced unconditionally**. Calling
   `WithJWT(...)` without `WithJWTIssuer(...)` (or the explicit
   `WithoutJWTIssuer()` opt-out) fails `Builder.Build()` validation.
   Migration: chain the issuer call. *Pre-v2; tightened from
   "non-development only" to unconditional in v2.*

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
    httpxbudget "github.com/bds421/rho-kit/httpx/middleware/budget"
    httpxtenant "github.com/bds421/rho-kit/httpx/middleware/tenant"
    leaderpg "github.com/bds421/rho-kit/infra/leaderelection/pgadvisory"
    natsbackend "github.com/bds421/rho-kit/infra/messaging/natsbackend"
    pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx"
)

b := app.New("my-service", "v2.0.0", cfg).
    // Production-safety validator runs automatically — no opt-in needed.

    // Auth (pick one or both — they coexist for migration)
    WithJWT(cfg.JWKSURL).WithJWTIssuer(cfg.Issuer).WithJWTAudience(cfg.Audience).
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
