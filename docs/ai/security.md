# Security — Encryption, Signing, JWT, TLS, SSRF, ASVS

Packages: `crypto/encrypt`, `crypto/envelope`, `crypto/envelope/awskms`,
`crypto/envelope/azurekeyvault`, `crypto/envelope/gcpkms`,
`crypto/envelope/vaulttransit`, `crypto/signing`,
`security/jwtutil`, `security/jwtutil/revocation`, `security/apikey`,
`data/apikey/postgres`, `httpx/middleware/apikey`, `app/apikey`,
`crypto/masking`, `security/netutil`, `security/mtlsidentity`, `security/asvs`

Snippet status: Go and JavaScript blocks in this recipe are illustrative
fragments unless explicitly introduced as generated or executable code.
Buildable golden-path evidence lives in `cmd/kit-new` scaffold tests and
`examples/agentic-service`.

## When to Use

| Need | Package |
|---|---|
| Encrypt DB columns (PII, secrets) | `crypto/encrypt` (FieldEncryptor) |
| Envelope-encrypt records with rotatable KEKs | `crypto/envelope` + `awskms` / `azurekeyvault` / `gcpkms` / `vaulttransit` |
| Encrypt files at rest | `storage/encryption` (see [storage.md](storage.md)) |
| Sign/verify webhooks | `crypto/signing` (HMAC-SHA256) |
| Sign service-to-service HTTP requests with replay protection | `httpx/sign` + `httpx/middleware/signedrequest` |
| Verify JWTs from a JWKS endpoint | `security/jwtutil` |
| Revoke JWTs after logout | `security/jwtutil/revocation` |
| Issue/verify opaque API keys for external/customer access | `security/apikey` + `httpx/middleware/apikey` + `app/apikey` |
| Persist API keys | `data/apikey/postgres` (or `apikey.NewMemoryRepository`) |
| mTLS between services | `security/netutil` |
| Normalize mTLS SAN/CN allowlists | `security/mtlsidentity` |
| Redact secrets in logs | `crypto/masking` |
| Prevent SSRF in user-supplied URLs | `security/netutil` |
| Inspect kit ASVS control manifests | `security/asvs` |

## Field Encryption (AES-256-GCM)

Encrypt sensitive DB columns with the current versioned prefix (`enc:v3:`).
Prefer the AAD-bound methods so ciphertext copied between rows, tenants, or
columns fails authentication.

```go
key := []byte(os.Getenv("FIELD_ENCRYPTION_KEY")) // exactly 32 bytes
enc, err := encrypt.NewFieldEncryptor(key)
if err != nil { return err }

aad := []byte("users:" + userID + ":ssn")

// Encrypt before storing:
encrypted, err := enc.EncryptWithContext(user.SSN, aad)
// "enc:v3:base64(nonce||ciphertext||tag)"

// Decrypt after reading:
plaintext, err := enc.DecryptWithContext(row.SSN, aad)
// Values without a recognised prefix fail closed with ErrPlaintextNotAllowed.

// Idempotent re-save path:
result, err := enc.EncryptIfPlainWithContext(value, aad)

// Nil-safe helper for optional encryption configuration:
result, err := encrypt.EncryptOptionalWithContext(enc, value, aad)
```

### Low-Level Byte Encryption

```go
gcm, err := encrypt.NewGCM(key32) // tink.AEAD
sealed, err := encrypt.EncryptBytes(gcm, plaintext) // nonce || ciphertext
opened, err := encrypt.DecryptBytes(gcm, sealed)
// AAD-binding variants: encrypt.EncryptBytesAAD, encrypt.DecryptBytesAAD.
```

## Envelope Encryption

Use `crypto/envelope` when records need per-write DEKs and online KEK rotation.
Production KEKs live in split modules so services only pull the SDK they use:
`crypto/envelope/awskms`, `crypto/envelope/azurekeyvault`,
`crypto/envelope/gcpkms`, and `crypto/envelope/vaulttransit`. Use
`crypto/envelope/kekstatic` only for tests and local development.

Credential rotation across DB, Redis, brokers, storage, CSRF, signed requests,
JWT, PASETO, and KMS/Vault is summarized in
[credential-rotation.md](credential-rotation.md).

Per-backend constructors are named `NewKEK` (each backend ships its own
type, so the verb stays explicit at the call site). Wrap an Encryptor
around any KEK with `envelope.NewEncryptor(kek)`.

