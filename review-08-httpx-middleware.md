# Code review: HTTPX middleware (stage 1 — unverified findings)

## Scope

- **Directories**: httpx/middleware/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 15 (lenses inferred: correctness, design, security; expected lens count: 3)
- Status: raw reviewer findings; adversarial verification (stage 2) pending.

## Summary

| Severity | Count |
|---|---|
| CRITICAL | 0 |
| HIGH | 0 |
| MEDIUM | 0 |
| LOW | 14 |
| **Total (deduplicated)** | **14** |

**Reviewer impressions:**

> This is an unusually security-mature middleware suite: fail-closed defaults everywhere (tenant required, nonce store mandatory, per-user idempotency scoping enforced at construction), constant-time MAC comparisons with zero-MAC fallbacks, panic containment around every caller-supplied callback, strict singleton-header validation, and audit-finding references (FR-018..FR-032) baked into comments and code. The surviving findings are mostly design trade-offs at trust boundaries (unscoped nonce namespace, X-Real-IP fast path, LRU eviction resetting rate counters) and consistency gaps where one package (apikey, session authenticator) missed the hardening its siblings received, rather than outright vulnerabilities.

> This is an unusually high-quality middleware tree: consistently fail-closed defaults, panic-isolated caller callbacks, strict singleton-header validation, constant-time credential comparison, careful ResponseWriter wrapper semantics (1xx, Hijack, Flush, ReadFrom), and construction-time panics on misconfiguration, all backed by dense and mostly accurate godoc citing prior audit findings. The issues found are edge cases and polish rather than exploitable flaws: the standout items are the idempotency capture writer dropping staged headers on 103 Early Hints, and the apikey package lagging behind the hardening conventions its auth siblings established. Some duplication (Limiter vs KeyedLimiter) and a few stale doc comments are the main maintainability debts.

> This is unusually careful, defense-conscious middleware code: singleton-header enforcement, fail-closed defaults, panic-containment wrappers around every user callback, correct mutex snapshotting in the timeout writer, and well-reasoned lifecycle handling (spooled bodies, 1xx passthrough, hijack detection) are applied consistently across ~70 files, and the signedrequest verifier in particular is close to exemplary (constant-time comparisons, secret zeroing, streaming hash with bounded memory, atomic SetNX replay checks). The most significant defects found are compositional rather than local: the r.Pattern-based route labels in metrics and tracing silently break under the kit's own stack.Default because downstream middleware clones the request, and a few cross-component invariants (nonce TTL vs clock skew) are documented but unenforced. Remaining issues are minor consistency and error-swallowing gaps that fall below the bar the rest of the code sets for itself.

> This is an unusually well-engineered middleware family: consistently fail-closed defaults, panic-on-misconfiguration option constructors, panic-guarded caller callbacks, singleton-header hardening, careful ResponseWriter wrappers (1xx, Hijack, Flush, ReadFrom), and extensive rationale comments citing prior audit findings. The signedrequest/csrf/idempotency cores are notably misuse-resistant. The real defects found are concentrated at the seams: a genuine streaming bug in compress (encoder Flush silently lost through the pool wrapper), cross-package inconsistencies where one package (apikey) missed the hardening its siblings received, and wholesale duplication between the two rate limiters that invites future divergence.

> This is a mature, security-conscious middleware family: fail-closed defaults, constant-time comparisons, careful header-singleton validation, nonce replay protection ordered after MAC verification, tenant-scoped idempotency keys, panic-guarded extension points, and extensive threat-model commentary throughout. The dominant issue is the signed-request verifier zeroing the resolver-returned secret slice — an undocumented mutation of caller-owned memory that the repo's own example trips over, breaking authentication after the first request. Aside from that, the code is high quality with only minor consistency gaps.

> This is unusually high-quality middleware code: consistently fail-closed defaults, panic-guarded caller-supplied callbacks, careful singleton-header validation, correct mutex/channel use (timeout writer, rate-limiter shards, nonce stores), context-detached post-handler store calls, and explicit resource cleanup for spooled bodies and temp files, all backed by audit-reference comments. No CRITICAL or HIGH defects survived verification; the issues found are edge-case inconsistencies — 1xx/hijack handling gaps in a couple of ResponseWriter wrappers, one client-vs-server error misclassification in signedrequest, and minor API-consistency gaps — rather than exploitable logic or concurrency flaws.

