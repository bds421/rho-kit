# Review backlog status

## Policy
**fix-first.** Docs/typos/naming/consistency/perf/tradeoffs are fixed, not refuted.
Breaking API changes are OK for this library (sole consumer) when the security or
lifecycle argument is clear — documented in `V3_BREAKING_PROPOSALS.md` as **APPLIED**.

## Remaining totals
| Severity | Count |
|---|---:|
| CRITICAL | 0 |
| HIGH | 0 |
| MEDIUM | 0 |
| LOW | 0 |
| **Total** | **0** |

## Intentional v2 breaks applied (end of remediation)
1. **WebSocket** — default 30s write timeout + 30s ping / 10s pong; opt out with `WithNoWriteTimeout` / `WithNoHeartbeat`.
2. **queue/stream `Consume`** — returns `error` (`ctx.Err()` on clean cancel).
3. **approval `TenantStore`** — requires `TenantScopedMutator` (atomic ForTenant); no check-then-act fallback.
4. **idempotency tenant keys** — user keys cannot use `tenant:`/`tns:`; wrapper stores opaque `tns:`+sha256 digests.
5. **storage** — removed dead Tagger/Versioner/BatchDeleter; multiparts kept; hooks/retry/CB use compact embed forwarders.
6. **sftp** — refcounted client leases so reconnect does not close under in-flight I/O.

## History
Stage-1 started ~907 findings; fixed via TDD with living review trackers.
