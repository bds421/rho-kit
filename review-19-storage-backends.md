# Code review: Storage backends (stage 1 — unverified findings)

## Scope

- **Directories**: infra/storage/s3backend, infra/storage/gcsbackend, infra/storage/azurebackend, infra/storage/sftpbackend, infra/storage/storagehttp/uploadsec
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 9 (lenses inferred: correctness, design, security; expected lens count: 3)
- Status: raw reviewer findings; adversarial verification (stage 2) pending.

## Summary

| Severity | Count |
|---|---|
| CRITICAL | 0 |
| HIGH | 0 |
| MEDIUM | 1 |
| LOW | 10 |
| **Total (deduplicated)** | **11** |

**Reviewer impressions:**

> This is unusually disciplined storage-backend code: consistent constructor/option/metrics patterns across providers, thorough godoc explaining non-obvious decisions (SSE defaults, credential rotation warnings, TOCTOU limits of SFTP symlink checks, capacity-error translation), enforced host-key verification for SFTP, and fail-closed ClamAV scanning with careful spool-file lifecycle handling. The findings are mostly consistency drift between siblings (metrics registration ordering, panic-vs-error on missing bucket, metrics not-found filtering on Copy) plus two real design flaws in the SFTP lister: unbounded in-memory collection before pagination and a whole-listing abort on any symlink. Test coverage is extensive, including edge cases like empty-blob Exists probes and clamd protocol quirks.

> The storage-backend family is unusually well hardened for its security surface: mandatory SSH host-key verification with no fail-open path, defense-in-depth key/prefix validation with traversal rejection, SFTP symlink rejection with honestly documented TOCTOU limits, fail-closed ClamAV scanning with clean infected/error separation, strict HTTPS endpoint validation with explicit insecure opt-ins, and consistent secret redaction in logs and errors. No exploitable injection, auth-bypass, or crypto flaws were found; the surviving findings are edge-case robustness issues (SDK retry interaction with the scan spool, S3 error misclassification, an undocumented validator-bypass via presigned PUTs) plus minor polish items.

> This scope is well above average: consistent per-backend contracts (not-found as control flow, capacity translation, redacted error wrapping, metrics parity), careful credential hygiene (LogValue redaction, weak-credential rejection, mandatory known_hosts for SFTP), and thoughtful hostile-server defenses (symlink rejection, GCS generation pinning, bounded clamd responses, fail-closed scanning). The S3, GCS, Azure, and ClamAV packages are close to production-clean; the defects cluster in sftpbackend, whose hand-rolled connection lifecycle (lock-held dials, grace-period connection reaping, Close races) and collect-then-yield List iterator carry the two highest-severity bugs (yield-after-stop panic and non-overwriting Rename).

> This scope is unusually high quality and heavily hardened against the assigned lens: SFTP mandates known_hosts host-key verification with no insecure fallback, all backends validate keys/prefixes against path traversal and control characters, credentials are consistently redacted from logs/errors/spans, endpoints are forced to https behind an explicit dev-only insecure opt-in, and the clamav validator fails closed (rejecting the upload) on malware, scanner-unavailable, and every ambiguous or truncated clamd response. I found no exploitable authn/authz bypass, no crypto misuse, no fail-open scan path, and no key/PII leakage into metrics or traces. The single observation below is a low-severity defense-in-depth note about the default plaintext transport to clamd.

> This scope is high-quality, defensively written code: credential handling avoids logging secrets (LogValue redaction), SFTP host-key verification is mandatory via known_hosts, ClamAV fails closed on every scanner/oversize/protocol error, and path/key sanitization plus symlink rejection are thorough. Concurrency in the SFTP backend is carefully synchronized (generation-tracked cleanup goroutines joined on Close). The main real defect is a narrow Close-vs-operation race in sftp getClient that can return a nil client and panic; the remaining findings are edge-case liveness/error-fidelity gaps.

