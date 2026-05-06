# crypto/ + security/ — encrypt, signing, jwtutil, netutil

The security primitives are largely correct (GCM with random nonces, HMAC via `subtle`, Lua scripts where they're needed). Remaining bugs are around defaults, missing constructors, and a couple of optional features.

## Landed

- ✅ **`FieldEncryptor.Encrypt` prefix-shortcut bypass removed** — `Encrypt` always encrypts; new `EncryptIfPlain` AEAD-verifies before pass-through (commit `99917ac`).
- ✅ **`FieldEncryptor` AAD support** — new `EncryptWithContext` / `DecryptWithContext` bind ciphertext to caller-supplied AAD (row pk, tenant ID, etc.) (commit `99917ac`). `SealBytesAAD` / `OpenBytesAAD` added at the byte primitive layer.
- ✅ **JWT `WithExpectedAudience`** — `KeySet.ExpectedAudience` field + `Provider` option; `jwt.WithAudience()` plumbed into `Verify` (commit `c502dd2`).
- ✅ **JWT `NewProvider` panics without expected issuer in non-dev** — `WithAllowAnyIssuer` opt-out for federated cases (commit `659babb`).
- ✅ **SSRF default TLS 1.3** — `SSRFSafeTransport.TLSClientConfig.MinVersion = tls.VersionTLS13` (commit `c502dd2`).
- ✅ **SSRF safe-redirect mode** — `SSRFSafeDynamicTransport` re-resolves and re-validates on every dial; `SSRFSafeClientFollowRedirects(maxHops)` follows redirects through the dynamic guard (commit `b6a4a9a`).
- ✅ **SSRF `*FromURL` constructors** — `SSRFSafeClientFromURL` / `SSRFSafeTransportFromURL` parse a raw URL, reject non-http/https schemes, return the parsed `*url.URL` (commit `a649495`).

## Open

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

### [MEDIUM] `Signer` has no `WithFutureSkew` option
**File**: `crypto/signing/signing.go:80`
**Issue**: Hard-coded 30s skew. Some integrations need wider tolerance; some want zero.
**Fix**: Add `WithFutureSkew(time.Duration)` option mirroring `WithClock`.

### Migration checklist

- [ ] Phase 2: JWT staleness metric + max-stale rejection.
- [ ] Phase 2: `StaticKeyStore` `New*E`/`Must*` split.
- [x] Phase 2: SSRF `*FromURL` constructors. ✅ `a649495`
- [x] Phase 2: JWT mandatory issuer (jwtutil layer). ✅ `659babb` (Builder integration still TODO)
- [ ] Phase 2: signing `WithFutureSkew`.

### Related new packages

- [new/03-crypto-passhash.md](../new/03-crypto-passhash.md) — argon2id helper (currently absent).
- [new/04-crypto-envelope.md](../new/04-crypto-envelope.md) — envelope encryption + KMS providers.
- [new/05-crypto-paseto.md](../new/05-crypto-paseto.md) — safer JWT alternative.
- [new/07-security-secret-string.md](../new/07-security-secret-string.md) — `SecretString` type.
