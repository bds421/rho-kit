# AGENTS.md — `data/idempotency/redisstore`

## When to use this package

- The service needs request-level idempotency and Redis is already in the path.
- TTL on entries is short enough (minutes to hours) that Redis memory is fine — Redis handles TTL natively, no sweeper job needed.
- Replicas can tolerate brief Redis failover overlap (idempotency is best-effort under failure).

## When to use something else

- **Postgres is already the primary datastore:** `data/idempotency/pgstore` — fits inside the transactional database; no extra failure mode.
- **Per-tenant key namespacing:** wrap with `data/idempotency/tenant.Wrap(s)`.
- **You only need "have I seen this exact key?" without response caching:** `data/cache/rediscache.SetNX` is leaner.

## Key APIs

Same contract as `pgstore`:
- `New(client, opts...)` — wraps a `goredis.UniversalClient`.
- `Get` / `TryLock` / `Set` / `Unlock` with identical signatures to `pgstore`.

No `DeleteExpired` — Redis handles TTL natively.

## Common mistakes

- **Skipping the fingerprint** — same as pgstore. Hostile clients can stuff different bodies under reused keys.
- **TTL longer than the deployment can afford in Redis memory** — Redis stores the full response body. At scale this matters; pick TTL based on memory budget, not on "what if a client retries 2 hours later".
- **Embedding tenant prefixes by hand** — use `data/idempotency/tenant.Wrap`.

## Observability

- OTel spans: `idempotency.Get` / `idempotency.Set` / `idempotency.TryLock` / `idempotency.Unlock` with `db.system=redis`, `kit.idempotency.backend=redisstore`.