> This is high-quality, defensively written code: host-key verification is mandatory (no InsecureIgnoreHostKey), key validation rejects traversal/control/format runes, SFTP adds symlink-ancestor rejection with an honestly documented TOCTOU caveat, and the clamd INSTREAM parser fails closed on unrecognized/partial responses. The main risks are subtle lifecycle/ergonomic issues rather than sanitization gaps: the clamav replay reader violates the ReadSeeker contract by removing its backing file on EOF (fragile under SDK checksum/retry rewinds), and the SFTP List buffers whole trees. Metrics-registration and capacity-classification inconsistencies between the four backends are minor but worth aligning.

> This is mature, carefully-reasoned code: credential handling redacts secrets, SFTP enforces known_hosts host-key verification and symlink/path-escape rejection, and clamav fails closed on scanner errors and unexpected responses. The dominant concern in my lens is the SFTP reconnect path, which holds the exclusive lock across a non-context-aware ssh.Dial, serializing all operations and shutdown during a reconnect, and the clamav removeOnEOF replay reader, whose close-and-delete-on-EOF behavior is unsafe for seek-back consumers like the S3 SDK. The remaining issues are low-severity error-classification/observability polish.

> This is a well-hardened, security-conscious scope: SFTP host-key verification is mandatory (knownhosts required in both Validate and buildSSHConfig, no InsecureIgnoreHostKey), the ClamAV validator fails closed on every non-clean verdict and defends against overflow/truncation, credentials are redacted via LogValue and zeroed after key parsing, and path handling adds symlink-ancestor rejection on top of key validation. Most exported items carry thorough godoc, and error/metric classification is consistent across providers. The findings are mostly quality/consistency issues rather than exploitable flaws; the one with real operational teeth is the SFTP List design, which buffers the whole subtree and makes keyset pagination quadratic.

