-- +goose Up
-- FetchPending claims status='pending' rows that are due
-- (next_retry_at IS NULL OR next_retry_at <= NOW()) oldest-first by
-- (created_at, id). The original idx_outbox_pending_ready was on
-- (created_at) WHERE status='pending' only, so deferred-by-backoff rows
-- (next_retry_at in the future) were still walked in created_at order
-- and discarded one-by-one via a heap recheck — O(deferred) work per
-- claim under a retry storm.
--
-- This composite partial index keeps (created_at, id) as the leading
-- columns so the ORDER BY / LIMIT is satisfied straight from the index
-- (no sort node), and carries next_retry_at as a trailing column so the
-- retry-eligibility predicate is evaluated on the index tuple itself —
-- deferred rows are filtered without a heap fetch. Leading on
-- created_at (rather than next_retry_at) is deliberate: it preserves the
-- FIFO ordering the claim path depends on, and it sidesteps the
-- NULLS-LAST trap a next_retry_at-leading range scan would hit for the
-- "eligible immediately" (NULL) rows.
--
-- Additive and safe on a live, already-migrated table: CREATE INDEX
-- IF NOT EXISTS is a no-op if re-run, and CONCURRENTLY is intentionally
-- NOT used so this stays inside the migrator's transaction like every
-- sibling migration. The narrower idx_outbox_pending_ready is dropped
-- because this index is a strict superset for the claim path (same
-- leading column, same partial predicate), so keeping both would only
-- add write amplification.
CREATE INDEX IF NOT EXISTS idx_outbox_entries_pending_due
    ON outbox_entries (created_at, id, next_retry_at)
    WHERE status = 'pending';

DROP INDEX IF EXISTS idx_outbox_pending_ready;

-- +goose Down
CREATE INDEX IF NOT EXISTS idx_outbox_pending_ready
    ON outbox_entries (created_at)
    WHERE status = 'pending';

DROP INDEX IF EXISTS idx_outbox_entries_pending_due;
