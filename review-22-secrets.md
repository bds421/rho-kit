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
| LOW | 4 |
| **Total (deduplicated)** | **4** |

**Reviewer impressions:**

> This is carefully written, well-documented code: the singleflight, stale-while-revalidate, and stale-on-error paths are thoughtfully bounded (detached contexts, fixed fetch timeout, single-refresh-per-key guard, panic recovery) and the backends map errors to the two sentinels deliberately. The main concern is a concurrency gap in the cache's secret-lifetime management: in-place zeroing of displaced/invalidated entries races with the hit-path copy and can silently return an empty secret, which the copyForCaller comment explicitly (and incorrectly) claims is safe. Secondary issues are minor Loader-contract inconsistencies (gcpsm/vaultkv not wrapping ErrLoaderUnavailable) that weaken the stale-fallback guarantee awssm provides.

> This is a carefully written, security-conscious scope: secret bytes are consistently wrapped in the zeroizable secret.String, callers get independent copies (copyForCaller), errors are routed through the redact helpers, singleflight coalesces stampedes with panic-safe recovery, and constant-time comparison lives in the underlying secret type. No crypto misuse, weak randomness, or secret-value logging was found. The main gaps are a narrow concurrency window where a Get racing an Invalidate can hand back an empty secret, verbatim secret-key/path leakage into errors and logs that contradicts the kit's own redact convention, a WithProject scoping bypass for fully-qualified GCP keys, and a contract inconsistency in how malformed-payload errors are (not) mapped to ErrLoaderUnavailable.

> This is thoughtfully engineered, well-documented code: the caching layer correctly reasons about single-flight coalescing, panic-poisoning, detached fetch contexts, and secret zeroization, and the three backends share a clean, consistent Loader shape with good godoc. The main gap is a subtle concurrency ordering bug where the cache reveals its entry buffer outside the lock (copyForCaller), so a concurrent Invalidate or completed refresh can hand a caller an empty secret with no error; secondary issues are cross-backend inconsistency in honoring the documented ErrLoaderUnavailable wrapping contract and a non-compiling doc example. Overall quality is high for a security-sensitive package, but the copyForCaller/lock interaction deserves a fix and a concurrent regression test.

> This is high-quality, security-conscious code: secret bytes are consistently wrapped in a zeroizable secret.String, error causes are redacted via the redact package before crossing trust boundaries, metrics carry no key labels, and only the (non-secret) key name is logged. The main risk is a concurrency invariant in CachedLoader: because callers read and Reveal the shared cache buffer after releasing the mutex while background refresh/Invalidate zero that same buffer, a caller can occasionally receive a silently-empty secret. A couple of minor contract/consistency gaps (ErrLoaderUnavailable wrapping, key in un-redacted error prefixes) round out the findings.

> This scope is unusually well-engineered for a secrets layer: the singleflight, stale-while-revalidate, panic-recovery, and secret-zeroization concerns are carefully thought through and heavily commented, and the redact wrapping keeps backend error text out of logs. The main gaps are contract-consistency ones — gcpsm and vaultkv don't wrap non-not-found errors in ErrLoaderUnavailable the way awssm does and the Loader contract mandates — plus a narrow but real race where Invalidate (or a completing refresh) can zero a cache entry a concurrent Get is about to copy, silently handing back an empty credential. Concurrency primitives are otherwise sound with no data races or goroutine leaks found.

> This is a carefully engineered, well-documented package: the CachedLoader's stale-while-revalidate, single-flight coalescing, panic-recovery, and secret-zeroization story are unusually thorough, and the godoc explains the tricky invariants. The main weaknesses are a cross-backend inconsistency where gcpsm/vaultkv break the stated Loader error-wrapping contract that awssm upholds, and a narrow concurrency window where the stale-fallback path can serve a zeroed secret; the rest are low-severity polish (double-allocation on the hot path, an un-zeroed copy in the rotating provider, a reimplemented stdlib helper).

## Findings

### [LOW] Raw secret key/path embedded into error prefixes and cache logs, contrary to the kit's redact convention

- **Where**: `infra/secrets/awssm/awssm.go:71`
- **Dimension**: security
- **Detail**: All three backends embed the caller-supplied secret key verbatim into the error prefix passed to redact.WrapError/WrapSentinel (awssm.go:71 and :84, gcpsm.go:83 and :113, vaultkv.go:72), and the WrapError prefix is rendered verbatim by Error() — the whole point of WrapError is 'safe to render verbatim across trust boundaries', which the embedded key undermines. CachedLoader also logs slog.String("key", key) verbatim (cache.go:196-198, :267, :283). The redact package's own StringValue doc explicitly classifies 'storage paths' and runtime identifiers as tenant/topology content to reduce to a length stamp. These errors propagate up through NewRotatingProvider to SDKs that may log or surface them, leaking secret-store topology (e.g. 'prod/tenant-b/postgres/api'). Not the secret value itself, hence LOW.
- **Suggestion**: Wrap the key with redact.StringValue(key) in error prefixes and log attributes, or drop it, so tenant-scoped secret paths are not surfaced verbatim across trust boundaries.

### [LOW] Get ignores the caller's context cancellation/deadline while fetching

- **Where**: `infra/secrets/cache.go:178`
- **Dimension**: concurrency
- **Detail**: The singleflight leader detaches the fetch via context.WithoutCancel + fixed fetchTimeout (10s) at line 178, and waiters block in singleflight.do on call.wg.Wait() (singleflight.go:34) with no ctx select. Neither leader nor waiter honors the caller's own ctx. Failure scenario: a caller passes ctx with a 200ms deadline expecting fast failure; on a cache miss with a slow/hung backend, Get blocks up to the full 10s fetchTimeout regardless of the caller's deadline, defeating caller-side timeout budgeting on a hot path. This is partly intentional (avoiding one caller poisoning coalesced waiters) but the caller-visible Get still silently disregards ctx.
- **Suggestion**: Return early when the caller's ctx is Done (select on ctx.Done() vs a channel signalling flight completion) even though the shared fetch continues on the detached context.

### [LOW] copyForCaller allocates and copies the secret twice on every cache hit

- **Where**: `infra/secrets/cache.go:302`
- **Dimension**: performance
- **Detail**: copyForCaller runs on the hottest path (the package's own doc: "every DB connection asks for the current password"). It calls src.Value.Reveal() which already allocates a fresh copy of the plaintext (secret.go:100-102), then passes that to secret.New(b) which allocates and copies a SECOND time (secret.go:69-71), then zeroes the intermediate. So each hit does two allocations plus two full copies of the secret bytes. Functionally correct but avoidable churn on a hot path.
- **Suggestion**: Add a Clone method on secret.String (single internal copy under one RLock) and call it here, eliminating the double allocation.

### [LOW] isNotFound matches on substring of error text, risking misclassification

- **Where**: `infra/secrets/vaultkv/vaultkv.go:103`
- **Dimension**: error-handling
- **Detail**: After the structured `*api.ResponseError` StatusCode==404 check, isNotFound falls back to `strings.Contains(msg, "secret not found")` / `"Code: 404"` on the raw error string. A transport/proxy error whose message happens to contain 'Code: 404' (or a wrapped upstream 404 unrelated to the KV path) would be classified as ErrSecretNotFound instead of ErrLoaderUnavailable, so CachedLoader skips stale-fallback (cache.go:187) and the caller sees a spurious not-found for a reachable-but-erroring backend.
- **Suggestion**: Prefer the structured *api.ResponseError check and, if a text fallback is kept, tighten it (e.g. require the Vault-specific 'metadata not found' wording rather than a generic 'Code: 404').

