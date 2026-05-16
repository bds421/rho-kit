# AGENTS.md — `infra/messaging/natsbackend`

## When to use this package

- NATS JetStream is the broker — subject-based, push consumer model fits the workload.
- Per-subject retention + at-least-once delivery semantics.
- Native NATS auth (credentials file, JWT, NKey).

## When to use something else

- **AMQP / Kafka / Redis Streams:** see the respective backends.
- **Plain NATS (no JetStream):** out of scope — the kit only wraps the JetStream surface.

## Key APIs

- `NewPublisher(conn, opts...)` / `NewConsumer(conn, opts...)`.
- `WithCredentials(...)` — supports credentials file path and inline credentials. NKey support via `WithNKeyOptions(...)`.

## Common mistakes

- **Treating retry as broker-side automatic** — NATS JetStream supports max-deliver but the kit consumer maps `messaging.Binding.Retry` to that. Without `WithoutRetry: true`, the default `Retry` policy applies.
- **Per-tenant stream / durable names (cardinality)** — opaque labels default since wave 140, but the JetStream config side still suffers if you have thousands of streams. Aggregate where possible.
- **Mixing JetStream with non-JetStream subjects** — this package assumes JetStream throughout.

## Observability

- Metrics: `nats_published_total`, `nats_publish_duration_seconds`, `nats_consumed_total`, `nats_handler_duration_seconds`. Labels `exchange`, `routing_key`, `outcome` (publish) / `stream`, `durable`, `outcome` (consume) — opaque since wave 140.
- OTel: `natsbackend/natstracing` for W3C trace context propagation.
