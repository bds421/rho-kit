-- +goose Up
-- Zero is the explicit legacy/unversioned value. New producers should set a
-- positive version and consumers can then select the matching schema.
ALTER TABLE outbox_entries
    ADD COLUMN IF NOT EXISTS schema_version BIGINT NOT NULL DEFAULT 0
    CHECK (schema_version >= 0);

-- +goose Down
ALTER TABLE outbox_entries
    DROP COLUMN IF EXISTS schema_version;