Azure support uses Azure Key Vault or Managed HSM `WrapKey`/`UnwrapKey`.
Configure the Azure `azkeys.Client` with the deployment's credential, vault
URL, retry policy, and transport, then construct
`azurekeyvault.NewKEK(client, azurekeyvault.Config{KeyName: "orders-dek"})`.
Leave `KeyVersion` empty to wrap with the current primary key version; the
envelope records Azure's version-qualified KID so old records unwrap with the
exact version that produced them.

Vault support is specifically HashiCorp Vault Transit. Configure the Vault
client with address, token, namespace/TLS/retry policy, then construct
`vaulttransit.NewKEK(client, vaulttransit.Config{KeyName: "orders-dek"})`.
Optional `Config.Context` maps to Vault Transit `context`, so the Transit key
must be `derived=true` when that field is set.

The current blob format is v3: keyID length is uint16 and the body AAD
is `domainSep || varint(len(callerAAD)) || callerAAD`. v2 blobs continue
to decrypt unchanged.

## HMAC Webhook Signing

Stripe-model webhook signatures:

```go
// Sender:
secret := signing.NewSecret([]byte("0123456789abcdef0123456789abcdef"))
sig, ts, err := signing.Sign(secret, body)
// sig = "sha256=<hex>", ts = unix timestamp
req.Header.Set("X-Signature", sig)
req.Header.Set("X-Timestamp", strconv.FormatInt(ts, 10))

// Receiver:
secret := signing.NewSecret([]byte("0123456789abcdef0123456789abcdef"))
tsHeaders := r.Header.Values("X-Timestamp")
sigHeaders := r.Header.Values("X-Signature")
if len(tsHeaders) != 1 || len(sigHeaders) != 1 {
    httpx.WriteError(w, 401, "invalid signature")
    return
}
ts, err := strconv.ParseInt(strings.TrimSpace(tsHeaders[0]), 10, 64)
if err != nil {
    httpx.WriteError(w, 401, "invalid signature")
    return
}
if err := signing.Verify(secret, body, ts, strings.TrimSpace(sigHeaders[0]), signing.DefaultSignatureMaxAge); err != nil {
    httpx.WriteError(w, 401, "invalid signature")
    return
}
```

`DefaultSignatureMaxAge` = 5 minutes. Constant-time comparison. No nonce — add external nonce store if within-window replay prevention is needed.

## Signed HTTP Requests (Replay-Protected)

Use `httpx/sign` with `httpx/middleware/signedrequest` for machine-to-machine
HTTP calls that need body, host, path, timestamp, nonce, and selected header
binding. Verification requires a `NonceStore`; use the in-memory store only for
single-instance deployments and a shared store such as Redis for multi-replica
services.

```go
// Receiver:
nonceStore := signedrequest.NewMemoryNonceStore(10 * time.Minute) // single instance
verifyMW := signedrequest.Middleware(keyResolver, nonceStore,
    signedrequest.WithRequiredHeaders("X-Tenant-ID"),
)

// Sender:
base := httpx.NewHTTPClient(10*time.Second, tlsConfig)
client := &http.Client{
    Transport: sign.Wrap(base.Transport, hmacKey, "key-2026-05",
        sign.WithIncludeHeaders("X-Tenant-ID"),
    ),
    Timeout: 10 * time.Second,
}
```

For outbound key rotation, pass a reloading key store to `sign.WrapKeyStore`
instead of rebuilding the HTTP client:

```go
client.Transport = sign.WrapKeyStore(base.Transport, keyStore,
    sign.WithIncludeHeaders("X-Tenant-ID"),
)
```

Pinned canonical headers must be singleton valid HTTP header values. The signer
and verifier reject duplicate values and control characters in `Content-Type`,
signature headers, and any headers listed in `WithIncludeHeaders` /
`WithRequiredHeaders`.
Nonce generation failures are returned as signing errors so callers can fail the
request without reusing or downgrading the nonce.
Key IDs used by `crypto/signing.StaticKeyStore` and `httpx/sign` must be
non-empty bounded header-safe tokens; rotate by adding a new safe ID and making
it current, not by changing the bytes under an unsafe or ambiguous ID.
Use `httpx/sign` with `httpx/middleware/signedrequest` for all service-to-service
HTTP signing. The legacy `httpx/reqsign` package was removed in v2.0.0.

## JWT Verification (JWKS)

