# Code review: Security package (stage 1 — unverified findings)

## Scope

- **Directories**: security/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 15 (lenses inferred: correctness, design, security; expected lens count: 3)
- Status: raw reviewer findings; adversarial verification (stage 2) pending.

## Summary

| Severity | Count |
|---|---|
| CRITICAL | 0 |
| HIGH | 0 |
| MEDIUM | 0 |
| LOW | 15 |
| **Total (deduplicated)** | **15** |

**Reviewer impressions:**

> This is an unusually well-hardened security package family: constant-time comparisons, length-prefixed MAC inputs, symmetric-key/alg-confusion filtering, confused-deputy (iss/aud) enforcement by construction, SSRF IP-pinning with per-dial re-validation, timing floors, fail-closed staleness windows, and careful redaction of secrets/paths in logs are all present and clearly documented with prior audit references (FR-xxx, wave 66). The remaining issues are second-order: a lifecycle gap in API-key rotation (revoked keys can be rotated back to life, and a failed rotation orphans an active record), and a handful of low-severity consistency gaps (IPv6 blocklist completeness, a test repository without locking, an unsanitized actor claim, and a PEM parse path that skips the JWKS hygiene filters).

> The security family is unusually well-engineered: constant-time comparisons, fail-closed staleness windows, atomic snapshot swaps, length-prefixed MAC inputs, and defensive option validation are applied consistently, and the code is heavily documented with threat rationale. The verified defects are almost all edge-case or contract-mismatch issues (a test-repository data race, a rotation path that ignores revocation, doc-vs-behavior gaps around TLS hardening and goroutine lifecycle) rather than exploitable flaws in the hot verification paths; csrf, session, ssrf, and the core JWT verify pipeline held up under close reading.

> This is an unusually well-engineered security scope: constant-time comparisons, fail-closed staleness windows, confused-deputy guardrails required by construction, length caps on every attacker-facing input, and extensive rationale-bearing godoc referencing prior audit findings. The defects that remain are mostly at the API-polish level — dead or inert options (session.WithClock, WithScopedHashTarget/NeedsRehash), doc/code drift in the apikey scoped-key surface, a concurrency-safety inconsistency between the two in-memory repositories, and formatting/file-size hygiene in jwtutil and identity. Test coverage of the tricky paths (rotation expiry math, CSRF MAC ordering, JWKS staleness) is genuinely good.

