// Package redisbackend adapts redis/stream to the messaging interfaces.
//
// It provides a Publisher and Consumer that satisfy messaging.Publisher
// and messaging.Consumer by wrapping redis/stream.Producer and
// redis/stream.Consumer respectively.
//
// Conceptual mapping:
//
//   - exchange → Redis stream name
//   - routing key → stored in message headers (unused by Redis Streams directly)
//   - consumer group → fixed at *stream.Consumer construction time; the
//     wrapper validates Binding.Queue against that group and rejects
//     mismatches (FR-064). It does NOT switch groups per binding.
//   - Binding.Retry / Binding.WithoutRetry → ignored by this backend.
//     Retry and dead-letter behaviour is configured on the underlying
//     [redisstream.Consumer] at construction time (max retries, backoff,
//     dead-letter stream) and applies uniformly to every Consume invocation
//     on this wrapper. Construct one wrapper per (stream, group, retry
//     policy) tuple if you need divergent retry behaviour.
package redisbackend