> This is an unusually disciplined middleware family: consistent fail-closed defaults, panic-guarded caller callbacks, careful singleton-header validation, correct nonce/replay ordering in signedrequest (MAC before nonce store, resolver before body buffering), and sound mutex/lifecycle discipline in the rate limiters and timeout writer. The findings that survived scrutiny are mostly edge-case divergences between sibling response-writer wrappers (1xx handling, hijack status, Push delegation) rather than exploitable flaws. The one high-severity item is a contract gap, not a logic error: signedrequest's verify() destructively zeroes the resolver-returned secret without documenting that resolvers must return a fresh copy, which bricks verification after the first request for any resolver returning a long-lived slice.

> This is an exceptionally security-conscious middleware family: the signedrequest verifier resolves secrets before buffering bodies (memory-amplification guard), uses constant-time MAC comparison with a fixed-length fallback, zeroes key material, and enforces nonce replay protection with a fail-closed store; CSRF, auth, idempotency, clientip/XFF trust, and the tenant/rate-limit key paths all fail closed, validate header singletons and character sets, and scope cache keys per-user. I found no CRITICAL/HIGH issue — no injection, authn/authz bypass, fail-open path, crypto misuse, or secret/PII leak survived tracing. The only observations are LOW-severity consistency gaps (the apikey.Middleware credential extractor is notably less hardened than its own sibling authenticator in the auth package).

> This is an unusually well-engineered middleware tree: consistent fail-fast option validation, defensive panic-recovery around every caller-supplied callback, careful header-trust hygiene (singleton enforcement, length caps, httpguts validation), correct ResponseWriter wrapper delegation (Flush/Hijack/Push/ReadFrom/1xx handling) in almost every package, and exceptional godoc that documents threat models and trade-offs inline. The signedrequest, csrf, and idempotency packages in particular show evidence of multiple hardening audit waves (FR-0xx references) and get subtle things right (spooled-body lifecycle, secret zeroing, replay ordering, fail-closed defaults). The findings are mostly consistency drift — the older apikey package missed the credential-header hardening applied to auth, the recover wrapper lags the delegation contract its siblings honor, and a handful of dead config fields and stale doc comments have accumulated in the two largest (god-file-sized) sources.

> This is an unusually well-audited, defensively-written middleware family. Header-trust surfaces (X-Forwarded-For, X-Real-IP, X-Forwarded-Proto, X-Tenant-Id, X-User-Id, Origin/Referer) are all consistently gated on a verified trust source (trusted-proxy CIDRs, verified mTLS chains, or explicit opt-in options) and fail closed; crypto paths (signedrequest HMAC + nonce replay, CSRF HMAC, apikey verify) use hmac.Equal / subtle.ConstantTimeCompare, resolve secrets before buffering bodies, zero key material, and cannot be replayed via alternate nonce encodings because the nonce is inside the MAC. Authn/authz middleware fail closed on missing claims and panic on unsafe misconfiguration. I found no exploitable authn/authz bypass, injection, secret leak, or crypto-misuse defect in scope; the one concrete issue below is a narrow correctness edge case.

> This is unusually well-engineered middleware: fail-closed defaults, careful header-trust boundaries (clientip/secheaders/tenant all refuse caller-controlled headers by default), constant-time credential comparisons, panic-safe callback wrappers, and extensive godoc that pre-empts most edge cases. Security-critical pieces (signedrequest MAC/nonce ordering, CSRF double-submit + origin allowlist, auth S2S trust marker) are correct and thoroughly reasoned. The findings are consistency/robustness gaps rather than exploitable flaws; the most substantive is that the compress ResponseWriter wrapper mishandles 1xx interim responses where its three sibling wrappers get it right.