> This is unusually high-quality security code: consistently defensive (constant-time compares, length caps, fail-closed staleness windows, alg-confusion and confused-deputy guards), with exceptional godoc that documents threat-model rationale and prior audit findings inline. The findings are mostly polish-level: a couple of genuine design gaps (Rotate resurrecting revoked keys, a dead WithClock option, an unlocked in-memory repository inconsistent with its sibling) and several LOW smells. Minor hygiene note: several files in scope (security/identity/*, security/session/session.go, security/jwtutil/jwtutil.go, security/jwtutil/revocation/revocation.go) are not gofmt-clean, suggesting a lint gap.

> This is unusually high-quality security code: consistently fail-closed, defensively documented (concurrency contracts, confused-deputy rationale, timing side channels), with correct use of atomics/snapshots for hot-path key material (Provider.mu + atomic counters, FilesCertificateSource's atomic.Pointer snapshot, SigningProvider's Close/Sign ordering) and constant-time comparisons throughout csrf/session/apikey. The surviving findings are mostly edge-case gaps between documented guarantees and code (KeySet policy snapshot, jwksHTTPClient TLS-floor claim, rotatedExpiry fallback) plus one unsynchronized test helper; nothing suggests systemic carelessness.

> This is one of the most security-conscious Go codebases I have reviewed: constant-time comparisons, HMAC-first CSRF verification with length-prefixed MAC inputs, symmetric-key filtering and typ pinning in JWT verification, fail-closed revocation and stale-JWKS handling, TLS 1.2+ floors enforced even on caller-supplied transports, and a genuinely thorough SSRF guard (IP pinning, redirect re-validation, NAT64/Teredo/6to4 ranges). The findings that survived verification are mostly edge-case lifecycle and API-contract gaps — revoked-key resurrection via Rotate and the plaintext fail-open on partially configured TLSConfig being the two worth fixing promptly — rather than exploitable crypto or injection flaws. Documentation of threat rationale inline (FR-xxx audit references) is exemplary and made adversarial review markedly easier.

> This is an unusually well-crafted security scope: constructors enforce invariants (issuer/audience required by default, HTTPS-only JWKS, minimum secret lengths), errors are typed sentinels with careful redaction, hostile-input bounds are everywhere, and the godoc frequently explains the threat model behind each decision. Test coverage is broad across all eight subpackages, including fuzz tests for SSRF. The findings are correspondingly modest — the most substantive are a policy-dropping KeySet() snapshot footgun, an unsynchronized in-memory repository that contradicts its sibling's concurrency contract, and a non-atomic Rotate; the rest are consistency and hot-path polish items.

> This is unusually well-hardened security code: constant-time comparisons throughout, HMAC-first verification ordering in csrf, JWKS symmetric-key/alg-confusion filtering, mandatory issuer/audience opt-outs at Provider construction, TLS 1.2+ floors with InsecureSkipVerify rejection, fail-closed stale-key windows, thorough SSRF IP-range coverage (including NAT64/Teredo/6to4), and careful redaction of secrets and paths from logs and errors. The evident weak spot is the apikey lifecycle surface, where Rotate can resurrect revoked/expired credentials and the mutating Manager operations lack owner scoping, plus a few consistency gaps (state-before-secret error ordering, single-key session signer) relative to the standards the rest of the family sets for itself.

> This is unusually high-quality security code: constant-time comparisons, clock injection, length-prefixed MAC inputs, atomic snapshot swaps for hot-reload paths, fail-closed staleness windows, and extensive rationale comments referencing prior audit findings (FR-nnn, R4, wave 66). Concurrency discipline is strong throughout (atomic.Pointer snapshots, single-writer refresh loops, documented set-once fields). The issues found are lifecycle and API-consistency gaps — key-rotation resurrecting revoked credentials, a policy-less KeySet snapshot, and a couple of silently dead options — rather than exploitable core flaws.

> This scope is high-quality security code: constant-time comparisons, injected clocks, atomic snapshot swaps for hot-rotatable TLS, careful context-scoped refresh loops with proper ticker/goroutine cleanup, and unusually thorough doc comments that spell out the threat model behind each guard. Concurrency primitives (atomic.Pointer, RWMutex, sync.Once, root-context cancellation) are used correctly and I found no deadlocks, response-body leaks, or goroutine leaks. The issues that remain are edge-case correctness bugs — an in-memory revoke that cannot move a revocation earlier, a rotation-expiry fallback that misfires on zero CreatedAt, a non-thread-safe test repository, and sub-second TTL truncation — rather than systemic flaws.

> This is a mature, unusually well-reviewed security scope: constructors fail closed (panic on missing issuer/audience), verification paths are constant-time and defense-in-depth layered, SSRF/TLS/JWKS hardening is thorough, and godoc is exceptionally detailed with audit/threat-model references. The findings are mostly ergonomic/misuse-resistance polish rather than exploitable flaws — the notable ones being a no-op session.WithClock option, a KeySet.Verify path that bypasses the Provider's issuer/audience enforcement, and a rotation-expiry fallback whose documented 'unknown CreatedAt' case it does not actually cover.

> This is an exceptionally well-hardened security package: fail-closed defaults throughout, constant-time comparisons for MACs/secrets, asymmetric-only JWT verification with explicit alg-confusion and confused-deputy (issuer/audience) mitigations, thorough SSRF IP filtering with DNS-rebinding-safe pinned transports, TLS 1.3 mTLS with hot reload, path-free error/log redaction, and a deliberate JWT verify-timing floor. I found no authn/authz bypass, injection, crypto-misuse, or fail-open path. The remaining items are low-severity defense-in-depth gaps and minor consistency issues (one API constructor skipping the alg-confusion normalization its sibling enforces, a couple of small information/timing oracles, and one uncovered deprecated IPv6 range).

> This is unusually mature, defense-in-depth security code: constant-time comparisons, alg-confusion filtering, confused-deputy (aud/iss) guardrails, SSRF pinning with rebinding/redirect defenses, hot-rotatable TLS, and thorough godoc that explains the threat model behind each choice. No CRITICAL or HIGH provable defect surfaced in this scope; the findings are a genuine logic gap in key-rotation expiry, a dead/misleading clock option, a concurrency/consistency gap in a memory repo, and minor smells. Overall quality is high and the exported APIs are largely misuse-resistant.

> This is a mature, security-conscious package: constant-time comparisons, clock injection for determinism, atomic snapshot swaps for hot-reloading TLS and JWKS, fail-closed defaults, and careful documentation of concurrency contracts (e.g. KeySet set-once fields, SigningProvider Close/Sign ordering). The concurrency primitives in jwtutil and netutil (atomic.Pointer snapshots, stopOnce/closeOnce, timeout-scoped refresh loops) are correct and well-reasoned. The findings are concentrated in the apikey lifecycle logic — an emergency-revoke that is silently swallowed when a rotation already scheduled a future revocation, and a rotated-key expiry calculation whose zero-CreatedAt fallback doesn't behave as its own comment claims — plus a couple of lower-severity gaps.

> This scope is high-quality, security-focused code with extensive defense-in-depth: constant-time comparisons, symmetric-key/alg-confusion filtering in JWKS parsing, mandatory issuer/audience pinning enforced by constructor panics, robust SSRF IP/CIDR filtering with DNS-rebinding-safe pinned transports, TLS 1.2/1.3 floors, fail-closed revocation and stale-key handling, and careful redaction of secrets/paths from logs and errors. The issues found are minor: an unsafe default on the lower-level KeySet.Verify API and two timing/ordering oracles on credential paths that the kit elsewhere explicitly avoids. No critical injection, fail-open, or crypto-misuse defects were identified.

## Findings

### [LOW] ScopedResolver.Resolve returns fast on unknown lookup prefix, giving a prefix-existence timing oracle

- **Where**: `security/apikey/scoped.go:208`
- **Dimension**: security
- **Detail**: Resolve calls repo.ActiveByPrefix(lookup) (line 208) and returns immediately on ErrScopedNotFound, performing no hash work; only an existing prefix reaches the bcryptcompat.Verify call (line 219). This is the exact kid-existence timing side channel that the sibling jwtutil package went to lengths to close with verifyTimingFloor, so the inconsistency is notable. Exploitability here is very low: the lookup prefix is 8 random alphanumerics (~47 bits), so enumerating valid prefixes by timing is infeasible, and an attacker still needs the secret to authenticate.
- **Suggestion**: Consider a constant-time floor or a decoy bcrypt verify on the not-found path to avoid leaking prefix existence, matching the jwtutil verify-timing-floor pattern.

### [LOW] ScopedResolver.Resolve returns repository backend errors verbatim to the auth path

- **Where**: `security/apikey/scoped.go:210`
- **Dimension**: error-handling
- **Detail**: The error from repo.ActiveByPrefix is returned unwrapped (line 210). Unlike the package's sentinel errors (ErrScopedNotFound, ErrRevoked, ErrExpired, ErrInvalidSecret), an infrastructure failure (SQL/Redis driver error) flows straight to transport middleware; error mappers that render err.Error() into 401/500 response bodies or logs can leak backend topology (DSN fragments, table names) from the authentication hot path. The sibling revocation package explicitly classifies errors to avoid this (revocation.go errorClass, line 384).
- **Suggestion**: Wrap non-not-found repository errors in a stable package sentinel (e.g. fmt.Errorf("apikey: lookup scoped key: %w", err)) so callers can distinguish auth-decision errors from infrastructure errors without exposing the raw message.

### [LOW] Duplicated token parser: parseScopedToken re-implements Parse

- **Where**: `security/apikey/scoped.go:254`
- **Dimension**: smell
- **Detail**: parseScopedToken (lines 254-260) is a near-verbatim copy of Parse (apikey.go lines 158-170): both strings.Split on '_', require exactly 3 parts, check parts[0]==prefix and non-empty parts[1]/parts[2], returning a malformed-token sentinel. Two copies of the same wire-format invariant can drift (e.g. a future field-count change applied to only one), and the wire format `<prefix>_<id>_<secret>` is exactly the invariant most needs a single source of truth.
- **Suggestion**: Extract one internal splitter both callers use, or have parseScopedToken delegate to Parse.

### [LOW] scanFileImports silently swallows parse errors, understating ASVS import evidence with no signal

- **Where**: `security/asvs/imports.go:253`
- **Dimension**: error-handling
- **Detail**: A file that fails parser.ParseFile returns (nil, nil) — deliberately, per the comment — but unlike ScanDir's text scanner (which does surface read errors), a syntactically-broken or generics-edge-case file contributes zero import claims with no indication anywhere in ImportReport. Failure scenario: a service's main.go (where kit middleware imports typically live) has a parse error under the toolchain version kit-doctor was built with; ScanImports reports those controls as Missing and the compliance report claims a weaker posture than reality, with nothing telling the operator a file was skipped.
- **Suggestion**: Count skipped files (e.g. ImportReport.SkippedFiles []string) so kit-doctor can render "N files could not be parsed" alongside the evidence table.

### [LOW] EvidenceSummary returns a slice of anonymous structs on an exported API

- **Where**: `security/asvs/imports.go:325`
- **Dimension**: api-design
- **Detail**: ImportReport.EvidenceSummary() returns []struct{ ID ID; Evidence Evidence }. Callers (kit-doctor renderers) cannot name this type in their own function signatures, variables of struct type must repeat the literal definition, and the anonymous type cannot grow a field without breaking every consumer that spelled it out. The repetition inside the function body (the type is written three times) shows the cost even locally.
- **Suggestion**: Introduce `type EvidenceEntry struct { ID ID; Evidence Evidence }` and return []EvidenceEntry.

### [LOW] A single source line over 1 MiB aborts the entire ScanDir walk

- **Where**: `security/asvs/scan.go:105`
- **Dimension**: error-handling
- **Detail**: scanFile uses bufio.Scanner with a 1 MiB max buffer (line 96). One generated .go file containing a line longer than that (embedded assets, generated tables — common in real service repos) makes scanner.Err() return bufio.ErrTooLong, which asvsFileError masks as the generic "asvs: scan source file failed" and ScanDir returns an error for the whole tree (line 82), so kit-doctor produces no ASVS report at all instead of skipping the pathological file. The masked error also hides which file caused it.
- **Suggestion**: Treat bufio.ErrTooLong as skip-this-file (annotations never legitimately live on megabyte lines), or raise the buffer and include the file path in the error.

### [LOW] ApplyJWTActor accepts arbitrary claim values as audit actor strings without sanitization

- **Where**: `security/identity/jwt.go:27`
- **Dimension**: security
- **Detail**: The subject is strictly normalized to a UUID, but the actor value taken from ServiceActorClaim/ActorClaim is used verbatim — no length bound, no control-character or whitespace rejection. identity.Format concatenates it into the audit actor string ("service:<actor>"), so a federated or misbehaving issuer that signs a claim containing newlines or very long values can inject line breaks / bloat into audit and action logs. This is notably inconsistent with the same repo's revocation package, which rejects control characters and caps lengths on everything destined for keys/logs.
- **Suggestion**: Validate the claim value in ApplyJWTActor (reject control chars/whitespace, cap length, e.g. reuse a validPart-style helper) and fall back to the UUID subject or fail closed when it is malformed.

### [LOW] jwtutil.go is a 1292-line god file spanning several unrelated concerns

- **Where**: `security/jwtutil/jwtutil.go:1`
- **Dimension**: smell
- **Detail**: One file contains UUID/subject normalization (IsUUID, NormalizeSubjectID), Claims parsing and claim-shape validation, KeySet/JWKS filtering, JOSE typ enforcement, hardened HTTP client construction (transport cloning, TLS floor, redirect blocking, content-type parsing with a hand-rolled eqIgnoreCase), Provider lifecycle/refresh loop, staleness policy, and the error taxonomy. At 1292 lines it exceeds the repo's own ~800-line ceiling and makes the verification hot path hard to review in isolation; the sibling signing_provider.go shows the package already knows how to split by concern.
- **Suggestion**: Split into subject.go (IsUUID/NormalizeSubjectID — subject_test.go already exists), claims.go, keyset.go, httpclient.go, and provider.go without changing the exported API.

### [LOW] KeySet's exported mutable policy fields rely on a doc-comment invariant instead of construction

- **Where**: `security/jwtutil/jwtutil.go:110`
- **Dimension**: api-design
- **Detail**: KeySet.ExpectedIssuer/ExpectedAudience are exported, mutable fields read by Verify without synchronisation; the type doc carries a long warning ("Set them once, before the KeySet is shared ... assigning a field ... is an unsynchronised data race that silently changes verification policy"). The safe path (Provider carrying policy) exists, but nothing prevents the documented misuse — a caller can still toggle fields on a shared *KeySet per request and get racy, policy-bleeding verification. Invariants this security-critical should be unrepresentable rather than documented.
- **Suggestion**: In a future API revision make the fields unexported with a WithExpectedIssuer/Audience-style constructor or a KeySet.WithPolicy(iss, aud) copy method (KeySet() already returns snapshots, so a copy-on-write setter is cheap).

### [LOW] ParseKeySetFromPEM skips the public-key normalization and use/key_ops filtering that ParseKeySet enforces

- **Where**: `security/jwtutil/jwtutil.go:223`
- **Dimension**: api-design
- **Detail**: ParseKeySet routes every JWKS entry through verificationKey(), which rejects symmetric keys, checks use/key_ops/alg, and calls PublicKey() so no private material is retained in the verifier. ParseKeySetFromPEM does none of this: if an operator misconfigures the signer's private-key PEM into the verifier (an easy swap of two mounted files), jwk.ParseKey happily wraps the private key and the verifier KeySet then carries live signing material in memory for the process lifetime, with verification still succeeding so the mistake is invisible. No filtering on use/key_ops is applied either.
- **Suggestion**: In ParseKeySetFromPEM, call key.PublicKey() before adding to the set (and optionally run the same verificationKey() filter) so a mistakenly supplied private key is reduced to its public part.

### [LOW] requireExpectedJWTType re-parses the entire JWS on every successful verification

- **Where**: `security/jwtutil/jwtutil.go:339`
- **Dimension**: performance
- **Detail**: After jwt.Parse has already fully parsed and signature-verified the token, verifyToken calls requireExpectedJWTType(tokenString), which runs jws.Parse over the whole compact serialization again — re-base64-decoding the header, payload, and signature and allocating a second message structure per verified request on the auth hot path. Only the protected header's typ field is needed; the payload/signature decode work is wasted. Failure scenario: none functional, but every authenticated request pays roughly double the parse allocation cost of verification.
- **Suggestion**: Decode just the protected header (split tokenString at the first '.', base64-decode, unmarshal the small header JSON, or use jws.Parse on the header segment only), or check typ via a jwt.ParseOption/validator hook so the token is parsed once.

### [LOW] populateStringClaims rebuilds the claim-name slice and dedup map on every verification

- **Where**: `security/jwtutil/jwtutil.go:412`
- **Dimension**: performance
- **Detail**: For each verified token, populateStringClaims allocates a fresh seen map and a fresh names slice via append(append([]string(nil), defaultStringClaims...), extra...), even though defaultStringClaims and Provider.extraStringClaims are fixed at construction time. On the request-path hot loop this is two avoidable heap allocations per token (plus the map buckets). Failure scenario: none functional; steady per-request allocation pressure in the auth middleware path.
- **Suggestion**: Precompute the deduplicated, empty-filtered claim-name list once (at Provider construction, or lazily with sync.Once) and iterate over it directly; only allocate c.stringClaims when a claim is actually present (already done).

### [LOW] Hand-rolled string helpers and error-text matching where stdlib idioms exist

- **Where**: `security/jwtutil/jwtutil.go:904`
- **Dimension**: smell
- **Detail**: jwksFetchErrorKind classifies failures with strings.Contains(err.Error(), "jwks endpoint returned unexpected content-type") / "jwks endpoint returned " — brittle string coupling to messages defined 300 lines away in fetch(); a wording tweak silently reclassifies metrics/log kinds to "fetch_failed". Nearby, eqIgnoreCase (line 704) reimplements strings.EqualFold and isJSONContentType (line 680) reimplements mime.ParseMediaType's parameter stripping. Neither path is hot enough (one fetch per ~10 min) to justify avoiding the stdlib.
- **Suggestion**: Introduce sentinel errors (errJWKSBadStatus, errJWKSUnexpectedContentType) in fetch() and match with errors.Is; replace eqIgnoreCase with strings.EqualFold and the manual media-type trimming with mime.ParseMediaType.

### [LOW] "stale-rejected" is counted per read, inflating a metric named jwks_fetch_failures_total

- **Where**: `security/jwtutil/jwtutil.go:1055`
- **Dimension**: api-design
- **Detail**: keySetWithReason increments fetchFailStaleRejected on every call made while the keyset is stale. Since Provider.Verify/VerifyContext and Provider.KeySet call it on every token verification and health probe, the counter grows at request rate during a JWKS outage, while the http/parse reasons in the same metric grow at fetch-attempt rate (every ~10 min). A dashboard or alert built on jwks_fetch_failures_total{reason="stale-rejected"} sees values orders of magnitude larger than the other reasons of the "same" counter, and its magnitude reflects traffic, not fetch health.
- **Suggestion**: Either count stale transitions once per fetch cycle, or split the per-request rejection into its own metric (e.g. jwt_verifications_rejected_total{reason="jwks_stale"}) and keep jwks_fetch_failures_total strictly fetch-scoped.

### [LOW] session.HMACSigner has no key-rotation overlap, unlike sibling csrf.Issuer

- **Where**: `security/session/session.go:114`
- **Dimension**: api-design
- **Detail**: csrf.NewIssuerWithSecrets deliberately supports a current-plus-previous secret ring so HMAC secrets can rotate without invalidating outstanding tokens. session.NewSigner accepts exactly one root key and Verify checks only that key, so rotating the session root instantly invalidates every live session across the fleet — there is no overlap-window API and no doc note warning about it. Failure scenario: an operator rotates the shared root secret following the csrf package's rotation pattern and every logged-in user is force-logged-out at once.
- **Suggestion**: Mirror the csrf design: accept previous roots for verify-only (NewSignerWithRoots(current, previous...)), signing always with the current key; or explicitly document that rotation is a global session reset.

