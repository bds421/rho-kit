// Package kafkabackend adapts Apache Kafka (via segmentio/kafka-go) to
// the kit's [messaging.Publisher] and [messaging.Consumer] interfaces.
//
// # Conceptual mapping
//
// Kafka is a partitioned, append-only log. The kit's messaging contract
// is RabbitMQ-shaped (exchange/routing-key, retry/dead-letter
// topology, explicit ack). This backend bridges the two as follows:
//
//   - exchange → Kafka topic. The publisher writes to a single Writer
//     bound to the configured brokers, so callers can publish to any
//     topic dynamically.
//   - routingKey → carried as the Kafka record key when non-empty. The
//     key drives partition assignment under kafka-go's default hash
//     balancer, so messages with the same routing key land on the
//     same partition (ordered relative to each other). It is also
//     mirrored into the X-Routing-Key record header for consumers that
//     prefer header-based dispatch.
//   - messaging.Message → JSON-encoded into the Kafka record Value.
//     Headers ride as Kafka record headers (kafka.Header{Key, Value}).
//   - consumer group → fixed at [NewSubscriber] construction time. The
//     resulting Subscriber satisfies [messaging.Consumer] for any
//     [messaging.Binding] whose Exchange names a topic the group is
//     subscribed to.
//   - Binding.Queue, when non-empty, must match the wrapped subscriber's
//     consumer-group name (mirrors the redisbackend [FR-064] guard) so a
//     service binding multiple "queues" to one Subscriber surfaces the
//     configuration drift at startup rather than silently routing every
//     delivery through the constructor-time group.
//   - Binding.Retry / Binding.WithoutRetry → ignored by this backend.
//     Kafka has no per-message redelivery primitive analogous to AMQP
//     dead-letter exchanges; retries are the application's job. A
//     handler that returns an error causes the subscriber to NOT commit
//     the offset, so the message is redelivered after the consumer
//     re-fetches from the last committed offset (typically on group
//     rebalance or restart). For application-level retry, wrap the
//     handler in [resilience/retry] or implement a dead-letter topic
//     pattern at the producer level.
//   - Ack semantics → returning nil from the handler causes the
//     subscriber to call kafka-go's [Reader.CommitMessages], advancing
//     the committed offset for the partition. Returning a non-nil
//     error skips the commit, leaving the offset at its previous
//     position; the message will be re-delivered on the next fetch
//     after a group rebalance or restart. Permanent errors
//     ([apperror.IsPermanent]) are *committed* (poison-pill discard) to
//     prevent a single bad record from blocking the partition forever —
//     matching the AMQP/NATS poison-pill handling.
//   - Offset reset → controlled by [SubscriberConfig.StartOffset]
//     (kafka-go's StartOffset). Defaults to [kafka.FirstOffset] so a
//     new group reads from the beginning of each partition; set to
//     [kafka.LastOffset] to skip backlog.
//   - Consumer-group rebalance → handled internally by kafka-go.
//     During a rebalance the Reader's [Reader.FetchMessage] returns
//     either the next message or a transient error; the subscriber
//     loops back into FetchMessage so rebalance is transparent to
//     handlers.
//
// # When to use this backend
//
//   - You need a partitioned log with operator-managed retention,
//     horizontal-scale fan-out across many consumers in a group, and
//     replay from a known offset.
//   - You need ordered per-key delivery across many parallel
//     consumers. Kafka's partition model gives that for free; AMQP
//     requires a topology trick.
//   - You already operate a Kafka cluster and don't want to introduce
//     RabbitMQ or NATS just for the kit's messaging interface.
//
// Use [amqpbackend] or [natsbackend] for AMQP-style retry topology,
// or [redisbackend] for a lightweight log-style alternative without
// the operational overhead of Kafka.
//
// # Interface concessions
//
// Kafka's consumer-group model does not natively express AMQP's
// queue-per-binding semantics. This backend handles that by pinning
// one Subscriber to one (brokers, groupID, topics) triple and
// validating Binding.Queue against the configured group at Consume
// time. Callers that need different consumer groups per binding must
// construct one Subscriber per group.
//
// Retry / dead-letter behaviour is intentionally NOT mapped from
// [messaging.RetryPolicy]. Kafka exposes no per-message TTL or
// delayed-redelivery primitive; a faithful implementation would need
// a separate broker-side retry topic + scheduler. The backend logs a
// warning when a Binding declares non-nil Retry and instructs callers
// to either set Binding.WithoutRetry=true or wrap their handler in
// the kit's [resilience/retry] package.
package kafkabackend
