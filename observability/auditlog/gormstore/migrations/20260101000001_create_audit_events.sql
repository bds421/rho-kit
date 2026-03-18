-- +goose Up
CREATE TABLE IF NOT EXISTS audit_events (
    id         VARCHAR(36) PRIMARY KEY,
    timestamp  TIMESTAMP NOT NULL,
    actor      VARCHAR(255) NOT NULL,
    action     VARCHAR(100) NOT NULL,
    resource   VARCHAR(500) NOT NULL,
    status     VARCHAR(50) NOT NULL,
    metadata   JSONB,
    trace_id   VARCHAR(64),
    ip_address VARCHAR(45)
);

CREATE INDEX IF NOT EXISTS idx_audit_events_timestamp ON audit_events (timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_events_actor ON audit_events (actor);
CREATE INDEX IF NOT EXISTS idx_audit_events_action ON audit_events (action);
CREATE INDEX IF NOT EXISTS idx_audit_events_resource ON audit_events (resource);

-- +goose Down
DROP TABLE IF EXISTS audit_events;
