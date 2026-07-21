# Code review: HTTPX core (stage 1 — unverified findings)

## Scope

- **Directories**: httpx/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 15 (lenses inferred: correctness, design, security; expected lens count: 3)
- Status: raw reviewer findings; adversarial verification (stage 2) pending.

## Summary

| Severity | Count |
|---|---|
| CRITICAL | 0 |
| HIGH | 0 |
| MEDIUM | 0 |
| LOW | 10 |
| **Total (deduplicated)** | **10** |

**Reviewer impressions:**

> The HTTPX core family is exceptionally well-hardened for its security surface: fail-closed authz middleware with panic recovery, constant-time HMAC comparison with domain separation on cursors, strict singleton identity-header parsing, TLS floors that panic on InsecureSkipVerify, redirect/URL-join helpers that defend against encoded scheme-relative and dot-segment tricks, and consistent non-leaky error mapping. Numerous comments reference prior hostile-review waves, and it shows — the classic vulnerability classes are all explicitly closed. The surviving findings are second-order design gaps: the outbound budget transport's hard-enforcement refund can be looped into unbounded upstream spend, and the webhook dispatcher's HMAC scope (body+timestamp only) doesn't authenticate the delivery ID it advertises for replay protection.

> The HTTPX core family is unusually high-quality, defensively engineered Go: constructors panic on wiring mistakes, trust-boundary headers get strict singleton handling, RoundTripper body-close contracts are honoured on every early-return path, and doc comments explain the threat model behind each default. The verified issues are concentrated in edge-case semantics rather than mainline behaviour — the circuit breaker's excluded-error-as-success accounting and the budget wrapper's refund-on-reject loop are the two findings with real production consequences, while the rest are API polish (a documented-but-dead Extensions field, a placeholder export, operationId collisions) and small smells. Test coverage across the scope is thorough, including fuzz tests for the security-sensitive cursor and redirect parsers.

> The HTTPX core scope is unusually high quality: defensive nil/option validation at construction, correct RoundTripper body-close discipline on every early-return path, careful lock ordering (the mcp async-audit stop/enqueue RWMutex+atomic protocol and the openapigen double-checked marshal cache are both correct), bounded drains/timeouts everywhere, and thorough documentation of intentional contract deviations. The issues that survive scrutiny are edge-case behavioral bugs rather than structural flaws — the two MEDIUMs (slohttp +Inf JSON encode failure, budget hard-enforcement refund enabling repeated zero-cost upstream spend) both fire specifically under the degraded conditions those features exist to handle. No exploitable security flaws, deadlocks, or resource leaks were found in scope.

> This scope is exceptionally hardened for its threat surface: constant-time HMAC comparisons, TLS 1.2 floors that panic on InsecureSkipVerify, redirects blocked by default, singleton trust-boundary header parsing, fail-closed authz with panic recovery, bounded body drains and cursor/body size caps throughout — the code shows evidence of many prior adversarial review rounds. The surviving issues are second-order design gaps rather than direct injection or bypass bugs: the outbound budget's hard-enforcement refund undermines its own spend cap, and the webhook package over-promises on its unauthenticated delivery-ID replay protection. Documentation is unusually thorough and generally matches behavior.

> This is unusually high-quality, defensively written HTTP infrastructure: constructors validate aggressively, security invariants (TLS floors, redirect blocking, HMAC-signed cursors, singleton identity headers, panic-recovering extractors) are enforced by construction, and the comments record the rationale from multiple prior adversarial review waves. The findings that remain are mostly polish — dead placeholders, a misleadingly unused parameter, one god file — plus one substantive accounting flaw in httpx/budget where the hard-enforcement refund path zeroes out the charge for work the upstream already performed, undermining the package's spend-control purpose, and an openapigen option-composition footgun that silently drops a response schema. Test coverage across the scope is broad and targets the tricky paths (signed cursors, reconcile refunds, real-server behaviours).