Auto-refreshing JWKS provider for asymmetric JWT verification:

```go
// Setup (in Builder, jwt.Module does this automatically):
provider := jwtutil.NewProvider(
    cfg.JWKSURL,
    httpx.NewHTTPClient(5*time.Second, nil),
    10*time.Minute, // refresh interval
    jwtutil.WithExpectedIssuer(cfg.JWTIssuer),
    jwtutil.WithExpectedAudience("my-service"),
)
go func() {
    if err := provider.Run(ctx); err != nil {
        logger.Error("jwt provider stopped", "err", err)
    }
}() // retries initial fetch indefinitely; one Run per provider

// In middleware (automatic via auth.JWT):
claims, err := provider.VerifyContext(r.Context(), tokenString, time.Now())
// claims.ID, claims.Subject, claims.Permissions, claims.Scopes
```

Pass `nil` for the HTTP client to use jwtutil's default JWKS client
(5s timeout, 64 KiB response-header cap, TLS 1.2+ floor). Use a custom
client only when you need service-mesh routing or additional transport
instrumentation. JWKS URLs must be absolute `https://` URLs without
embedded credentials; local `http://` endpoints require the explicit
`jwtutil.WithAllowInsecureURL()` opt-in.
`Provider.Run` returns an error for nil contexts, nil receivers, and duplicate
starts. Treat providers as one-shot lifecycle components; construct a new
provider instead of restarting a stopped one.

### JWT Revocation

Use a shared cache backend when a service needs logout / admin-revoke semantics.
The revocation key is based on `iss` + `jti` and expires with the token.

```go
revocations := revocation.New(cacheBackend) // data/cache.Cache-compatible
provider := jwtutil.NewProvider(
    cfg.JWKSURL,
    httpx.NewHTTPClient(5*time.Second, nil),
    10*time.Minute,
    jwtutil.WithExpectedIssuer(cfg.JWTIssuer),
    jwtutil.WithExpectedAudience("my-service"),
    jwtutil.WithRevocationChecker(revocations),
)

// Logout / admin revoke:
claims, err := provider.VerifyContext(r.Context(), tokenString, time.Now())
if err == nil {
    err = revocations.Revoke(r.Context(), claims)
}
```

Tokens must carry a `jti` claim to participate in revocation. When a
revocation checker is configured, missing `jti` fails closed.

**Env vars:**
| Variable | Default | Notes |
|---|---|---|
| `JWKS_URL` | unset | Required by services that register `jwt.Module(jwksURL, ...)` or construct a `jwtutil.Provider` |
| `JWT_CACHE_TTL_MINUTES` | `5` | Key cache TTL |

### Testing JWTs

```go
ks, _ := jwtutil.ParseKeySetFromPEM(testPrivKeyPEM, "test-kid")
provider := jwtutil.NewProviderWithKeySet(ks) // no HTTP fetch
```

## API Keys (Opaque, External-Facing)

Use opaque API keys for external/customer/AI-agent access — the convention
OpenAI, Anthropic, Stripe and GitHub all use. Prefer these over JWTs when the
credential is long-lived and must be **revocable on demand**; prefer JWTs (above)
for your own short-lived sessions where statelessness matters.

A key is a high-entropy random secret shown to the owner **exactly once**; only
its SHA-256 hash is stored. The token format is `<prefix>_<id>_<secret>` — the
public `id` is an indexed lookup key, so verification is one indexed read plus
one constant-time hash compare. SHA-256 (not argon2/bcrypt) is correct here:
the secret already carries 256 bits of entropy, so a slow KDF buys nothing and
its per-row salt would make lookup-by-hash impossible.

```go
// Issue (privileged side — behind an authenticated admin endpoint):
mgr := apikey.NewManager(repo) // repo: apikey.Repository (postgres or in-memory)
key, token, err := mgr.Issue(ctx, apikey.IssueOptions{
    Owner:     "tenant-1",
    Scopes:    []string{string(scopeOrdersRead)}, // strings; validated at the edge
    ExpiresAt: time.Now().Add(90 * 24 * time.Hour),
})
// Deliver token.RevealString() to the owner ONCE; never log or store it.
// Only `key` (with the hash) is persisted by Issue.
```

