# rho-kit Structured Logging Conventions

> **Audience:** operators triaging incidents; coding agents
> writing kit-aware log queries; reviewers ensuring new kit
> packages emit fields consumers can already query.

Every kit-emitted log line carries a uniform set of structured
fields so a single Loki / Splunk / CloudWatch / Datadog query
correlates incidents across services. The contract is encoded
in [`observability/logattr`](../observability/logattr/logattr.go) —
**use those constructors rather than ad-hoc `slog.String(...)` calls**
to keep field names typo-free and queryable.

## The field contract

| Field          | Emitted by                                  | Cardinality | Example value                  | Why it exists                                                     |
|----------------|---------------------------------------------|-------------|--------------------------------|-------------------------------------------------------------------|
| `error`        | every error log line                        | high        | `redacted error: *url.Error`   | Redacted error chain; safe to surface in logs/exports.            |
| `component`    | every lifecycle-managed component           | low         | `outbox-relay`, `centrifuge`   | Routes alerts and correlates "which subsystem panicked."          |
| `request_id`   | every HTTP/gRPC handler under kit middleware| high        | `01HXYZ…`                       | Cross-service correlation; matches `X-Request-ID` header.         |
| `trace_id`     | every log line under an active span        | high        | `0af7651916cd…`                 | OTel correlation — drop into the tracing UI from a log line.     |
| `span_id`      | every log line under an active span        | high        | `b7ad6b71…`                     | OTel correlation; pairs with `trace_id`.                          |
| `method`       | every HTTP/gRPC handler log                 | low         | `GET`, `POST`                  | Filter by verb; combine with `path` to find a single route.       |
| `path`         | every HTTP handler log                      | medium      | `/orders/{id}` (redacted)      | Redacted to drop user-controlled segments.                        |
| `status`       | every HTTP handler log                      | low         | `200`, `503`                   | Numeric for range queries.                                        |
| `duration`     | every operation log                         | high        | `42ms`                         | Latency analysis.                                                 |
| `instance`     | every backend-adapter log                   | medium      | `pool-pg-primary` (redacted)   | Distinguishes pools / clients of the same backend type.           |
| `operation`    | every audit / action log                    | medium      | `user.create` (redacted)       | Audit trail correlation.                                          |
| `queue`        | every queue consumer/publisher log          | medium      | `orders.created` (redacted)    | Per-queue filtering.                                              |
| `topic`        | every messaging consumer/publisher log      | medium      | `events.orders` (redacted)     | Per-topic filtering.                                              |
| `stream`       | every redis-stream / event-stream log       | medium      | `events.lifecycle` (redacted)  | Per-stream filtering.                                             |
| `url`          | every outbound HTTP client log              | high        | `https://api.svc/...` (redacted)| Redacted to avoid surfacing query-string secrets.                 |
| `attempt`      | every retry log line                        | low         | `2`                            | Retry-attempt count for backoff analysis.                         |
| `delay`        | every retry log line                        | medium      | `250ms`                        | Backoff duration.                                                 |
| `count`        | every batch operation                       | medium      | `512`                          | Batch size for throughput analysis.                               |
| `user_id`      | every per-user log                          | high        | redacted                       | Redacted; emit hashed in production exports.                      |
| `email`        | every per-user log involving email          | high        | `a***@example.com`             | Local part masked; domain preserved for triage.                   |

The full constructor set is in
[`observability/logattr/logattr.go`](../observability/logattr/logattr.go).
When the kit emits a log line about a thing not in the table
above, the contract is "use `redact.String(field, value)` so the
value is redaction-aware."

## Secret-safe fields

| Constructor                         | What it does                              | When to use                                       |
|-------------------------------------|-------------------------------------------|---------------------------------------------------|
| `logattr.Secret(key, val)`          | Emits `<redacted N bytes>` — no digest    | Default — for OTPs, short codes, reset tokens.    |
| `logattr.SecretWithDigest(key, val)`| Emits `<redacted N bytes sha256:abc12345>`| High-entropy secrets only (JWTs, API keys, ≥32 B).|

**Never** use `slog.String("password", v)` or `slog.String("token", v)`
on raw secret values. `slog.String` has no awareness of secrecy and
the kit's redaction does not retroactively rewrite emitted records.

## Logger discovery

Inside an HTTP/gRPC handler under kit middleware, the
request-scoped logger is on `ctx`:

```go
logger := httpx.Logger(ctx)
logger.InfoContext(ctx, "order accepted",
    logattr.Component("orders-api"),
    logattr.RequestID(httpx.RequestID(ctx)),
)
```

The middleware in `httpx/middleware/logging.WithRequestLogger`
installs the logger; downstream code never needs to plumb
`*slog.Logger` through every function signature.

## Canonical incident queries

These are the queries an operator on-call runs first. They're
written for Loki; the field names translate directly to Splunk
SPL, Datadog log search, and CloudWatch Logs Insights.

### "What's failing right now in service X?"

