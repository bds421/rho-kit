# Runbook: availability

Covers `RhoKitHighErrorRate` (5xx ratio > 5% for 10m) and
`RhoKitElevated4xxRate` (4xx ratio > 20% for 30m).

Snippet status: PromQL blocks in this runbook are illustrative query fragments
to adapt to the service's Prometheus labels and SLO windows.

## What this alert says

The HTTP server is returning a high proportion of error responses.
For 5xx, the server itself is failing — bug, dependency outage, or
resource exhaustion. For 4xx, clients are sending requests the server
rejects — schema mismatch, an auth break, or an attack.

## Likely causes (in order of frequency)

1. **Recent deploy** — confirm by correlating the alert start time
   against the latest production deploy in your CD log. Elevated
   error rate within minutes of a deploy is almost always the new
   build.
2. **Downstream dependency outage** — confirm with the per-area
   dashboards: DB pool exhausted? Redis timeouts? Outbox backlog?
   The HTTP RED dashboard's error rate by route narrows down which
   handlers fail (e.g. only `/orders` failing => order DB issue).
3. **Resource exhaustion** — confirm with the runtime-go dashboard:
   goroutine count climbing without bound, heap not GC'ing,
   in-flight requests piling up.
4. **Auth/schema change (4xx)** — confirm with status-class
   distribution: 401/403 spike => auth deploy on caller side; 400
   spike => API contract change.
5. **Traffic spike past SLO budget** — confirm with the request-rate
   panel; if rate is 3-5× normal, the service may be load-shedding
   correctly.

## Immediate response

1. Open the HTTP RED dashboard for the affected service.
2. Identify which routes contribute most to the error rate (panel:
   *Error rate by route*).
3. If the alert started < 1 deploy ago: roll back. The error rate
   will drop within one scrape interval; confirm before declaring
   incident over.
4. If pre-deploy: open the per-area dashboard for the most likely
   culprit (DB / Redis / Storage / Outbox). The panels are sized to
   show pool exhaustion, latency spikes, or error counters within
   30s.
5. If neither helps: check the runtime-go dashboard for a leak
   (goroutines, heap) and the application logs for a panic loop in
   `recover` middleware.

## Longer-term fix

- For repeat 4xx spikes: add a contract test or schema-validation
  middleware to fail at deploy time, not at request time.
- For repeat 5xx-on-deploy: add a smoke test in the canary stage of
  CD that exercises the error path of the changed handler.
- For dependency-driven 5xx: install per-dependency saturation
  alerts (this kit ships `alerts-saturation.yaml`).

## Related metrics / queries

```promql
# Error ratio over 5m, per route
sum by (namespace, service, route) (rate(http_errors_total[5m]))
  / sum by (namespace, service, route) (rate(http_requests_total[5m]))

# Top 5 routes by 5xx in last 10m
topk(5,
  sum by (route) (
    increase(http_errors_total{status_class="5.."}[10m])
  )
)

# Rolling deploy correlation: requests served per pod in 1m windows
sum by (instance) (rate(http_requests_total[1m]))
```
