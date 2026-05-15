# Runbook: outbox backlog

Covers `RhoKitOutboxBacklogGrowing` (pending growing & > 100 for
30m, warn) and `RhoKitOutboxBacklogCritical` (pending > 10k for
10m, page).

Snippet status: PromQL blocks in this runbook are illustrative query fragments
to adapt to the service's Prometheus labels and SLO windows.

## What this alert says

The outbox table has more pending entries than the relay can
publish in a reasonable window. The transactional-outbox pattern
relies on the relay catching up quickly so the gap between
"committed in DB" and "delivered to broker" stays small. A growing
backlog means consumers downstream are seeing stale data and the
outbox table is growing toward bloat.

## Likely causes (in order of frequency)

1. **Broker outage or slowness** — confirm on the outbox dashboard:
   `outbox_relay_latency_seconds` p99 climbed at the same time the
   backlog started growing. Each publish is taking longer than it
   should.
2. **Relay process not running** — confirm: `outbox_published_total`
   is flat (rate ≈ 0). The relay loop is either crashed, not
   scheduled (leader election failure), or hasn't been restarted
   after a config change.
3. **Workload spike** — confirm: application's transactional write
   rate is 5×+ normal. The relay is keeping pace per-message but
   there are simply more messages than the relay can drain at its
   current concurrency.
4. **Relay errors** — confirm: `outbox_errors_total` rate is
   non-zero and roughly matches the publish rate. The relay is
   trying but failing — typically auth, broker topology change, or
   a payload schema mismatch.
5. **DB lock contention** — confirm: the relay's claim query is
   slow because another process holds the row lock. Less common
   but possible if multiple relay replicas race.

## Immediate response

1. Open the outbox dashboard. Read off `outbox_pending_count` and
   the publish rate over the last 30 minutes.
2. If publish rate ≈ 0 and pending > 0: the relay is stuck. Check
   pod status, logs, and leader-election state (register
   `app/leader.Module(elector)` and read via `leader.Elector(infra)`).
   Restart the pod if the
   process is alive but the loop is wedged.
3. If publish rate > 0 but errors are also > 0: open the broker
   dashboard or `kubectl logs` for the broker. Common signals:
   AMQP `connection.close`, NATS auth-revoked, Redis OOM.
4. If publish rate ≈ normal and the backlog is purely from a
   workload spike: raise relay concurrency (the kit's outbox relay
   accepts a `MaxParallel` option). Confirm broker can absorb the
   throughput before scaling.
5. For the critical alert (pending > 10k): consider draining a
   subset of consumers if delivery order isn't strictly required,
   or page the team responsible for downstream consumers.

## Longer-term fix

- Add an SLO on `outbox_pending_count` (e.g. p95 < 100 over 1h)
  and a Grafana panel showing the trend over 7d.
- Pre-warm relay capacity for predictable spikes (e.g. nightly
  batch). Schedule a higher `MaxParallel` during the window.
- Periodically vacuum/archive the outbox table so the relay's
  claim query stays fast even after a long backlog episode.

## Related metrics / queries

```promql
# Backlog growth rate per minute
deriv(outbox_pending_count[15m]) * 60

# Net drain rate (publishes - errors per second)
rate(outbox_published_total[5m]) - rate(outbox_errors_total[5m])

# Time-to-drain estimate (seconds), assumes constant rate
outbox_pending_count
  / clamp_min(rate(outbox_published_total[5m]), 0.0001)
```
