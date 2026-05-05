# app/ — Builder + JWT module + lifecycle wiring

The Builder is the golden path: most consumers wire infrastructure through it. Bugs here propagate to every service.

### [HIGH] JWT module hardcodes the issuer; consumers can't tell when issuer-checking is silently disabled
**File**: `app/jwt_module.go:45`
**Issue**: Defaults the expected issuer to `"https://oathkeeper"`. If a deployment uses a different Oathkeeper URL and forgets to override this, `KeySet.ExpectedIssuer` reads the deployment's value but `Verify` only compares when non-empty — so a typo silently disables the check rather than failing startup.
**Fix**: Require an explicit `WithExpectedIssuer` (panic at builder time if not set). Same with `WithExpectedAudience` (see [03-crypto-and-security.md](03-crypto-and-security.md)).
**Effort**: S
**Migration**: Existing services must add `WithExpectedIssuer` + `WithExpectedAudience` calls. Phase 2.

### [LOW] Builder partial-state on infra failure
**File**: `app/builder.go` (general)
**Issue**: If `WithPostgres` succeeds and `WithRedis` then panics, the partially-built Infrastructure isn't cleaned up. Most consumers panic-and-die at startup so this is benign, but a wrapper that catches the panic and re-tries leaks a DB pool.
**Fix**: Document that Builder failures must be treated as fatal; do not promise rebuild support.

### [LOW] `runner.AddFunc` panics → service-wide SIGTERM with no warning
**File**: `app/builder.go:476-491`
**Issue**: `WithIPRateLimit`, keyed limiter, JWT provider — all use `runner.AddFunc`. A panic inside any of those goroutines kills the whole service via the lifecycle Runner. Consumers don't get a heads-up.
**Fix**: Document the behavior in `WithIPRateLimit` / `WithJWT`; recommend monitoring `goroutine_panicked` log events. Consider a per-component recover with restart policy as a Tier‑3 enhancement.

### Migration checklist

- [ ] Phase 2: require `WithExpectedIssuer` + `WithExpectedAudience` (ties into [03-crypto-and-security.md](03-crypto-and-security.md)).
- [ ] Document Builder failure semantics in the bootstrap recipe.
- [ ] Document that `WithIPRateLimit`/`WithJWT` background goroutines crash the service on panic.
