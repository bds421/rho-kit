# httpx/middleware/ — stack, csrf, idempotency, ratelimit, timeout, logging, auth, secheaders

Largest single audit area. Middleware composition (`stack.Default`) ships incomplete and individual middleware have safety gaps that compound.

## Landed

- ✅ **Auth middleware checks `VerifiedChains`** — `RequireS2SAuth` rejects when `r.TLS.VerifiedChains == nil` in addition to the PeerCertificates check (commit `c502dd2`).
- ✅ **Idempotency body-fingerprint plumbing** — middleware computes SHA-256 of request body (≤1 MiB), passes through `Store.Get` / `Store.TryLock`, returns 422 on mismatch (commit `1f06b5e`).
- ✅ **CSRF Origin allowlist** — `WithAllowedOrigins(...)` validates Origin/Referer against allowlist on state-changing requests (commit `409cdbb`).
- ✅ **Idempotency `WithTTL` rejects non-positive durations** — panics in constructor instead of producing permanent locks in Redis (commit `36cf34b`); backends now also return `ErrInvalidTTL` (commit `a01fad7`).
- ✅ **`clientip` default tightened to loopback only** — RFC1918 + ULA defaults removed; `ParseTrustedProxiesStrict` returns errors on invalid CIDR entries (commit `ab4df5c`).
- ✅ **CSRF requires shared secret** — `csrf.New` panics in non-dev when no secret is configured; `WithDevSecret()` opt-in for the random per-process fallback (commit `7f0efe3`).
- ✅ **CSRF SkipCheck regen bug** — regenerated cookie now triggers a 403 with retry hint instead of letting the request through with the stale invalid cookie (commit `7f0efe3`).
- ✅ **CSRF rejects SameSite=None without Secure** — panics at construction (commit `3784af8`). Browsers reject the combination anyway; catching it at startup avoids the silent "no cookie installed" failure.
- ✅ **Timeout buffer cap default lowered to 1 MiB** — adds `WithMaxBufferSize` for endpoints that legitimately stream multi-megabyte JSON (commit `30113f9`).
- ✅ **`secheaders` honours `X-Forwarded-Proto`** — `WithTrustedProxiesForProto` enables HSTS behind TLS-terminating ingresses; `WithForceHSTS` for topologies the kit cannot observe (commit `b324d2e`).
- ✅ **`stack.Default` includes Timeout middleware** — 30s default; `WithTimeout(d)` / `WithoutTimeout()` to override (commit `a0b49e8`). Closes the "production stack ships without a wall-clock cap" gap.
- ✅ **`stack.Default` includes panic-recovery middleware** — new `httpx/middleware/recover` sub-package; prepended as the OUTERMOST kit layer; `http_panics_total{method}` counter; `WithoutRecover()` opt-out (commit `e96ffdf`). Closes the original CRITICAL #2.
- ✅ **Idempotency mandatory user scoping + identity-header strip** — `Middleware()` panics without `WithUserExtractor` or `WithAllowSharedKeys`; `Set-Cookie`/`Authorization`/`WWW-Authenticate`/`Proxy-Authenticate`/`Strict-Transport-Security` stripped from cached responses; `WithPreserveHeaders` lets callers override per header (commit `83da31b`).

## Open

## Recently Landed (Phase 3, commit `326210d`)

- ✅ **`timeout.WithHard()`** — hard-timeout mode returns 503 immediately and stops waiting for the cooperative handler goroutine; the handler keeps running in the background but cannot stall the HTTP worker. Cooperative mode remains the default and is now explicitly documented.
- ✅ **Shared client-IP resolver** — `logging.WithClientIPResolver` + `WithTrustedProxies` let consumers pin one resolver across logging and ratelimit so log lines and limit decisions agree.
- ✅ **Tracing hijack handling** — when the response is hijacked the tracing middleware now records `HTTPResponseStatusCode(101)` instead of the recorder's misleading 200 default.
- ✅ **Rate-limit cleanup doc** — comment rewritten to reflect that LRU pressure is the real GC; cleanup is a best-effort O(1000) hint.

CSRF session-bound HMAC primitive landed as `security/csrf` (commit `ca3f5aa`). The httpx/middleware/csrf refit (becoming a thin wrapper) is a follow-up and remains tracked under `new/06`.

### Open

### [HIGH] CSRF middleware session-bound HMAC still missing
**File**: `httpx/middleware/csrf/csrf.go:128-189`
**Issue**: HMAC is over a random nonce only — bind it to the session ID so a sibling app on the same eTLD+1 cannot Set-Cookie an attacker-controlled token. The Secure-default half of the audit's recommendation is closed (commit `3784af8`); session binding is genuinely a different primitive and belongs in [new/06-security-csrf-tokens.md](../new/06-security-csrf-tokens.md).
**Fix**: HMAC over `session_id || nonce`; ship as the new package and let the existing csrf middleware become a thin wrapper.
**Effort**: M

### Migration checklist

- [x] Phase 1: prepend recover middleware in `stack.Default`. ✅ `e96ffdf`
- [x] Phase 1: timeout middleware buffer cap default 1 MiB. ✅ `30113f9`
- [x] Phase 1: secheaders honor `X-Forwarded-Proto` from trusted proxies. ✅ `b324d2e`
- [x] Phase 1: `clientip` default to no-trusted-proxies; require explicit CIDRs via `Builder.WithTrustedProxies`. ✅ `ab4df5c` (Builder integration still TODO)
- [x] Phase 1: CSRF require shared secret in non-dev; add `Builder.WithCSRFSecret`. ✅ `7f0efe3` (Builder integration still TODO)
- [x] Phase 1: CSRF SkipCheck regeneration bug fix. ✅ `7f0efe3`
- [x] Phase 1: CSRF reject SameSite=None without Secure. ✅ `3784af8`
- [x] Phase 2: idempotency identity-header strip + mandatory user extractor. ✅ `83da31b`
- [x] Phase 2: timeout middleware hard-timeout mode. ✅ `326210d`
- [ ] Phase 2: CSRF session-bound HMAC (depends on [new/06](../new/06-security-csrf-tokens.md)).
- [x] Phase 3: shared client-IP resolver for logging + ratelimit. ✅ `326210d`
- [x] Phase 3: tracing hijack handling. ✅ `326210d`

### Related new packages

- [new/01-httpx-middleware-recover.md](../new/01-httpx-middleware-recover.md) — recover middleware.
- [new/06-security-csrf-tokens.md](../new/06-security-csrf-tokens.md) — session-bound CSRF primitive.
- [new/08-security-csp-nonce.md](../new/08-security-csp-nonce.md) — CSP nonce middleware.
- [new/16-observability-red-metrics.md](../new/16-observability-red-metrics.md) — RED middleware with proper buckets.
- [new/17-httpx-problem-details.md](../new/17-httpx-problem-details.md) — RFC 7807 writer.
