# Code review: Testing kits (stage 1 — unverified findings)

## Scope

- **Directories**: testing/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 1 (lenses inferred: design; expected lens count: 2)
- **WARNING**: only 1/2 review lenses completed — coverage is partial.
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

> The Testing kits scope is high quality: the kittest module is almost entirely thin, well-documented re-exports, and the integration tests show real care about flakiness — most have deliberately replaced fixed sleeps with readiness polling (createTopic/waitForConsumerGroupAssignment, Eventually loops) and correctly guard shared state with mutexes/atomics/sync.Once. The residual issues are localized: a few tests hardcode names on the shared RabbitMQ broker (contradicting the helper's own contract and coupling tests together), one stream handler drops the sync.Once guard its sibling test documents, and a couple of container-lifecycle/error paths lean on Ryuk instead of prompt teardown. None are production-severity, but the shared-broker naming and unguarded wg.Done are worth fixing to keep CI deterministic.

## Findings

