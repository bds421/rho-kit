# infra/outbox — outbox pattern + GORM store

### [CRITICAL] Tight retry loop with no backoff
**File**: `infra/outbox/gormstore/gormstore.go:188-201`
**Issue**: After publish failure, `IncrementAttempts` flips status back to `pending` without `next_retry_at`. Relay polls every 1s; `FetchPending` filters only on `status='pending'` → persistent failure re-attempts every second. With `maxAttempts=10` the budget is exhausted in ~10s. Entries land in `failed` permanently. `cleanup` never deletes failed rows → unbounded table bloat.
**Fix**: Add `next_retry_at TIMESTAMP` column. Set it to `now() + backoff(attempts)` on increment. Filter `WHERE status='pending' AND (next_retry_at IS NULL OR next_retry_at <= now())`. Use exponential backoff (`2^attempt * baseDelay`, capped). Add `DeleteFailedBefore(retention)` — call from `cleanup`.
**Effort**: M
**Phase**: 1
**Migration**: Schema migration adds `next_retry_at`. Backfill existing rows to NULL. Existing relay versions still work (NULL filter).

### [HIGH] `outbox.Writer.Write` doesn't require an ambient transaction
**File**: `infra/outbox/outbox.go:94-126` + `gormstore.go:97-104`
**Issue**: Happily inserts using the root DB connection when no GORM tx is in ctx. Silently breaks the outbox atomicity guarantee — the entire point of the pattern. Callers who forget `db.Transaction(...)` get a "working" call that publishes events even when the domain write rolled back.
**Fix**: Add `WithRequireTransaction()` Writer option that returns an error when `gormdb.DBFromContext` falls back to `s.db`. Default to strict for new constructions; document escape hatch.
**Effort**: S
**Phase**: 2

### [HIGH] Cleanup never deletes `failed` entries (table bloat)
**File**: `infra/outbox/relay.go:291-308` + `gormstore.go:204-212`
**Issue**: `DeletePublishedBefore` only removes `status='published'`. Failed entries stay forever. Combined with the tight-retry bug, a flapping downstream produces unbounded failed rows; index `idx_outbox_entries_status_created` slows poll over time.
**Fix**: Add `DeleteFailedBefore` (longer retention) or generic `DeleteEntriesBefore(status, before)`. Have `cleanup` invoke both. Optionally archive failed rows.
**Effort**: S
**Phase**: 1 (lands with the tight-retry fix)

### [MEDIUM] `Relay.poll` processes entries serially → head-of-line blocking
**File**: `infra/outbox/relay.go:198-203`
**Issue**: With `batchSize=100` and 100ms-per-publish, one cycle takes 10s. New entries inserted during that time wait full duration.
**Fix**: Add `WithMaxConcurrentPublishes(n)`; dispatch entries into a worker pool; collect results and call `MarkPublished` per entry as they finish.

### [MEDIUM] gormstore SQLite `FOR UPDATE SKIP LOCKED` is no-op → multi-relay double-publish
**File**: `infra/outbox/gormstore/gormstore.go:113-148`
**Issue**: SQLite ignores SKIP LOCKED. Two relay instances against same SQLite file each fetch full pending set → double-publish.
**Fix**: Reject SQLite at construction when concurrent relay is enabled, or detect dialect at startup and gate `FetchPending` with a process-local mutex on SQLite.

### Migration checklist

- [ ] Phase 1: schema migration adds `next_retry_at`; gormstore exponential backoff; `DeleteFailedBefore` invoked from `cleanup`.
- [ ] Phase 2: `Writer.WithRequireTransaction()` (default on for new constructions).
- [ ] Phase 3: `WithMaxConcurrentPublishes` worker pool; SQLite multi-instance guard.
