# AGENTS.md — `data/budget`

## When to use this package

- Per-tenant cost / quota enforcement (LLM tokens, API calls, storage GB-hours, anything billable).
- The service has a meaningful "this tenant has consumed X of Y allowed" signal that should hard-stop further consumption when exhausted.

## When to use something else

- **Per-IP rate limiting:** `httpx/middleware/ratelimit` instead — different semantics (sliding window vs hard cap).
- **Per-user counters that don't need cross-process consistency:** plain in-memory atomic counters are leaner.

## Key APIs

- `Budget` interface: `Consume(ctx, key, amount)` + `Peek(ctx, key)`. Optional `Refunder` capability for two-phase reconciliation (estimate → actual usage).
- `data/budget/memory` — single-process backend. Good for development, single-replica services.
- `data/budget/redis` — atomic Lua, cross-instance, `WithRedisTime` for clock-skew-free fairness. **The production backend.** Caps must fit Lua's exact integer range.
- `httpx/middleware/budget` — inbound charge per request. Fails closed on missing/invalid keys (no key = no service). Rejects exhausted budgets with `429 + X-Budget-Remaining + Retry-After`.
- `httpx/budget` — outbound `RoundTripper` with two-phase reconciliation (estimate up front, true-up after the call completes).

## Common mistakes

- **Memory backend in a multi-replica service** — different replicas have different views. Use Redis.
- **Negative budget values** — the kit rejects them at the API surface. Always provide a non-negative integer.
- **`Consume` for the LLM "true cost" before the call completes** — estimate up front via `Consume`, then refund via the `Refunder` capability with the actual cost. Otherwise an over-estimated charge persists even if the call was cheaper than predicted.
- **Per-tenant Redis keys with raw tenant ID** — use `data/tenant.Key(ctx, parts...)` for length-prefixed encoding.

## Observability

- Metrics depend on which middleware/backend is wired. `httpx/middleware/budget` emits `budget_decisions_total{outcome}` with bounded enum `(allowed|denied|errored)`.
