-- +goose Up
-- Add next_retry_at to outbox_entries so the relay can apply exponential
-- backoff between retry attempts. Without this column, IncrementAttempts
-- resets a row to status='pending' immediately and the 1-second poll loop
-- exhausts the maxAttempts budget in seconds against a flapping downstream.
--
-- The relay's FetchPending now skips rows whose next_retry_at is still in
-- the future. NULL means "eligible immediately" so legacy rows from before
-- this migration continue to be picked up.
ALTER TABLE outbox_entries ADD COLUMN next_retry_at TIMESTAMP NULL;

-- New composite index optimised for the FetchPending query: we want pending
-- rows whose next_retry_at is null OR <= now, ordered by created_at. The
-- existing idx_outbox_entries_status_created stays for the count/cleanup
-- paths; this one accelerates the hot polling query.
CREATE INDEX idx_outbox_entries_pending_next_retry
    ON outbox_entries (status, next_retry_at, created_at);

-- +goose Down
DROP INDEX IF EXISTS idx_outbox_entries_pending_next_retry;
ALTER TABLE outbox_entries DROP COLUMN next_retry_at;