> The HTTPX core family is unusually high-quality, defensively written code: RoundTripper body-close contracts are honored on every early-return path, contexts are detached-and-bounded for cleanup work (budget refunds, audit appends), check-then-send races are correctly locked (mcp async audit queue), and prior hostile-review fixes are pinned with explanatory comments. The issues found are concentrated at policy/edge seams rather than core logic: the budget transport's hard-enforcement refund defeats its own spend-control purpose, and the openapigen default-response probe creates spec/runtime drift; the rest are shutdown races, retry misclassification, and comment/behavior drift. Concurrency primitives (mutexes, atomics, worker drain on Stop) were traced and are sound.

> The HTTPX core scope is unusually high quality: pervasive fail-fast validation, careful RoundTripper body-close contract handling, correctly locked double-checked caches (openapigen.Spec), a well-reasoned async audit worker pool in mcp whose Stop/enqueue race was explicitly designed out with an RWMutex-over-atomic pattern, and panic-isolation around every user-supplied extractor. Concurrency primitives are used correctly almost everywhere; the surviving findings are mostly edge-case classification/accounting bugs rather than structural defects. The one substantive issue is the outbound budget transport's hard-enforcement refund path, which undermines the package's own spend-control guarantee at exactly the moment it should bind.

> This scope is unusually high quality for a review: consistent fail-closed defaults (redirects blocked, TLS 1.2 floor with an InsecureSkipVerify panic, strict header singleton validation, constant-time HMAC comparison, signed cursors, panic-recovering authz extractors), extensive threat-model-referencing comments, and evidence of prior hostile-review waves closing edge cases. The findings that remain are second-order: an accounting hole in the outbound budget transport's hard-enforcement path, one unredacted secret-bearing log field in the webhook dispatcher, and audit-invariant gaps in the MCP server's async/strict interplay. No injection, crypto misuse, authz bypass, or TLS weaknesses were found.