```go
// Authenticate (request path) — wire via the app module at PhaseAuth:
return app.New("my-service", version, base).
    With(apikey.Module(repo)). // app/apikey: extracts, verifies, attaches scopes
    Router(...).Run()

// Or inline with the middleware package directly:
h = apikeymw.Middleware(apikeymw.Config{Repository: repo})(
    apikeymw.RequireScopes(scopeOrdersRead)(next), // 403 if the key lacks it
)
// In handlers: apikeymw.OwnerFromContext(r), KeyIDFromContext(r), ScopesFromContext(r)
```

The middleware reads `Authorization: Bearer <token>` (or `X-API-Key`), returns
401 for missing/malformed/expired/revoked/unknown keys (never revealing which
ids exist), and 403 when a required scope is absent. `RequireScopes` validates
its scopes against the shared authz registry at **construction**, so a typo
panics at startup instead of silently rejecting every request.

**Rotation with overlap** — issue a replacement while the old key keeps working
for a grace window, so clients can migrate without an outage:

```go
newKey, newToken, err := mgr.Rotate(ctx, oldKeyID, 24*time.Hour)
// old key stays valid for 24h, then stops; new key inherits owner/scopes/kind.
mgr.Revoke(ctx, keyID) // immediate kill
```

Rotation needs no special storage: `Rotate` schedules the old key's revocation
at `now + overlap`, and `Verify` treats a future `RevokedAt` as "still valid
until then."

**Key kinds:** `apikey.KindAPI` (default) authenticates requests;
`apikey.KindRoot` marks keys permitted to manage other keys — gate your
issuance endpoint on it.

**Testing:** `apikey.NewMemoryRepository()` is a drop-in `Repository` for unit
tests; the Postgres implementation lives in `data/apikey/postgres` (embed
`postgres.Migrations`). See `examples/api-gateway` (`/api/keys-demo` route) for
end-to-end wiring.

## mTLS

```go
// Load from env (TLS_CA_CERT, TLS_CERT, TLS_KEY):
tlsCfg := netutil.LoadTLS()
if err := tlsCfg.Validate(); err != nil { return err }

// Server: TLS 1.3, requires verified client certificates by default
serverTLS, _ := tlsCfg.ServerTLS()

// Gateway-fronted services can explicitly downgrade to verify-if-present:
gatewayTLS, _ := tlsCfg.ServerTLS(netutil.WithOptionalClientCert())
serverLog := slog.NewLogLogger(logger.Handler(), slog.LevelWarn)
srv := httpx.NewServer(addr, handler, httpx.WithTLSConfig(serverTLS), httpx.WithErrorLog(serverLog))

// Client: presents client cert, verifies server against CA
clientTLS, _ := tlsCfg.ClientTLS()
client := httpx.NewHTTPClient(10*time.Second, clientTLS)
```

Set all three env vars together. If any is empty, TLS is disabled.

`httpx/middleware/auth` and `grpcx/interceptor` both use
`security/mtlsidentity` to normalize allowed SAN/CN identities. Use the same
package for custom transports so DNS SANs, URI SANs, empty entries, and invalid
identity errors behave consistently across HTTP and gRPC.

## Masking (Log Sanitization)

```go
masking.MaskURL(url)         // "https://example.com/***"
masking.MaskString(apiKey, 4) // "sk-p****" or "[REDACTED]" if too short
masking.MaskMapValues(m)      // copy with all values = "***"

// Decrypt then mask (for encrypted URLs in DB):
masking.DecryptAndMaskURL(encryptedURL, encryptor)
```

## SSRF Prevention

```go
// Validate user-supplied URL doesn't resolve to private IP:
ip, err := netutil.ResolveAndValidate(ctx, userHost, nil)
if err != nil {
    return apperror.NewValidation("URL resolves to private network")
}

// Full SSRF-safe HTTP transport (prevents DNS rebinding):
transport, resolvedIP, err := netutil.SSRFSafeTransport(ctx, userHost, nil)
if err != nil { return err }
client := &http.Client{Transport: transport, Timeout: 10*time.Second}
resp, err := client.Get("https://" + userHost + "/webhook")
```

`IsPrivateIP` covers: RFC 1918, loopback, link-local, CGNAT, Teredo, 6to4, NAT64.

**Important:** Create a new `SSRFSafeTransport` per request — the resolved IP may go stale.
The transport is pinned to the host it resolved at construction and rejects
later requests that try to dial a different host through the same client.

Use `SSRFSafeClientFollowRedirects(maxHops, ...)` only when redirects are part
of the upstream contract. It validates the initial request URL and every
redirect hop, then re-resolves each dial through the SSRF guard.

