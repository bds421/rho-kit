# AGENTS.md — `infra/leaderelection/k8slease`

## When to use this package

- The service runs on Kubernetes and the operator wants `kubectl get lease` visibility into leadership.
- The deployment already has client-go in its dep closure (no extra heavy SDK pulled).
- Strong-consistency leader election within a single Kubernetes API server's view.

## When to use something else

- **Not on Kubernetes:** `pgadvisory` (Postgres) / `redislock` (Redis) / `etcd` (etcd).
- **Service spans multiple Kubernetes clusters:** k8slease leadership is scoped to a single API server. Cross-cluster coordination needs etcd or a federated approach (out of kit scope).

## Key APIs

- `New(client, namespace, name, identity, opts...)` — `identity` MUST be unique per replica (typically `POD_NAME`).
- `Run(ctx, callbacks)` — delegates to client-go's `LeaderElector.Run`. Returns when ctx cancels OR the Lease was taken by a peer. **Unlike pgadvisory/redislock, this is one-shot** — wrap in `lifecycle.Runner` for restart-on-loss behavior.
- `ReleaseOnCancel: true` is enabled — orderly shutdown hands the Lease back immediately instead of forcing peers to wait `WithLeaseDuration`.

## Common mistakes

- **Two replicas sharing `identity`** — Lease ownership uses identity as the strict token. Always pass `POD_NAME`.
- **Wrapping `Run` in `for { Run(...) }`** without checking ctx — leak risk if `Run` returns nil (clean shutdown). Use `lifecycle.Runner`'s restart policy instead.
- **`WithLeaseDuration <= WithRenewDeadline`** — the constructor panics. Defaults `15s / 10s / 2s` mirror client-go upstream.
- **Long `OnAcquired` that ignores ctx** — leader ctx cancels on Lease loss; if the callback hangs the drain warn-watchdog fires every `WithCallbackDrainWarnInterval`. Set `WithCallbackDrainTimeout` to make the orphan operator-visible.

## Observability

Same as etcd adapter: `leaderelection_callback_drain_seconds{namespace,name,state}` + `leaderelection_callback_drain_warn_total{namespace,name}`. The label set uses `(namespace, name)` because that matches the operator's mental model for Kubernetes objects.
