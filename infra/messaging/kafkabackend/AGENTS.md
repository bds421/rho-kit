# AGENTS.md — `infra/messaging/kafkabackend`

## When to use this package

- Kafka is the existing message bus.
- Partition-ordered processing is required (per-key ordering matters).
- Replay from offset is a desired operational capability.

## When to use something else

- **AMQP:** `amqpbackend` — broker-side retry / DLX semantics.
- **NATS JetStream:** `natsbackend` — simpler operational story for "fan-out subjects".
- **Per-message retry needed in-handler:** Kafka cannot do broker-side retry. `messaging.ErrRetryUnsupported` will fire from `Consume` if a `BindingSpec` has a non-nil `Retry` without `WithoutRetry: true`. Either set `WithoutRetry: true` (ack-and-discard on first error) OR wrap the handler in `resilience/retry`.
- **At-least-once redelivery on graceful shutdown is unacceptable:** Kafka's commit-on-cancelled-ctx may surface a message twice. Handlers MUST be idempotent.

## Key APIs

- `NewPublisher(brokers []string, opts...) (*Publisher, error)` / `NewSubscriber(brokers []string, groupID string, topics []string, opts...) (*Subscriber, error)` — backend implementations of `messaging.Publisher` / `messaging.Consumer`.
- SASL is configured via `Config` fields (`SASLMechanism`, `SASLUsername`, `SASLPassword`) passed through `NewPublisherWithConfig` / `NewSubscriberWithConfig` — there is no `WithSASL` option. Supported mechanisms: `PLAIN`, `SCRAM-SHA-256`, `SCRAM-SHA-512`. **OAUTHBEARER is not supported.**

## Common mistakes

- **Non-idempotent handlers** — Kafka commit semantics make at-least-once redelivery a normal outcome. If reprocessing a message twice would be wrong, the handler is wrong, not Kafka.
- **`Retry` policy without `WithoutRetry: true`** — see above. Will be rejected at `Consume` entry with `messaging.ErrRetryUnsupported`.
- **Per-tenant consumer groups (high cardinality)** — group becomes a Prometheus label. The wave-140 default projects through `promutil.OpaqueLabelValue`, but per-tenant groups are usually a deployment-design smell anyway.
- **Wiring this directly when an outbox would be safer** — see `outbox.MessagingPublisher`.

## Observability

- Metrics: `kafka_published_total`, `kafka_publish_duration_seconds`, `kafka_consumed_total`, `kafka_handler_duration_seconds`. Labels `topic`, `routing_key`, `outcome` (publish) / `topic`, `group`, `outcome` (consume) — opaque since wave 140.
- OTel: `kafkabackend/kafkatracing` — call `kafkatracing.StartPublisherSpan` / `StartConsumerSpan` for W3C trace context propagation through Kafka headers.
