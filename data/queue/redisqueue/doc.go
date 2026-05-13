// Package redisqueue provides a Redis LIST-based FIFO queue.
//
// Prometheus metrics are exposed under the `redis_queue_` prefix and
// share a single `queue` label. Default collectors include:
//
//   - `redis_queue_queue_depth{queue}` — pending messages in the main list.
//   - `redis_queue_processing_depth{queue}` — in-flight messages claimed by this consumer.
//   - `redis_queue_dlq_depth{queue}` — entries in the dead-letter list, polled on the same cadence as queue_depth.
//
// The DLQ gauge is updated by the same depth poller as queue_depth — no
// extra goroutine is started. A growing dlq_depth without an
// operator-driven drain is a strong signal of a poison message in the
// pipeline.
package redisqueue
