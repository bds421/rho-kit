# Repository audit — 2026-05-05 (snapshot)

Independent audit of `github.com/bds421/rho-kit` (65 Go modules as of the
v2.0.0 release-candidate tree) focused on
security defaults, correctness under concurrency, and operational reliability.

## Verification run (snapshot)

| Check | Result | Notes |
|---|---|---|
| `make test` | Pass | Full workspace. |
| `make test-race` | Pass | Full workspace. |
| `make vet` | Pass | Full workspace. |
| `make build` | Pass | Full workspace. |
| `make lint` | Fail | Sequential workaround applied — closed via commit `56bf04e`; see [docs/audit/CRITICAL.md](../audit/CRITICAL.md). |
| Sequential lint loop | Pass | Same modules, same version, run one module at a time: 0 issues. |
| `make vulncheck` | Now passing | After Go 1.26.2 toolchain bump (commit `5df122f`). |

## Status

This audit's findings have been **fully reconciled** into the structured
audit ledger at [`docs/audit/`](../audit/). Per-finding status (closed,
landed-with-commit-hash, or open) is tracked there:

- [`docs/audit/CRITICAL.md`](../audit/CRITICAL.md) — cross-package CRITICAL
  ledger plus the operational-footgun HIGH cluster.
- [`docs/audit/ROADMAP.md`](../audit/ROADMAP.md) — phase-by-phase status
  and v2.1+ deferred items.
- [`docs/audit/THREAT_MODEL.md`](../audit/THREAT_MODEL.md) — STRIDE
  surface and the GAP-01..10 follow-up list.

All findings from this audit (P0 / P1 / P2 / P3) closed in Wave 1+2+3+4+5
plus the v2.0.0 push. Remaining work is the explicitly-deferred set in
[`ROADMAP.md`](../audit/ROADMAP.md) (cloud KMS, k8slease/etcd, Kafka,
AMQP/rate-limit dashboard panels, kit-new flags, per-package benchmarks).
GAP-01..10 are closed in [`THREAT_MODEL.md`](../audit/THREAT_MODEL.md) §8.

If you re-run this audit, write the new snapshot under a different filename
(e.g. `repo-audit-YYYY-MM-DD.md`) and reconcile findings into the structured
ledger rather than maintaining two parallel sources of truth.
