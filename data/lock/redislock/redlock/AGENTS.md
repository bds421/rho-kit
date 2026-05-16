# AGENTS.md — `data/lock/redislock/redlock`

## When to use this package

- The service requires correctness across the failure of a single Redis instance — i.e. one Redis node in each of N availability zones, and a zone loss must NOT release every lock.
- Critical sections that must NEVER overlap: leader election, schema migrations, batch-job dispatch, ledger writes.
- N ≥ 3 independent Redis instances are available in the deployment.

## When to use something else

- **Single-Redis deployment, brief failover overlap acceptable:** parent `redislock` — one round-trip per acquire instead of N.
- **Postgres in path:** `data/lock/pgadvisory` — true fencing, no TTL juggling, sessions auto-release on connection loss. Almost always preferable to Redlock when Postgres exists.
- **Just-once compute (no mutual exclusion guarantee needed):** `data/cache/rediscache.SetNX`.
- **High-frequency mutex:** Redlock is N× round-trips. For hot-loop locks, the latency multiplier dominates. Use `pgadvisory` or in-process synchronization.

## Key APIs

- `NewQuorumLocker(clients, opts...)` — clients MUST point at independent Redis instances. Pointing two clients at the same instance defeats the algorithm. Panics on N<3.
- Same `Acquire` / `WithLock` / `Lock.Release` / `Lock.Extend` surface as the parent `redislock` package.

## Common mistakes

- **N = 4** (even count) — a 2-2 partition cannot recover a quorum on either side. Always use an odd N (typically 3 or 5).
- **All Redis instances in the same AZ / pod / host** — co-location defeats the availability win. Each instance must be in a distinct fault domain.
- **TTL shorter than (network RTT × N + clock drift)** — Redlock's safety depends on TTL >> RTT. Pick TTL with a 10× margin over worst-case acquire latency.
- **Unsynchronized clocks** — severe clock drift between Redis instances and the locker can falsely shorten or lengthen the effective lock duration. NTP is mandatory.
- **Using Redlock when single-Redis would suffice** — Redlock has real cost (round-trips, operational complexity). Only choose it when "two holders briefly" is unacceptable.

## Observability

- Same span surface as parent `redislock` via shared types: `lock.Acquire` etc. with `db.system=redis`, `kit.lock.backend=redislock` (the redlock sub-package piggybacks on the same span names).
