# Code review: Examples (stage 1 — unverified findings)

## Scope

- **Directories**: examples/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 2 (lenses inferred: correctness, design; expected lens count: 1)
- Status: raw reviewer findings; adversarial verification (stage 2) pending.

## Summary

| Severity | Count |
|---|---|
| CRITICAL | 0 |
| HIGH | 0 |
| MEDIUM | 0 |
| LOW | 0 |
| **Total (deduplicated)** | **0** |

**Reviewer impressions:**

> This is unusually careful example code: graceful-shutdown synchronization (agentic-service serveListener), the reference-counted keyedMutex (saga-coordinator), and mutex-guarded in-memory stores are all correctly implemented, and each service documents its security caveats and production-swap path in detail. No genuine race, deadlock, goroutine leak, or nil-deref surfaced across the six services. The remaining findings are template-footgun issues in the correctness/error-handling lens (silently swallowed idempotency errors and a disabled fingerprint check) rather than live bugs.

> This is unusually careful example code for its category — the graceful-shutdown synchronization in agentic-service (serveListener), the reference-counted keyedMutex in saga-coordinator, and the resilience-chain error classification in api-gateway are all correct under concurrency, and I could not fault them despite close tracing (including verifying redact.WrapError preserves the Unwrap chain that api-gateway's errors.Is checks rely on). Security-relevant relaxations are consistently documented and gated behind env-supplied secrets. The only correctness concerns are error-handling gaps: silent swallowing of idempotency cache-write failures in the saga template, and a minor double-read of the HMAC env key.

## Findings

_All stage-1 findings for this family are fixed or applied as intentional v2 breaks. See V3_BREAKING_PROPOSALS.md (APPLIED) and git history._
