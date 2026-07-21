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
| LOW | 0 |
| **Total (deduplicated)** | **1** |

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

