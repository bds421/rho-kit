# resilience/ — retry, circuitbreaker

### [HIGH] Default retry policy retries ALL errors (no `RetryIf` predicate)
**File**: `resilience/retry/retry.go:66-72,240`
**Issue**: `DefaultPolicy.RetryIf == nil`. `doWithPolicy` interprets nil as "retry every error" — non-idempotent ops, validation errors, permission denials, `apperror.Permanent` failures. The convenience `RetryIfNotPermanent` exists but isn't wired into the default. AGENTS.md implies `apperror.ShouldRetry` is wired by default; reality is it isn't.
**Fix**: Default `DefaultPolicy.RetryIf` to `RetryIfNotPermanent` (or `apperror.ShouldRetry`). Document that nil = "retry everything" and require explicit opt-in for that behavior.
**Effort**: S
**Phase**: 1
**Migration**: Existing callers relying on the retry-everything default for permanent errors will see fewer retries — this is a correctness improvement but document loudly.

### [MEDIUM] CircuitBreaker has no context support; nil receiver silently passes through
**File**: `resilience/circuitbreaker/circuitbreaker.go:101-112`
**Issue**: `Execute(fn func() error)` takes no `context.Context`. Breaker can't be cancelled when caller's ctx is done. When `cb == nil`, helper invokes `fn()` directly — silent passthrough surprising for a defensive helper.
**Fix**: Add `ExecuteCtx(ctx, fn func(ctx) error) error`; short-circuit on `ctx.Err()` before calling fn. Document the `cb == nil` passthrough or panic instead.

### Migration checklist

- [ ] Phase 1: `DefaultPolicy.RetryIf = RetryIfNotPermanent`.
- [ ] Phase 3: `CircuitBreaker.ExecuteCtx`; document or change nil-receiver semantics.