> This is exceptionally well-hardened middleware: fail-closed defaults, careful header-trust boundaries (loopback-only trusted proxies, explicit opt-ins for header-derived tenant/actor/proto), constant-time comparisons, bounded body spooling with temp-file cleanup, panic-guarded callbacks, and correct nonce-store ordering (MAC verified before the replay store is touched). Concurrency is handled cleanly — the timeout writer's mutex/channel design is race-free, the ratelimit shards and cleanup use two-phase locking correctly, and Start/Stop lifecycle guards are sound. I found no CRITICAL/HIGH correctness or concurrency defect; the only issues are LOW-severity inconsistencies in the response-writer wrappers around hijacked connections and the outermost recover wrapper not re-exposing the ReadFrom/Push optional interfaces that its inner siblings preserve.

> This is an unusually high-quality middleware family: exhaustive godoc that spells out threat models and trade-offs, consistent fail-fast panics at construction, fail-closed request handling, constant-time credential comparisons, panic-guarded caller callbacks, and careful handling of tricky ResponseWriter surfaces (1xx, Flush, Hijack, ReadFrom). Genuine defects are scarce and mostly confined to edge-case integer handling and minor cross-package inconsistencies rather than logic or security flaws. The strongest issue is an integer-overflow footgun in the signed-request body reader when the size cap is set to math.MaxInt64.

> This scope is among the most defensively engineered Go middleware I have reviewed: fail-closed defaults, constant-time secret/MAC comparisons (apikey dummy-verify, csrf/signedrequest hmac.Equal), bounded memory (spooled request bodies, capped nonce/key/prefix lengths, response-buffer caps), tenant/user-scoped idempotency keys with identity-header stripping, low-cardinality metrics that never label on client-controlled values, and panic containment around every caller-supplied callback. Header trust (X-Forwarded-For/X-Real-IP, X-Tenant-Id, mTLS SAN/CN) is consistently gated behind explicit trusted-proxy or verified-chain checks with secure defaults. I found no exploitable authn/authz bypass, injection, crypto misuse, or tenant-isolation defect; the three findings are low-severity posture/design observations rather than concrete vulnerabilities.

> Exceptionally high-quality, heavily audited middleware code: extensive fail-closed defaults, careful constant-time and secret-zeroing handling in signedrequest/csrf/apikey, and correct use of mutexes, buffered channels, tickers, and context detachment throughout. Concurrency is handled soundly (ratelimit shards, keyed active-keys collector, timeout handler/timeout goroutine split, nonce/spooled-body lifecycle) with no data races, deadlocks, or resource leaks found. My correctness/concurrency lens surfaced no CRITICAL or HIGH defects — only one niche LOW gap around 1xx interim-response header handling in the idempotency response capture.

## Findings


### [LOW] Double-submit CSRF tokens are not bound to a session/user by default

- **Where**: `httpx/middleware/csrf/csrf.go:564`
- **Dimension**: security
- **Detail**: In the legacy double-submit mode (csrf.New without WithSessionExtractor), a valid token is any server-HMAC-signed random value; the state-changing check is only constant-time equality of cookie vs header plus signature verification (lines 558-566). The token is not bound to any user or session, so a token minted for user A validates for user B. If an attacker on a sibling subdomain / same eTLD+1 can Set-Cookie-overwrite the CSRF cookie, they control both halves and the check passes. This is the known double-submit weakness; the package documents it and provides WithAllowedOrigins and WithSessionExtractor as mitigations, but neither is on by default, so the out-of-the-box posture is weaker than a security-conscious default. Failure scenario: multi-tenant app on shared parent domain, attacker subdomain plants a self-minted token cookie+header and drives a cross-origin state-changing request without an Origin allowlist configured.
- **Suggestion**: Consider defaulting to session-bound tokens when a session extractor is available, or emit a startup warning when neither WithAllowedOrigins nor WithSessionExtractor is configured, so operators consciously accept the bare double-submit posture.

### [LOW] Legacy double-submit CSRF tokens have no server-side expiry; a leaked token verifies forever until secret rotation

