# AGENTS.md — `data/idempotency/pgstore`

## When to use this package

- The service needs "request seen before? return cached response" semantics — typically wired via `httpx/middleware/idempotency` for inbound HTTP.
- Postgres is already in the path (kit convention: don't add a new datastore just for idempotency).
- TTL on idempotency entries fits inside Postgres' TTL discipline (job runs `DeleteExpired` periodically; the store does not auto-expire).

## When to use something else

- **Redis is the natural fit (no Postgres in path):** `data/idempotency/redisstore` — Redis handles TTL natively, no sweeper job needed.
- **Per-tenant key namespacing:** wrap with `data/idempotency/tenant.Wrap(s)` — this namespaces the key part, NOT the body fingerprint, so fingerprints can still detect "same request body, different tenant" attempts.
- **Just-once compute across replicas (no response caching):** `data/cache/rediscache.SetNX` is leaner. Idempotency stores the response body; SetNX doesn't.

## Key APIs

- `New(db, opts...)` — wraps `*sql.DB`. Apply migrations from `migrations.go` first.
- `Get(ctx, key, fingerprint)` returns `(*CachedResponse, ok, error)` — `ok=true, resp=nil` means "key exists with different fingerprint" (request mismatch); `ok=true, resp!=nil` means "cached response present".
- `TryLock(ctx, key, fingerprint, ttl)` returns `(token, ok, fingerprintMatch, err)` — the `token` is the fencing identifier for `Unlock`/`Set`.
- `Set(ctx, key, token, resp, ttl)` — completes the in-flight idempotency entry. Returns `ErrLockLost` if the token no longer matches.
- `DeleteExpired(ctx)` — run from a periodic job. Postgres has no TTL on rows.

## Common mistakes

- **Never running `DeleteExpired`** — entries accumulate forever, table grows unbounded. Wire a cron / lifecycle.Component to call this on a schedule (every few minutes).
- **Storing tenant ID INSIDE the key string** — defeats the per-tenant namespace wrapper. Use `data/idempotency/tenant.Wrap`.
- **Treating `(nil, false, nil)` from `Get` as an error** — it's the "no cached response, proceed with handler" path. Branch on `(resp, ok)`.
- **Skipping the fingerprint check** — passing `fingerprint=nil` to `Get`/`TryLock` disables the "same key, different body" rejection. Hostile clients can stuff arbitrary bodies under reused idempotency keys.
- **Picking a TTL < your worst-case request time** — TTL applies from the `TryLock`; if the handler runs longer than TTL, the row expires mid-flight and the response can't be cached.

## Observability

- OTel spans: `idempotency.Get` / `idempotency.Set` / `idempotency.TryLock` / `idempotency.Unlock` / `idempotency.DeleteExpired` with `db.system=postgresql`, `kit.idempotency.backend=pgstore`. Keys are NEVER attached as span attributes (PII).
