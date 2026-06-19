# data/saga/pgstore

## Purpose

Postgres-backed [saga.StateStore] for runtime/saga.DurableExecutor.
Persists per-step state with optimistic-concurrency safety so multiple
replicas can share the same instance pool without double-advancing.

## Public API

- `New(db *sql.DB, opts ...Option) *Store`
- `WithTableName(string) Option`
- `Store.Put / Get / ListResumable / Delete` — implements `saga.StateStore`
- `Store.DeleteTerminalBefore(ctx, cutoff)` — backend-specific retention
  sweep (not on `saga.StateStore`): prunes completed/failed instances
  older than `cutoff`. Run from a scheduled job; the executor never
  deletes terminal rows on its own, so without this the table grows
  unbounded. Mirrors `outbox.DeletePublishedBefore` /
  `idempotency.DeleteExpired`.
- `ErrConcurrentUpdate` — surfaced when Put fails the optimistic
  updated_at check

## Schema

`migrations/20260601000002_create_saga_instances.sql`:

```sql
CREATE TABLE saga_instances (
    id            VARCHAR(64)  PRIMARY KEY,
    definition    VARCHAR(128) NOT NULL,
    state         VARCHAR(32)  NOT NULL,
    current_step  INT          NOT NULL DEFAULT 0,
    compensated   JSONB        NOT NULL DEFAULT '[]'::jsonb,
    input         BYTEA,
    step_results  JSONB        NOT NULL DEFAULT '[]'::jsonb,
    last_error    TEXT         NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_saga_instances_resumable
    ON saga_instances (state, updated_at)
    WHERE state IN ('pending', 'running', 'compensating');
```

`migrations/20260601000003_terminal_index.sql` adds the partial index
backing `DeleteTerminalBefore`:

```sql
CREATE INDEX idx_saga_instances_terminal
    ON saga_instances (state, updated_at)
    WHERE state IN ('completed', 'failed');
```

## Retention

Terminal (completed / failed) instances are never deleted by the
executor, so wire `DeleteTerminalBefore(ctx, cutoff)` into a periodic
job (the same way idempotency wires `DeleteExpired`). The
`idx_saga_instances_terminal` partial index keeps the sweep
proportional to the rows being removed. Without a scheduled prune the
table grows for the life of the deployment.

## Concurrency

Put uses an INSERT … ON CONFLICT DO UPDATE … WHERE updated_at = $9
optimistic-concurrency guard. When two replicas read the same instance
and both attempt Put, the second one's RowsAffected is 0 and returns
ErrConcurrentUpdate; the executor re-reads and re-decides.

## Tests

Unit tier: panic guards on nil-DB and unsafe table name.
SQL-roundtrip tests belong under `//go:build integration` with
infra/sqldb/dbtest (same pattern as data/cron/pgstore).

## See also

- `runtime/saga` — the executor that uses this StateStore.
- `runtime/saga.NewMemoryStateStore` — alternative backend for tests
  and single-process services.