- **Where**: `httpx/middleware/csrf/csrf.go:859`
- **Dimension**: security
- **Detail**: mintSignedToken produces hex(32 random bytes) + "." + HMAC(tokenHex) with no timestamp, and isValidSignedToken accepts any token signed by any secret in the ring indefinitely. The 24h cookie MaxAge is only client-enforced. A CSRF token exfiltrated once (XSS on a sibling property, log leak, referer leak of a page embedding it) remains valid for as long as the HMAC secret lives — potentially years — whereas the session-bound path (securitycsrf.Issuer with DefaultTTL/WithSessionTTL) correctly expires tokens. Combined with the documented sibling-subdomain Set-Cookie planting weakness of double-submit, unlimited token lifetime widens the replay window materially.
- **Suggestion**: Embed an issued-at timestamp in the signed payload (tokenHex + "." + ts + "." + hmac) and reject tokens older than a TTL (e.g. 24h to match the cookie MaxAge), mirroring the session-bound issuer's expiry semantics.

### [LOW] idempotency.go is an 1127-line god file mixing five distinct concerns

- **Where**: `httpx/middleware/idempotency/idempotency.go:1`
- **Dimension**: smell
- **Detail**: The single file contains the Prometheus Metrics constructor, ~15 functional options, the middleware orchestration, body fingerprinting/semantic-header hashing, cache-key derivation, and a full response-capture ResponseWriter implementation (responseCapture/captureSink/writerOnly). The sibling signedrequest package splits the exact same shape into signedrequest.go/body.go/metrics.go/noncestore.go, and the repo's own conventions favor many small cohesive files. At 1127 lines this is the largest file in the middleware tree and the response-capture writer alone (~130 lines) is an independently testable unit.
- **Suggestion**: Split into metrics.go, options.go, fingerprint.go, and capture.go, mirroring the signedrequest package layout.

### [LOW] Limiter and KeyedLimiter are ~400-line near-verbatim duplicates

- **Where**: `httpx/middleware/ratelimit/keyed.go:42`
- **Dimension**: smell
- **Detail**: KeyedLimiter duplicates Limiter (ratelimit.go:48-392) almost line-for-line: identical shard array + FNV-1a getShard, identical Start/Stop lifecycle state machine (startMu/started/stopped/cancel/doneCh, the same stop-before-start latching, the same panic-recovering ticker loop), and a structurally identical two-phase cleanup. Even the fixed-window bookkeeping differs only cosmetically (windowAt+elapsed vs windowEnd) — a divergence the comments then have to explain away (keyed.go:192-195). Any future fix to the lifecycle or cleanup logic (e.g. the Stop race handling) must be applied twice and can silently drift. The IP limiter is conceptually just a KeyedLimiter whose key is the client IP plus a clientip resolver.
- **Suggestion**: Extract the shared shard/lifecycle/cleanup machinery into an unexported core (or implement Limiter as a thin wrapper over KeyedLimiter with an IP key extractor), keeping both exported types as facades.

### [LOW] Limiter.maxPerShard is a config field with no configuration path; KeyedLimiter hardcodes its equivalent

- **Where**: `httpx/middleware/ratelimit/ratelimit.go:54`
- **Dimension**: api-design
- **Detail**: Limiter carries a maxPerShard field that is always set to defaultMaxPerShard (line 143) — no LimiterOption exposes it, so it is a dead knob that reads as configurable but is not. KeyedLimiter takes the opposite approach and passes the constant defaultMaxKeyedPerShard directly to lru.New (keyed.go:129) with no field at all. Failure scenario: an operator whose service legitimately tracks more than 160k distinct client IPs (16 shards x 10k) cannot raise the cap without forking; under IP-spray the LRU silently evicts live counters and resets windows, and there is no supported tuning path even though the field's existence implies one — while the two sibling limiters disagree on the internal pattern.
- **Suggestion**: Either add WithMaxPerShard/WithKeyedMaxPerShard options wired to both limiters, or remove the Limiter field and use the constant directly to match KeyedLimiter.

### [LOW] Client-supplied X-Request-Id / X-Correlation-Id is trusted and reflected into logs and response

