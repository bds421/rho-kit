# resilience/ — retry, circuitbreaker

## Landed

- ✅ **`retry.DefaultPolicy.RetryIf = RetryIfNotPermanent`** — retrying everything by default (including `apperror.Permanent` failures and validation errors) was the documented-but-not-actual behaviour; now wrapping `retry.Permanent(err)` actually short-circuits the loop (commit `270c901`).

## Open

### [MEDIUM] CircuitBreaker has no context support; nil receiver silently passes through
**File**: `resilience/circuitbreaker/circuitbreaker.go:101-112`
**Issue**: `Execute(fn func() error)` takes no `context.Context`. Breaker can't be cancelled when caller's ctx is done. When `cb == nil`, helper invokes `fn()` directly — silent passthrough surprising for a defensive helper.
**Fix**: Add `ExecuteCtx(ctx, fn func(ctx) error) error`; short-circuit on `ctx.Err()` before calling fn. Document the `cb == nil` passthrough or panic instead.

### Migration checklist

- [ ] Phase 3: `CircuitBreaker.ExecuteCtx`; document or change nil-receiver semantics.
