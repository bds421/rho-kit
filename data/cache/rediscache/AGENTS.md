# AGENTS.md — `data/cache/rediscache`

## When to use this package

- The service needs a fast key/value cache with TTL semantics and a Redis instance is already in the deployment.
- The cached values fit comfortably under 10 MiB each (configurable via `WithCacheMaxValueSize`).
- The deployment can tolerate brief inconsistencies during Redis failover (cache is best-effort by definition).

## When to use something else

- **Hot-loop reads, no cross-process consistency needed:** `data/cache/memory` is faster and has no network hop.
- **Per-tenant key namespacing:** wrap this with `data/cache/tenant.Wrap(c)` — do NOT prepend tenant prefixes by hand; the wrapper handles length-prefixed encoding so tenant `A:B` cannot collide with `A` + `:B`.
- **Idempotency keys (request fingerprint + response body):** `data/idempotency/redisstore` — separate primitive with the right TTL semantics for "have I seen this request?".
- **Distributed locks:** `data/lock/redislock` (single-instance) or `data/lock/redislock/redlock` (quorum). NEVER simulate a lock with `SetNX` + sleep loop; the kit's `Locker` handles fencing tokens correctly.

## Key APIs

- `NewCache(client, name, opts...)` — `name` is operator-facing (Prometheus label), validated as a static label value at construction.
- `Get`, `Set`, `Delete`, `Exists`, `MGet`, `MSet`, `SetNX` — the standard surface; every method validates keys against `sharedcache.ValidateKey` before any Redis round-trip.
- Errors flow through `redact.WrapError` — never embed inner error text in your own log lines.

## Common mistakes

- **Embedding user IDs / tenant IDs / request IDs in `name`** — `name` is a Prometheus label and validated as such at construction. Use `data/cache/tenant.Wrap` for per-tenant namespacing instead of inflating cache instances.
- **Treating `ErrCacheMiss` as an error** — it's normal control flow. The wave 168 OTel spans surface it as `kit.cache.miss=true` attribute, NOT an error status.
- **Skipping `WithCacheMaxValueSize`** — without a cap, a hostile or buggy writer can stuff multi-MB values that explode response-handler memory on `Get`.
- **Calling `Get` then `Set` for compute-once semantics** — use `SetNX` which is atomic across replicas. The Get+Set race is the canonical cache stampede pattern.

## Observability

- Hit/miss counters: `redis_cache_hits_total{name}` / `redis_cache_misses_total{name}`.
- OTel spans: `cache.Get` / `cache.Set` / etc. with `db.system=redis` and `kit.cache.name`. Keys are NEVER attached as span attributes (PII).
