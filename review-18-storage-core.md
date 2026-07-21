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
| LOW | 13 |
| **Total (deduplicated)** | **14** |

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

### [LOW] DeleteMany's combined error discards the key→error association

- **Where**: `infra/storage/batch.go:111`
- **Dimension**: api-design
- **Detail**: batchError iterates only map values (line 110-112) and joins errors wrapped as "delete object", so the keys that failed are not recoverable from the returned error. A caller of storage.DeleteMany that gets a partial failure cannot tell which of up to 1000 keys still exist without re-probing every key, and the redacted wrap typically hides key information from the message too — unlike the BatchDeleter interface itself, which deliberately returns map[string]error.
- **Suggestion**: Include the key in each wrapped error (static-safe formatting) or return the failures map alongside the error.

### [LOW] EncryptedStorage.Put forwards caller meta to the backend without CloneObjectMeta

- **Where**: `infra/storage/encryption/encryption.go:317`
- **Dimension**: smell
- **Detail**: Every other decorator Put clones metadata before handing it to the wrapped backend — circuitbreaker.Put and retry.Put use storage.CloneObjectMeta(meta), hooks.Put clones opMeta, copy.go/migrate.go clone via CloneObjectMeta. encryption.Put (line 317) instead passes the caller-supplied meta value directly to e.backend.Put after only setting meta.Size. Because ObjectMeta.Custom is a reference map, the caller's Custom map is aliased through to the backend; a backend or downstream validator that mutates meta.Custom in place would corrupt the caller's map. The inconsistency also makes this the one Put in the decorator set that violates the codebase's otherwise-uniform copy-before-forward invariant.
- **Suggestion**: Clone before forwarding: build putMeta := storage.CloneObjectMeta(meta); putMeta.Size = int64(len(ciphertext)); e.backend.Put(ctx, key, bytes.NewReader(ciphertext), putMeta).

### [LOW] Encrypted List unconditionally subtracts GCM overhead from every listed object's size

- **Where**: `infra/storage/encryption/encryption.go:500`
- **Dimension**: bug
- **Detail**: encryptedLister.list rewrites every yielded object's Size by subtracting the fixed 28-byte GCM overhead whenever Size >= 28 (lines 500–502), assuming every object under the prefix was written through this encryption layer. If the same bucket/prefix also holds objects written directly to the underlying backend (mixed usage, pre-existing data, or objects put by another writer), their reported sizes are silently reduced by 28 bytes, so callers relying on List sizes (e.g. quota accounting, progress bars via MigrateCount-style flows) see wrong values with no error. The >=28 guard only prevents underflow, not the mis-sizing.
- **Suggestion**: Document that the encrypted namespace must not be mixed with plaintext writes, or persist an encryption marker in object metadata and only adjust Size for objects that carry it.

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

### [LOW] Dead no-op zero-time normalization in ServeFile's seekable path

- **Where**: `infra/storage/storagehttp/serve.go:134`
- **Dimension**: smell
- **Detail**: Lines 133-135 read `modTime := meta.LastModified; if modTime.IsZero() { modTime = time.Time{} }` — assigning the zero value when the value is already zero is a no-op. It suggests an intended-but-missing behavior (e.g. substituting a sentinel so http.ServeContent skips Last-Modified handling, which it already does for zero times) and misleads readers into thinking something is being normalized.
- **Suggestion**: Delete the conditional and pass meta.LastModified directly to http.ServeContent.

### [LOW] Read error while discarding a non-file part is swallowed

- **Where**: `infra/storage/storagehttp/upload.go:161`
- **Dimension**: error-handling
- **Detail**: `n, _ := io.Copy(io.Discard, io.LimitReader(part, limit+1))` discards the io.Copy error. If reading a skipped multipart part fails (truncated body, client disconnect, malformed part boundary), the error is dropped and the loop continues to the next NextPart() call. In practice the corruption usually resurfaces on the subsequent mr.NextPart(), but relying on that is fragile and the swallow makes a mid-part transport failure indistinguishable from a cleanly-skipped part. It is inconsistent with the rest of the function, which wraps every other multipart error via storage.WrapSafe.
- **Suggestion**: Capture the io.Copy error and return a wrapped error (e.g. storage.WrapSafe("read non-file part", err)) instead of discarding it.

