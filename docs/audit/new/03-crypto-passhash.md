# NEW: crypto/passhash

**Phase**: 4 (Tier‑1 missing primitive)
**Module path**: `github.com/bds421/rho-kit/crypto/passhash`

## Why

Every service that stores user credentials needs argon2id (or bcrypt). Today consumers roll their own; the kit has nothing. This is the most-asked missing primitive in security-focused Go service kits.

## Public API

```go
package passhash

// Params controls argon2id cost. Defaults are calibrated for ~100ms on a 2025-era server.
type Params struct {
    Memory      uint32 // KiB; default 64*1024 (64 MiB)
    Iterations  uint32 // default 3
    Parallelism uint8  // default 1 (deterministic across cores)
    SaltLen     uint32 // default 16 bytes
    KeyLen      uint32 // default 32 bytes
}

// DefaultParams returns OWASP-recommended defaults (subject to per-release re-calibration).
func DefaultParams() Params

// Hash returns an encoded argon2id hash string (PHC-format).
//
//   $argon2id$v=19$m=65536,t=3,p=1$<salt>$<hash>
//
// The format embeds parameters; Verify reads them so callers don't need to track them.
func Hash(password string, params Params) (string, error)

// Verify returns (matched, needsRehash, error). needsRehash is true when the
// stored hash uses parameters weaker than the current target (encourages
// transparent upgrade on next login).
func Verify(password, encoded string, target Params) (matched bool, needsRehash bool, err error)
```

Internals:
- Random salt via `crypto/rand`.
- `subtle.ConstantTimeCompare` for the final equality.
- PHC string format so external rotation tools can introspect.
- `needsRehash` triggers when stored params are weaker than `target` along any dimension.

## Integration

- Consumed by services directly; no Builder integration needed.
- Document the verify-then-rehash pattern in `docs/ai/security.md`.

## Definition of done

- [ ] Package + tests covering:
  - [ ] Hash produces PHC-format string.
  - [ ] Verify accepts a valid hash; rejects modified hash.
  - [ ] needsRehash=true when stored params < target.
  - [ ] Constant-time comparison.
  - [ ] Empty-password rejected (not silently hashed).
- [ ] Benchmark to confirm ~100ms hash time on representative hardware.
- [ ] Recipe entry in `docs/ai/security.md` showing the verify-then-rehash pattern.
