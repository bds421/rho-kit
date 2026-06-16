# AGENTS.md — `infra/messaging/redisbackend`

## When to use this package

- Redis is already in the deployment and a dedicated broker would be overkill.
- Workload tolerates Redis Streams' single-broker model (no replication-based HA for messages themselves).
- Throughput and operational footprint are the prioritized axes.

## When to use something else

- **High throughput / partition-ordered:** `kafkabackend`.
- **Traditional pub/sub with rich topology:** `amqpbackend`.
- **Subject-based JetStream semantics:** `natsbackend`.

## Key APIs

- `NewPublisher(producer *stream.Producer, opts...)` / `NewConsumer(consumer *stream.Consumer, logger)` — backend implementations of `messaging.Publisher` / `messaging.Consumer`. They wrap an already-constructed `redisstream` producer/consumer (not a raw redis client); `NewConsumer` takes no options.
- Internally uses Redis Streams (XADD + XREADGROUP).

## Common mistakes

- **Treating Redis Streams as a replicated message bus** — Redis replication is async, and Streams persistence depends on RDB/AOF config. For "no message left behind" semantics, use Kafka or AMQP.
- **Per-tenant stream names** — same cardinality concerns as other backends. Opaque labels default since wave 140.

## Observability

- Metrics: `redis_stream_published_total` / `_publish_duration_seconds` / `_consumed_total` / `_handler_duration_seconds`. Same opaque-label discipline.
- OTel: `redisbackend/redistracing`.
