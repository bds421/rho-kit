# app/ — Builder + JWT module + lifecycle wiring

The Builder is the golden path: most consumers wire infrastructure through it. Bugs here propagate to every service.

## Open

_Closed — see Recently Landed below._

## Recently Landed (Phase 3, commit `4d04fe1`)

- ✅ **`Builder.WithJWTIssuer` / `WithJWTAudience` / `WithJWTAllowAnyIssuer`** — issuer enforcement is now first-class. In `KIT_ENV=production`, `WithJWT` panics unless paired with one of `WithJWTIssuer` or the explicit opt-out `WithJWTAllowAnyIssuer`. Non-prod still falls back to the legacy `https://oathkeeper` default but with a logged warning so operators get a signal.
- ✅ **Builder failure semantics documented** — Builder is a composition root, not a reusable factory. `Build()` failures must be treated as fatal; explicitly recorded in the Builder type doc.
- ✅ **`runner.AddFunc` panic behaviour documented** — `WithIPRateLimit` doc now warns that the limiter's background sweeper runs via `runner.AddFunc` and a panic kills the service via the lifecycle Runner; operators should monitor the `goroutine_panicked` log event.

### Migration checklist

- [x] Phase 2: require `WithExpectedIssuer` + `WithExpectedAudience` (ties into [03-crypto-and-security.md](03-crypto-and-security.md)). ✅ `4d04fe1`
- [x] Document Builder failure semantics in the Builder type doc. ✅ `4d04fe1`
- [x] Document that `WithIPRateLimit`/`WithJWT` background goroutines crash the service on panic. ✅ `4d04fe1`
