-- +goose Up
-- JSONB normalizes formatting and numbers, so new rows keep the exact signed
-- metadata bytes (empty when there is none). NULL marks rows written before
-- this migration, which are verified with the legacy encoding.
ALTER TABLE audit_log_events
    ADD COLUMN IF NOT EXISTS signed_metadata BYTEA;

-- +goose Down
ALTER TABLE audit_log_events DROP COLUMN IF EXISTS signed_metadata;
