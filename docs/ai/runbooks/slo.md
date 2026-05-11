# Runbook: SLO burn

Covers `RhoKitSLOFastBurn` (1h@14.4× burn, page) and
`RhoKitSLOSlowBurn` (6h@6× burn, warn).

Snippet status: PromQL blocks in this runbook are illustrative query fragments
to adapt to the service's Prometheus labels and SLO windows.

## What this alert says

The service is burning its error budget faster than the SLO permits.
Default budget: 99.9% over 30 days = 43.2 minutes of allowed
"badness" per month.

- **Fast burn (14.4×)**: at this rate, the entire monthly budget
  gets consumed in ~2 hours. A user-impacting incident is in
  progress *right now*.
- **Slow burn (6×)**: the entire monthly budget is being consumed
  in ~5 days. Not on fire, but the trajectory is bad — fix before
  the budget is exhausted and the error count translates into
  customer churn.

The two-window logic (`for: 2m` / `for: 15m` plus an `and` of two
windows) means both a recent burst *and* a sustained pattern have
to be present. False positives from a single 5-minute glitch are
filtered out.

## Likely causes (in order of frequency)

1. **Active incident** (fast burn) — same root causes as
   `availability.md`: deploy regression, dependency outage,
   resource exhaustion. SLO burn is the customer-impact framing of
   those failures.
2. **Slow degradation** (slow burn) — a memory leak that takes 12h
   to OOM, a query plan that gets slower as the table grows, a
   cache that's gradually losing its hit ratio. The slow burn
   alert is calibrated to fire before the cumulative damage
   exhausts the budget.
3. **A bad cohort of requests** (slow burn) — one customer's
   misconfigured webhook flooding a single endpoint with 5xx,
   never enough to trip the 5%/10m availability alert but enough
   to bleed the budget over a day.
4. **SLO is wrong** — if the service has been healthy for weeks
   but the alert keeps firing, the SLO target is too tight. Adjust
   the multiplier or move the SLO line.

## Immediate response

1. Check whether `RhoKitHighErrorRate` is also firing. If yes,
   work the availability runbook first — the SLO alert is a
   downstream signal of that incident.
2. If only the SLO alert is firing: check the HTTP RED dashboard's
   error-rate panel over the alert window (1h for fast, 6h for
   slow). The spike will be visible there even if it's below the
   instantaneous 5%/10m threshold.
3. For fast burn: treat as a page. Roll back the latest deploy if
   the burn started in that window. Otherwise escalate per usual
   incident process.
4. For slow burn: work it as a high-priority bug, not a 3am page.
   File a ticket, root-cause within the next business day, fix
   before another 14 days of budget erodes.

## Longer-term fix

- Examine which routes contribute most to the budget burn and
  consider a per-route SLO if one route is structurally noisier
  than the rest (e.g. a webhook endpoint with retries).
- Add error-budget burn dashboards (these alerts already use the
  recorded ratios — a Grafana dashboard plotting them over 30d
  is a 5-minute add).
- Calibrate: if either alert fires more than 2× per month without
  an actionable cause, the SLO target is too tight or the service
  isn't ready for that target yet.

## Related metrics / queries

```promql
# Current 1h burn rate (1.0 = burning at SLO rate, 14.4 = fast burn)
(
  sum by (namespace, service) (rate(http_errors_total{status_class=~"5.."}[1h]))
    / sum by (namespace, service) (rate(http_requests_total[1h]))
) / 0.001

# Total budget consumed in last 30d (1.0 = budget exhausted)
(
  sum by (namespace, service) (increase(http_errors_total{status_class=~"5.."}[30d]))
    / sum by (namespace, service) (increase(http_requests_total[30d]))
) / 0.001
```
