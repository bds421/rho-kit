# Review backlog status

## Policy
**fix-first.** Docs/typos/naming/consistency/perf/tradeoffs are fixed, not refuted.
Breaking (v3) API changes need explicit user go-ahead (`V3_BREAKING_PROPOSALS.md`).

## Remaining totals
| Severity | Count |
|---|---:|
| CRITICAL | 0 |
| HIGH | 0 |
| MEDIUM | 5 |
| LOW | 5 |
| **Total** | **10** |

## Per-file (non-zero only)
| File | C | H | M | L | Total |
|---|---:|---:|---:|---:|---:|
| `review-18-storage-core.md` | 0 | 0 | 1 | 3 | 4 |
| `review-12-data-core-b.md` | 0 | 0 | 1 | 2 | 3 |
| `review-19-storage-backends.md` | 0 | 0 | 1 | 0 | 1 |
| `review-11-data-core-a.md` | 0 | 0 | 1 | 0 | 1 |
| `review-09-websocket-realtime.md` | 0 | 0 | 1 | 0 | 1 |

## Remaining work classes
- **MEDIUM (5)**: deferred v3 design items — websocket defaults, TenantStore atomic Store contract, tenant key unforgeability, storage optional APIs, SFTP reconnect leases.
- **LOW**: `Consumer.Consume` error returns (v3), storage hooks combinatorial boilerplate.

## Commits
Stage-1 remediation landed on `main` in thematic commits; continue LOWs; tackle MEDIUM/v3 last.