### Dev Mode (localhost / private IPs)

During local development, services often talk to each other over localhost or
Docker-internal networks. Pass `WithAllowPrivateIPs()` to skip private-IP filtering:

```go
client, ip, err := netutil.SSRFSafeClient(ctx, "localhost", nil, netutil.WithAllowPrivateIPs())
```

A `slog.Warn` is emitted every time this option is active.

**Never use `WithAllowPrivateIPs` in production** — it completely disables SSRF protection.

## CSRF Protection (Double-Submit Cookie)

The `httpx/middleware/csrf` package implements the double-submit cookie pattern with
HMAC-signed tokens:

1. On every request, the middleware sets a `__csrf` cookie with a random token
   signed by a server-side HMAC secret (`HttpOnly=false`, `SameSite=Lax`,
   `Secure=true` by default).
2. The frontend reads the cookie via JavaScript and sends it back as the
   `X-CSRF-Token` header on mutating requests (POST, PUT, PATCH, DELETE).
3. The middleware verifies: (a) constant-time comparison of cookie and header
   values, and (b) the HMAC signature is valid (token was issued by this server).
4. Safe methods (GET, HEAD, OPTIONS) are exempt.

```go
csrfMiddleware := csrf.New(
    csrf.WithSecrets(cfg.CSRFSecret, cfg.PreviousCSRFSecrets...),
    csrf.WithAllowedOrigins("https://app.example.com"),
)
mux.Handle("/api/", csrfMiddleware(apiHandler))
```

For additional defense-in-depth, combine with `RequireJSONContentType`:
```go
csrfMiddleware := csrf.New(
    csrf.WithSecrets(cfg.CSRFSecret, cfg.PreviousCSRFSecrets...),
    csrf.WithAllowedOrigins("https://app.example.com"),
)
mux.Handle("/api/", csrfMiddleware(csrf.RequireJSONContentType(apiHandler)))
```

The frontend must include the token header on every mutating request:
```javascript
const csrfToken = document.cookie.match(/(?:^|;\s*)__csrf=([^;]*)/)?.[1];
fetch('/api/resource', {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken, 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify(data),
});
```

Options:
- `WithSecret(key)` — Required HMAC key (min 32 bytes).
- `WithSecrets(current, previous...)` — Zero-downtime rotation; signs new cookies with `current` and accepts previous secrets until the overlap window ends.
- `WithDevSecret()` — Local-development-only random per-process secret.
- `WithSecure(false)` — Explicit local plain-HTTP opt-out; default is `Secure=true`.
- `WithAllowedOrigins(origin...)` — Origin/Referer allowlist; each entry must be a full `http(s)://host[:port]` origin with no path, query, fragment, userinfo, or trailing slash.
- `WithCookieName(name)` — Custom cookie name (default: `__csrf`).
- `WithHeaderName(name)` — Custom header name (default: `X-CSRF-Token`).
- `WithSameSite(mode)` — SameSite attribute (default: `Lax`).

**Note:** `csrf.RequireCSRF` (header-presence-only check) is deprecated.
Use `csrf.New()` for all new code.

## Anti-Patterns

- **Never** hardcode encryption keys — use env vars (`FIELD_ENCRYPTION_KEY`).
- **Never** use encryption keys shorter than 32 bytes — `NewFieldEncryptor` rejects them.
- **Never** log decrypted values — use `crypto/masking` to sanitize before logging.
- **Never** trust `X-User-Id` / `x-user-id` identity metadata without mTLS verification and an impersonation guard — use `auth.RequireS2SAuth` or `grpcx/interceptor.MTLSAuthUnary`.
- **Never** reuse `SSRFSafeTransport` across requests — DNS can change.
- **Never** skip SSRF validation on user-supplied webhook URLs.
- **Never** use `csrf.RequireCSRF` for new browser flows — use `csrf.New(...)` with a shared secret.
- **Never** store API-key plaintext or log it — persist only the SHA-256 hash; `apikey.Generate`/`Manager.Issue` return the token once.
- **Never** run API keys through `crypto/passhash` (argon2) — that's for low-entropy passwords; high-entropy keys use the SHA-256 lookup hash in `security/apikey`.
- **Never** distinguish "unknown key" from "wrong secret" in responses — both are 401, as `httpx/middleware/apikey` does, so endpoints don't leak which key ids exist.