> The HTTPX core family is unusually high quality for its size: constructors fail fast on misconfiguration, security invariants (TLS floors, redirect blocking, cursor signing, header singleton checks, audit fail-closed) are enforced by construction and thoroughly documented, and every package in scope has a test file. The findings are mostly edge-case accounting/contract slips and polish items; the one substantive issue is the httpx/budget hard-enforcement refund path, which undoes the pre-charge after the upstream has performed paid work and thereby re-opens the exact spend-control gap the package exists to close. A secondary theme is doc/API drift (openapigen doc vs. wave-162 behaviour, DecodeJSON's cap advice, placeholder exports) that would mislead a careful consumer of the kit.

> This is unusually high-quality, security-conscious code: exhaustively documented, defensively written, with many prior hostile-review fixes baked in (nil-guards, body-close contracts on RoundTripper error paths, constant-time cursor compares, detached cleanup contexts, HTTP/2 hardening). Invariants are mostly enforced by construction and idioms are consistent with the Go stdlib and across sibling packages. The findings are concentrated in misuse-resistance gaps (undocumented ListFn limit+1 contract, WithDefaultAmount(0), 429 retry policy) and minor polish rather than outright bugs.

> The HTTPX core scope is unusually high quality: consistently fail-closed (authz, MCP strict audit, cursor/HMAC verification, budget pre-charge), correct crypto usage (HMAC-SHA256, crypto/subtle constant-time compares, crypto/rand nonces, secret zeroing), and strong input hardening (redirect open-redirect guards, urlutil path-traversal escaping, body size caps, singleton trust-boundary headers, TLS floor with InsecureSkipVerify panic). Nearly every risky decision is documented with prior hostile-review references. The only concrete gap is an inconsistency in webhook.go where the full delivery URL is logged unredacted, plus two minor documented/hardening notes.

> The HTTPX core scope is of very high quality: RoundTripper wrappers (resilient/circuit-breaker, deadline-budget, budget, sign) carefully handle body-close semantics, context detachment for cleanup accounting, and error wrapping/unwrapping; the mcp async-audit worker pool and openapigen Spec cache use correct lock/atomic discipline with well-reasoned shutdown ordering. Concurrency, error-handling, and time handling are consistently sound, with extensive comments documenting prior hostile-review fixes. The only defects I found are minor contract/validation gaps (a missing request-body close on one early error path in sign, and an incomplete mount-time URL check in healthhttp); no correctness or concurrency bugs of consequence surfaced.

> This scope is unusually high quality and security-conscious: outbound signing zeroes secrets and uses crypto/rand nonces, cursor pagination uses HMAC-SHA256 with constant-time compare and domain separation, SafeRedirect and urlutil handle encoded/backslash traversal and scheme-relative bypasses carefully, authz reads r.RemoteAddr (never X-Forwarded-For) for trusted-proxy checks and fails closed, TLS floors reject InsecureSkipVerify, and error/audit paths consistently redact and avoid reflecting caller bytes. I found no injection, crypto-misuse, authz-bypass, or fail-open defects. The two issues noted are defense-in-depth gaps in outbound/SSRF handling and a mount-time validation omission, both with documented or low-impact context.

> This is unusually careful, defensively-written code: the transport wrappers (resilient circuit breaker, deadline budget, budget/sign RoundTrippers) correctly honor the http.RoundTripper body-close contract, error paths are fail-closed, and security-sensitive helpers (cursor signing, redirect validation, urlutil path escaping) are backed by fuzz tests. Godoc is thorough and often explains prior review findings. The issues I found are minor API-ergonomics and dead-code polish rather than correctness or security defects; the most notable is a documented-but-non-functional Extensions escape hatch in openapigen.

> This is exceptionally high-quality, defensively-written code: RoundTripper body-close contracts, context detachment for accounting, constant-time HMAC comparison, integer-overflow guards in pagination, and fail-closed audit invariants are all handled carefully and thoroughly documented. I traced every concurrency-sensitive path (circuit-breaker error unwrapping, deadline-budget cancel-on-close, MCP async audit stop/enqueue race, cursor-signer Close vs Use) and verified them against the vendored MCP SDK's internal locking and the mutex-guarded secret.String — I found no correctness, race, deadlock, resource-leak, or error-handling defects at MEDIUM or above. The only issues are cosmetic/robustness nits.

## Findings

### [LOW] Upstream-controlled actual-cost header is trusted without an upper bound

- **Where**: `httpx/budget/budget.go:370`
- **Dimension**: security
- **Detail**: reconcile() parses the response's actual-cost header (lines 361-375) and charges any non-negative int64 delta against the tenant's budget. There is no sanity cap relative to the estimate or a configured maximum, so a compromised or misbehaving upstream can report e.g. actual=9e18 and, in audit-only mode, silently drain the entire per-key budget in one response (in hard mode the same response also denies the caller the payload). The estimate header on the request side deliberately falls back to a default on junk, but the reconciliation side has no equivalent guard. Failure scenario: a proxy or upstream bug duplicates cost units (tokens vs. characters), one response instantly exhausts every tenant's budget and all subsequent requests fail with ErrBudgetExceeded — a self-inflicted denial of service driven entirely by one untrusted-in-practice header.
- **Suggestion**: Add an optional WithMaxActual (absolute or multiple-of-estimate) cap; log and clamp deltas exceeding it rather than charging them blindly.

### [LOW] Logger discriminates 'unset' from 'set' by identity-comparing against slog.Default()

- **Where**: `httpx/logger.go:40`
- **Dimension**: bug
- **Detail**: Logger falls back via `if l := logging.FromContext(ctx); l != slog.Default()`. logging.FromContext returns slog.Default() when nothing is stored, so this identity comparison is the only 'was it set' signal. Failure scenario: middleware stores the current default logger explicitly (logging.WithContext(ctx, slog.Default()) — which WithContext itself does when handed nil), then slog.SetDefault is called later; alternatively a caller deliberately stores slog.Default(); in both cases the stored logger compares equal to slog.Default() and Logger silently returns the fallback argument instead of the context logger, so request-scoped attrs configured on the default-derived logger are dropped. It is also racy against concurrent slog.SetDefault between FromContext's internal Default() call and the comparison.
- **Suggestion**: Have logging expose FromContext returning (logger, ok) or a LoggerFromContextOnly helper, and branch on presence rather than pointer identity.

### [LOW] Strict-audit invariant bypassable when tenant extraction fails between precheck and recordActionLog

- **Where**: `httpx/mcp/actionlog.go:117`
- **Dimension**: error-handling
- **Detail**: recordActionLog re-extracts the tenant (line 116) and returns nil — no entry written, no error surfaced — when extraction now fails (`!ok || tenantID == ""`, lines 117-119). auditPrecheck validated the tenant before dispatch, but if the caller-supplied tenantExtractor panics (extractTenant recovers to "", false) or returns differently on the second call, the tool has already executed and its result is returned to the caller with no audit entry, even in strict mode where the documented invariant is "every executed tool call produces a signed entry". Failure scenario: a tenantExtractor backed by a request-scoped cache that is evicted mid-call panics on the post-execution lookup; a destructive tool call completes successfully with zero audit trail and no error.
- **Suggestion**: Resolve tenant (and actor) once in auditPrecheck and thread the resolved values through to recordActionLog, or in strict mode return an error (like errAuditActorMissing) instead of nil when the tenant cannot be re-resolved.

### [LOW] mcp.go is a 1150-line god file mixing six concerns

- **Where**: `httpx/mcp/mcp.go:1`
- **Dimension**: smell
- **Detail**: mcp.go carries the option surface (~25 ServerOption/ToolOption constructors), server lifecycle + async audit worker pool, generic tool registration and schema resolution, SDK dispatch wrapping (wrapToolHandler), reason sanitisation, and error-mapping — six distinguishable concerns in one file at 1150 lines, well past the repo's own many-small-files guidance (200-400 typical, 800 max). Audit plumbing was already split into actionlog.go, showing the intended decomposition; the rest never followed. This makes the trickiest code in the package (wrapToolHandler's audit-ordering invariants at lines 948-1065) harder to review in isolation.
- **Suggestion**: Split along the existing seams: options.go (Server/Tool options), register.go (Register + schema resolution/validation), dispatch.go (wrapToolHandler, callHandlerSafely, mapErrorForCaller, errorResult), audit worker pool into actionlog.go.

