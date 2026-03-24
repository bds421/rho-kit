// Package messaging provides transport-agnostic interfaces for message
// publishing and consuming. Backend implementations live in sub-packages:
//
//   - amqpbackend — RabbitMQ (AMQP 0-9-1) publisher, consumer, and topology
//   - redisbackend — Redis Streams publisher and consumer
//   - membroker — in-memory broker for unit tests
//
// The root package defines [MessagePublisher], [MessageConsumer], [Handler],
// [Message], [Delivery], [Binding], [Connector], and [BufferedPublisher] — the
// types that application code depends on. Backend selection happens at wiring
// time (app.Builder or manual setup) and is invisible to handlers.
package messaging