- **Where**: `httpx/middleware/requestid/requestid.go:21`
- **Dimension**: security
- **Detail**: WithRequestID (and identically correlationid/correlationid.go:26) takes the inbound X-Request-Id header verbatim when it matches the safe token alphabet, stores it on the request context, echoes it back in the response header, and stamps it onto every access-log / request-scoped log line for the request. Because the value is fully attacker-controlled (any unauthenticated caller can set it), an attacker can choose an ID that collides with a legitimate user's request ID, or inject a chosen constant across many requests, to defeat log-based forensic correlation and SIEM grouping. The token alphabet check (contextutil.IsValidCorrelationToken) prevents CR/LF header/log injection, so impact is limited to correlation-integrity, not code injection. Failure scenario: attacker sends `X-Request-Id: <victim-request-id>` on abusive requests so an incident responder tracing that ID pulls back the attacker's traffic mixed with the victim's.
- **Suggestion**: Only trust the inbound ID when RemoteAddr is a configured trusted proxy (mirroring clientip's trust model), otherwise always generate server-side; or at minimum document that these headers must be stripped/re-stamped at the ingress.

### [LOW] Signed-request bodies over 64 KiB are spooled unencrypted to the shared OS temp directory

- **Where**: `httpx/middleware/signedrequest/body.go:114`
- **Dimension**: security
- **Detail**: appendChunk spools body overflow to os.CreateTemp("", "rho-signedrequest-body-*.bin"). Webhook/S2S payloads routinely contain PII or secrets; those bytes now hit disk in the world-shared temp dir (mode 0600, and unlinked immediately on Unix, which is good), but on Windows the named file persists until Close/cleanup and survives a hard crash before cleanup runs; on any OS the plaintext may also land in swap-less tmpfs vs. persistent disk depending on TMPDIR. Operators handling regulated payloads get disk persistence they did not opt into and cannot currently disable or redirect.
- **Suggestion**: Expose the spool directory as an option (so operators can point it at tmpfs/an encrypted volume) and document the disk-spill behaviour on WithInMemoryBodyMax; consider O_TMPFILE on Linux and best-effort delete-on-close FILE_FLAG_DELETE_ON_CLOSE semantics on Windows.

### [LOW] WithCallTimeout and WithNonceTimeout are duplicate exported options for the same knob

- **Where**: `httpx/middleware/signedrequest/redis/redis.go:85`
- **Dimension**: api-design
- **Detail**: Two exported Options configure the identical callTimeout field, with the docs declaring one an "alias" of the other "for callers that prefer one name over the other". This doubles the API surface for zero capability, creates a which-one-is-canonical question the docs answer inconsistently (WithNonceTimeout is called "the canonical name" while WithCallTimeout carries the full semantics text), and invites confusing call sites passing both (last one silently wins). No other option in the httpx middleware tree has a same-behavior alias.
- **Suggestion**: Keep one name (WithCallTimeout matches the field and the sibling idempotency WithPostHandlerTimeout naming), and deprecate or delete the alias before the API is widely consumed.

### [LOW] buildCanonicalFromHash clones and sorts the static requiredHeaders slice on every request

- **Where**: `httpx/middleware/signedrequest/signedrequest.go:614`
- **Dimension**: performance
- **Detail**: When WithRequiredHeaders is configured, every verify() call (and every SignCanonical call) executes `hdrs := append([]string(nil), requiredHeaders...); sort.Strings(hdrs)` even though the header list is fixed at middleware construction and never changes per request. That is an allocation plus an O(n log n) sort on the request hot path of an auth middleware that is otherwise carefully tuned (streaming body hash, lazy buffers, pre-sized limits). With several pinned headers at high RPS this is measurable garbage for zero benefit.
- **Suggestion**: Sort and dedupe cfg.requiredHeaders once in Middleware() (and in normalizeHeaders for SignCanonical) and have buildCanonicalFromHash iterate the pre-sorted slice directly.

### [LOW] Default middleware stack applies no request-body size limit

- **Where**: `httpx/middleware/stack/stack.go:109`
- **Dimension**: security
- **Detail**: stack.Default wires recover, secheaders, metrics, request/correlation IDs, tracing, logging, timeout, and request-logger, but does NOT include maxbody. The timeout middleware only bounds the buffered *response* (1 MiB), not the request body, and net/http imposes no default body cap. A handler mounted behind the canonical stack that reads the full body (e.g. a decode that is not itself length-limited) can therefore be driven to arbitrary heap use by a single large upload. Failure scenario: a POST route on a Default-stack service that calls io.ReadAll(r.Body) or an unbounded JSON decode is fed a multi-gigabyte body, exhausting memory. The kit ships maxbody.MaxBodySize but it is neither in the Default chain nor surfaced as a Config toggle.
- **Suggestion**: Add maxbody with a generous default cap to Default (with a Without*/override option), or document prominently that callers must add maxbody themselves for any body-reading route.

### [LOW] stack.Default cannot inject a Prometheus registerer for the metrics stage, unlike every other configurable stage

- **Where**: `httpx/middleware/stack/stack.go:202`
- **Dimension**: api-design
- **Detail**: When EnableMetrics is true the stack calls mwmetrics.Metrics(h), a package-level singleton pinned to prometheus.DefaultRegisterer (metrics/metrics.go:166-171). Config forwards options for secheaders (SecHeadersOptions), compress (CompressOptions), and auditlog (AuditLogOptions), but exposes no way to pass metrics.WithRegisterer. Failure scenario: a service using a non-default registry (common in tests and multi-tenant binaries) must call WithoutMetrics() and hand-wire NewHTTPMetrics(WithRegisterer(...)).Middleware via WithOuter/WithInner at a position that no longer matches the canonical chain ordering the stack documents — or silently double-registers on the global default registry.
- **Suggestion**: Add a MetricsOptions []metrics.MetricsOption field (or WithHTTPMetrics(*HTTPMetrics) option) mirroring the SecHeadersOptions/CompressOptions pattern.

### [LOW] Handler panic stack trace is lost when the panic is re-raised from the middleware goroutine

- **Where**: `httpx/middleware/timeout/timeout.go:148`
- **Dimension**: error-handling
- **Detail**: The handler goroutine's deferred recover (lines 147-152) captures only the panic VALUE into handlerResult and the middleware re-panics with it on the request goroutine (lines 158-159, 186-188). By the time the outer recover middleware calls debug.Stack(), the stack shown is the timeout middleware's re-panic site, not the handler frame that actually panicked — nil-pointer panics become nearly undebuggable ('panic recovered' with a stack pointing at timeout.go). stdlib http.TimeoutHandler captures debug.Stack() at the original recover site and re-panics with value+stack for exactly this reason.
- **Suggestion**: Capture debug.Stack() in the handler goroutine's recover and re-panic with a wrapper carrying both value and original stack (mirroring net/http's timeoutHandler), or log the original stack before re-raising.

