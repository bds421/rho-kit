# httpx/middleware/ — stack, csrf, idempotency, ratelimit, timeout, logging, auth, secheaders

Largest single audit area. Middleware composition (`stack.Default`) ships incomplete and individual middleware have safety gaps that compound.

## Landed

- ✅ **Auth middleware checks `VerifiedChains`** — `RequireS2SAuth` rejects when `r.TLS.VerifiedChains == nil` in addition to the PeerCertificates check (commit `c502dd2`).
- ✅ **Idempotency body-fingerprint plumbing** — middleware computes SHA-256 of request body (≤1 MiB), passes through `Store.Get` / `Store.TryLock`, returns 422 on mismatch (commit `1f06b5e`). Identity-header strip and mandatory user-extractor are still open below.
- ✅ **CSRF Origin allowlist** — `WithAllowedOrigins(...)` validates Origin/Referer against allowlist on state-changing requests (commit `409cdbb`). Session-bound HMAC and Secure-default still open below.
- ✅ **Idempotency `WithTTL` rejects non-positive durations** — panics in constructor instead of producing permanent locks in Redis (commit `36cf34b`); backends now also return `ErrInvalidTTL` (commit `a01fad7`).
- ✅ **`clientip` default tightened to loopback only** — RFC1918 + ULA defaults removed; `ParseTrustedProxiesStrict` returns errors on invalid CIDR entries (commit `ab4df5c`).
- ✅ **CSRF requires shared secret** — `csrf.New` panics in non-dev when no secret is configured; `WithDevSecret()` opt-in for the random per-process fallback (commit `7f0efe3`).
- ✅ **CSRF SkipCheck regen bug** — regenerated cookie now triggers a 403 with retry hint instead of letting the request through with the stale invalid cookie (commit `7f0efe3`).
- ✅ **Timeout buffer cap default lowered to 1 MiB** — adds `WithMaxBufferSize` for endpoints that legitimately stream multi-megabyte JSON (commit `30113f9`).
- ✅ **`secheaders` honours `X-Forwarded-Proto`** — `WithTrustedProxiesForProto` enables HSTS behind TLS-terminating ingresses; `WithForceHSTS` for topologies the kit cannot observe (commit `b324d2e`).
- ✅ **`stack.Default` includes Timeout middleware** — 30s default; `WithTimeout(d)` / `WithoutTimeout()` to override (commit `a0b49e8`). Closes the "production stack ships without a wall-clock cap" gap.

## Open

### [CRITICAL] `stack.Default` has no panic recovery
**File**: `httpx/middleware/stack/stack.go:41-121`
**Issue**: Composes secheaders → metrics → requestid → correlationid → tracing → reqlogger → logging → handler. There is no recover middleware in the kit at all. A panic in a handler crashes the goroutine; Go's stdlib recover logs to `ErrorLog` (also unset) with no JSON, no request_id correlation, no metric.
**Fix**: Create `httpx/middleware/recover` (see [new/01-httpx-middleware-recover.md](../new/01-httpx-middleware-recover.md)) and prepend as the OUTERMOST layer in `Default` (before secheaders, so secheaders are still set on the panic response).
**Effort**: S
**Phase**: 1

### [HIGH] `timeout` middleware is not a hard timeout — handler can hold connection forever
**File**: `httpx/middleware/timeout/timeout.go:47`
**Issue**: After timeout, the middleware writes the timeout response but then **waits for the handler goroutine to finish** before returning. A handler that ignores ctx cancellation holds the HTTP goroutine and connection indefinitely. Documented as cooperative — but the contract is still ambiguous to first-time consumers.
**Fix**: Either (a) re-document as a *cooperative* timeout with an explicit name (e.g., `timeout.Cooperative`), OR (b) add a *hard* mode that returns after the timeout and safely discards later handler writes (using a write-once response wrapper that no-ops after deadline).
**Effort**: M
**Phase**: 2

### [HIGH] CSRF middleware session-bound HMAC + Secure default still missing
**File**: `httpx/middleware/csrf/csrf.go:128-189`
**Issue**: Origin allowlist landed in commit `409cdbb`; the remaining gaps are (a) the HMAC is over a random nonce only — bind it to the session ID so a sibling app on the same eTLD+1 cannot Set-Cookie an attacker-controlled token, and (b) `Secure` defaults to `false` which leaks the cookie over plaintext when SameSite is None.
**Fix**: HMAC over `session_id || nonce`; default `Secure=true` when SameSite is None. See [new/06-security-csrf-tokens.md](../new/06-security-csrf-tokens.md) for the underlying primitive.
**Effort**: M

### [HIGH] Idempotency replays Set-Cookie / Authorization headers verbatim
**File**: `httpx/middleware/idempotency/idempotency.go:230-249,268-276`
**Issue**: Cached response copies ALL headers and replays them. Includes `Set-Cookie`, `Authorization`, `WWW-Authenticate`, `Strict-Transport-Security`. If the original handler set a session cookie, every replay over the next 24h sets it for potentially different users. Per-user scoping is opt-in (warning logged), not enforced.
**Fix**: Drop hop-by-hop and identity-bearing headers from cached response. Make `WithUserExtractor` mandatory (panic without it).
**Effort**: S

### [HIGH] Logging middleware uses default trusted proxies — disagrees with rate limiter
**File**: `httpx/middleware/logging/logging.go:44`
**Issue**: `clientip.ClientIP(r)` always uses package-default trusted proxies. If the operator configured different trusted proxies for the rate limiter, the access log records a different IP than what the limiter saw. Inconsistent attribution kills incident response. **Root cause is the `clientip` default itself** (see the first finding in this file) — fixing the default eliminates the cross-middleware disagreement.
**Fix**: Once the `clientip` default is hardened, add a `WithClientIPResolver` option here so consumers can swap a shared resolver across all middleware in `stack.Default`.
**Effort**: M

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
- [x] Phase 1: timeout middleware buffer cap default 1 MiB. ✅ `30113f9`
- [x] Phase 1: secheaders honor `X-Forwarded-Proto` from trusted proxies. ✅ `b324d2e`
- [x] Phase 1: `clientip` default to no-trusted-proxies; require explicit CIDRs via `Builder.WithTrustedProxies`. ✅ `ab4df5c` (Builder integration still TODO)
- [x] Phase 1: CSRF require shared secret in non-dev; add `Builder.WithCSRFSecret`. ✅ `7f0efe3` (Builder integration still TODO)
- [x] Phase 1: CSRF SkipCheck regeneration bug fix. ✅ `7f0efe3`
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
