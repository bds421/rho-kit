# Runbook: outbox errors

Covers `RhoKitOutboxErrorRateHigh` (error ratio > 5% for 15m, warn),
`RhoKitOutboxNoProgress` (no publishes in 10m with backlog > 0,
page), and `RhoKitOutboxRelayLatencyHigh` (p99 > 5s for 15m, warn).

## What this alert says

The outbox relay is failing to publish messages — either errors are
piling up, the relay loop has stopped, or each publish takes too
long for the relay to keep up. Different from `outbox-backlog.md`
in that the *rate* of failure is the signal, not the *depth* of the
queue.

## Likely causes (in order of frequency)

1. **Broker outage** — confirm: `outbox_errors_total` rate climbs
   abruptly and `outbox_published_total` rate drops to zero at the
   same time. Check broker dashboards or
   `kubectl get pods -l app=rabbitmq` style status.
2. **Broker auth or topology change** — confirm: errors began
   immediately after a known config change (broker restart, role
   rotation, queue redeclare). The relay's logs will contain
   broker-specific reject codes (`ACCESS_REFUSED`, `NOT_FOUND`).
3. **Schema mismatch** — confirm: errors happen on a *subset* of
   messages, not all of them. The error message in the relay log
   typically points at a serialization or broker-side validation
   failure.
4. **Network partition / DNS flap** — confirm: `error rate` is
   bursty rather than steady. Underlying connection errors with
   "no route to host" or "lookup failed" in the relay logs.
5. **Broker slow / overloaded** — confirm: high p99 relay latency
   without correspondingly high error rate. The broker is taking
   the messages but each round-trip is slow.

## Immediate response

1. Open the outbox dashboard. Compare publish rate, error rate, and
   error ratio over the alert window.
2. **Stuck relay** (`RhoKitOutboxNoProgress`): verify the relay
   process is alive (`kubectl get pods`, look for restarts or
   CrashLoopBackOff). If it's running but not making progress,
   check:
   - Leader-election state (only one replica should be leader).
   - The relay's database claim query — is it blocked by a lock?
3. **Error rate** (`RhoKitOutboxErrorRateHigh`): tail the relay
   logs for the most recent error. The kit logs each publish error
   with the broker's response.
   - If the error is the *same* on every message: broker is down or
     auth/topology is wrong. Fix the broker.
   - If the error is *different* per message: schema validation
     issue per-payload. Fix the producer or the broker schema.
4. **Slow relay** (`RhoKitOutboxRelayLatencyHigh`): check broker
   metrics. If broker p99 is also high, treat as a broker capacity
   issue. If broker p99 is fine, check network: a TCP retransmit
   storm shows up as elevated p99 on the client without server-side
   slowness.

## Longer-term fix

- Add per-error-type buckets (auth vs topology vs payload) so the
  alert summary tells the on-call which subsystem to check. Today
  `outbox_errors_total` has no labels.
- For schema-mismatch errors: add producer-side validation that
  fails the *transactional write*, not the relay. A bad message
  should never enter the outbox in the first place.
- For broker outages: configure a fallback broker or buffer
  messages locally with an idempotency key (rho-kit's
  `BufferedPublisher` is the building block for this).

## Related metrics / queries

```promql
# Error ratio (5m average)
rate(outbox_errors_total[5m])
  / clamp_min(
      rate(outbox_published_total[5m])
        + rate(outbox_errors_total[5m]),
      0.0001
    )

# Per-second publish rate (compare with workload write rate to
# detect a stuck relay)
rate(outbox_published_total[5m])

# Relay p99 latency
outbox_relay_latency_seconds:p99:5m
```
