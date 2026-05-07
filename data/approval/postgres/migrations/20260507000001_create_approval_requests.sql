-- +goose Up
CREATE TABLE IF NOT EXISTS approval_requests (
    id          VARCHAR(36) PRIMARY KEY,
    tenant_id   VARCHAR(255) NOT NULL,
    actor       VARCHAR(255) NOT NULL,
    action      VARCHAR(255) NOT NULL,
    resource    VARCHAR(500) NOT NULL DEFAULT '',
    payload     JSONB,
    state       VARCHAR(20)  NOT NULL,
    decided_by  VARCHAR(255) NOT NULL DEFAULT '',
    decided_at  TIMESTAMP NULL,
    reason      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL,
    expires_at  TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_approval_requests_tenant_state
    ON approval_requests (tenant_id, state);
CREATE INDEX IF NOT EXISTS idx_approval_requests_state_expires
    ON approval_requests (state, expires_at);
CREATE INDEX IF NOT EXISTS idx_approval_requests_actor
    ON approval_requests (actor);

-- +goose Down
DROP TABLE IF EXISTS approval_requests;
