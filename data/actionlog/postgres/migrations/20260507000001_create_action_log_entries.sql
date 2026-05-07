-- +goose Up
CREATE TABLE IF NOT EXISTS action_log_entries (
    id                VARCHAR(36) PRIMARY KEY,
    tenant_id         VARCHAR(255) NOT NULL,
    actor             VARCHAR(255) NOT NULL,
    action            VARCHAR(255) NOT NULL,
    resource          VARCHAR(500) NOT NULL DEFAULT '',
    outcome           VARCHAR(20)  NOT NULL,
    reason            TEXT NOT NULL DEFAULT '',
    metadata          JSONB,
    -- TIMESTAMPTZ (not TIMESTAMP) so the round-trip preserves UTC
    -- regardless of the database session timezone. The HMAC signing
    -- input formats OccurredAt as RFC3339Nano UTC, so a session-local
    -- TIMESTAMP would cause every signature verification to fail after
    -- a round trip on drivers that interpret the column literally.
    occurred_at       TIMESTAMPTZ NOT NULL,
    signature_key_id  VARCHAR(64) NOT NULL,
    signature         VARCHAR(128) NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_action_log_entries_tenant_occurred
    ON action_log_entries (tenant_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_action_log_entries_actor
    ON action_log_entries (actor);
CREATE INDEX IF NOT EXISTS idx_action_log_entries_action
    ON action_log_entries (action);

-- +goose Down
DROP TABLE IF EXISTS action_log_entries;
