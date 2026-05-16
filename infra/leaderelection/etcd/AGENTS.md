# AGENTS.md — `infra/leaderelection/etcd`

## When to use this package

- The deployment runs on bare metal / VMs / hybrid and already operates an etcd cluster (for service discovery, configuration, or another control-plane purpose).
- Strong-consistency leader election is required (true single leader at any instant under partition; not "single leader most of the time").
- `kubectl get lease` is not available because the workload is not on Kubernetes.

## When to use something else

- **Workload runs on Kubernetes:** `infra/leaderelection/k8slease` — the leadership state lives in the same control plane as the workload, visible via `kubectl get lease`. No extra dependency.
- **Postgres is in the path, no etcd:** `infra/leaderelection/pgadvisory` — uses session-scoped advisory locks, no separate broker needed.
- **Redis is in the path, occasional dual-leader window acceptable:** `infra/leaderelection/redislock` — much lighter than running an etcd cluster purely for coordination.

## Key APIs

- `New(client, electionKey, identity, opts...)` — `electionKey` MUST begin with `/`, must not exceed 256 bytes, no control bytes. `identity` MUST be unique per replica (typically pod name).
- `Run(ctx, callbacks)` — blocks until ctx cancels or `WithCallbackDrainTimeout` fires. Loops internally; one return per process lifetime is normal.
- `IsLeader()` — eventually-consistent boolean. Use inside `OnAcquired` callbacks to gate per-tick work that could race against teardown.

## Common mistakes

- **Two replicas sharing an `identity`** — etcd records identity as the leader value; duplicates cannot distinguish themselves and race the local leader flag. Always use POD_NAME or a per-replica UUID.
- **`electionKey` without leading `/`** — etcd prefix convention; the kit panics at construction. Format: `/services/<service-name>/leader`.
- **Re-running `Run` after it returned with `ErrCallbackDrainTimeout`** — the orphan goroutine is still running. The orchestrator MUST restart the process; in-place retry guarantees nothing.
- **Long-running work in `OnAcquired` that ignores ctx** — the leader ctx is cancelled when the etcd session loses keepalive (network partition, etcd quorum loss). If the callback doesn't observe ctx, the drain warn-watchdog fires repeatedly and `WithCallbackDrainTimeout` (if set) eventually surfaces `ErrCallbackDrainTimeout`.
- **Lease TTL << network RTT** — a slow renewal misses the TTL window and triggers a needless leader change. Default 15s mirrors `k8slease` for cross-adapter familiarity.

## Observability

- Metrics: `leaderelection_callback_drain_seconds{election,state}` and `leaderelection_callback_drain_warn_total{election}` — only when `WithMetrics` is wired. `election` label is the configured key prefix, validated as a static label at construction.
- OTel spans: not currently emitted (the Run loop is long-lived; per-term spans would inflate exporter load).