```logql
{service="orders-api"}
  | json
  | level="error"
  | line_format "{{.error}} | {{.component}} | {{.request_id}}"
  | __error__ = ""
```

### "Trace a single request across services"

```logql
{cluster="prod"}
  | json
  | trace_id = "0af7651916cd43dd8448eb211c80319c"
  | line_format "[{{.service}}] [{{.component}}] {{.level}} {{.msg}}"
```

### "Recent retries against a specific downstream"

```logql
{service="orders-api"}
  | json
  | attempt > 1
  | instance =~ "billing-.*"
  | line_format "[{{.attempt}}/{{.delay}}] {{.error}}"
```

### "P99 endpoint latency from logs (when metrics are noisy)"

```logql
quantile_over_time(0.99,
  {service="orders-api"} | json | __error__ = "" | unwrap duration_ms [5m]
) by (path)
```

### "Outbox relay errors in the last hour"

```logql
{service=~".*"}
  | json
  | component="outbox-relay"
  | level="error"
  | __error__ = ""
```

### "Idempotency lock-lost incidents"

```logql
{service=~".*"}
  | json
  | error =~ ".*lock no longer held.*"
  | line_format "{{.component}} {{.operation}} {{.error}}"
```

### "Authentication failures clustering on one user"

```logql
sum by (user_id) (
  count_over_time(
    {service=~".*"}
      | json
      | component="jwt-auth"
      | level="warn" [10m]
  )
)
```

### "Rate-limit decisions that hit the cap"

```logql
{service=~".*"}
  | json
  | operation="ratelimit.decision"
  | outcome="limited"
```

## Per-package queries

The kit's runbooks under `docs/ai/runbooks/` cite the alerts they
respond to. Operators picking up an incident should be able to
go from the alert → runbook → a log query that surfaces the
underlying log lines.

| Runbook                              | Log query                                                                                                                                  |
|--------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------|
| `outbox-errors.md`                   | `{service=~".*"} \| json \| component="outbox-relay" \| level="error"`                                                                     |
| `outbox-backlog.md`                  | `{service=~".*"} \| json \| component="outbox-relay" \| msg=~"backlog.*"`                                                                  |
| `leader-election.md`                 | `{service=~".*"} \| json \| component=~"leaderelection.*" \| msg=~"drain.*"`                                                               |
| `centrifuge.md`                      | `{service=~".*"} \| json \| component="centrifuge" \| outcome=~"rejected\|error"`                                                          |
| `grpc-stream-limits.md`              | `{service=~".*"} \| json \| component="grpcx" \| error =~ ".*resource exhausted.*"`                                                        |
| `redis-pool.md`                      | `{service=~".*"} \| json \| component=~"redis.*" \| level="warn" \| msg=~"pool.*"`                                                         |
| `db-saturation.md`                   | `{service=~".*"} \| json \| component=~"sqldb.*" \| msg=~".*wait.*"`                                                                       |
| `ratelimit.md`                       | `{service=~".*"} \| json \| component="ratelimit" \| outcome="limited"`                                                                    |
| `amqp-messaging.md`                  | `{service=~".*"} \| json \| component="amqp" \| level=~"warn\|error"`                                                                      |
| `nats-messaging.md`                  | `{service=~".*"} \| json \| component="nats" \| level=~"warn\|error"`                                                                      |
| `redis-stream.md`                    | `{service=~".*"} \| json \| component="redisstream" \| msg=~"dead.*\|fail.*"`                                                              |

These are starting points — every service will need to filter
by `{service=...}` or `{namespace=...}` first.

## Adding new fields

When a kit package needs a new log field:

1. **Check if `logattr` has one already.** Field names should be
   reused across packages so consumers' queries keep working.
2. **If not, add it to `observability/logattr`.** A constructor
   like `func Foo(value string) slog.Attr { return redact.String("foo", value) }`
   so future callers find it. Wrap with `redact.String` when
   the value could carry user-controlled bytes.
3. **Add a row to the table above.** This file is the contract.
4. **If the field has medium-to-high cardinality**, document in
   the field's row whether operators should `unwrap` it or
   filter by it.

## CI gate

The kit currently does NOT enforce "use logattr instead of
slog.String" via lint — that would generate too many false
positives in the existing codebase. The expectation is reviewed
at PR time. A future kit-doctor rule
`raw-slog-string-for-known-field` could enforce this for new
code without churning the existing surface.

## What this is NOT

- **A log SHAPE contract.** Whether your service emits JSON,
  logfmt, or text is up to your `slog.Handler`. The kit uses
  `slog.JSONHandler` in examples; consumers can swap freely.
- **A drop-in Loki schema.** The queries above are illustrative
  starting points. Real production queries pin labels
  (`{cluster=…}`, `{tenant=…}`) that the kit doesn't dictate.
- **A replacement for metrics or traces.** Logs answer "what
  happened in this one request"; metrics answer "what's the
  rate"; traces answer "where did the time go." Use all three.
