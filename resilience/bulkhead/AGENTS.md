# AGENTS.md — `resilience/bulkhead`

## When to use this package

- The service fans out to a SHARED downstream resource (database
  pool, downstream HTTP service, broker) and a slow downstream
  could exhaust the service's goroutine + connection budget.
- Per-downstream concurrency cap is the right primitive — you
  want "at most N concurrent calls to downstream X" semantics.
- Pair with `resilience/circuitbreaker` (outer) and
  `resilience/retry` (inner) for the canonical fault-tolerant
  fan-out chain.

## When to use something else

- **Global server-wide stream cap (not per-downstream):**
  `grpcx/interceptor.MaxConcurrentStreamsServer` /
  `httpx/websocket.WithMaxConnections` already cap inbound
  concurrency at the server boundary.
- **Per-tenant rate limit, not per-downstream concurrency:**
  `httpx/middleware/ratelimit` (sliding window) or
  `data/ratelimit/redis` (distributed GCRA).
- **Budget for request-scoped total time across multiple
  downstreams:** `resilience/timeoutbudget` — different
  semantics; bulkhead caps concurrency, budget caps elapsed time.

## Key APIs

- `New(name, max, opts...)` — Construct. `name` is a static
  Prometheus label (validated at construction). `max` must be
  positive. Apply `WithMaxQueueWait(d)` to allow callers to
  wait up to `d` when full; default is immediate rejection
  with `ErrBulkheadFull`.
- `ExecuteCtx(ctx, fn)` — Acquire a slot, run fn, release.
  Returns `ErrBulkheadFull` on full + wait timeout, ctx error
  on caller cancel. Panicking fns release the slot then
  re-raise so consumer panic-recovery middleware can observe.
- `InFlight()` / `Capacity()` — Observability accessors.
- `WithMetrics(m)` — Wire `bulkhead_acquisitions_total` +
  `bulkhead_acquire_duration_seconds` metrics keyed by name and
  outcome.

## Common mistakes

- **Single global bulkhead** — couples unrelated downstreams'
  failure modes. Construct one bulkhead per distinct downstream.
- **Bulkhead INSIDE the retry loop, not outside.** Putting
  bulkhead inside retry means each retry attempt re-acquires;
  bulkhead OUTSIDE means one acquisition covers all retries.
  Almost always you want the outer position so retries don't
  exhaust the slot count.
- **Missing `WithMaxQueueWait` when callers should wait
  briefly.** The default is "immediate rejection on full" which
  is the right call for latency-critical paths. But for batch
  / background work, a few hundred milliseconds of wait avoids
  thrash.
- **Forgetting that `ErrBulkheadFull` is the kit's saturation
  signal.** Map it to HTTP 503 (or domain-appropriate),
  emit a metric, do NOT log-at-error every occurrence — under
  load this fills logs.

## Observability

- Metrics: `bulkhead_acquisitions_total{name,outcome}` (counter,
  outcome ∈ acquired / full / ctx_cancelled / error),
  `bulkhead_acquire_duration_seconds{name,outcome}` (histogram).
- The `name` label is validated as a static value via
  `promutil.ValidateStaticLabelValue` so a per-request name
  cannot inflate cardinality.
- OTel spans: not emitted per acquisition (would dominate
  high-throughput downstream traces). Caller-level spans on
  the wrapped operation give the right granularity.
