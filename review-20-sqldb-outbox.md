# Code review: SQL DB & outbox (stage 1 — unverified findings)

## Scope

- **Directories**: infra/sqldb/, infra/outbox/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 8 (lenses inferred: correctness, design, security; expected lens count: 3)
- Status: raw reviewer findings; adversarial verification (stage 2) pending.

## Summary

| Severity | Count |
|---|---|
| CRITICAL | 0 |
| HIGH | 0 |
| MEDIUM | 0 |
| LOW | 1 |
| **Total (deduplicated)** | **1** |

**Reviewer impressions:**

> This scope is unusually high quality: the pgx TLS enforcement, outbox claim-token fencing, and FIFO-preserving claim SQL are carefully reasoned, with godoc that explains not just what but why (including audit trail references), and non-trivial logic like lastSSLMode and the routing pool is well covered by tests. The defects found are mostly edge cases and drift between documentation and behavior — the standouts being replica eviction on caller-side cancellation, the un-fenced outbox Heartbeat that breaks the otherwise-uniform claim-token design, silent override of DSN pool parameters, and unbounded Prometheus series growth on RoutingPool churn. Nothing suggests data loss in the outbox delivery path itself.

> This scope is unusually high quality: defensive nil/zero-value checks throughout, careful claim-token fencing in the outbox store (ABA-aware, with clear comments tying design to audit findings), correct FOR UPDATE SKIP LOCKED claim semantics, and clean goroutine lifecycle management (stopOnce/wg in readreplica, context-detached UNLISTEN cleanup in pgx.Listen). The surviving issues are mostly edge-case gaps in otherwise deliberate designs — a dead string-match error classifier, a raw-DSN sslmode scanner that diverges from pgx's own parser, replica health accounting that misattributes caller cancellation, and two small fencing/cleanup omissions — rather than structural flaws.

> This scope is unusually well-hardened for its security lens: all SQL is parameterized, identifiers passed to COPY/LISTEN are validated with strict allowlists and pgx.Identifier.Sanitize, credentials are consistently kept out of logs via slog.LogValuer and redact.WrapError, and the outbox delivery path uses claim-token fencing plus FOR UPDATE SKIP LOCKED to keep at-least-once guarantees intact under concurrent relays. The TLS-enforcement layer is thoughtfully defended (loopback gating, last-wins DSN scanning, FR-079 require rejection) but hangs its require-vs-verify distinction entirely on scanning the DSN string, leaving a fail-open gap when sslmode is sourced from PG* environment variables. Overall a mature, audit-scarred codebase with only edge-case security gaps remaining.

> This scope is unusually careful and well-documented: the outbox store's claim_token fencing against the ABA/stale-reset race, the pgx DSN TLS enforcement, and the read-replica health accounting via CAS-guarded gauges all show real distributed-systems maturity, and most concurrency primitives are used correctly. The main gaps are edge-case robustness rather than core-logic errors — chiefly that caller-side context cancellation is misattributed to replica health (with a fan-out amplification across all replicas), and Heartbeat's lack of the claim fence that the rest of the module rigorously applies. Overall quality is high; the findings are refinements to an otherwise solid design.

> This scope is high quality: the code is densely and thoughtfully documented, concurrency is handled carefully (CAS-guarded gauges, per-row claim-token fencing, SKIP LOCKED FIFO), and there is extensive, largely-effective security hardening around TLS/DSN parsing and identifier escaping. The main gaps are a residual crafted-DSN bypass of the FR-079 sslmode policy (the raw-DSN scanner disagrees with pgx's percent-decoding), and a read-replica health path that conflates caller context cancellation with replica failure, which can trigger spurious mass-failover. A couple of exported items also have godoc that no longer matches their implementation.

> This scope is high-quality, security-conscious code: parameterized queries throughout, identifier allowlisting for COPY/LISTEN/column names, consistent redact.WrapError usage, slog.LogValuer credential guards on Config, and a well-reasoned claim-token fencing design for outbox delivery. The main residual risk is that the pgx TLS-hardening (FR-079) leans on a hand-rolled DSN string scan as the require-vs-verify oracle, which does not cover all of pgx's config sources and can fail open to unverified TLS. Otherwise findings are minor (a non-redacted error log).

> This scope is unusually well-engineered and defensively reviewed: the outbox claim-token fencing correctly closes the stale-reset/re-claim ABA race for outcome updates, the pgx TLS/DSN enforcement is carefully hardened against parser-disagreement bypasses, and the readreplica health accounting uses CompareAndSwap consistently to keep the gauge balanced. The findings are edge cases rather than systemic flaws — the most impactful being IsNotFound not recognizing pgx.ErrNoRows on the kit's canonical driver, and the zero-replica pass-through mode spamming warnings/fallback metrics. Concurrency primitives (mutex-guarded claim map, atomic replica state, goroutine lifecycle in health loop and Listen) are otherwise sound.

> This scope is unusually well-engineered for its problem domain: the outbox claim-token fencing, the FIFO CTE with explicit row_number ordering, and the pgx TLS/sslmode enforcement are all carefully reasoned and heavily documented, and most exported APIs are misuse-resistant (fail-fast panics, verbose opt-out field names, LogValue redaction). The findings are mostly consistency/ergonomics gaps rather than deep bugs: a couple of godoc-vs-behavior mismatches (PrimaryHealthy), one invariant the store enforces everywhere except Heartbeat, and some silent config/metric loss. Overall quality is high; the issues are edge-case correctness and API-polish items worth tightening before release.

## Findings

### [LOW] Neither Pinger nor ContextPinger is satisfied by the kit's own canonical pgx pool

- **Where**: `infra/sqldb/health.go:22`
- **Dimension**: api-design
- **Detail**: The kit declares pgx as the single supported Postgres driver, yet infra/sqldb/pgx.Pool exposes Ping(ctx) — which matches neither Pinger (Ping() error) nor ContextPinger (PingContext(ctx) error). The godoc itself concedes this and tells every caller to hand-write a closure shim. The two health-check interfaces are shaped around *sql.DB (the legacy driver) rather than the sibling package that is supposed to be the golden path, forcing boilerplate at every call site and making the composition awkward.
- **Suggestion**: Add a ContextPinger adapter constructor in the pgx package (e.g. pgx.Pool.PingContext or a small wrapper) so the canonical driver plugs into HealthCheckContext without a caller-written shim.

