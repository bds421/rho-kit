# Code review: Secrets management (stage 1 — unverified findings)

## Scope

- **Directories**: infra/secrets/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 6 (lenses inferred: correctness, design; expected lens count: 3)
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

> This is carefully written, well-documented code: the singleflight, stale-while-revalidate, and stale-on-error paths are thoughtfully bounded (detached contexts, fixed fetch timeout, single-refresh-per-key guard, panic recovery) and the backends map errors to the two sentinels deliberately. The main concern is a concurrency gap in the cache's secret-lifetime management: in-place zeroing of displaced/invalidated entries races with the hit-path copy and can silently return an empty secret, which the copyForCaller comment explicitly (and incorrectly) claims is safe. Secondary issues are minor Loader-contract inconsistencies (gcpsm/vaultkv not wrapping ErrLoaderUnavailable) that weaken the stale-fallback guarantee awssm provides.

> This is a carefully written, security-conscious scope: secret bytes are consistently wrapped in the zeroizable secret.String, callers get independent copies (copyForCaller), errors are routed through the redact helpers, singleflight coalesces stampedes with panic-safe recovery, and constant-time comparison lives in the underlying secret type. No crypto misuse, weak randomness, or secret-value logging was found. The main gaps are a narrow concurrency window where a Get racing an Invalidate can hand back an empty secret, verbatim secret-key/path leakage into errors and logs that contradicts the kit's own redact convention, a WithProject scoping bypass for fully-qualified GCP keys, and a contract inconsistency in how malformed-payload errors are (not) mapped to ErrLoaderUnavailable.

> This is thoughtfully engineered, well-documented code: the caching layer correctly reasons about single-flight coalescing, panic-poisoning, detached fetch contexts, and secret zeroization, and the three backends share a clean, consistent Loader shape with good godoc. The main gap is a subtle concurrency ordering bug where the cache reveals its entry buffer outside the lock (copyForCaller), so a concurrent Invalidate or completed refresh can hand a caller an empty secret with no error; secondary issues are cross-backend inconsistency in honoring the documented ErrLoaderUnavailable wrapping contract and a non-compiling doc example. Overall quality is high for a security-sensitive package, but the copyForCaller/lock interaction deserves a fix and a concurrent regression test.

> This is high-quality, security-conscious code: secret bytes are consistently wrapped in a zeroizable secret.String, error causes are redacted via the redact package before crossing trust boundaries, metrics carry no key labels, and only the (non-secret) key name is logged. The main risk is a concurrency invariant in CachedLoader: because callers read and Reveal the shared cache buffer after releasing the mutex while background refresh/Invalidate zero that same buffer, a caller can occasionally receive a silently-empty secret. A couple of minor contract/consistency gaps (ErrLoaderUnavailable wrapping, key in un-redacted error prefixes) round out the findings.

> This scope is unusually well-engineered for a secrets layer: the singleflight, stale-while-revalidate, panic-recovery, and secret-zeroization concerns are carefully thought through and heavily commented, and the redact wrapping keeps backend error text out of logs. The main gaps are contract-consistency ones — gcpsm and vaultkv don't wrap non-not-found errors in ErrLoaderUnavailable the way awssm does and the Loader contract mandates — plus a narrow but real race where Invalidate (or a completing refresh) can zero a cache entry a concurrent Get is about to copy, silently handing back an empty credential. Concurrency primitives are otherwise sound with no data races or goroutine leaks found.

> This is a carefully engineered, well-documented package: the CachedLoader's stale-while-revalidate, single-flight coalescing, panic-recovery, and secret-zeroization story are unusually thorough, and the godoc explains the tricky invariants. The main weaknesses are a cross-backend inconsistency where gcpsm/vaultkv break the stated Loader error-wrapping contract that awssm upholds, and a narrow concurrency window where the stale-fallback path can serve a zeroed secret; the rest are low-severity polish (double-allocation on the hot path, an un-zeroed copy in the rotating provider, a reimplemented stdlib helper).

## Findings

_All stage-1 findings for this family are fixed or applied as intentional v2 breaks. See V3_BREAKING_PROPOSALS.md (APPLIED) and git history._
