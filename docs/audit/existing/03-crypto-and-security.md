# crypto/ + security/ — encrypt, signing, jwtutil, netutil

The security primitives are largely correct (GCM with random nonces, HMAC via `subtle`, Lua scripts where they're needed). The bugs are in defaults, missing audience checks, and the `FieldEncryptor` prefix shortcut.

### [HIGH] `FieldEncryptor.Encrypt` silently bypasses encryption when input begins with the prefix
**File**: `crypto/encrypt/encrypt.go:42-48`
**Issue**: Returns input unchanged when it starts with `enc:v1:` (printable ASCII!) or `\x00enc:v2:`. Any user-supplied value beginning with `enc:v1:` is stored as plaintext with no warning. An attacker who controls a value submitted into an encrypted field can deliberately bypass encryption with a 7-byte prefix. The "idempotent re-encrypt" use case is also unsafe — there's no AEAD verification.
**Fix**: Remove the idempotency shortcut. If callers need it, expose a separate `EncryptIfPlain(value string) string` that verifies the suffix base64-decodes and authenticates under the current key before treating it as already-encrypted.
**Effort**: M
**Migration**: Phase 2 — call sites that intentionally re-encrypt must switch to the new helper.

### [HIGH] `FieldEncryptor` doesn't bind ciphertext to row context (no AAD)
**File**: `crypto/encrypt/encrypt.go` (entry points) + `crypto/encrypt/gcm.go:64`
**Issue**: `gcm.Seal(nonce, nonce, plaintext, nil)` passes nil AAD. Ciphertext from row A can be swapped into row B and decrypts cleanly. For database fields the standard mitigation is to bind ciphertext to a stable row identifier.
**Fix**: Add `EncryptWithContext(plaintext, aad []byte)` / `DecryptWithContext(ciphertext, aad []byte)` and document the row-binding pattern.
**Effort**: M

### [HIGH] JWT verifier never validates audience
**File**: `security/jwtutil/jwtutil.go:81-94`
**Issue**: Validates issuer (when `ExpectedIssuer != ""`) and signature/exp, but never `aud`. A token issued for service A is valid at service B if both trust the same Oathkeeper. No `WithExpectedAudience` exists.
**Fix**: Add `KeySet.ExpectedAudience` field + `WithExpectedAudience` provider option; pass `jwt.WithAudience(aud)` in `Verify`. Treat missing audience config as a startup error in `app/jwt_module.go`.
**Effort**: S
**Migration**: All consumers must set audience. Tie this to the [01-app-and-builder.md](01-app-and-builder.md) Builder change.

### [HIGH] JWT Provider serves stale keys forever after JWKS endpoint goes permanently bad
**File**: `security/jwtutil/jwtutil.go:226-240`
**Issue**: After initial fetch, periodic refresh failures only log; cached keys are served indefinitely. After rotation + permanent JWKS outage, the kit verifies with old keys (potentially compromised) and rejects all new tokens forever.
**Fix**: Track `lastSuccessfulFetch`. After a configurable max-stale duration (default 1h) start rejecting tokens with `ErrKeysetStale`. Expose staleness as a metric/health check.
**Effort**: S

### [HIGH] `StaticKeyStore` panics make rotation fragile
**File**: `crypto/signing/keystore.go:45-56`
**Issue**: `NewStaticKeyStore` panics on empty/short keys/missing currentID. With keys from env vars, one bad rotation kills the process at startup with a panic.
**Fix**: Add `NewStaticKeyStoreE(...) (*StaticKeyStore, error)`; keep panic version as `MustNewStaticKeyStore`.
**Effort**: S

### [HIGH] `SSRFSafeTransport`/`SSRFSafeClient` `host` parameter ambiguity
**File**: `security/netutil/ssrf.go:165-186`
**Issue**: `host` is fed both to `LookupIPAddr` (bare hostname) and `TLSClientConfig.ServerName` (bare hostname). Natural caller pattern uses `u.Host` which carries `:port` and breaks both. No validation.
**Fix**: Add `SSRFSafeTransportFromURL(ctx, *url.URL, ...)` that does the right extraction internally; deprecate the raw-host variant or validate it doesn't contain `:`/`/`.
**Effort**: S

### [HIGH] `SSRFSafeClient` returns 3xx responses to caller — redirect-following bypasses SSRF
**File**: `security/netutil/ssrf.go:198-203`
**Issue**: `CheckRedirect` returns `ErrUseLastResponse`. Callers that do their own redirect-following bypass SSRF entirely.
**Fix**: Add `SSRFSafeClientFollowRedirects(ctx, *url.URL, maxHops int)` that re-runs `ResolveAndValidate` for each redirect target before dialing.
**Effort**: M

### [HIGH] `SSRFSafeTransport` caches resolved IP forever; no re-resolution
**File**: `security/netutil/ssrf.go:147-186`
**Issue**: IP captured at construction; never re-resolved. With `DisableKeepAlives: true` every reuse dials the same potentially-stale IP. Doc says "short-lived use" but API doesn't enforce.
**Fix**: Either expire transport after a TTL (return error from `DialContext` once expired) or re-resolve and re-validate on every dial inside `DialContext`. The latter is the textbook safe pattern.
**Effort**: M

### [MEDIUM] `SSRFSafeTransport` allows TLS 1.2 while internal mTLS uses 1.3
**File**: `security/netutil/ssrf.go:180`
**Issue**: Inconsistent — outbound calls to user URLs accept weaker TLS than internal service-to-service.
**Fix**: Default `MinVersion: TLS 1.3` with explicit opt-out for legacy interop.

### [MEDIUM] `Signer` has no `WithFutureSkew` option
**File**: `crypto/signing/signing.go:80`
**Issue**: Hard-coded 30s skew. Some integrations need wider tolerance; some want zero.
**Fix**: Add `WithFutureSkew(time.Duration)` option mirroring `WithClock`.

### Migration checklist

- [ ] Phase 2: remove `FieldEncryptor.Encrypt` prefix shortcut; add `EncryptWithContext`/`DecryptWithContext`.
- [ ] Phase 2: JWT `WithExpectedAudience` (mandatory in app builder).
- [ ] Phase 2: JWT staleness metric + max-stale rejection.
- [ ] Phase 2: `StaticKeyStore` `New*E`/`Must*` split.
- [ ] Phase 2: SSRF — `*FromURL` constructors, safe-redirect mode, default TLS 1.3.
- [ ] Phase 2: signing `WithFutureSkew`.

### Related new packages

- [new/03-crypto-passhash.md](../new/03-crypto-passhash.md) — argon2id helper (currently absent).
- [new/04-crypto-envelope.md](../new/04-crypto-envelope.md) — envelope encryption + KMS providers.
- [new/05-crypto-paseto.md](../new/05-crypto-paseto.md) — safer JWT alternative.
- [new/07-security-secret-string.md](../new/07-security-secret-string.md) — `SecretString` type.
