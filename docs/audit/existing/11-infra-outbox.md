# infra/outbox — outbox pattern + GORM store

## Landed

- ✅ **`next_retry_at` column + exponential backoff + failed-row retention** — schema migration `20260505000001_outbox_next_retry_at.sql`, `Store.IncrementAttempts(..., nextRetryAt)`, `Store.DeleteFailedBefore`, `relay.retryBackoff(attempt)` with 2s base / 5min cap / 30d default failed retention; cleanup tick now invokes both `DeletePublishedBefore` and `DeleteFailedBefore` (commit `4b522b3`).
- ✅ **`Writer.WithRequireTransaction(check)`** — pluggable predicate that runs at the top of `Write`; rejects entries written outside an ambient transaction. Decoupled from any specific tx backend so callers can wire `gormdb.TxFromContext` (or `pgx`, raw `database/sql`) without making outbox depend on a SQL module (commit `5cfa5c9`). Disabled by default — turning it on for every existing caller would be a behaviour break; new services should adopt it.

## Open

_Closed — see Recently Landed below._

## Recently Landed (Phase 3, commit `039061e`)

- ✅ **`WithMaxConcurrentPublishes(n)`** — bounded-semaphore worker pool dispatches entries concurrently while preserving the per-entry `MarkPublished`/`IncrementAttempts` semantics. Default remains `n=1` (serial) for backwards compatibility.
- ✅ **SQLite multi-relay process-local guard** — `gormstore` detects the dialect at construction (`db.Dialector.Name() == "sqlite"`); `FetchPending` is gated by `sqliteMu sync.Mutex` so two relays in one process are safe (the cross-process case still requires Postgres or MySQL since SQLite ignores SKIP LOCKED — documented in `WithLogger`).

The HIGH-fix from the post-merge code review (relay.go ctx-aware sem send) is captured in commit `4d04fe1`.

### Migration checklist

- [x] Phase 2: `Writer.WithRequireTransaction()`. ✅ `5cfa5c9` (default OFF; new services should opt in)
- [x] Phase 3: `WithMaxConcurrentPublishes` worker pool. ✅ `039061e`
- [x] Phase 3: SQLite multi-instance guard. ✅ `039061e`
