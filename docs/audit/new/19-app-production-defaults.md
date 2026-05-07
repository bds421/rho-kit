# NEW: app — production-safe defaults

**Phase**: 6 (originally Phase 2)
**Status**: landed and superseded — see "How it actually shipped" below.
**Module path**: `github.com/bds421/rho-kit/app` (extends existing Builder)

## Why

The audit identified dozens of defaults that should be tightened in
production but were loose-by-default for local-dev ergonomics: trusted-
proxy CIDRs, idempotency TTL minimum, CSRF shared secret, JWT audience,
MemoryCache cap, retry-everything default, AMQP `mandatory=true`,
Postgres `sslmode=require`, idempotency-store-must-be-Redis, etc.

Operators (and AI agents) should not have to remember 20 individual
options. The kit should ship a default-deny posture and require explicit
opt-outs for relaxations.

## How it actually shipped

The original spec proposed a single `Builder.WithProductionDefaults()`
meta switch that flipped every tightening at once. That landed in
`35aad31` and `4d04fe1`.

The v2.0.0 work that followed (`c113451`) **removed development mode
entirely** and made the production-safe defaults unconditional. The meta
switch and its companion `KIT_ENV` reads were deleted. The replacement
posture:

- The Builder runs `validateProductionSafety()` unconditionally in
  `Build()`. Every check that was previously gated on
  `WithProductionDefaults()` now applies to every service the kit builds.
- Each relaxation has a typed per-feature opt-out the operator declares
  consciously:
  - `WithoutTLS` — was `WithProductionAllowPlaintext`
  - `WithInternalNonLoopback` — was `WithProductionInternalExposed`
  - `WithoutJWTIssuer` — was `WithJWTAllowAnyIssuer`
  - `WithoutJWTAudience` — was `WithJWTAllowAnyAudience`
- There is no `KIT_ENV` (or `APP_ENV`) escape hatch in any kit code
  path. Tests use the `AllowPlaintextLoopbackForTests` field on
  `pgx.Config` (loopback-only by construction) where they need to bypass
  the TLS handshake.

The eight-pass v2.0.0 security audit (closed, `a3136df`) drove the
remaining bypass classes (IPv4 wildcard variants, multi-host pgx DSN,
duplicate-key sslmode, bracket-only host forms) until the validator
matches `net.Listen` and pgxpool on every reachable input.

## Tightenings the validator enforces

- Postgres sslmode `require` or stricter (rejects `disable`, `prefer`,
  `allow`, and bypasses via duplicate `sslmode=` keys).
- JWT issuer and audience pinning (`Without*` opt-outs are the only
  relaxation).
- Internal ops port bound to loopback (rejects every wildcard form
  `net.Listen` accepts: IPv4 zero-forms, hex/octal numeric IPv4, IPv6
  unspecified, bracket-only).
- TLS required on the public listener (`WithoutTLS` opt-out).
- Tracing sample rate capped at 0.1 unless explicitly raised.
- `WithTenantBudget` requires `WithMultiTenant` (no fail-open when the
  budget KeyFunc returns `(_, false)`).
- pgx loopback gate honours `AllowPlaintextLoopbackForTests` only when
  the resolved host (and every multi-host fallback) is loopback.

## Migration from the meta switch

```go
// Before v2.0.0
app.New("svc", v, cfg).
    WithProductionDefaults().
    WithProductionAllowPlaintext().
    WithProductionInternalExposed().
    WithJWTAllowAnyIssuer().
    Run()

// v2.0.0
app.New("svc", v, cfg).
    // The validator runs automatically; remove WithProductionDefaults().
    WithoutTLS().                  // was WithProductionAllowPlaintext
    WithInternalNonLoopback().     // was WithProductionInternalExposed
    WithoutJWTIssuer().            // was WithJWTAllowAnyIssuer
    Run()
```

## Related

- [new/18-tools-kit-doctor.md](18-tools-kit-doctor.md) — kit-doctor
  checks that consumer services haven't drifted from the same posture
  the Builder validator enforces.
- `docs/RELEASE_NOTES_v2.md` — full migration guide for the rename and
  the unconditional-validator posture.
