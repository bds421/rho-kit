# AGENTS.md — `data/queue/redisqueue`

## When to use this package

- Simple background-job queue backed by Redis (uses `hibiken/asynq` internally).
- Workload is "fire and (eventually) forget" — async tasks with retry + at-least-once delivery.
- Operational footprint matters: Redis only, no separate Postgres queue table.

## When to use something else

- **Postgres-backed queue with stronger durability + transactional enqueue:** `data/queue/riverqueue` — Insert is transactional with the calling DB transaction.
- **Long-running workflow with compensation:** `runtime/saga` (in-process) or Temporal (multi-process).
- **Stream-style fan-out (multiple consumer groups reading independently):** `data/stream/redisstream` instead.

## Key APIs

- `NewQueue(client goredis.UniversalClient, opts ...Option) *Queue` — `client` is a go-redis client; the asynq client is created internally from it.
- `(*Queue).Enqueue(ctx, queue string, msg Message) error` — `queue` is the asynq queue name; subject to validation (no control bytes, reasonable length).

## Common mistakes

- **Cross-process transactional enqueue** — Redis enqueue commits independently of your DB write. If atomicity matters use `riverqueue` or wrap with `outbox`.
- **Hot-loop enqueue without batching** — every Enqueue is a Redis round-trip. Batch where the workload permits.

## Observability

- Metrics emitted by asynq directly; the kit doesn't add wrappers.
