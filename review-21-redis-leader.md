# Code review: Redis infra & leader election (stage 1 — unverified findings)

## Scope

- **Directories**: infra/redis/, infra/leaderelection/
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

> This scope is a mature, security-conscious codebase that has clearly been through multiple adversarial review passes: it enforces a TLS 1.2 floor, rejects InsecureSkipVerify and skip_verify, redacts errors and secrets from logs, validates every operator-supplied Prometheus label to prevent cardinality/label injection, and reasons carefully about split-brain/drain windows (the Redis fencing gap is explicitly documented, not hidden). The leader-election electors and the Redis connection lifecycle are well-guarded against fail-open and secret-leak patterns. The one real security-relevant gap is that the individual-fields Redis config path is structurally plaintext-only, coupling 'use host/port/password config' with 'send the password in the clear and disable the auth guard'.

> This scope is unusually well-engineered and heavily documented: the leader-election adapters share a consistent Option/New/Run shape, carefully handle panic-recovery, drain watchdogs, and detached release contexts, and the k8slease TOCTOU coordination is genuinely thoughtful. The main weaknesses are cross-adapter drift in behavior that the packages themselves claim is symmetric — most notably the IsLeader()-during-drain flag not being dropped uniformly, and inconsistent/incorrect drain-duration metric accounting — plus a Redis read-only-failover feature whose automatic trigger path never actually fires. None are data-corruption critical, but the IsLeader and READONLY-detection gaps are real correctness surprises for operators relying on those signals.

> This scope is unusually well-engineered for a concurrency-heavy area: the leader-election backends have careful ctx propagation, bounded release/extend contexts, panic-guarded callbacks, and extensive rationale comments, and the redis Connection health loop and metrics wiring are clean and mutex-disciplined. The main real defect is in k8slease, where the term-mutex serializes the acquired/stopped bookkeeping but not the two IsLeader() atomic stores, so a scheduling race can leave IsLeader() stuck true after a term ends — contradicting the code's own comments. The remaining findings are lower-severity consistency gaps (IsLeader staying true through the drain window in redislock/etcd, and edge behaviours in the redis health/reconnect paths).

> This is a mature, security-conscious scope: the Redis config path enforces a TLS 1.2 floor, rejects InsecureSkipVerify and skip_verify, scrubs credentials from LogValue and error strings, and gates plaintext/passwordless connections behind an explicit opt-out; the leader-election adapters are heavily commented, panic-guard callbacks, redact log fields, and validate metric labels to prevent cardinality/label injection. The strongest issues are consistency gaps in when the local IsLeader() flag is cleared relative to the post-loss callback drain — pgadvisory and k8slease clear it before draining, while etcd and redislock hold it true through a potentially unbounded drain, widening the observable split-brain window. No injection, crypto-misuse, or secret-leak defects were found; findings are limited to the split-brain observability gap and a minor FR-077 credential-check weakness.

> This scope is unusually high-quality and heavily hardened: the leader-election adapters share a consistent structure, guard callback panics, bound Extend/Release with deadlines, use detached release contexts, and the code carries dense comments documenting prior hostile-review fixes. The remaining issues are subtle concurrency/observability edge cases rather than obvious defects — most notably a k8slease leader-flag store ordering that does not actually hold the term lock its own comment claims, and a split-brain-visibility inconsistency where redislock/etcd keep IsLeader() true across the drain window that pgadvisory deliberately closes. The redis infra (connection health loop, FR-077 gating, metric cardinality allowlisting) is solid and I found no correctness defects there.

> This is high-quality, unusually well-documented infrastructure code: the four leader-election backends share a coherent drain-watchdog design, defensively recover from callback panics, bound every backend round-trip with a deadline, and the redis Connection carefully guards its health-state machine against close/ping races. The weaknesses are almost all cross-adapter consistency gaps around the shared Elector contract (Run reusability, OnLost-error handling, early-return semantics, and one metric-labeling divergence) rather than crashes or data loss, plus a ReadOnlyAware capability that is fully specified but never wired into any call site.

## Findings

_All stage-1 findings for this family are fixed or applied as intentional v2 breaks. See V3_BREAKING_PROPOSALS.md (APPLIED) and git history._
