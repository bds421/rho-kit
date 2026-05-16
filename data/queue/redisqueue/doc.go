// Package redisqueue is the kit's Redis-backed FIFO job queue. In v2 the
// implementation is a thin wrapper around [hibiken/asynq] — the kit owns
// the public `Queue` seam (Enqueue/EnqueueBatch/Len/Process/DepthCheck)
// plus the audit/metric/redact/lifecycle integration, while asynq's
// `Client`/`Server`/`Inspector` provide the storage, claim model, and
// invisibility-timeout-based recovery.
//
// Wire envelope: every enqueue creates an [asynq.Task] of type
// `rho.envelope` whose JSON payload is the kit's [Message]. Routing is by
// asynq's per-queue priority map — every Process call binds exactly one
// queue name to its asynq.Server.
//
// Prometheus metrics are exposed under the `redis_queue_` prefix and
// share a single `queue` label. Default collectors include:
//
//   - `redis_queue_queue_depth{queue}` — pending messages (asynq's
//     "Pending" state).
//   - `redis_queue_processing_depth{queue}` — in-flight messages claimed
//     by active workers (asynq's "Active" state).
//   - `redis_queue_dlq_depth{queue}` — entries in the asynq archive
//     (dead-letter), polled on the same cadence as queue_depth.
//
// All three gauges are updated by the same depth poller (running on the
// `WithHealthCheckInterval` cadence; default 30s). A growing dlq_depth
// without an operator-driven drain is a strong signal of a poison
// message in the pipeline.
//
// Migration from pre-v2: see docs/release/MIGRATION_V2.md. In-flight
// tasks from the pre-v2 LIST/heartbeat scheme are NOT readable by the v2
// asynq-backed queue — operators must drain or manually migrate.
package redisqueue
