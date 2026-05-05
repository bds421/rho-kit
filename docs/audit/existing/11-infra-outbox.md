# infra/outbox — outbox pattern + GORM store

## Landed

- ✅ **`next_retry_at` column + exponential backoff + failed-row retention** — schema migration `20260505000001_outbox_next_retry_at.sql`, `Store.IncrementAttempts(..., nextRetryAt)`, `Store.DeleteFailedBefore`, `relay.retryBackoff(attempt)` with 2s base / 5min cap / 30d default failed retention; cleanup tick now invokes both `DeletePublishedBefore` and `DeleteFailedBefore` (commit `4b522b3`).

## Open

### [HIGH] `outbox.Writer.Write` doesn't require an ambient transaction
**File**: `infra/outbox/outbox.go:94-126` + `gormstore.go:97-104`
**Issue**: Happily inserts using the root DB connection when no GORM tx is in ctx. Silently breaks the outbox atomicity guarantee — the entire point of the pattern. Callers who forget `db.Transaction(...)` get a "working" call that publishes events even when the domain write rolled back.
**Fix**: Add `WithRequireTransaction()` Writer option that returns an error when `gormdb.DBFromContext` falls back to `s.db`. Default to strict for new constructions; document escape hatch.
**Effort**: S
**Phase**: 2

### [MEDIUM] `Relay.poll` processes entries serially → head-of-line blocking
**File**: `infra/outbox/relay.go:198-203`
**Issue**: With `batchSize=100` and 100ms-per-publish, one cycle takes 10s. New entries inserted during that time wait full duration.
**Fix**: Add `WithMaxConcurrentPublishes(n)`; dispatch entries into a worker pool; collect results and call `MarkPublished` per entry as they finish.

### [MEDIUM] gormstore SQLite `FOR UPDATE SKIP LOCKED` is no-op → multi-relay double-publish
**File**: `infra/outbox/gormstore/gormstore.go:113-148`
**Issue**: SQLite ignores SKIP LOCKED. Two relay instances against same SQLite file each fetch full pending set → double-publish.
**Fix**: Reject SQLite at construction when concurrent relay is enabled, or detect dialect at startup and gate `FetchPending` with a process-local mutex on SQLite.

### Migration checklist

- [ ] Phase 2: `Writer.WithRequireTransaction()` (default on for new constructions).
- [ ] Phase 3: `WithMaxConcurrentPublishes` worker pool; SQLite multi-instance guard.
