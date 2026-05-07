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
    -- TIMESTAMPTZ (not TIMESTAMP) so the round-trip preserves UTC
    -- regardless of the database session timezone. State-machine
    -- comparisons (expires_at > now()) and audit forensics rely on a
    -- single, unambiguous timezone.
    decided_at  TIMESTAMPTZ NULL,
    reason      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_approval_requests_tenant_state
    ON approval_requests (tenant_id, state);
CREATE INDEX IF NOT EXISTS idx_approval_requests_state_expires
    ON approval_requests (state, expires_at);
CREATE INDEX IF NOT EXISTS idx_approval_requests_actor
    ON approval_requests (actor);

-- +goose Down
DROP TABLE IF EXISTS approval_requests;
