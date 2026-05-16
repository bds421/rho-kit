# AGENTS.md — `grpcx/interceptor`

## When to use this package

- Constructing a custom interceptor chain instead of using `grpcx.NewServer` defaults.
- Adding the wave-166 stream resource discipline (`MaxConcurrentStreamsServer`, `StreamIdleTimeout`) on top of an otherwise-default server.

## When to use something else

- **You're happy with `grpcx.NewServer` defaults** — that's the path of least surprise. Reach for this package when you need custom ordering.

## Key interceptors

### Resource discipline (wave 166)
- `MaxConcurrentStreamsServer(max, metrics)` — server-wide cap. gRPC's built-in `MaxConcurrentStreams` is per-HTTP/2-connection. Returns `codes.ResourceExhausted` before handler entry.
- `StreamIdleTimeout(d, metrics)` — cancels streams with no `SendMsg` / `RecvMsg` activity within `d`. Surfaces as `codes.DeadlineExceeded` when the watchdog fires (distinguishable from caller-initiated cancel).
- `NewStreamLimitMetrics(opts...)` — Prometheus collectors used by both.

### Per-RPC discipline
- `DeadlineUnary(d)` / `DeadlineStream(d)` — server-side default deadline (wave 119).
- `RecoveryUnary(logger)` / `RecoveryStream(logger)` — panic-to-error conversion. Always be FIRST in the chain.
- `LoggingUnary(logger)` / `LoggingStream(logger)` — structured access log.
- `MTLSAuthUnary(opts...)` / `MTLSAuthStream(opts...)` — mTLS identity gating with allowed SAN/CN.
- `Metrics().UnaryInterceptor()` / `StreamInterceptor()` — Prometheus + OTel spans.

## Interceptor ordering

Outermost first (the interceptor wrapper applies in the order given). Always: **Recovery → Metrics → Logging → Auth → Deadline → MaxConcurrent → StreamIdleTimeout → (custom)**. A panic in a `StreamIdleTimeout`-watched handler must surface through `Recovery` BEFORE the timeout records it as an idle close.

## Common mistakes

- **Recovery interceptor NOT first** — panics inside Metrics/Logging interceptors bypass recovery and crash the goroutine.
- **`MaxConcurrentStreamsServer(0, ...)`** — panics at construction. Pick a value based on your service's connection budget.
- **`StreamIdleTimeout(<your slowest-message interval, ...)`** — the watchdog will reap legitimate slow streams. Set the value > worst-case-message-interval × 2.
- **Two `Metrics` interceptors in the chain** — counts every call twice.

## Observability

See parent `grpcx/AGENTS.md`. The stream-limit metrics (`grpc_server_active_streams`, `grpc_server_streams_rejected_total{reason}`, `grpc_server_streams_idle_closed_total`) are only emitted when `NewStreamLimitMetrics` is wired.
