# NEW: app.WithProductionDefaults()

**Phase**: 2 (lands after the individual phase-1 fixes; bundles them into one switch)
**Module path**: `github.com/bds421/rho-kit/app` (extends existing Builder)

## Why

This audit identifies dozens of defaults that should be tightened in production but are loose-by-default for local-dev ergonomics: trusted-proxy CIDRs, idempotency TTL minimum, CSRF shared secret, JWT audience, MemoryCache cap, retry-everything default, AMQP `mandatory=true`, Postgres `sslmode=require`, idempotency-store-must-be-Redis, etc.

A single `Builder.WithProductionDefaults()` call should flip all of them at once — agents and humans don't need to remember 20 individual options. It's a force-multiplier for every other phase-1 fix.

## Public API

```go
// WithProductionDefaults tightens many defaults at once. Call before any other
// configuration option that you want to override per-service. Calling this
// when env != production logs a warning (it's still allowed, useful for
// staging environments that should match prod).
//
// Tightened defaults:
//   - Postgres sslmode required (rejects empty/disable)
//   - JWT audience and issuer required (panics if not set on Build)
//   - CSRF shared secret required (no per-process random fallback)
//   - Idempotency store must be Redis (rejects MemoryStore)
//   - Idempotency middleware requires WithUserExtractor + body fingerprint
//   - clientip trusted proxies required (no RFC1918 default)
//   - Idempotency TTL minimum 1s (rejects zero/negative)
//   - MemoryCache max size required (no math.MaxInt64 default)
//   - retry default policy uses RetryIfNotPermanent
//   - AMQP publisher mandatory=true with NotifyReturn handling
//   - Tracing sample rate ≤ 0.1 (rejects 1.0 unless explicitly set)
//   - Baggage propagator off (TraceContext only)
//   - HTTP server ErrorLog routed through slog
//   - SSRF transports require TLS 1.3
//   - Recover middleware mandatory in stack
//   - gRPC Recovery interceptors mandatory
func (b *Builder) WithProductionDefaults() *Builder
```

## Behavior

- **Each tightening is itself a phase-1 PR** (recover middleware, mandatory=true, sslmode=require, etc.). `WithProductionDefaults` is a *meta* option that flips them collectively and adds startup validation.
- All "required" tightenings cause a clear startup error if not satisfied. No silent fallback to insecure defaults.
- The error message names the override knob: `"production: CSRF shared secret required (set CSRF_SECRET or call WithCSRFSecret)"`.
- Internal services that genuinely don't need (say) CSRF can opt out per-feature: `WithProductionDefaults().WithoutCSRF()`.

## Validation order

`Build()` runs validation in two phases:
1. Static checks (config struct field presence) — fail before any infra is opened.
2. Smoke checks (Postgres TLS handshake, Redis ping, JWKS fetch) — fail before serving traffic.

## Doc + AGENTS.md update

Add a recipe to `docs/ai/bootstrap.md` showing the production-bound golden path:

```go
app.New("my-service", version, cfg.BaseConfig).
    WithProductionDefaults().
    WithPostgres(cfg.Database, cfg.DatabasePool, &Model{}).
    WithRedis(...).
    WithJWT(cfg.JWKSURL).
    WithExpectedAudience("https://my-service.example.com").
    WithCSRFSecret(cfg.CSRFSecret).
    WithTrustedProxies(cfg.TrustedProxyCIDRs).
    Run()
```

## Definition of done

- [ ] `WithProductionDefaults()` method on Builder.
- [ ] Each tightening also exposed as a standalone option (so non-`WithProductionDefaults` services can pick individually).
- [ ] `WithoutXxx()` opt-out for each tightening.
- [ ] Startup-time validation with helpful error messages.
- [ ] Recipe in `docs/ai/bootstrap.md`.
- [ ] `kit-doctor` rule that warns when `WithProductionDefaults()` is missing in services declared `env=production`.

## Related

- [new/18-tools-kit-doctor.md](18-tools-kit-doctor.md) — ships a rule that flags missing `WithProductionDefaults`.
- Every phase-1 fix in [ROADMAP.md](../ROADMAP.md) — this option exists to bundle them.
