-- +goose Up
CREATE TABLE IF NOT EXISTS idempotency_keys (
    key           VARCHAR(512) PRIMARY KEY,
    status_code   INT,
    headers       JSONB,
    response_body BYTEA,
    expires_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_idempotency_keys_expires_at ON idempotency_keys (expires_at);

-- +goose Down
DROP TABLE IF EXISTS idempotency_keys;
