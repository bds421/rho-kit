# httpx/middleware/ — stack, csrf, idempotency, ratelimit, timeout, logging, auth, secheaders

Largest single audit area. Middleware composition (`stack.Default`) ships incomplete and individual middleware have safety gaps that compound.

## Landed

- ✅ **Auth middleware checks `VerifiedChains`** — `RequireS2SAuth` rejects when `r.TLS.VerifiedChains == nil` in addition to the PeerCertificates check (commit `c502dd2`).
- ✅ **Idempotency body-fingerprint plumbing** — middleware computes SHA-256 of request body (≤1 MiB), passes through `Store.Get` / `Store.TryLock`, returns 422 on mismatch (commit `1f06b5e`). Identity-header strip and mandatory user-extractor are still open below.
- ✅ **CSRF Origin allowlist** — `WithAllowedOrigins(...)` validates Origin/Referer against allowlist on state-changing requests (commit `409cdbb`). Session-bound HMAC and Secure-default still open below.
- ✅ **Idempotency `WithTTL` rejects non-positive durations** — panics in constructor instead of producing permanent locks in Redis (commit `36cf34b`).

## Open

### [CRITICAL] `stack.Default` has no panic recovery
**File**: `httpx/middleware/stack/stack.go:41-121`
**Issue**: Composes secheaders → metrics → requestid → correlationid → tracing → reqlogger → logging → handler. There is no recover middleware in the kit at all. A panic in a handler crashes the goroutine; Go's stdlib recover logs to `ErrorLog` (also unset) with no JSON, no request_id correlation, no metric.
**Fix**: Create `httpx/middleware/recover` (see [new/01-httpx-middleware-recover.md](../new/01-httpx-middleware-recover.md)) and prepend as the OUTERMOST layer in `Default` (before secheaders, so secheaders are still set on the panic response).
**Effort**: S
**Phase**: 1

### [HIGH] `clientip` default trusts ALL RFC1918 + ULA IPv6 — internal callers can spoof client IP
**File**: `httpx/middleware/clientip/clientip.go:9,43,81` + `httpx/middleware/ratelimit/ratelimit.go:56`
**Issue**: Default trusted-proxy list includes all RFC1918 ranges and unique-local IPv6. Any caller reaching the service from inside the VPC, pod network, or Docker network can set `X-Real-IP`/`X-Forwarded-For` and be treated as that client IP. Invalid explicit proxy config is silently skipped and falls back to the same broad defaults. IP rate limits, audit logs, abuse detection, and any auth that consumes client IP can be bypassed or poisoned by an internal client.
**Fix**: Default to trusting NO forwarded headers (or loopback only). Require explicit proxy CIDRs from config for production. Fail fast on invalid entries. Expose through `app.Builder.WithTrustedProxies(cidrs)`.
**Effort**: S
**Phase**: 1
**Migration**: Existing services running behind a TLS-terminating ingress must add `WithTrustedProxies` or the operator's known proxy IPs to keep client IPs accurate.

### [HIGH] CSRF default secret is per-process — multi-instance deployments break
**File**: `httpx/middleware/csrf/csrf.go:119`
**Issue**: `csrf.New` silently generates a random HMAC secret when none is configured. Each pod gets a different secret. Tokens minted by pod A fail on pod B → intermittent 403s after every deploy, autoscale, or pod rotation. The default is cryptographically safe but operationally unsafe for any production topology.
**Fix**: Provide a development-only random default; require a configured shared secret in production profiles. Add `WithSecret([]byte)` (panic if missing in non-dev). Expose through `app.Builder.WithCSRFSecret`. Pair with [new/19-app-production-defaults.md](../new/19-app-production-defaults.md).
**Effort**: S
**Phase**: 1

### [HIGH] `timeout` middleware is not a hard timeout — handler can hold connection forever
**File**: `httpx/middleware/timeout/timeout.go:47`
**Issue**: After timeout, the middleware writes the timeout response but then **waits for the handler goroutine to finish** before returning. A handler that ignores ctx cancellation holds the HTTP goroutine and connection indefinitely. The user-facing contract says "requests exceeding duration receive 503"; the actual behavior is "you may receive 503 *and* the handler keeps running invisibly". Even if the response is written, the goroutine isn't reclaimed. The 10MiB buffer issue (already listed) compounds this.
**Fix**: Either (a) re-document as a *cooperative* timeout with an explicit name (e.g., `timeout.Cooperative`), OR (b) add a *hard* mode that returns after the timeout and safely discards later handler writes (using a write-once response wrapper that no-ops after deadline).
**Effort**: M
**Phase**: 2

### [HIGH] CSRF middleware session-bound HMAC + Secure default still missing
**File**: `httpx/middleware/csrf/csrf.go:128-189`
**Issue**: Origin allowlist landed in commit `409cdbb`; the remaining gaps are (a) the HMAC is over a random nonce only — bind it to the session ID so a sibling app on the same eTLD+1 cannot Set-Cookie an attacker-controlled token, and (b) `Secure` defaults to `false` which leaks the cookie over plaintext when SameSite is None.
**Fix**: HMAC over `session_id || nonce`; default `Secure=true` when SameSite is None. See [new/06-security-csrf-tokens.md](../new/06-security-csrf-tokens.md) for the underlying primitive.
**Effort**: M

