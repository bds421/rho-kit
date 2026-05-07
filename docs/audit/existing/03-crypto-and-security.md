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

_Closed — see Recently Landed below._

## Recently Landed (Phase 3, commit `69fcc85`)

- ✅ **JWT Provider max-stale rejection** — `lastSuccessfulFetch` tracked atomically; default max-stale = 1 hour; `WithMaxStale(d)` overrides; `LastSuccessfulFetch()` and `Staleness()` accessors expose it for health/metrics.
- ✅ **`StaticKeyStore` error-API split** — `NewStaticKeyStoreE(keys, currentID) (*StaticKeyStore, error)` for callers that load keys from env vars; the original panic-on-bad-rotation constructor stays as `NewStaticKeyStore` (now a thin wrapper).
- ✅ **`signing.WithFutureSkew(d)`** — option replaces the hard-coded 30s constant; default preserved when not set.

### Migration checklist

- [x] Phase 2: JWT staleness metric + max-stale rejection. ✅ `69fcc85`
- [x] Phase 2: `StaticKeyStore` `New*E`/`Must*` split. ✅ `69fcc85`
- [x] Phase 2: SSRF `*FromURL` constructors. ✅ `a649495`
- [x] Phase 2: JWT mandatory issuer (jwtutil layer). ✅ `659babb` + Builder enforcement `4d04fe1`
- [x] Phase 2: signing `WithFutureSkew`. ✅ `69fcc85`

### Related new packages

- [new/03-crypto-passhash.md](../new/03-crypto-passhash.md) — argon2id helper (currently absent).
- [new/04-crypto-envelope.md](../new/04-crypto-envelope.md) — envelope encryption + KMS providers.
- [new/05-crypto-paseto.md](../new/05-crypto-paseto.md) — safer JWT alternative.
- [new/07-security-secret-string.md](../new/07-security-secret-string.md) — `SecretString` type.
