# rho-kit workspace code review — cross-family summary (stage 1)

- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Scope**: 26 package families spanning all workspace modules; 3 review lenses each (correctness/concurrency, security, design/quality), except examples (1) and testing-kits (2).
- **Total open findings**: ~8 OPEN MEDIUMs remaining in other families; LOW batch addressed 2026-07-21 (CRITICAL 0, HIGH 0). See family reports for FIXED markers.
- **Status**: OPEN findings only. Findings with audited FIXED evidence (code + tests) were removed from family reports. Prior mass-refute was reversed.
- Per-family detail: see `review-01-*.md` … `review-26-*.md`.

## Findings per family

| # | Family | Crit | High | Med | Low | Total | Report |
|---|---|---|---|---|---|---|---|
| 01 | Core primitives & IO | 0 | 0 | 0 | 0 | 0 | `review-01-core-io.md` |
| 02 | Runtime & Resilience | 0 | 0 | 0 | 0 | 0 | `review-02-runtime-resilience.md` |
| 03 | App DI & wiring | 0 | 0 | 0 | 0 | 0 | `review-03-app-wiring.md` |
| 04 | Crypto & envelope encryption | 0 | 0 | 0 | 0 | 0 | `review-04-crypto.md` |
| 05 | Security package | 0 | 0 | 0 | 0 | 0 | `review-05-security.md` |
| 06 | OAuth2 & AuthZ | 0 | 0 | 0 | 0 | 0 | `review-06-auth-authz.md` |
| 07 | HTTPX core | 0 | 0 | 0 | 0 | 0 | `review-07-httpx-core.md` |
| 08 | HTTPX middleware | 0 | 0 | 0 | 0 | 0 | `review-08-httpx-middleware.md` |
| 09 | WebSocket & realtime | 0 | 0 | 1 | 0 | 1 | `review-09-websocket-realtime.md` |
| 10 | gRPC toolkit | 0 | 0 | 0 | 0 | 0 | `review-10-grpcx.md` |
| 11 | Data interfaces A | 0 | 0 | 1 | 0 | 1 | `review-11-data-core-a.md` |
| 12 | Data interfaces B | 0 | 0 | 1 | 2 | 3 | `review-12-data-core-b.md` |
| 13 | Postgres data stores | 0 | 0 | 0 | 0 | 0 | `review-13-data-pg-stores.md` |
| 14 | Redis data stores | 0 | 0 | 0 | 0 | 0 | `review-14-data-redis-stores.md` |
| 15 | Queues & streams | 0 | 0 | 0 | 0 | 0 | `review-15-queues-streams.md` |
| 16 | Messaging core | 0 | 0 | 0 | 0 | 0 | `review-16-messaging-core.md` |
| 17 | Messaging backends | 0 | 0 | 0 | 0 | 0 | `review-17-messaging-backends.md` |
| 18 | Storage core | 0 | 0 | 1 | 0 | 1 | `review-18-storage-core.md` |
| 19 | Storage backends | 0 | 0 | 1 | 0 | 1 | `review-19-storage-backends.md` |
| 20 | SQL DB & outbox | 0 | 0 | 0 | 0 | 0 | `review-20-sqldb-outbox.md` |
| 21 | Redis infra & leader election | 0 | 0 | 0 | 0 | 0 | `review-21-redis-leader.md` |
| 22 | Secrets management | 0 | 0 | 0 | 0 | 0 | `review-22-secrets.md` |
| 23 | Observability & flags | 0 | 0 | 0 | 0 | 0 | `review-23-observability-flags.md` |
| 24 | CLI tools | 0 | 0 | 0 | 0 | 0 | `review-24-cmd-clis.md` |
| 25 | Examples | 0 | 0 | 0 | 0 | 0 | `review-25-examples.md` |
| 26 | Testing kits | 0 | 0 | 0 | 0 | 0 | `review-26-testing-kits.md` |
| | **TOTAL** | **0** | **0** | see families | see families | see families | |

## Findings by dimension

| Dimension | Count |
|---|---|
| (recompute after OPEN-only cleanup; see family reports) | — |

## All CRITICAL and HIGH findings (0)

Ranked leads for stage-2 verification. Each links to its family report.

## Recurring themes

These patterns appear across multiple families (see individual reports for instances):

- **Fail-open under degradation**: several components default to permissive/allow behavior when a dependency is unavailable or misconfigured.
- **Silent security downgrades from ordering/wiring**: leader gating, TLS option ordering, and callback overrides can be silently defeated by registration order or caller options.
- **Secrets/PII in logs**: webhook URLs with capability tokens and broker credentials logged unredacted in multiple places.
- **Context lifecycle bugs**: cancelled contexts retained for later refresh/rollback, and successful side-effecting calls reported as failures on cancellation.
- **Sibling-package divergence**: paired core/store or interface/impl packages have drifted (validation present in one, missing in the sibling), a recurring source of surprise.

## Method & caveats

- Findings produced by independent per-lens reviewer agents, deduplicated by `file:line`.
- **FIXED cleanup**: only findings with audited code+test evidence were removed from family reports. OPEN findings remain. Docs/typos/naming/consistency items are treated as fix-first, not refuted.
- Line numbers are as reported by reviewers and may be slightly off; verify against the cited file.
