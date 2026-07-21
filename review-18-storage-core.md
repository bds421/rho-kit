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
| MEDIUM | 1 |
| LOW | 4 |
| **Total (deduplicated)** | **5** |

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

### [MEDIUM] Tagger/Versioner/MultipartUploader/BatchDeleter are dead API surface with no implementations, and opaque decorators would silently strip them

- **Where**: `infra/storage/multipart.go:18`
- **Dimension**: api-design
- **Detail**: MultipartUploader (multipart.go), Tagger (tagging.go), Versioner (version.go) and BatchDeleter (batch.go) plus their As* helpers are exported, but no backend in the workspace implements any of them (grep finds only hookedStorage.DeleteMany, a forwarder; s3backend has no multipart/tagging/versioning/batch-delete files despite the docs citing "e.g. S3 multipart upload", "e.g. S3 DeleteObjects"). Worse, the retry, circuitbreaker and encryption decorators are OpaqueDecorators that compose only {Lister, Copier, PresignedStore, PublicURLer} (retry/combinators.go composeRetry), so the moment a backend gains one of these four capabilities it will be invisible through any resilience wrapper: AsBatchDeleter/AsTagger/AsVersioner/AsMultipartUploader stop at the opaque marker and return false, silently degrading (DeleteMany falls back to sequential) or removing the feature.
- **Suggestion**: Either remove/park the unimplemented interfaces until a backend needs them, or implement them in s3backend and extend the decorator composition (and hooks) to forward them; add a compile-time/test guard that every optional interface is forwarded by each decorator.

### [LOW] WithHooks hand-enumerates 16 near-identical combination wrapper types (~450 lines of forwarding)

- **Where**: `infra/storage/hooks.go:66`
- **Dimension**: api-design
- **Detail**: To preserve the {Lister,Copier,PresignedStore,PublicURLer} capabilities of the wrapped backend, WithHooks selects among 2^4 concrete wrapper structs (lines 66-99) each redefining the same List/Copy/PresignGetURL/PresignPutURL/URL forwarders (lines 351-539). This is acknowledged in-code, but it is a genuine cohesion/duplication hazard: adding a new hookable optional capability (e.g. a Tagger or Versioner hook) doubles the matrix to 32 variants, and any change to a forwarder must be replicated across up to 8 structs. The file is 539 lines almost entirely of mechanical forwarding.
- **Suggestion**: Since capability discovery already goes through AsLister/AsCopier/... which walk Unwrap, consider whether the combination structs are needed at all, or generate them, or reduce to per-capability hook shims that don't require the full cross-product.

### [LOW] hooks.go is a 540-line god file with 16 near-identical combinatorial wrapper types

- **Where**: `infra/storage/hooks.go:341`
- **Dimension**: smell
- **Detail**: WithHooks enumerates all 2^4 combinations of {Lister,Copier,PresignedStore,PublicURLer} as 16 concrete wrapper structs (lines 351-539), each re-declaring the same forwarding methods that just call the shared *hookedStorage helpers. This is ~200 lines of mechanical duplication and the single largest file in scope; adding a 5th optional capability doubles it to 32. The rationale is documented, but it is a maintenance/cohesion hazard.
- **Suggestion**: Consider consolidating (e.g. a code-generated table, or exposing capabilities through a single dispatch type) so a new optional interface does not require doubling the wrapper matrix.

### [LOW] The 15-type capability-combination boilerplate is triplicated across hooks, retry, and circuitbreaker

- **Where**: `infra/storage/hooks.go:346`
- **Dimension**: smell
- **Detail**: hooks.go (lines 341-539), retry/combinators.go, and circuitbreaker/combinators.go each hand-enumerate the same 15 {Lister, Copier, PresignedStore, PublicURLer} wrapper structs plus an identical 16-way switch — roughly 700 lines of structurally identical code in three packages. Each file acknowledges the pattern, but the cost is concrete: adding one new forwarded capability (e.g. the Statter suggested for ServeFile, or BatchDeleter forwarding) requires editing 3 × 16 cases and doubling each package's wrapper count, and a missed case in one package silently diverges capability behavior between decorators.
- **Suggestion**: Extract a single internal composition helper (e.g. an internal/compose package with the 16 wrapper types parameterized over a small interface of listImpl/copyImpl/presign*/urlImpl funcs) that all three decorators reuse, so the combination matrix lives in exactly one place.

### [LOW] TOCTOU between ensureRegular (Lstat) and root.Open weakens the symlink-object refusal

- **Where**: `infra/storage/localbackend/local.go:306`
- **Dimension**: security
- **Detail**: Get (and Copy's source open) enforce the "refusing symlink object" contract via ensureRegular's root.Lstat (local.go:299, 410-432), then separately call root.Open(rel) (local.go:306). os.Root follows symlinks that resolve inside the root, so an attacker with write access inside the storage root can swap the regular file for an in-root symlink between the Lstat and the Open; Get then reads through a symlink the contract says is refused (e.g. aliasing another tenant's object stored under the same root). Escapes outside the root remain blocked by os.Root, so impact is limited to cross-key aliasing within the root, but the check-then-act pattern contradicts the file's own TOCTOU rationale for using os.Root.
- **Suggestion**: Open the file first, then fstat the returned handle (f.Stat on the open fd) and reject non-regular results — validating the object actually opened instead of a pre-open Lstat.