> This is a notably hardened, defense-in-depth scope: keys are strictly validated (no traversal, control/format runes rejected), credentials are redacted in logs via slog.LogValuer and never embedded in wrapped errors, SFTP mandates known_hosts host-key verification with no InsecureIgnoreHostKey/InsecureSkipVerify anywhere, temp suffixes use crypto/rand, and the clamav scanner fails closed on every error path (only a literal \"OK\"/\": OK\" verdict is treated as clean, size/limit/EOF conditions all surface ErrScannerUnavailable). The main weakness is in the SFTP recursive List: the directory walk trusts server-supplied entry names and swallows the relPath \"escapes root\" error, so the RootPath containment that the rest of the package works hard to enforce (symlink rejection, depth caps) can be bypassed by a plain \"..\" entry, and matching objects are fully buffered regardless of MaxKeys.

## Findings

### [MEDIUM] Reconnect cleanup closes the old connection under in-flight transfers (immediately under flapping, after 5s otherwise)

- **Where**: `infra/storage/sftpbackend/sftp.go:275`
- **Dimension**: concurrency
- **Detail**: connect() replaces the client and spawns a cleanup goroutine that closes the old sftp.Client/ssh.Conn after at most 5 seconds — or immediately when another reconnect bumps cleanupGen (the `for b.cleanupGen.Load() == gen` loop exits straight to closeOld). An operation that obtained the old client via getClient and is streaming a large Put/Get for longer than the grace period has its connection closed mid-transfer, failing with a generic remote error; under server flapping the grace shrinks to ~0, so even short in-flight operations race with the close. The code comments acknowledge the 5s heuristic but the generation-bump early-close makes the window effectively unbounded downward.
- **Suggestion**: Reference-count client leases (getClient returns a release func; cleanup closes when the count drains) instead of a fixed grace period.

### [LOW] AccountName is interpolated into the default endpoint host without format validation

- **Where**: `infra/storage/azurebackend/azure.go:119`
- **Dimension**: security
- **Detail**: New (azure.go:119) and NewWithTokenCredential (azure.go:160) build the endpoint as fmt.Sprintf("https://%s.blob.core.windows.net", cfg.AccountName), but Config.Validate only checks the account name is non-empty. A value containing '.', '/', '@', or ':' silently changes the request host (e.g. AccountName "evil.com/x" yields https://evil.com/x.blob.core.windows.net), redirecting all storage traffic — including signed requests — to an unintended host. Explicit Endpoint overrides go through storage.ValidateEndpointURL, so this default-endpoint path is the only place an unvalidated string becomes a URL.
- **Suggestion**: Validate AccountName against the Azure storage account grammar (^[a-z0-9]{3,24}$) in validateCommon before it is embedded in a URL.

### [LOW] Listing capability inconsistent across sibling storage backends

- **Where**: `infra/storage/gcsbackend/gcs.go:26`
- **Dimension**: api-design
- **Detail**: s3backend and sftpbackend implement storage.Lister (they have list.go with a compile-time _ storage.Lister assertion), but gcsbackend and azurebackend do not implement List at all — the gcs Backend only asserts storage.Storage (line 26). Azure even carries NewListBlobsFlatPager in its BlobClient interface but wires it solely into Healthy. A caller writing backend-agnostic code that type-asserts storage.Lister will silently get no listing on GCS/Azure, an easy-to-hold-it-wrong asymmetry between providers that are otherwise presented as interchangeable.
- **Suggestion**: Either implement Lister for gcsbackend and azurebackend for parity, or document explicitly in each package's doc.go that listing is unsupported so the capability gap is discoverable.

### [LOW] s3 Copy counts expected source-not-found as an operation error, breaking the cross-backend metrics contract

- **Where**: `infra/storage/s3backend/copy.go:59`
- **Dimension**: bug
- **Detail**: Copy passes the raw err to observeOp ('b.metrics.observeOp(b.instance, "copy", start, err)'), while Get/Delete/Exists route through s3MetricErr specifically so expected not-found does not inflate storage_s3_operation_errors_total. Comments across the backends (e.g. azure.go line 319, gcs.go line 235) declare the dashboard contract that expected not-found must not count as errors 'consistently across providers'. A storage.Move over a missing source — normal control flow returning ErrObjectNotFound — will therefore bump the copy error counter and skew error-rate alerts.
- **Suggestion**: Filter through a not-found-aware helper, e.g. observeOp(..., func() error { if isCopySourceNotFound(err) { return nil }; return err }()).

### [LOW] Generic S3 "InvalidRequest" with a declared size is misclassified as ErrInsufficientCapacity

- **Where**: `infra/storage/s3backend/s3.go:352`
- **Dimension**: error-handling
- **Detail**: translateS3Capacity maps any smithy APIError with code "InvalidRequest" to storage.ErrInsufficientCapacity whenever meta.Size>0 (lines 351-354). InvalidRequest is one of S3's most overloaded error codes (bad headers, missing Content-SHA256, malformed SSE parameters, unsupported operations, etc.), and most of those requests carry a non-zero body size. Those failures would then satisfy apperror.IsStorageFull and page operators to a STORAGE_FULL runbook — the same misrouting the code explicitly avoids for ServiceUnavailable (lines 334-339).
- **Suggestion**: Drop the InvalidRequest branch or gate it on a more specific signal (e.g. an S3 message substring), and let generic InvalidRequest fall through to the safe wrapper as a normal backend error.

### [LOW] sftpbackend.New has no context-aware variant; eager connect hardcodes context.Background

- **Where**: `infra/storage/sftpbackend/sftp.go:172`
- **Dimension**: api-design
- **Detail**: New performs an eager SSH dial with b.connect(context.Background()) (line 172), so a caller cannot bound startup connection time with its own ctx or cancel a hung eager connect beyond the fixed 10s sshConnectTimeout — and a configured PasswordProvider is invoked under a Background-derived ctx rather than the caller's startup deadline. s3backend addresses the same problem with a NewContext constructor and documents why (s3.go lines 123-135); sftpbackend, whose constructor does strictly more remote I/O (TCP dial + SSH handshake + SFTP subsystem + optional password fetch), offers no equivalent.
- **Suggestion**: Add NewContext(ctx, cfg, opts...) mirroring s3backend, delegating New to it with context.Background().

### [LOW] sftpRemoteError discards the underlying cause, dropping context-cancellation identity

- **Where**: `infra/storage/sftpbackend/sftp.go:504`
- **Dimension**: error-handling
- **Detail**: For non-validation errors sftpRemoteError returns a fresh fmt.Errorf('sftpbackend: %s failed') that does not wrap err (line 504). When an SFTP op fails due to context cancellation/deadline (or any other cause), the returned error no longer satisfies errors.Is(err, context.Canceled/DeadlineExceeded) or any typed check, so callers cannot distinguish a cancelled operation from a generic remote failure. This is an intentional redaction choice but it silently strips error identity that callers may rely on for retry/backoff decisions.
- **Suggestion**: Preserve the sentinel where it is safe — e.g. detect ctx.Err()/context sentinels and wrap them, or attach a typed classification — while still redacting topology-leaking strings.

### [LOW] Put temp files are visible to List and orphaned on crash

- **Where**: `infra/storage/sftpbackend/sftp.go:570`
- **Dimension**: bug
- **Detail**: Put writes to remotePath + ".tmp-" + suffix in the destination directory. List/walkDir has no filter for these names, so a concurrent List during an upload yields phantom keys like 'dir/file.tmp-a1b2c3d4e5f6a7b8' (Get on them later races the rename and returns not-found). If the process dies between Create and Rename, the temp file is orphaned permanently — no sweep exists, and the orphan then appears in every subsequent listing.
- **Suggestion**: Filter the temp-name pattern out of walkDir results (and reject it in ValidateKey-adjacent logic), and document the orphan behavior or add a best-effort startup sweep for stale '.tmp-*' files older than a threshold.

### [LOW] Healthy() never attempts connection, so WithLazyConnect plus CriticalHealthCheck can deadlock readiness

- **Where**: `infra/storage/sftpbackend/sftp.go:825`
- **Dimension**: api-design
- **Detail**: Healthy() returns false whenever b.connected is false (sftp.go:824-828) and never triggers connect(). A backend built with WithLazyConnect stays disconnected until its first storage operation. If such a backend is wired into CriticalHealthCheck, the readiness endpoint returns 503 forever, the orchestrator never routes traffic, no operation ever runs, and the connection is never established — a permanent not-ready deadlock with no error logged.
- **Suggestion**: Either have Healthy()/the health check attempt a (rate-limited) lazy connect, or document that WithLazyConnect must not be combined with CriticalHealthCheck before the first operation.

### [LOW] clamd INSTREAM defaults to plaintext TCP

- **Where**: `infra/storage/storagehttp/uploadsec/clamav/clamav.go:272`
- **Dimension**: security
- **Detail**: defaultNetwork is "tcp" and Scan dials clamd with no transport encryption. When clamd runs on a separate host (a common deployment), the full upload payload (which may contain PII/sensitive files being scanned) and the scan verdict traverse the network in cleartext, and an on-path attacker could tamper with the verdict to force a clean result. Mitigations exist (WithNetwork("unix") for a local socket, or WithDialer for a TLS/mTLS-wrapped conn), but the out-of-the-box default is unencrypted. Failure scenario: clamd deployed on a peer node reachable over a shared L2/L3 segment; an attacker sniffs uploaded documents or MITMs the socket to rewrite an infected verdict to "stream: OK".
- **Suggestion**: Document that remote clamd must be reached over a private/trusted path, prefer a unix socket, or provide a first-class TLS dialer helper; consider warning at construction when network=="tcp" and the address is non-loopback.

### [LOW] Scan timeout (conn deadline) does not bound reads from the upload body

- **Where**: `infra/storage/storagehttp/uploadsec/clamav/clamav.go:301`
- **Dimension**: resource-leak
- **Detail**: WithScanTimeout is documented as bounding 'the whole dial/write/read scan exchange', and the ctx deadline is applied only to the clamd conn (SetDeadline, line 279). streamBody reads from the caller-supplied body with no deadline (line 301). A reader that stalls indefinitely (never returns) blocks in body.Read before any conn.Write, so the conn deadline is never reached and Scan hangs past scanTimeout; a pathological reader returning (0, nil) repeatedly spins the loop (n==0 -> readErr==nil -> continue) burning CPU with no timeout. The primary StorageValidator path spools to a local temp file first, so it is unaffected; the exposure is limited to direct Scanner.Scan callers passing a live network reader.
- **Suggestion**: Either document that body must be a non-blocking/bounded reader, or wrap body reads with a ctx-aware reader (or run streamBody in a goroutine cancelled by ctx) so scanTimeout actually bounds the body read.

