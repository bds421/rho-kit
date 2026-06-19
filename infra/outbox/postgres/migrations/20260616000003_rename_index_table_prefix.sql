-- +goose Up
-- Align the remaining outbox index names with the kit convention
-- idx_<table>_<cols>. The table is outbox_entries, but the original
-- migration named these idx_outbox_* (dropping the _entries suffix),
-- which makes cross-table operator tooling (pg_indexes greps) less
-- uniform than the sibling tables (idx_action_log_entries_*,
-- idx_audit_log_events_*, idx_approval_requests_*).
--
-- Cosmetic-only: ALTER INDEX RENAME does not touch the index data, so
-- it is safe and near-instant on a live table. IF EXISTS keeps the
-- rename idempotent and tolerant of a DB that has not yet created the
-- old-named index. The pending-claim index was already renamed to
-- idx_outbox_entries_pending_due by the prior migration.
ALTER INDEX IF EXISTS idx_outbox_processing_updated
    RENAME TO idx_outbox_entries_processing_updated;
ALTER INDEX IF EXISTS idx_outbox_published_at
    RENAME TO idx_outbox_entries_published_at;
ALTER INDEX IF EXISTS idx_outbox_failed_updated
    RENAME TO idx_outbox_entries_failed_updated;
ALTER INDEX IF EXISTS idx_outbox_processing_claim_token
    RENAME TO idx_outbox_entries_processing_claim_token;

-- +goose Down
ALTER INDEX IF EXISTS idx_outbox_entries_processing_updated
    RENAME TO idx_outbox_processing_updated;
ALTER INDEX IF EXISTS idx_outbox_entries_published_at
    RENAME TO idx_outbox_published_at;
ALTER INDEX IF EXISTS idx_outbox_entries_failed_updated
    RENAME TO idx_outbox_failed_updated;
ALTER INDEX IF EXISTS idx_outbox_entries_processing_claim_token
    RENAME TO idx_outbox_processing_claim_token;
