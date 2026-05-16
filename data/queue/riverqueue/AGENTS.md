# AGENTS.md — `data/queue/riverqueue`

## When to use this package

- Postgres is the primary datastore and you want background jobs in the same database (single source of truth for state).
- Need transactional enqueue: `Insert` participates in the calling DB transaction so "write order + enqueue notify" is atomic.
- River-flavored ergonomics (typed args, workers with `Work(ctx, job)` signature).

## When to use something else

- **Redis is the path of least resistance:** `data/queue/redisqueue` (asynq).
- **Distributed workflow (cross-process compensation, long-running):** Temporal.

## Key APIs

- `NewPublisher(client, opts...)` — wraps `*river.Client[pgx.Tx]`.
- `Enqueue(ctx, queue, msg kitqueue.Message)` — adapts to river's `Insert`. The `Message.ID`, if non-empty, drives river's `UniqueOpts{ByArgs, ByQueue}` for deduplication.
- `WithoutUniqueByID()` — opt out of FR-059 dedup if you genuinely want every Enqueue to deliver.

## Common mistakes

- **Re-using `Message.ID` across semantically-different jobs** — same ID + same args + same queue dedupes. If two jobs are functionally different but happen to share an ID, one is silently dropped.
- **Enqueuing outside a transaction when atomicity matters** — the river Insert is transactional iff you pass the transaction. Make sure your wiring threads the transaction.
- **Asserting on inner validation text in tests** — error text is redacted via `redact.WrapError`. Use `errors.Is(err, kitqueue.ErrInvalidName)` etc.

## Observability

- River exposes its own metrics; the kit doesn't add wrappers.
