# AGENTS.md — `infra/messaging/amqpbackend`

## When to use this package

- The service publishes/consumes via RabbitMQ (or any AMQP 0-9-1 broker).
- Topology is operator-managed (exchanges, queues, bindings declared by the broker config or by this package's `DeclareAll`).
- Retry / dead-letter semantics live in the broker (DLX) rather than in handler code.

## When to use something else

- **Kafka:** `kafkabackend` — partition-ordered, log-retention semantics, different consumer-group model.
- **NATS JetStream:** `natsbackend` — subject-based, simpler consumer model, native at-most-once / at-least-once.
- **Redis Streams (no separate broker):** `redisbackend` — minimal operational footprint, single-broker only.
- **Outbox-driven publishing:** wrap this with `outbox.MessagingPublisher` so the publish call is transactional with your DB write.
- **Mid-level subscription ergonomics:** wrap the `Consumer` with `messaging.Subscription` / `messaging.TypedSubscription[T]` so the handler operates on typed values and the consumer loop is a `lifecycle.Component`.

## Key APIs

- `NewPublisher(conn, opts...)` / `NewConsumer(conn, opts...)` — backend implementations of `messaging.Publisher` / `messaging.Consumer`.
- `DeclareAll(conn, binding)` — idempotent topology declaration. Run once at startup; safe to re-run on restart.
- `BindingSpec.WithoutRetry: true` — opt out of broker-side retry/DLX. Without this AND without `Retry`, the kit applies a default policy and warns (per wave 141).

## Common mistakes

- **Forgetting `DeclareAll` at startup** — the consumer attaches to a non-existent queue; messages quietly drop. Always run topology setup before starting consumers.
- **`exchange` / `routing_key` with caller-controlled values** — these become Prometheus labels via `WithOpaqueRouteLabels` (the v2 default since wave 140); high-cardinality route segments still hurt query performance even when hashed. Keep route shapes operator-managed.
- **Treating `messaging.ErrRetryUnsupported` as a fatal error** — only fires on Kafka (no broker-side retry primitive). AMQP supports DLX, so this never trips here.
- **Publishing without `outbox` when the publish must be transactional with a DB write** — use `outbox.MessagingPublisher` + a Postgres outbox table to get exactly-once-effective publishing across process crashes.

## Observability

- Metrics: `amqp_published_total`, `amqp_publish_duration_seconds`, `amqp_consumed_total`, `amqp_handler_duration_seconds`. Labels: `exchange`, `routing_key`, `outcome` (publish-side) / `exchange`, `queue`, `outcome` (consume-side). Route labels default to opaque since wave 36.
- OTel: `amqpbackend/amqptracing` is the helper for trace context propagation. Call `amqptracing.StartConsumerSpan` inside handlers and `amqptracing.StartPublisherSpan` around `publisher.Publish`.
