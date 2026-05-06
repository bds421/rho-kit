# infra/outbox — outbox pattern + GORM store

## Landed

- ✅ **`next_retry_at` column + exponential backoff + failed-row retention** — schema migration `20260505000001_outbox_next_retry_at.sql`, `Store.IncrementAttempts(..., nextRetryAt)`, `Store.DeleteFailedBefore`, `relay.retryBackoff(attempt)` with 2s base / 5min cap / 30d default failed retention; cleanup tick now invokes both `DeletePublishedBefore` and `DeleteFailedBefore` (commit `4b522b3`).
- ✅ **`Writer.WithRequireTransaction(check)`** — pluggable predicate that runs at the top of `Write`; rejects entries written outside an ambient transaction. Decoupled from any specific tx backend so callers can wire `gormdb.TxFromContext` (or `pgx`, raw `database/sql`) without making outbox depend on a SQL module (commit `5cfa5c9`). Disabled by default — turning it on for every existing caller would be a behaviour break; new services should adopt it.

## Open

### [MEDIUM] `Relay.poll` processes entries serially → head-of-line blocking
**File**: `infra/outbox/relay.go:198-203`
**Issue**: With `batchSize=100` and 100ms-per-publish, one cycle takes 10s. New entries inserted during that time wait full duration.
**Fix**: Add `WithMaxConcurrentPublishes(n)`; dispatch entries into a worker pool; collect results and call `MarkPublished` per entry as they finish.

### [MEDIUM] gormstore SQLite `FOR UPDATE SKIP LOCKED` is no-op → multi-relay double-publish
**File**: `infra/outbox/gormstore/gormstore.go:113-148`
**Issue**: SQLite ignores SKIP LOCKED. Two relay instances against same SQLite file each fetch full pending set → double-publish.
**Fix**: Reject SQLite at construction when concurrent relay is enabled, or detect dialect at startup and gate `FetchPending` with a process-local mutex on SQLite.

### Migration checklist

- [x] Phase 2: `Writer.WithRequireTransaction()`. ✅ `5cfa5c9` (default OFF; new services should opt in)
- [ ] Phase 3: `WithMaxConcurrentPublishes` worker pool; SQLite multi-instance guard.