### [HIGH] CSRF SkipCheck still uses tampered cookie after regeneration
**File**: `httpx/middleware/csrf/csrf.go:131-159`
**Issue**: When the incoming cookie is invalid (bad HMAC), the middleware regenerates and `SetCookie`s a new token — but does NOT update the local `cookie` variable. The subsequent token comparison still uses the stale invalid cookie → 403 even though a fresh cookie was just issued.
**Fix**: After regenerating, update local `cookie.Value` to the new token, OR short-circuit with a 403 explaining the client must retry with the new cookie.
**Effort**: S

### [HIGH] Idempotency replays Set-Cookie / Authorization headers verbatim
**File**: `httpx/middleware/idempotency/idempotency.go:230-249,268-276`
**Issue**: Cached response copies ALL headers and replays them. Includes `Set-Cookie`, `Authorization`, `WWW-Authenticate`, `Strict-Transport-Security`. If the original handler set a session cookie, every replay over the next 24h sets it for potentially different users. Per-user scoping is opt-in (warning logged), not enforced.
**Fix**: Drop hop-by-hop and identity-bearing headers from cached response. Make `WithUserExtractor` mandatory (panic without it).
**Effort**: S

### [HIGH] Timeout middleware buffers up to 10 MiB per in-flight request
**File**: `httpx/middleware/timeout/writer.go:21-82`
**Issue**: 10 MiB cap × N concurrent attackers held during the timeout race window. 10k concurrent requests = 100 GiB transient memory.
**Fix**: Lower default to 1 MiB; make configurable; document that `timeout.Timeout` should be paired with `maxbody.MaxBodySize` and a connection limiter.
**Effort**: S

### [HIGH] Logging middleware uses default trusted proxies — disagrees with rate limiter
**File**: `httpx/middleware/logging/logging.go:44`
**Issue**: `clientip.ClientIP(r)` always uses package-default trusted proxies. If the operator configured different trusted proxies for the rate limiter, the access log records a different IP than what the limiter saw. Inconsistent attribution kills incident response. **Root cause is the `clientip` default itself** (see the first finding in this file) — fixing the default eliminates the cross-middleware disagreement.
**Fix**: Once the `clientip` default is hardened, add a `WithClientIPResolver` option here so consumers can swap a shared resolver across all middleware in `stack.Default`.
**Effort**: M

### [HIGH] `secheaders` HSTS gated on `r.TLS != nil` — never fires behind a TLS-terminating ingress
**File**: `httpx/middleware/secheaders/secheaders.go:124`
**Issue**: With Ingress/Oathkeeper terminating TLS, `r.TLS` is always nil at the Go server, so HSTS is never sent — the most common deployment topology.
**Fix**: Honor `X-Forwarded-Proto: https` from trusted proxies, OR add `WithForceHSTS()` option.
**Effort**: S

### [MEDIUM] Rate-limit cleanup: snapshot+release pattern is benign but doc misleads
**File**: `httpx/middleware/ratelimit/ratelimit.go:143-163`
**Issue**: Two-phase locking (snapshot keys → re-lock → Peek+Remove) is racy in theory but actually safe (Peek won't trigger false eviction). However the comment claims "evicts expired" while `keys[:limit]` only scans 1000 entries per shard regardless of LRU size. Real GC is via LRU pressure, not cleanup.
**Fix**: Either drop the two-phase locking (cleanup is already O(1000) bounded), or document that LRU eviction is the real GC under load and `cleanup` is a best-effort hint.

### [LOW] Hijack records but tracing still sets HTTPResponseStatusCode(0)
**File**: `httpx/middleware/response_recorder.go:62-71` + `tracing.go:60-65`
**Issue**: After hijack (WebSocket upgrade), tracing reads `rec.Status()` and sets the span status to 200 (the recorder default), producing misleading 200-status spans for connections that may run for hours.
**Fix**: In tracing middleware, if `rec.WasHijacked()` set `HTTPResponseStatusCode(101)` or skip status entirely.

### Migration checklist

- [ ] Phase 1: prepend recover middleware in `stack.Default` (depends on [new/01](../new/01-httpx-middleware-recover.md)).
- [ ] Phase 1: timeout middleware buffer cap default 1 MiB.
- [ ] Phase 1: secheaders honor `X-Forwarded-Proto` from trusted proxies.
- [ ] Phase 1: `clientip` default to no-trusted-proxies; require explicit CIDRs via `Builder.WithTrustedProxies`.
- [ ] Phase 1: CSRF require shared secret in non-dev; add `Builder.WithCSRFSecret`.
- [ ] Phase 1: CSRF SkipCheck regeneration bug fix.
- [ ] Phase 2: timeout middleware hard-timeout mode (or rename to Cooperative).
- [ ] Phase 2: idempotency identity-header strip + mandatory user extractor.
- [ ] Phase 2: CSRF session-bound HMAC + Secure default.
- [ ] Phase 3: shared client-IP resolver for logging + ratelimit.
- [ ] Phase 3: tracing hijack handling.

### Related new packages

- [new/01-httpx-middleware-recover.md](../new/01-httpx-middleware-recover.md) — recover middleware.
- [new/06-security-csrf-tokens.md](../new/06-security-csrf-tokens.md) — session-bound CSRF primitive.
- [new/08-security-csp-nonce.md](../new/08-security-csp-nonce.md) — CSP nonce middleware.
- [new/16-observability-red-metrics.md](../new/16-observability-red-metrics.md) — RED middleware with proper buckets.
- [new/17-httpx-problem-details.md](../new/17-httpx-problem-details.md) — RFC 7807 writer.
