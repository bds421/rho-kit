// Package redisbackend adapts redis/stream to the messaging interfaces.
//
// It provides a Publisher and Consumer that satisfy messaging.MessagePublisher
// and messaging.MessageConsumer by wrapping redis/stream.Producer and
// redis/stream.Consumer respectively.
//
// Conceptual mapping:
//
//   - exchange → stream name
//   - routing key → stored in message headers (unused by Redis Streams directly)
//   - consumer group → Binding.Queue
//   - Binding.Retry → StreamConsumer max retries
package redisbackend
