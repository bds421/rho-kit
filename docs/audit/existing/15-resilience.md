# resilience/ — retry, circuitbreaker

## Landed

- ✅ **`retry.DefaultPolicy.RetryIf = RetryIfNotPermanent`** — retrying everything by default (including `apperror.Permanent` failures and validation errors) was the documented-but-not-actual behaviour; now wrapping `retry.Permanent(err)` actually short-circuits the loop (commit `270c901`).

## Open

_Closed — see Recently Landed below._

## Recently Landed (Phase 3)

- ✅ **CircuitBreaker.ExecuteCtx** — new method `ExecuteCtx(ctx, fn func(ctx) error) error` short-circuits on `ctx.Err()` before invoking fn; `Execute` continues to work for context-free callers. Nil-receiver passthrough is now explicitly documented for both Execute and ExecuteCtx.

### Migration checklist

- [x] Phase 3: `CircuitBreaker.ExecuteCtx`; document or change nil-receiver semantics.