### [LOW] Hardcoded SDK Implementation version "v0.1.0" and 1150-line god file

- **Where**: `httpx/mcp/mcp.go:432`
- **Dimension**: smell
- **Detail**: NewServer always advertises `Version: "v0.1.0"` in the MCP Implementation handshake (mcp.go:430-433) while the module ships as v2.3.1 — every kit-based MCP server reports the same stale version to clients, defeating client-side capability/version diagnostics, and there is no option to override it. Separately, mcp.go is 1150 lines mixing option plumbing, schema resolution, name validation, dispatch, and error mapping — well past the repo's own file-size conventions and hard to review; actionlog.go shows the natural seam already exists.
- **Suggestion**: Derive the version from the module (or add a WithServerInfo option), and split mcp.go into options/registration/dispatch files.

### [LOW] Register leaves kit catalog inconsistent if SDK AddTool panics after the slot is reserved

- **Where**: `httpx/mcp/mcp.go:700`
- **Dimension**: error-handling
- **Detail**: Register reserves the kit slot (s.toolMeta[name] and s.tools append) under s.mu and releases the lock, then calls s.sdk.AddTool outside the lock. AddTool panics on a non-object schema or missing input schema. Current code pre-validates schemas via requireObjectSchema/validateSchemaOverride so this is unreachable today, but if a future SDK version or an override path introduces a panic, the kit catalog already advertises the tool (Tools() lists it, re-Register returns 'already registered') while the SDK never received it, and the panic unwinds the caller. Latent robustness gap rather than an active bug.
- **Suggestion**: Either perform s.sdk.AddTool before committing the kit-side catalog entry, or wrap the AddTool call and roll back the reserved toolMeta/tools entry on failure.

