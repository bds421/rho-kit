# NEW: crypto/paseto

**Phase**: 4 (Tier‑1 missing primitive)
**Module path**: `github.com/bds421/rho-kit/crypto/paseto`

## Why

JWT has well-known footguns the kit's existing `security/jwtutil` already mitigates (no `alg=none`, infer from key). PASETO eliminates these by design — only one signing algorithm per version, opaque tokens, no negotiation. For new internal services, PASETO is a safer default.

This package isn't meant to replace `jwtutil` (Oathkeeper integration) but to give greenfield services the option of a safer token format.

## Public API

```go
package paseto

// Public-key purpose (asymmetric, Ed25519): for client-presented tokens
// where many services verify with the public key.
type V4Public struct { /* ... */ }

// New constructs a v4.public verifier from PEM-encoded Ed25519 public keys.
// Matches jwtutil.KeySet's idiom (parse, ExpectedAudience, ExpectedIssuer).
func NewV4Public(keys []ed25519.PublicKey, opts ...Option) *V4Public

// Local purpose (symmetric, XChaCha20-Poly1305): for tokens that don't leave
// the trust boundary; useful for session tokens or cookies.
type V4Local struct { /* ... */ }

func NewV4Local(key [32]byte, opts ...Option) *V4Local

// Verify parses, validates signature/MAC, expiration, audience, issuer, and
// returns the parsed claims as a typed Claims struct (mirrors jwtutil.Claims).
func (v *V4Public) Verify(token string, now time.Time) (*Claims, error)
func (v *V4Local) Verify(token string, now time.Time) (*Claims, error)

// Sign / Seal are the issuance side, for services that issue tokens.
func (v *V4Public) Sign(claims Claims, key ed25519.PrivateKey) (string, error)
func (v *V4Local) Seal(claims Claims) (string, error)

// Claims aligns with jwtutil.Claims (Subject, Audience, Issuer, IssuedAt,
// ExpiresAt, NotBefore) plus a generic Custom map for app-specific claims.
type Claims struct {
    Subject   string
    Audience  []string
    Issuer    string
    IssuedAt  time.Time
    ExpiresAt time.Time
    NotBefore time.Time
    Custom    map[string]any
}

// Provider mirrors jwtutil.Provider for periodic key refresh from a remote source.
type Provider struct { /* ... */ }
```

## Implementation

Use [`aidantwoods/go-paseto`](https://github.com/aidantwoods/go-paseto) (PASETO v4 reference impl in Go). Wrap with the same conventions as `jwtutil`:

- `WithExpectedAudience` mandatory.
- `WithExpectedIssuer` mandatory.
- Audience/issuer/exp validated automatically inside `Verify`.
- `Provider.Run(ctx)` for periodic public-key refresh from a JWKS-equivalent endpoint.

## Builder integration

Add `app.Builder.WithPASETO(provider)` analogous to `WithJWT`. Keep `WithJWT` for Oathkeeper compatibility.

## Definition of done

- [ ] Package with V4 public + V4 local types.
- [ ] Round-trip tests + RFC-vector tests for v4.public and v4.local.
- [ ] Audience/issuer/exp/nbf validation tests.
- [ ] Provider with periodic refresh.
- [ ] Builder method `WithPASETO`.
- [ ] Recipe in `docs/ai/security.md` comparing PASETO and JWT use-cases.
