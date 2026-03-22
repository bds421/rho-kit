# Security — Encryption, Signing, JWT, TLS, SSRF

Packages: `crypto/encrypt`, `crypto/signing`, `security/jwtutil`, `crypto/masking`, `security/netutil`, `security/netutil`

## When to Use

| Need | Package |
|---|---|
| Encrypt DB columns (PII, secrets) | `crypto/encrypt` (FieldEncryptor) |
| Encrypt files at rest | `storage/encryption` (see [storage.md](storage.md)) |
| Sign/verify webhooks | `crypto/signing` (HMAC-SHA256) |
| Verify JWTs from Oathkeeper | `security/jwtutil` |
| mTLS between services | `security/netutil` |
| Redact secrets in logs | `crypto/masking` |
| Prevent SSRF in user-supplied URLs | `security/netutil` |

## Field Encryption (AES-256-GCM)

Encrypt sensitive DB columns with a versioned prefix (`enc:v1:`):

```go
key := []byte(os.Getenv("FIELD_ENCRYPTION_KEY")) // exactly 32 bytes
enc, err := encrypt.NewFieldEncryptor(key)
if err != nil { return err }

// Encrypt before storing:
encrypted, err := enc.Encrypt(user.SSN)
// "enc:v1:base64(nonce||ciphertext)"

// Decrypt after reading:
plaintext, err := enc.Decrypt(row.SSN)
// Values without "enc:v1:" prefix pass through unchanged (migration-safe)

// Nil-safe helper (enc may be nil if encryption is optional):
result, err := encrypt.EncryptOptional(enc, value)
```

### Low-Level Byte Encryption

```go
gcm, err := encrypt.NewGCM(key32) // cipher.AEAD
sealed, err := encrypt.SealBytes(gcm, plaintext)   // nonce || ciphertext
opened, err := encrypt.OpenBytes(gcm, sealed)
```

## HMAC Webhook Signing

Stripe-model webhook signatures:

```go
// Sender:
secret := []byte("your-webhook-secret")
sig, ts := signing.Sign(body, secret)
// sig = "sha256=<hex>", ts = unix timestamp
req.Header.Set("X-Signature", sig)
req.Header.Set("X-Timestamp", strconv.FormatInt(ts, 10))

// Receiver:
secret := []byte("your-webhook-secret")
ts, _ := strconv.ParseInt(r.Header.Get("X-Timestamp"), 10, 64)
ok, err := signing.Verify(secret, body, ts, r.Header.Get("X-Signature"), signing.DefaultSignatureMaxAge)
if err != nil || !ok {
    httpx.WriteError(w, 401, "invalid signature")
    return
}
```

`DefaultSignatureMaxAge` = 5 minutes. Constant-time comparison. No nonce — add external nonce store if within-window replay prevention is needed.

## JWT Verification (JWKS)

Auto-refreshing JWKS provider for Oathkeeper ES256 tokens:

```go
// Setup (in Builder, WithJWT does this automatically):
provider := jwtutil.NewProvider(
    cfg.JWKSURL, // default: "https://oathkeeper:4456/.well-known/jwks.json"
    httpx.NewHTTPClient(5*time.Second, nil),
    10*time.Minute, // refresh interval
    jwtutil.WithExpectedIssuer("https://oathkeeper"),
)
go provider.Run(ctx) // retries initial fetch indefinitely

// In middleware (automatic via auth.RequireUserWithJWT):
ks := provider.KeySet()
claims, err := ks.Verify(tokenString, time.Now())
// claims.Subject, claims.Permissions, claims.Scopes
```

**Env vars:**
| Variable | Default | Notes |
|---|---|---|
| `JWKS_URL` | `https://oathkeeper:4456/.well-known/jwks.json` | |
| `JWT_CACHE_TTL_MINUTES` | `5` | Key cache TTL |

### Testing JWTs

```go
ks, _ := jwtutil.ParseKeySetFromPEM(testPrivKeyPEM, "test-kid")
provider := jwtutil.NewProviderWithKeySet(ks) // no HTTP fetch
```

## mTLS

```go
// Load from env (TLS_CA_CERT, TLS_CERT, TLS_KEY):
tlsCfg := netutil.LoadTLS()
if err := tlsCfg.Validate(); err != nil { return err }

// Server: TLS 1.3, optional client cert verification
serverTLS, _ := tlsCfg.ServerTLS()
srv := httpx.NewServer(addr, handler, httpx.WithTLSConfig(serverTLS))

// Client: presents client cert, verifies server against CA
clientTLS, _ := tlsCfg.ClientTLS()
client := httpx.NewHTTPClient(10*time.Second, clientTLS)
```

Set all three env vars together. If any is empty, TLS is disabled.

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
   `Secure` when configured).
2. The frontend reads the cookie via JavaScript and sends it back as the
   `X-CSRF-Token` header on mutating requests (POST, PUT, PATCH, DELETE).
3. The middleware verifies: (a) constant-time comparison of cookie and header
   values, and (b) the HMAC signature is valid (token was issued by this server).
4. Safe methods (GET, HEAD, OPTIONS) are exempt.

```go
csrfMiddleware := csrf.New(csrf.WithSecure(true)) // Secure=true in production
mux.Handle("/api/", csrfMiddleware(apiHandler))
```

For additional defense-in-depth, combine with `RequireJSONContentType`:
```go
mux.Handle("/api/", csrf.New(csrf.WithSecure(true))(csrf.RequireJSONContentType(apiHandler)))
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
- `WithSecret(key)` — HMAC key (min 32 bytes). Auto-generated if not set.
- `WithSecure(true)` — Set `Secure` flag on cookie (requires HTTPS).
- `WithCookieName(name)` — Custom cookie name (default: `__csrf`).
- `WithHeaderName(name)` — Custom header name (default: `X-CSRF-Token`).
- `WithSameSite(mode)` — SameSite attribute (default: `Lax`).

**Note:** `csrf.RequireCSRF` (header-presence-only check) is deprecated.
Use `csrf.New()` for all new code.

## Anti-Patterns

- **Never** hardcode encryption keys — use env vars (`FIELD_ENCRYPTION_KEY`).
- **Never** use encryption keys shorter than 32 bytes — `NewFieldEncryptor` rejects them.
- **Never** log decrypted values — use `crypto/masking` to sanitize before logging.
- **Never** trust `X-User-Id` header without mTLS verification — use `auth.RequireS2SAuth`.
- **Never** reuse `SSRFSafeTransport` across requests — DNS can change.
- **Never** skip SSRF validation on user-supplied webhook URLs.
