# NEW: security/csrf

**Phase**: 4 (Tier‑1 missing primitive)
**Module path**: `github.com/bds421/rho-kit/security/csrf`

## Why

The existing `httpx/middleware/csrf` middleware does HMAC + double-submit cookie but:

- No Origin/Referer check.
- Token isn't bound to the session (a sibling-subdomain XSS can overwrite the cookie and the kit accepts it).
- Default `Secure=false`.

Move the token primitive into a standalone package so the middleware becomes a thin wrapper, and so other components (form helpers, gRPC bridges) can reuse the verifier.

## Public API

```go
package csrf

// Token is an opaque CSRF token suitable for double-submit cookie patterns.
type Token string

// Issuer signs tokens bound to a session identifier and an issued-at timestamp.
// The session ID can be any opaque per-user value — JWT subject, session
// cookie content, etc.
type Issuer struct { /* ... */ }

func NewIssuer(secret []byte, opts ...Option) *Issuer

// Issue returns a fresh token for the given session identifier.
func (i *Issuer) Issue(sessionID string) (Token, error)

// Verify checks HMAC, expiration, and session binding.
func (i *Issuer) Verify(t Token, sessionID string, now time.Time) error

// Errors:
var (
    ErrTokenInvalid   = errors.New("csrf: invalid token")
    ErrTokenExpired   = errors.New("csrf: token expired")
    ErrSessionMismatch = errors.New("csrf: session binding mismatch")
)

// OriginAllowlist returns a predicate suitable for use in middleware that
// checks the Origin / Referer header against a configured allowlist.
type OriginAllowlist struct { /* ... */ }

func NewOriginAllowlist(origins ...string) *OriginAllowlist

func (a *OriginAllowlist) Allowed(r *http.Request) bool
```

Token format:
```
base64url( hmac(secret, sessionID || iat || nonce) || sessionID-prefix-hash || iat || nonce )
```

The session-prefix-hash lets `Verify` short-circuit when the wrong session is supplied (without revealing the full session ID in the token).

## Middleware refit

`httpx/middleware/csrf/csrf.go` becomes a thin shim over `security/csrf.Issuer` plus an `OriginAllowlist`. Default `Secure=true`. Mandatory `WithSessionExtractor func(*http.Request) string` (panic in `Default` if missing).

## Definition of done

- [ ] `security/csrf` package with Issuer + OriginAllowlist.
- [ ] Tests: round-trip; tampered token rejected; expired token; session mismatch; constant-time HMAC compare.
- [ ] `httpx/middleware/csrf` refit to use it.
- [ ] Default `Secure=true` for cookies.
- [ ] Mandatory session extractor.
- [ ] Recipe update in `docs/ai/http.md`.
