# AGENTS.md — `infra/leaderelection/pgadvisory`

## When to use this package

- Postgres is already the service's primary datastore.
- Long-lived single-leader needed: cron-job dispatcher, batch coordinator, schema migrator.
- "Leadership = database session" mental model is acceptable (one connection pinned per active leader).

## When to use something else

- **No Postgres in path:** `redislock` / `k8slease` / `etcd`.
- **Short-lived "do this once" exclusion:** `data/lock/pgadvisory.AcquireTx` directly (transaction-scoped, no leader-election loop overhead).
- **Hot-loop leader checks across millions of operations:** kit pins one connection per active leader; this is fine for one or a few simultaneous elections, expensive at scale.

## Key APIs

Same `New / Run / IsLeader` surface as the other leader-election adapters. Wraps `data/lock/pgadvisory` in a renew loop.

## Common mistakes

- **Pool sized too small for the number of active elections** — each election holds one connection. A pool of 25 + 25 elections + normal queries = deadlock. Size accordingly.
- **Postgres session timeout < your acquire renewal cadence** — the kit pings every renewal cycle; if the server drops idle sessions faster than that, leadership flaps.
- Same `OnAcquired` drain caveats as `etcd` and `k8slease` — use `WithCallbackDrainTimeout` to make stuck callbacks operator-visible.

## Observability

Shared `leaderelection_callback_drain_seconds{backend,target,state}` / `_warn_total{backend,target}` shape. Here `backend="pgadvisory"` and `target` is the operator-supplied election key.
