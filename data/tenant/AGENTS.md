# AGENTS.md — `data/tenant`

## When to use this package

- Multi-tenant service that needs strict tenant isolation in keys, queries, and metrics.
- Constructing a tenant-scoped key for cache / lock / idempotency / budget.

## When to use something else

- **Single-tenant service:** skip entirely; tenant primitives are pure overhead.
- **Tenant ID propagation through context:** `core/tenant` (the ctx helpers — `Required`, `From`, `With`).

## Key APIs

- `tenant.Scope(tenantID)` — value type. Use as a method receiver where the tenant must be explicit.
- `tenant.Key(ctx, parts...)` — length-prefixed encoding of `(tenant, parts...)` into a single string. **Always use this for cache/idempotency/lock keys** instead of hand-concatenating `tenant + ":" + key`.
- `tenant.WhereClause(col, ctx)` — SQL helper that generates `<col> = $N` and the argument value from ctx.
- `data/cache/tenant.Wrap(c)` / `data/idempotency/tenant.Wrap(s)` — wrap a non-tenant-aware primitive with tenant namespacing.

## Common mistakes

- **`tenantID + ":" + key`** — `A:B` collides with `A` + `:B`. The length-prefixed `tenant.Key` encoding prevents this.
- **`tenant.From(ctx)` without checking `tenant.Required(ctx)` first** — `From` returns the zero `Scope` for empty ctx; `Required` panics. For middleware-enforced endpoints, use `Required`.
- **Embedding tenant ID in Prometheus labels** — explodes cardinality. Use `observability/promutil.OpaqueLabelValue` if you must include tenant in a label, otherwise omit the tenant dimension from metrics entirely.

## Observability

- `observability/promutil/labelguard` is the companion package — drops + counts disallowed label values when a tenant ID accidentally lands in a label position.
