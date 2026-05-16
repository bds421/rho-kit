# Tracing the rho-kit Surface

## Why This Runbook Exists

Waves 167–169 instrumented the kit's internal call paths with
OpenTelemetry spans so a single trace can follow a request from
HTTP middleware → cache lookup → idempotency check → handler →
publish to messaging backend → outbox relay. This runbook
documents WHAT the kit emits so you know what's available before
you go hunting for it.

The kit does NOT push or sample traces itself. If you're not
seeing kit spans in your tracing backend:

1. Confirm the global OpenTelemetry tracer provider is configured
   in your service (`otel.SetTracerProvider(...)`).
2. Confirm an exporter (OTLP, Jaeger, Zipkin) is wired and
   reachable.
3. Confirm propagation is configured
   (`otel.SetTextMapPropagator(...)`).

The kit uses `otel.Tracer(...)` calls; if the global provider is
the no-op default, every span call is silently dropped.

## What the Kit Spans

### Data adapters (wave 168)

- `data/cache/rediscache`: `cache.Get`, `cache.Set`,
  `cache.Delete`, `cache.Exists`, `cache.MGet`, `cache.MSet`,
  `cache.SetNX`. Attributes: `db.system=redis`, `kit.cache.name`,
  `kit.cache.miss` (where applicable).
- `data/idempotency/pgstore` + `data/idempotency/redisstore`:
  `idempotency.Get`, `idempotency.Set`, `idempotency.TryLock`,
  `idempotency.Unlock`, `idempotency.DeleteExpired` (pgstore
  only). Attributes: `db.system=postgresql|redis`,
  `kit.idempotency.backend=pgstore|redisstore`. **Keys are
  NEVER attached** (PII safety).
- `data/lock/pgadvisory` + `data/lock/redislock`: `lock.Acquire`,
  `lock.AcquireTx` (pgadvisory only), `lock.Release`,
  `lock.Extend`. Attributes: `db.system=postgresql|redis`,
  `kit.lock.backend=pgadvisory|redislock`.

### Lifecycle + resilience (wave 169)

- `runtime/lifecycle`: spans around every `Component.Start` and
  `Component.Stop` call with `kit.lifecycle.component` attribute.
  Useful for diagnosing slow startup sequences.
- `resilience/retry`: `retry.Do` span around the entire retry
  loop. Per-attempt details are kept as span events rather than
  child spans to avoid trace cardinality blow-up.
- `resilience/circuitbreaker`: `breaker.Execute` /
  `breaker.ExecuteCtx` spans with `kit.breaker.state` attribute
  on completion. `ErrCircuitOpen` is recorded as an attribute,
  not a span error — open circuits are an expected steady state,
  not an exception.

### Messaging (wave 167)

Three per-backend tracing-helper sub-packages — `kafkatracing`,
`natstracing`, `redistracing` — provide `InjectHeaders` /
`ExtractContext` for cross-process propagation plus
`StartConsumerSpan` / `StartPublisherSpan` for backend-aware
semconv attributes. Direct backend implementations call these
helpers themselves; consumers using the high-level
`messaging.Subscription` get propagation transparently.

## What's Intentionally NOT Spanned

- **Per-message high-throughput loops**: e.g. the AMQP consume
  loop body. A span per delivery would inflate exporter load by
  orders of magnitude. Instead the consumer-level
  `messaging.Subscription` emits one span per dispatch.
- **The leader-election `Run` loop**: long-lived; a single
  long-running span per leader term would distort waterfall
  views. Term-level events are emitted as metrics
  (callback-drain) instead.
- **Internal heartbeat / keepalive ticks**: noise, not signal.

## Debugging Tips

- **Trace shows a kit span but no children for downstream
  work**: the downstream code probably calls a non-instrumented
  path. The kit only spans what it owns; consumer code is
  consumer-responsibility.
- **All kit spans have the same `db.system`**: that's the
  attribute carrying which backend the adapter is talking to.
  Filter on it when the same operation runs against multiple
  stores (pgstore vs redisstore idempotency, for instance).
- **Span shows the operation succeeded but the metric shows an
  error**: check whether the operation is one the kit
  intentionally treats as non-error (e.g. `ErrCircuitOpen`,
  `http.ErrServerClosed`). These are attribute-only on the
  span; they DO surface in error metrics if you've wired the
  consumer-side error counter.

## What's NOT Available

There is no kit-emitted "service health" trace. Traces are
request-scoped; for health overviews use the dashboards under
`observability/dashboards/grafana/` and the alerts under
`observability/dashboards/prometheus/`.
