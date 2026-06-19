# data/cron/pgstore

## Purpose

Persist cron schedules to Postgres so a service restart does NOT lose
schedule state, and so an operator can add / remove / enable jobs
without redeploying the binary.

## Public API

- `New(db *sql.DB, opts ...Option) *Store`
- `Store.Add(ctx, ScheduleRecord) error`             — insert, error on duplicate
- `Store.Upsert(ctx, ScheduleRecord) error`          — insert-or-update
- `Store.Remove(ctx, name) error`                    — idempotent delete
- `Store.Enable(ctx, name, enabled bool) error`      — flip flag
- `Store.Get(ctx, name) (ScheduleRecord, error)`     — ErrScheduleNotFound if absent
- `Store.List(ctx) ([]ScheduleRecord, error)`        — ASCII-sorted
- `Store.ApplyTo(ctx, *cron.Scheduler, map[string]JobFunc) ([]string, error)`
  — registers every enabled record whose name appears in the jobs map;
    returns names of stored-but-unknown schedules so the caller can warn
- `WithTableName(string)` option

## What this package does NOT persist

- **The job function.** Functions don't serialize. Callers declare a
  `map[name]JobFunc` in code; the store records (name, schedule,
  enabled) tuples and `ApplyTo` intersects them.

## Schema

`migrations/20260601000001_create_cron_schedules.sql`:

```sql
CREATE TABLE cron_schedules (
    name        VARCHAR(128) PRIMARY KEY,
    spec        VARCHAR(128) NOT NULL,
    enabled     BOOLEAN      NOT NULL DEFAULT TRUE,
    description TEXT,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);
```

## Validation

- Name: lowercase letters, digits, hyphens, underscores; must start
  with a letter; max 128 chars. Same alphabet as Prometheus label
  values to keep cron job metric cardinality predictable.
- Spec: non-empty, max 128 chars. The runtime/cron Scheduler does
  cron-syntax validation when ApplyTo registers the job (panics on
  invalid spec).
- Table name (WithTableName): alphanumeric + underscore, optional
  schema prefix.

## Tests

Unit tier (`go test -race ./...`): validators, panic guards, regex
shape. SQL-roundtrip tests (Add/Upsert/Remove/Enable/Get/List/ApplyTo)
belong under `//go:build integration` with `infra/sqldb/dbtest` so
the default unit run stays Docker-free.

## See also

- `runtime/cron` — the in-memory Scheduler this store wires.
- `infra/leaderelection/*` — pair `cron.WithLeaderGate(elector.IsLeader)`
  with this store for replicated services that must run jobs on only
  one replica at a time.
