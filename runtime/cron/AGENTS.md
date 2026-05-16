# AGENTS.md — `runtime/cron`

## When to use this package

- The service has periodic work — DB cleanup, cache warmup, report
  generation, lease expiry sweeps, idempotency-store
  `DeleteExpired` — that should fire on a cron-like schedule.
- Wall-clock-driven, not event-driven. (Event-driven async lives
  in `messaging.Subscription` instead.)
- The job runs in-process inside the same binary as the HTTP
  server / consumer. No separate cron container needed.

## When to use something else

- **Multiple replicas, must run on exactly one:** combine with
  `infra/leaderelection` and `WithLeaderGate(elector.IsLeader)`.
  Without the gate, every replica runs every job every tick.
- **Workload is too big for a single-process scheduler:** use
  `data/queue/riverqueue` (Postgres-durable) or
  `data/queue/redisqueue` and have one replica enqueue jobs that
  any worker picks up.
- **Schedule depends on external state** (Kubernetes
  `CronJob`, an external scheduler): leave the kit out — let the
  orchestrator invoke a one-shot binary on its schedule.

## Key APIs

- `New(logger, opts...)` — Construct a Scheduler. Apply
  `WithLeaderGate(fn)` for single-leader behavior,
  `WithRegisterer(reg)` for non-default Prometheus registry,
  `WithLocation(tz)` for non-UTC schedules.
- `Add(name, schedule, fn)` — Register a job. `schedule` is a
  cron expression (`"@daily"`, `"*/15 * * * *"`, `"@every 5m"`).
  `fn` receives a context cancelled at scheduler shutdown.
- `SetJobTimeout(name, d)` — Per-job context deadline. Without
  this, a stuck job blocks the next tick until ctx cancels.
- `Start(ctx)` / `Stop(ctx)` — `lifecycle.Component` shape.
  `Start` blocks until ctx cancels.

## Common mistakes

- **Forgetting `WithLeaderGate` on a multi-replica deployment.**
  Three replicas without a leader gate run the cleanup job three
  times per tick. Always wire a leader-election adapter
  (`pgadvisory`, `k8slease`, `etcd`, `redislock`) and pass
  `elector.IsLeader` here.
- **Long-running jobs without `SetJobTimeout`.** The scheduler
  cancels the job ctx at `Stop`, but a job that ignores ctx
  blocks shutdown until `Stop`'s caller times out. Set an
  explicit per-job deadline so the next tick can fire even when
  the previous one wedged.
- **Cron expression with timezone surprises.** Default is UTC.
  If a job needs to fire at "9 AM in Berlin" use
  `WithLocation(time.LoadLocation("Europe/Berlin"))`.
- **Registering jobs after `Start` is called.** `Add` is safe
  after Start but the new job won't fire until the next tick;
  this surprises operators expecting "register then start" to
  be the only valid order.

## Observability

- Metrics: `cron_jobs_started_total`, `cron_jobs_completed_total`,
  `cron_jobs_failed_total`, `cron_job_duration_seconds`,
  `cron_jobs_skipped_total` (labeled by `reason="not_leader"` or
  `reason="panic"`). All labeled with `job` (the operator-supplied
  name from `Add`).
- OTel spans: not currently emitted per-tick to avoid trace
  exporter inflation on high-frequency schedules. The kit's
  wave-169 `lifecycle.Component` start/stop span wraps the
  whole Scheduler instead.
- Panic recovery: per-job panics are recovered, logged with a
  redacted error, and counted as `cron_jobs_failed_total`. The
  Scheduler keeps running.