### [LOW] Timeout middleware conflates client disconnect / parent cancellation with deadline expiry

- **Where**: `httpx/middleware/timeout/timeout.go:162`
- **Dimension**: bug
- **Detail**: The select's `case <-ctx.Done():` fires both when the WithTimeout deadline expires and when the parent request context is cancelled (client disconnected, server shutting down). In the cancellation case the middleware still calls tw.writeTimeout(), writing a 503 {"error":"request timeout","code":"TIMEOUT"} response and letting the outer access-log/metrics middleware record a 503 timeout for a request that never actually timed out — e.g. a client that gives up after 1s on a 30s-timeout route is logged and counted as a server timeout. This skews 5xx/timeout dashboards and alerting under any burst of client cancellations.
- **Suggestion**: Check ctx.Err(): only write the TIMEOUT body when errors.Is(ctx.Err(), context.DeadlineExceeded); on plain cancellation skip the 503 (or record a distinct client-cancelled outcome).

### [LOW] timeoutWriter forwards handler Content-Length verbatim while the buffered body may have been truncated

- **Where**: `httpx/middleware/timeout/writer.go:157`
- **Dimension**: bug
- **Detail**: writeToReal() copies every buffered handler header (including a handler-set Content-Length) to the real ResponseWriter and then writes tw.buf, but Write() silently truncates the body to bufferCap (default 1 MiB) and returns ErrResponseTooLarge (lines 119-126). Failure scenario: a handler that sets Content-Length: 2000000 via Header().Set and streams a 2 MiB body while ignoring the Write error will, on the normal completion path, emit a response advertising 2 MB but carrying only ~1 MB of body; net/http logs a Content-Length mismatch and the client reads a truncated/short response. Narrow (requires a handler that manually sets Content-Length and ignores the truncation error), hence LOW.
- **Suggestion**: In writeToReal, drop the Content-Length header before flushing whenever the buffer was truncated (track a truncated flag in Write), so net/http re-derives the correct length from the bytes actually sent.

