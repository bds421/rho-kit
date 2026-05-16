# Leader Election Callback-Drain Alerts

## Alerts

- `RhoKitLeaderCallbackDrainStuck`
- `RhoKitLeaderCallbackDrainTimeout`

## What These Alerts Mean

Every adapter under `infra/leaderelection/*` (`k8slease`, `etcd`,
`pgadvisory`, `redislock`) uses the same `Callbacks.OnAcquired` /
`Callbacks.OnLost` shape. When leadership ends, the leader ctx
passed to `OnAcquired` is cancelled and the kit waits for the
callback to return. While it waits:

- A `pending` observation is recorded every warn tick (default 10s).
- A `leaderelection_callback_drain_warn_total` counter ticks.
- When the callback returns: a terminal `drained` observation with
  the total wait time.
- When `WithCallbackDrainTimeout` fires before the callback returns:
  a terminal `timeout` observation and `ErrCallbackDrainTimeout` is
  returned from `Run`.

A drain warn is the leading indicator. A drain timeout is terminal —
the orchestrator MUST restart the process.

## First Checks

1. Open the leader-election dashboard and filter to the alerting
   `namespace`, `service`, and `election` key.
2. Look at the warn-tick rate — is one election particularly bad, or
   are all of them sticky?
3. Cross-check `outbox_pending_count`, `redis_command_duration_seconds`,
   or whatever the OnAcquired body is doing — the callback is almost
   always stuck in a downstream blocking call.
4. Check process logs for the leader's identity and the duration in
   the drain warn message.

## Mitigation

- **Drain warns, callback eventually returns:** The OnAcquired body
  is doing a too-long blocking operation that ignores ctx. The fix
  is in the consumer code, not the kit — wire ctx through every
  downstream call (HTTP client, DB query, Redis call).
- **Drain timeouts (terminal):** Restart the process. The orphan
  goroutine is still running; `Run` cannot recover in place. If this
  is recurring, audit the callback body for ctx-ignoring code paths
  (blocking channel reads, `time.Sleep` without ctx, missing
  `select { case <-ctx.Done() }`).
- **All electors warn together:** The shared downstream resource
  (the broker, the database) is slow. Investigate that first; the
  leader election is just exposing the symptom.

## Adapter Differences

- `k8slease`: lease TTL is what the kit refreshes; drain delay
  doesn't risk dual-leadership but does risk the next campaign
  starting late.
- `etcd`: session-based; drain delay holds the session keepalive
  open past the point of usefulness. A drain timeout means the
  session is dead but the callback still runs.
- `pgadvisory`: session-scoped; the connection stays pinned for the
  duration of the drain.
- `redislock`: TTL-based; if drain delay exceeds the lock TTL,
  another replica can acquire the lock while the old leader still
  thinks it's running. The kit attempts to surface this via
  `OnLost`, but the consumer SHOULD check ctx before every critical
  write.

## Metric Contract

- `leaderelection_callback_drain_seconds{election,state}` —
  histogram, buckets `[1, 5, 10, 30, 60, 120, 300]`. `state` is
  one of `pending` (snapshot during drain), `drained` (terminal,
  callback returned), `timeout` (terminal, drain-timeout fired).
  `election` is the configured key prefix and is validated as a
  static label at construction.
- `leaderelection_callback_drain_warn_total{election}` — counter,
  ticks once per warn interval per stuck drain.

The `election` label is intentionally bounded: misconfiguration
surfaces at `New(...)` rather than at first emission.