### [LOW] Validation error text reflected verbatim to MCP caller

- **Where**: `httpx/mcp/mcp.go:1123`
- **Dimension**: security
- **Detail**: mapErrorForCaller returns ve.Error() to the MCP client when a handler returns a validation error carrying Fields. The comment asserts these carry only field names, but nothing enforces that: a handler that constructs a validation error whose field Message embeds caller-supplied bytes or internal detail (e.g. echoing an offending value or a downstream error string) will have that text surfaced back to the tool caller, unlike every other error class which collapses to a generic string. This depends on handler-authored messages, so risk is low.
- **Suggestion**: Document the invariant as a handler contract, or sanitize/strip free-form text from field messages before reflecting them.

### [LOW] Webhook dispatcher SSRF guard is scheme-only by default

- **Where**: `httpx/webhook/webhook.go:213`
- **Dimension**: security
- **Detail**: validateURL only rejects non-http(s) schemes; it performs no private/link-local/metadata-IP filtering. Because Send POSTs a signed body (carrying the valid X-Kit-Signature HMAC) to a caller-/customer-supplied URL, the default configuration allows SSRF to internal services (169.254.169.254 metadata, internal admin APIs) unless the operator remembers to wire an SSRF-aware transport into Config.HTTPClient. This is explicitly documented as intentional with a named mitigation, so it is a hardening/unsafe-default note rather than a defect.
- **Suggestion**: Consider defaulting to (or prominently offering a one-liner for) an SSRF-safe transport, or accept an allowlist of destination hosts at construction.

### [LOW] All transport errors from HTTPClient.Do are classified retryable, including deterministic permanent failures

- **Where**: `httpx/webhook/webhook.go:247`
- **Dimension**: error-handling
- **Detail**: attempt() wraps every `d.cfg.HTTPClient.Do(req)` error as retryable(...) with no discrimination. With the kit's recommended clients (httpx.NewResilientHTTPClient / NewHTTPClient), redirects are blocked by default, so a receiver that deterministically answers 3xx surfaces as a Do error (url.Error wrapping ErrRedirectBlocked) and is retried through the entire retry policy with full backoff, even though every attempt is guaranteed to fail identically. The same applies to TLS certificate-verification failures and circuitbreaker.ErrCircuitOpen (retrying while the breaker is open just burns the retry budget). Compare: 3xx HTTP responses that survive to a status code are correctly classified permanent (line 281), so the behavior is inconsistent depending on whether the client follows or blocks the redirect.
- **Suggestion**: Classify known-permanent transport errors (ErrRedirectBlocked, x509 errors) as permanent, or expose a RetryIf hook so callers can refine the default.

### [LOW] 3xx delivery responses fall into the mislabelled "webhook 4xx; giving up" branch, and blocked redirects are retried as transport errors

- **Where**: `httpx/webhook/webhook.go:267`
- **Dimension**: error-handling
- **Detail**: attempt() classifies only >=500 as retryable and everything else non-2xx as the "4xx" branch, so a 301/302 from a receiver is logged as "webhook 4xx; giving up" (line 276) — misleading for operators. Worse, with the doc-recommended kit clients (NewResilientHTTPClient / NewHTTPClient), redirects are blocked in CheckRedirect, so client.Do returns an error (line 245-247) that is wrapped retryable(); a receiver doing a deterministic http→https 301 upgrade then burns the entire retry budget (default retry.DefaultPolicy backoffs) before failing, on every Send.
- **Suggestion**: Add an explicit 3xx branch (permanent, correctly-worded log), and detect ErrRedirectBlocked in the Do error path to mark it permanent instead of retryable.
