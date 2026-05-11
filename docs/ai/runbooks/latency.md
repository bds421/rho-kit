# Runbook: latency

Covers `RhoKitLatencyP99High` (p99 > 1s for 15m, warn) and
`RhoKitLatencyP99Critical` (p99 > 5s for 5m, page).

Snippet status: PromQL blocks in this runbook are illustrative query fragments
to adapt to the service's Prometheus labels and SLO windows.

## What this alert says

The slowest 1% of requests are taking longer than the SLO permits.
p99 spikes almost always originate downstream of the HTTP handler —
a slow database query, a saturated Redis pool, an upstream API that
slowed down. Less commonly: a code regression that added work to the
hot path.

## Likely causes (in order of frequency)

1. **Slow database query** — confirm on the DB pool dashboard: open
   connections climb because each query holds a connection longer.
   `_db_wait_count_total` rising means new queries queue too. A
   slow query plan after a schema/data change is the textbook cause.
2. **Redis saturation** — confirm on the Redis dashboard: command
   p99 climbs, pool timeouts increase, reconnect attempts spike.
3. **Downstream HTTP dependency** — confirm by checking client-side
   metrics or the per-route latency panel: if only routes that fan
   out to a single upstream are slow, that's your culprit.
4. **GC pressure** — confirm on the runtime-go dashboard: GC pause
   rate elevated, heap growing fast. A leak or sudden allocation
   pattern change adds 100ms+ pauses.
5. **Code regression** — confirm with deploy correlation. A new sync
   call in a hot loop, a missing index, an N+1 query.
6. **Cold-start spike after deploy** — confirm by looking at the
   alert window vs deploy time; this self-corrects in 5-10 minutes
   as the JIT/cache warms.

## Immediate response

1. Identify the slow route on the HTTP RED dashboard's *Latency
   p50/p95/p99 by route* panel.
2. If only one route: open the per-area dashboard most likely
   responsible (DB, Redis, Storage). Most p99 spikes have a clear
   downstream signal.
3. If many routes share the spike: it's a shared resource — DB pool,
   Redis pool, or runtime (GC, goroutines).
4. If the alert started < 1 deploy ago and the spike is broad: roll
   back. Slow queries from a new code path almost always go away
   when the code is reverted.
5. If a single noisy tenant: throttle or shed at the rate-limit
   layer. The HTTP RED dashboard shows requests-in-flight; a single
   tenant pinning that gauge is usually visible.

## Longer-term fix

- For repeat DB-driven spikes: add an index, batch the query, or
  cache the result. Validate with EXPLAIN before deploying.
- For repeat upstream-driven spikes: add a deadline / circuit
  breaker via `resilience/breaker`.
- For GC-driven spikes: run a heap profile and look for retention
  paths that point at slices keeping old entries alive.

## Related metrics / queries

```promql
# Per-route p99 latency (rolling 5m)
http_request_duration_seconds:p99:5m

# Compare p50 vs p99 per route — large gap = tail latency problem
http_request_duration_seconds:p99:5m
  - ignoring(quantile) http_request_duration_seconds:p50:5m

# DB connection wait correlation
sum by (namespace, service)
  (rate({__name__=~".+_db_wait_count_total"}[5m]))

# Upstream dependency: if you instrument outbound HTTP, replace
# below with your client-side histogram name.
# histogram_quantile(0.99, sum by (le) (rate(<client>_duration_bucket[5m])))
```
