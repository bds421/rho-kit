# NEW: httpx/middleware/signedrequest

**Phase**: 5 (Tier‑2)
**Module path**: `github.com/bds421/rho-kit/httpx/middleware/signedrequest` (server-side) + `httpx/sign` (client-side)

## Why

Webhook receivers, S2S APIs, and any "machine-to-machine HTTP that crosses a trust boundary without mTLS" need request signing. The kit has `crypto/signing` (HMAC primitive) but no HTTP middleware that wraps it with the standard fields: timestamp, body hash, host, method, path, nonce.

Today consumers either roll their own (and forget at least one of: replay protection, body binding, host binding) or skip signing entirely.

## Public API

### Server-side middleware

```go
package signedrequest

// Middleware verifies signed inbound requests. Adds timestamp + body-hash +
// host + method + path + nonce to the canonical signing string. Rejects
// requests outside the configured clock skew, with replayed nonces, or
// without the configured headers.
func Middleware(verifier crypto/signing.Verifier, opts ...Option) func(http.Handler) http.Handler

type Option func(*config)

func WithMaxClockSkew(time.Duration) Option        // default 5min
func WithNonceCache(c data/cache.Cache) Option      // for replay protection (REQUIRED)
func WithRequiredHeaders(...string) Option          // additional headers signed (e.g. X-Tenant-ID)
func WithBodyMaxSize(int64) Option                  // default 10 MiB
```

The nonce cache holds nonces for `2 × clock skew` to prevent replay; without one, the middleware refuses to start (fails closed).

### Client-side helper

```go
package sign

// Wrap returns an http.RoundTripper that signs every outbound request before
// dispatching. Works with any http.Client.
func Wrap(rt http.RoundTripper, signer crypto/signing.Signer, opts ...Option) http.RoundTripper

func WithKeyID(string) Option                       // sets x-signature-key-id header
func WithIncludeHeaders(...string) Option           // additional headers in the signing string
```

## Wire format

Request headers added by the client (and verified by the middleware):

```
X-Signature-Timestamp: 1715016234        # unix seconds
X-Signature-Nonce: <base64 16-byte>       # random per request
X-Signature-Key-Id: prod-2026-01          # which key signed this
X-Signature: hmac-sha256=<base64 hmac>    # HMAC of canonical string
```

Canonical signing string (deterministic, normalized):

```
<method>\n
<path>\n
<host>\n
<timestamp>\n
<nonce>\n
<sha256(body)>\n
<canonical headers in lexicographic order, lowercase name + value>
```

## Replay protection

Nonces are stored in a cache with TTL = 2 × clock skew. If `data/cache/rediscache` is used, replay protection works across all instances; if `data/cache.MemoryCache`, only per-instance (warn at startup if running multi-instance).

## Definition of done

- [ ] Server middleware with mandatory nonce cache.
- [ ] Client RoundTripper wrapper.
- [ ] Tests: round-trip; replayed nonce rejected; expired timestamp rejected; modified body rejected; modified header rejected.
- [ ] Builder integration (`Builder.WithSignedRequests(verifier, nonceCache)`).
- [ ] Recipe in `docs/ai/security.md`.

## Related

- [existing/03-crypto-and-security.md](../existing/03-crypto-and-security.md) — relies on `crypto/signing` (already exists).
- [new/06-security-csrf-tokens.md](06-security-csrf-tokens.md) — same family of "session/request-scoped HMAC" patterns.
