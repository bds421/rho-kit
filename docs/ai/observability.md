# Observability - Health, Metrics, Tracing, Profiling

Packages: `observability/health`, `observability/redmetrics`, `observability/runtimemetrics`, `observability/slo`, `observability/pprof`, `observability/tracing`, `observability/logattr`, `observability/promutil`, `observability/auditlog`, `observability/auditlog/postgres`

Snippet status: Go blocks in this recipe are illustrative fragments unless
explicitly introduced as generated or executable code. Buildable golden-path
evidence lives in `cmd/kit-new` scaffold tests and `examples/agentic-service`.

## When to Use

| I need to... | Use |
|---|---|
| Expose readiness and dependency status | `observability/health`, `httpx/healthhttp` |
| Collect HTTP/gRPC request RED metrics | `observability/redmetrics` |
| Export Go runtime metrics | `observability/runtimemetrics` |
| Check latency/error-rate SLOs | `observability/slo` |
| Add internal-only profiling endpoints | `observability/pprof` |
| Initialize OpenTelemetry tracing | `observability/tracing` |
| Add consistent log attributes | `observability/logattr` |
| Register Prometheus collectors safely | `observability/promutil` |
| Persist structured audit events | `observability/auditlog` |
| Start from production dashboard/runbook templates | `observability/dashboards`, `docs/ai/runbooks` |

## Tracing

Use `app/tracing.Module` for Builder services:

```go
app.New("orders", version, cfg.BaseConfig).
    With(tracing.Module(tracing.Config{
        ServiceName:    "orders",
        ServiceVersion: version,
        Environment:    cfg.Environment,
        Endpoint:       "otel-collector:4317",
        SampleRate:     0.05,
        Compression:    "gzip",
    }))
```

`tracing.Config.Validate` runs in `tracing.Init` and `Builder.Validate`.
When `Endpoint` is set, `ServiceName` is required. `Endpoint` must be
`host[:port]`, not a URL, and it must not contain credentials, path, query,
or fragment components. OTLP exporter headers are treated as secret-bearing:
logs expose only whether headers are configured, and `Init` snapshots the
header map before exporter setup. Set `Insecure: true` only for local collectors
or trusted sidecar hops where plaintext is intentionally accepted.

Keep `EnableBaggage` off unless every downstream service treats baggage as
untrusted input. Baggage propagates arbitrary key/value pairs and is easy to
leak into logs.

## Health

Builder services expose internal readiness through `/ready` on the internal
listener. Add dependency checks through Builder methods or `AddHealthCheck`:

```go
b.AddHealthCheck(health.DependencyCheck{
    Name: "search",
    Check: func(ctx context.Context) string {
        if err := searchClient.Ping(ctx); err != nil {
            return health.StatusUnhealthy
        }
        return health.StatusHealthy
    },
})
```

Check names must be stable, low-cardinality identifiers. Do not include
tenant IDs, request IDs, hostnames that change per deploy, or user input.
Use `health.OpaqueCheckName("search", rawEndpoint)` when a check needs a
stable disambiguator for a topology-bearing value without exposing the value.

## Metrics

Metrics constructors accept a `prometheus.Registerer` option where practical.
Use a custom registry in tests to avoid duplicate collector registration:

```go
reg := prometheus.NewRegistry()
mw := redmetrics.NewHTTPMiddleware(redmetrics.WithRegisterer(reg))
```

Use `promutil` helpers when registering collectors from shared libraries so
duplicate registrations degrade predictably instead of panicking in process
startup.
Use `promutil.OpaqueLabelValue("queue", rawQueueName)` for metric labels
derived from Redis keys, storage resources, hosts, or other topology-bearing
identifiers. The resulting label is stable but not cleartext; keep the source
dimension low-cardinality anyway.
`redmetrics.HTTPLatencyBuckets()` and `redmetrics.BatchDurationBuckets()`
return detached default bucket slices; custom bucket options clone their input
so caller-side slice mutation cannot alter registered histograms.

## Dashboards And Runbooks

The v2 dashboard bundle lives under `observability/dashboards/` and is
validated by `.github/workflows/dashboards.yml`. Grafana JSON dashboards cover
HTTP RED, gRPC RED, DB pool, Redis, Outbox, direct AMQP, HTTP rate limits,
direct NATS JetStream, direct Redis Streams, Storage overview,
provider-specific S3/GCS/Azure/SFTP storage panels, Go runtime, and service
overview. Prometheus rules cover latency, availability, saturation, messaging,
rate limiting, recording rules, and SLO templates. Alert `runbook_url`
annotations point to the matching pages under
`docs/ai/runbooks/`.

Keep dashboard changes paired with the metric contract they depend on. A new
collector or label dimension should update the dashboard, runbook, and alert
rules in the same change so operators do not receive panels or alerts that
cannot be explained from the docs.

## Audit Events

`observability/auditlog` owns the `Store` and `Logger` contracts for structured
audit events. `NewMemoryStore` is for tests and local demos only. Production
services use `observability/auditlog/postgres`, which implements the full Store
contract (advisory-lock-serialised `AppendChained`, append-order `RangeChain`
for `Logger.VerifyChain`, signed-cursor `Query` pagination, `LastHMAC` for
operator tooling). Apply its schema with `kit-migrate publish --to=./migrations
auditlog`. Use `LogE` when the originating action must fail if the audit
append fails. `Log` is best-effort and reports drops through logs,
`WithDroppedCounter`, and `WithOnDrop`.

Use `httpx/middleware/auditlog` for request-level HTTP audit capture. Configure
the same trusted proxy CIDRs as access logging so `IPAddress` is derived from
the same trust boundary. The middleware keeps the request trace context while
detaching from client cancellation, so audit entries retain trace correlation
without being dropped on disconnect.

`Event.Metadata` is copied on logger and memory-store boundaries. Still keep it
small and do not place raw credentials, bearer tokens, cookies, or payment data
in audit metadata.

## Profiling

`observability/pprof` is for internal listeners only. Do not mount pprof on
public routers. In Builder services, keep profiling behind the internal
operations address and network policy. `pprof.Handler()` and `pprof.Mount()`
default to loopback-only access; use `MountWith(..., pprof.WithAuth(...))` for
authenticated non-loopback internal listeners, or `WithUnsafePublicMount()` only
when an external control plane already enforces access.

## Anti-Patterns

- **Never** put user IDs, tenant IDs, request IDs, or raw route parameters in metric names or high-cardinality labels.
- **Never** expose `/metrics`, `/ready`, or pprof handlers on a public listener without explicit network isolation.
- **Never** configure tracing with a URL endpoint; use `host[:port]`.
- **Never** enable Baggage propagation as a default.
- **Never** ignore tracing fallback in production; wire `OnInitFallback` to an alert or audit signal when collector reachability is required.
- **Never** use `auditlog.NewMemoryStore()` as a production audit store.
- **Never** write secrets, cookies, bearer tokens, or payment data to audit metadata.
