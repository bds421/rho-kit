# AGENTS.md — `infra/leaderelection/redislock`

## When to use this package

- Redis is already in the path.
- Brief dual-leader windows during Redis failover are acceptable for the workload (cache warmers, non-critical reconciliation, observability collectors).

## When to use something else

- **Dual-leader windows are NOT acceptable (financial writes, schema migrations):** `pgadvisory` (true fencing) or `etcd` (strong consistency).
- **On Kubernetes:** `k8slease` — leadership lives in the same control plane as the workload.

## Key APIs

Same `New / Run / IsLeader` surface. Wraps `data/lock/redislock` in a renew loop.

## Common mistakes

- **TTL << acquire RTT** — renewals miss the window and leadership flaps. Default mirrors the other adapters.
- **Treating Redis replication as "HA leader election"** — it isn't. Async replication means failover can hand the lock to two replicas briefly. If that's a problem, use `pgadvisory` or `etcd`.
- Same drain caveats as other adapters.

## Observability

Same `leaderelection_callback_drain_seconds` / `_warn_total` metric shape.
