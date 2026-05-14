# Runbook: DB pool saturation

Covers `RhoKitDBPoolWaiting` (waits/s > 1 for 10m, warn) and
`RhoKitDBPoolNearExhaustion` (>90% in-use for 5m, page).

Snippet status: PromQL blocks in this runbook are illustrative query fragments
to adapt to the service's Prometheus labels and SLO windows.

## What this alert says

The database connection pool is full or close to full. New queries
either wait for an existing connection to free up (visible as
increases in `<service>_db_wait_count_total`) or, if `MaxOpenConns`
allows growth, open new connections — but eventually that ceiling is
hit too. Symptoms upstream: HTTP p99 latency climbs, errors when
`context.DeadlineExceeded` cancels waiting queries.

## Likely causes (in order of frequency)

1. **Slow queries holding connections** — confirm: long
   query-duration p99 from the application logs, or a `pg_stat_activity`
	   snapshot showing queries running > 1s. Often
   triggered by missing indexes or table-scan-inducing parameter
   values.
2. **Pool size too small for load** — confirm: in-use is at
   MaxOpenConns *and* DB CPU is < 70%. The DB has headroom; the pool
   is artificially capped.
3. **Connection leak** — confirm: open count grows monotonically and
   never drops between traffic dips. Look for missing `rows.Close()`
   or transactions that escape their scope.
4. **Long-running transaction** — confirm: a single transaction has
   held a connection for minutes, visible in `pg_stat_activity` with
   `state='idle in transaction'`. Often a buggy retry loop.
5. **Connection storm after DB restart** — confirm: timestamp of the
   spike correlates with a known DB event. Self-corrects in a few
   minutes as the pool re-warms.

## Immediate response

1. Open the DB pool dashboard. Read off the in-use vs open
   ratio and the wait rate.
2. If wait rate > 1/s and in-use ≈ open: the pool is too small *or*
   queries are too slow. Decide which:
	   - Run `SELECT pid, now()-query_start AS d, state, query FROM
	     pg_stat_activity ORDER BY d DESC LIMIT 10;`. If many queries are running >
     1s, fix the queries before raising the pool size.
   - If queries are fast but the pool is small: raise `MaxOpenConns`
     by 50%. Confirm DB CPU stays under 70%.
3. If a leak: the open count graph trends up linearly. Bounce a
   single pod to drain leaked connections; file a bug to find the
   missing `Close()`.
4. If a stuck transaction: kill the offending PID with
   `pg_terminate_backend(<pid>)`.
   File a bug to find the orphaned transaction.

## Longer-term fix

- Thread a context with a per-query deadline so context-cancelled
  queries actually release their pgx connection. rho-kit's
  `infra/sqldb` (pgx-backed in v2; lib/pq and GORM were removed) does
  this when callers thread a context — the pgx driver propagates
  cancellation to PostgreSQL via its cancel-request protocol.
- Run `EXPLAIN ANALYZE` on the slowest endpoint's query and add
  the missing index.
- Set `MaxOpenConns` to a value that the DB can actually handle:
  rule of thumb is `(num_db_cores * 2) + spindles`, capped at the
  DB's `max_connections` divided by the number of pods.
- Ship a per-tenant query budget if a single tenant can hog the
  pool.

## Related metrics / queries

```promql
# In-use ratio (>0.9 is bad)
(
  sum by (namespace, service) ({__name__=~".+_db_open_connections"})
  - sum by (namespace, service) ({__name__=~".+_db_idle_connections"})
)
/ sum by (namespace, service) ({__name__=~".+_db_open_connections"})

# Wait rate per second
sum by (namespace, service)
  (rate({__name__=~".+_db_wait_count_total"}[5m]))

# Cumulative waits since process start
sum by (namespace, service)
  ({__name__=~".+_db_wait_count_total"})
```
