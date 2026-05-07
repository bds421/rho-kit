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
    occurred_at       TIMESTAMP NOT NULL,
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