### [LOW] Chain's final rewind returns the raw Seek error, unlike the redacted per-validator rewind

- **Where**: `infra/storage/storagehttp/uploadsec/uploadsec.go:217`
- **Dimension**: error-handling
- **Detail**: In Chain, the per-validator rewind at line 206-207 wraps Seek failures with redact.WrapError("uploadsec: rewind body", err), but the final rewind (lines 216-217) returns the error verbatim. When the body is a spooled *os.File (the expected way to satisfy io.ReadSeeker for multipart uploads, and what clamav's spool does), a Seek failure is an *os.PathError whose message embeds the temp-file path — leaking server filesystem detail into an error that upload handlers commonly echo into responses/logs, inconsistent with the package's redaction discipline everywhere else.
- **Suggestion**: Return redact.WrapError("uploadsec: rewind body", err) on the final rewind as well.

### [LOW] AllowSVG buffers the entire body unbounded, unlike every other validator in the package

- **Where**: `infra/storage/storagehttp/uploadsec/uploadsec.go:507`
- **Dimension**: resource-leak
- **Detail**: AllowSVG does io.ReadAll(body) with no limit (line 507) and then holds a second full copy from SanitizeSVG. The raster path in the same file deliberately caps buffering at imageBodyReadLimit (32 MiB) to bound per-request memory, but an SVG upload spooled to a large seekable body buffers its full size twice in RAM. Concrete failure: with ParseAndStore configured Unlimited (documented opt-out) or a large MaxFileSize, N concurrent SVG uploads of size S hold ~2·N·S bytes, enabling memory exhaustion the rest of the package explicitly defends against.
- **Suggestion**: Apply an svgBodyReadLimit (LimitReader + over-limit rejection) mirroring validateImageBody.

### [LOW] 64 KiB image-header read limit rejects metadata-heavy JPEGs; inconsistent with the 512 KiB limit in storage.ImageDimensions

- **Where**: `infra/storage/storagehttp/uploadsec/uploadsec.go:573`
- **Dimension**: bug
- **Detail**: checkImageBody and MaxImageDimensions buffer only imageHeaderReadLimit=64 KiB (line 573) before image.DecodeConfig. JPEGs whose SOF marker sits after >64 KiB of APPn metadata (multi-segment XMP, MPF/thumbnail-heavy camera output) fail DecodeConfig on the truncated header and are rejected with ErrInvalidImage / ErrImageTooLarge-adjacent errors despite being valid images. The sibling validator storage.ImageDimensions (infra/storage/validate.go, imageDimensionReadLimit=512 KiB) chose 512 KiB for the same purpose, so the two pipelines disagree on which uploads are valid.
- **Suggestion**: Raise imageHeaderReadLimit to match validate.go's 512 KiB, or fall back to the full (already 32 MiB-bounded) body when DecodeConfig fails on the truncated header.

### [LOW] Predictable known_hosts path in shared os.TempDir() is symlink-attackable

- **Where**: `infra/storage/storagetest/sftp.go:123`
- **Dimension**: security
- **Detail**: writeKnownHostsFile writes to filepath.Join(os.TempDir(), "rho-kit-sftp-known-hosts-"+port) with os.WriteFile (sftp.go:123-127). On a shared CI/dev host, /tmp is world-writable and the name is predictable once the mapped port is observable: a local attacker can pre-create a symlink at that path (os.WriteFile follows symlinks and truncates, clobbering an attacker-chosen file writable by the CI user) or pre-own the path so the test's 0600 write of trusted host-key material can be influenced. Integration-test-only code, hence LOW, but it runs on shared runners.
- **Suggestion**: Write the known_hosts file with os.CreateTemp (random name, O_EXCL) or under t.TempDir()-style private directories instead of a fixed name in os.TempDir().

