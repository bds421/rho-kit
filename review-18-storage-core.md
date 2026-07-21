# Code review: Storage core (stage 1 — unverified findings)

## Scope

- **Directories**: infra/storage/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 9 (lenses inferred: correctness, design, security; expected lens count: 3)
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

> This is unusually defensive, well-documented storage code: consistent key/metadata validation across backends and decorators, careful io.Reader contract handling (verifyReader, limitReader), os.Root-based TOCTOU-safe local backend, correctly-plumbed semaphores in the encryption layer, and opaque-decorator capability discovery that prevents encryption/breaker bypass. The concurrency primitives (Manager, membackend, circuit breaker iterator gating, retry rewind logic) are all sound; no races, deadlocks, or resource leaks were found in the core paths. The surviving findings are edge-case inconsistencies — contradictory limits between layers (upload wire cap vs skipped-part budget, 64 KiB vs 512 KiB image headers), silent omissions in localbackend listing, and a few error-classification gaps — rather than fundamental defects.

> This scope is unusually well-hardened for infrastructure code: consistent key/prefix/metadata validation at every layer, os.Root-confined filesystem access with explicit TOCTOU reasoning, AAD-bound AES-256-GCM with memory scrubbing and concurrency caps, opaque-decorator capability discovery that prevents presign/public-URL bypass of the encryption layer, and systematic error redaction. The residual weaknesses are concentrated in content-type trust on the upload/serve pipeline (inline serving of attacker-influenced types, SVG through image/* wildcards) rather than in path traversal or crypto, which are handled rigorously. Test coverage and inline security rationale (referencing prior hostile-review waves) indicate a mature, iteratively audited codebase.

> This is an unusually high-quality storage layer: consistent validation at every public entry point, disciplined error redaction, os.Root-based TOCTOU-safe local backend, opaque-decorator semantics that correctly stop capability discovery from bypassing encryption/retry, and thoughtful godoc explaining tradeoffs (streaming-error caveat in ServeFile, checksum/copy interop, dry-run semantics). The issues found are mostly API-surface gaps rather than defects: the uploadsec/storagehttp validator type mismatch with no adapter, four optional interfaces with zero implementations that resilience decorators would silently strip, a Lister contract divergence in localbackend, and some memory/round-trip inefficiencies (local List buffering, full Get on 304). Residual review-tracker artifacts in godoc and the triplicated combinator boilerplate are the main polish items.

> This is an unusually well-hardened storage layer: path traversal is closed via os.Root with per-syscall re-anchoring and TOCTOU-safe temp+rename, keys/prefixes/metadata are validated against control/format runes and traversal, encryption binds the storage key as GCM AAD, errors are consistently redacted, and the polyglot/decompression-bomb defenses in uploadsec are thorough and structurally correct. The residual security concern is not in the low-level primitives but in the defaults of the higher-level HTTP serving/validation surface: ServeFile trusts stored Content-Type and serves inline by default, and the base MIME allowlist admits SVG, so a caller who omits a restrictive validator can end up with stored XSS. No injection, fail-open, weak-crypto, or tenant-isolation defects were found in scope.

> This is an unusually hardened, well-documented codebase: path traversal is handled correctly via os.Root re-anchoring with genuine TOCTOU closure, size enforcement (limitReader) and MIME/polyglot defenses are carefully reasoned, decorator capability discovery honors opaque markers, and resource cleanup (readers, iter.Pull2 stop(), semaphore slots, temp files) is consistently correct across the retry/circuitbreaker/encryption/hooks layers. I found no CRITICAL or HIGH correctness/concurrency defects; the mutex usage in Manager/membackend, context propagation, error wrapping (errors.Is/As), and channel/semaphore patterns are all sound. The only issues are minor, fail-safe gaps concentrated in the HTTP conditional-GET (ETag) handling plus one swallowed error, all LOW severity.

> This is unusually mature, defensively-written code: path traversal is enforced both by ValidateKey and kernel-anchored os.Root, content-type handling in ServeFile is careful (nosniff, header-injection guards, RFC 7232 ETag parsing), and prior hostile-review findings are visibly closed with tests. The strongest remaining issues are performance/resource-lifecycle rather than correctness: localbackend.List buffers the whole subtree regardless of MaxKeys, and the encryption Get semaphore ties a global concurrency limit to caller Close discipline. The 16-variant hooks combinatorial and a stray dead field are minor polish items in otherwise well-factored packages.

> The Storage core is unusually security-mature: path traversal is defended in depth (ValidateKey rejects ..,control/format runes,backslashes plus os.Root kernel-anchored confinement in localbackend), the encryption layer correctly uses AES-256-GCM with the storage key bound as AAD and scrubs key/plaintext buffers, header-injection surfaces (ETag, Content-Disposition, Cache-Control) are validated, and inputs are consistently bounded against DoS. The main residual risk is a design default rather than a coding flaw: ServeFile renders user-controlled/active content types inline, which can become stored XSS depending on how the caller wires uploads and serving origins.

> This is unusually careful, mature code: keys/prefixes/metadata are validated by construction, the localbackend goes to real lengths to close symlink TOCTOU races via os.Root, error wrapping is consistently redacted, and immutability (CloneObjectMeta/CloneCustomMeta) is respected across the copy/migrate/hooks paths. Godoc is thorough and most tricky invariants are explained inline. The findings are mostly edge-case robustness (an integer-overflow panic at MaxInt64, silently-dropped weak ETags) and design/scalability smells (unbounded List buffering, the 16-variant hooks god file) rather than systemic defects.

> This scope is high quality: carefully written, heavily defensive, and evidently hardened by prior hostile-review passes (nil-ctx normalisation, semaphore/ReadCloser leak handling, opaque-decorator capability gating, TOCTOU-safe os.Root filesystem access, and correct iter.Pull2/circuit-breaker resource cleanup are all handled well). Concurrency primitives (Manager RWMutex, membackend locking, atomic temp counter) are sound and I found no data races, deadlocks, or leaked goroutines/bodies. The residual issues are edge-case correctness and scalability concerns in the local backend's listing and the encryption decorator, rather than core-logic defects.

## Findings

_All stage-1 findings for this family are fixed or applied as intentional v2 breaks. See V3_BREAKING_PROPOSALS.md (APPLIED) and git history._
