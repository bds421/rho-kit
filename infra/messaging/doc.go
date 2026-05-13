// Package messaging provides transport-agnostic interfaces for message
// publishing and consuming. Backend implementations live in sub-packages:
//
//   - amqpbackend — RabbitMQ (AMQP 0-9-1) publisher, consumer, and topology
//   - natsbackend — NATS JetStream publisher and consumer
//   - redisbackend — Redis Streams publisher and consumer
//   - membroker — in-memory broker for unit tests
//
// The root package defines [Publisher], [Consumer], [Handler], [Message],
// [Delivery], [Binding], [Connector], and [BufferedPublisher] — the types
// that application code depends on. Backend selection happens at wiring
// time (app.Builder or manual setup) and is invisible to handlers.
//
// Message metadata is a shared transport contract. Use [NewMessage] plus
// [Message.WithHeader], or call [ValidateMessage] for manually-constructed
// messages, so IDs, types, payloads, and headers stay portable across AMQP,
// NATS, Redis, and the in-memory broker.
//
// Observability for [BufferedPublisher] is opt-in via
// [NewPrometheusMetrics] (or [WithPrometheusMetrics]). The default
// collectors are namespaced under `buffered_publisher_` and labelled by
// publisher name:
//
//   - `buffered_publisher_dropped_total{publisher, reason}` — back-pressure drops.
//   - `buffered_publisher_state_writes_total{publisher, outcome}` — state-file writes.
//   - `buffered_publisher_pending{publisher}` — current buffer depth.
//   - `buffered_publisher_buffered_bytes{publisher}` — approximate bytes pending.
//
// State-file persistence (THREAT_MODEL §4.3 M-05) is configured with
// two cooperating options:
//
//   - [WithStateDirectory] sets the absolute directory the state file
//     must live in.
//   - [WithStateFile] names the file relative to that directory.
//
// The constructor rejects absolute paths, `..` traversal, and any
// resolved location outside the configured directory so a hostile or
// buggy STATE_FILE env value cannot write outside the operator-chosen
// area. Calling [WithStateFile] without [WithStateDirectory] panics.
package messaging
