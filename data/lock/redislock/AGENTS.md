# AGENTS.md — `data/lock/redislock`

## When to use this package

- The service needs a distributed mutex with TTL semantics and the deployment runs a single Redis instance (or a replicated one — see caveats below).
- The critical section is short-lived (seconds to a few minutes) and the work is **safe to overlap briefly** with a second holder during failover.

## When to use something else

- **True mutual exclusion required (no overlap window acceptable):** `data/lock/redislock/redlock` — Antirez's quorum algorithm across N independent Redis instances. ~3-5 Redis round-trips per acquire instead of 1.
- **Postgres is already in the path:** `data/lock/pgadvisory` — session-scoped advisory lock with true fencing. No TTL juggling, automatic release on session loss.
- **Leader election (long-lived, one-leader-per-cluster):** `infra/leaderelection/{redislock, pgadvisory, k8slease, etcd}` — these wrap the lock primitives in a renew loop with `OnAcquired`/`OnLost` callbacks. Don't reimplement the loop yourself.
- **Just-once compute (cache-warm style):** `data/cache/rediscache.SetNX` is the right primitive; it's atomic and doesn't pin a connection.

## Key APIs

- `NewLocker(client, opts...)` — defaults to 30s TTL, no retry, no max-wait. Always set `WithTTL` to comfortably exceed your worst-case critical section.
- `Acquire(ctx, key) (Lock, ok, err)` — `(nil, false, nil)` is contention, NOT an error. Branch on `ok`.
- `WithLock(ctx, key, fn)` — preferred over manual Acquire/Release because the kit handles the panic-safe release path correctly via a detached caller context.
- `Lock.Extend(ctx) (ok, err)` — heartbeat. `(false, nil)` means the TTL expired or another holder took it; this is normal control flow, branch on `ok`.

## Common mistakes

- **TTL shorter than the critical section** — the kit cannot save you here. The lock expires mid-fn, another holder enters, both modify state. Pick a TTL with margin AND drive `Extend` on a heartbeat if the work is long.
- **Treating `ErrLockLost` as a fatal error** — it's the "your TTL expired during work" signal. Inspect via `errors.Is(err, lock.ErrLockLost)` and reconcile (recheck state, retry the critical section idempotently).
- **Calling `Release` without `defer`** — a panicking handler will orphan the lock until TTL. `WithLock` handles this for you.
- **Composing this with Redis replication for "HA locks"** — Redis replication is asynchronous. Failover can hand the lock to two holders briefly. If that's intolerable, use the `redlock` sub-package or `pgadvisory`.
- **Embedding tenant IDs / user IDs / fingerprints in keys** — the kit validates keys against `MaxLockKeyLen` (1024 bytes) and rejects control bytes, but unbounded growth still hurts Redis. Use `data/tenant.Key(ctx, parts...)` to construct.

## Observability

- OTel spans: `lock.Acquire` / `lock.Release` / etc. with `db.system=redis`, `kit.lock.backend=redislock`. `lock.ErrLockLost` surfaces as `kit.lock.lost=true` attribute (not error status).
- The redsync internals do not emit Prometheus metrics by default. Wrap with your own metric if acquire latency matters.
