# Code review: Runtime & Resilience (stage 1 — unverified findings)

## Scope

- **Directories**: runtime/, resilience/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 6 (lenses inferred: correctness, design, security; expected lens count: 3)
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

> This is unusually disciplined library code: consistent fail-fast option validation, panic containment around every user callback, redaction of panic payloads, nil-safe metrics wrappers, and thorough godoc that frequently documents subtle concurrency and lifecycle contracts (often citing internal audit IDs). Test coverage is extensive across both directories, and most of the genuinely tricky logic (tombstoned eventbus handlers, worker-pool shutdown races, stop-budget allocation) is both handled and explained. The findings are mostly contract/documentation drift (bulkhead default mode, saga compensation bookkeeping, lifecycle doc) and a few edge-case behaviors (timeoutbudget reservation clearing, saga panic-path asymmetry) rather than systemic defects; the largest structural debt is the duplicated retry state machine between Loop and doWithPolicy.

> This scope is unusually high quality for infrastructure code: consistent construction-time validation, panic containment around every user callback, bounded metric label cardinality, redacted panic payloads, and thoughtful lifecycle/shutdown semantics with honest doc comments about cooperative cancellation limits. The weakest area is the durable saga executor, which has a real rollback-loss bug (failed compensations finalized as done) and is also the main deviation from the kit's otherwise well-enforced error-redaction discipline, alongside retry.Loop's raw error logging. Remaining findings are doc/contract mismatches rather than exploitable security flaws; no injection, weak-randomness, or fail-open authz issues were found in scope.

> This is unusually disciplined resilience/runtime code: consistent panic containment, nil-option validation, tombstone/compaction and worker-pool designs are carefully reasoned, and most concurrency (mutex-guarded lifecycle flags, snapshot-under-RLock dispatch, once-guarded teardown) is correct with the tradeoffs explicitly documented in comments. The significant defects cluster in cross-cutting semantics rather than raw races: the durable saga permanently abandons failed compensations (the one real data-consistency bug), the retry helper can report failure for a succeeded side-effecting call, and the lifecycle runner has a subtle signal-registration race. The remaining findings are edge-case contract deviations (jitter exceeding MaxDelay, cancelled-ctx handling at entry points) in otherwise high-quality packages.

> This is an unusually well-hardened scope: pervasive panic recovery with consistent redaction of panic payloads, validated Prometheus label values to prevent cardinality abuse, bounded-by-default concurrency (worker pools, fan-out limits, resume concurrency), and fail-fast option validation throughout. Because these packages are in-process runtime/resilience primitives with no network, SQL, crypto, or user-input surface, classic injection/authn attack classes are structurally absent; the residual issues are trust of external Retry-After-style input, a fail-open nil-breaker composition path, saga duplicate-execution under concurrent drivers, and durable persistence of unredacted error text. Documentation quality is exceptional, though in a couple of places (eventbus Stop, executor OCC claims) the docs promise slightly more than the code enforces.

> This scope is unusually high quality for review: nearly every exported item has thoughtful godoc explaining invariants and trade-offs, construction-time validation with fail-fast panics is applied consistently, panic recovery around user callbacks is thorough, and metrics/tracing follow a clear kit-wide convention with cardinality guards. The findings are correspondingly mostly documentation-vs-behavior drift and consistency polish; the one substantive correctness issue is in the durable saga executor, whose compensation path lacks the detached-context and retry-safety properties its in-memory sibling already implements, undermining the durability guarantee the component exists to provide.

> This is unusually disciplined, defensively written code: pervasive panic recovery around user callbacks, careful lock discipline, validated options that fail fast at construction, and honest doc comments about cooperative-cancellation limits — clearly hardened by multiple audit waves (the FR-xxx annotations). The remaining defects are semantic edge cases rather than systemic flaws: the most significant are the durable saga executor marking failed compensations as done (defeating its crash-recovery purpose) and lacking any per-instance concurrency control, retry.Do converting a successful call into a reported failure on late context cancellation, and the timeout-budget clear path breaking its own concurrent-reservation invariant. Lifecycle, eventbus, cron, batchworker, fanout, bulkhead, and circuitbreaker are solid with only low-severity contract/doc mismatches.

## Findings

