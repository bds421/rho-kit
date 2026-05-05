-- +goose Up
ALTER TABLE idempotency_keys
    ADD COLUMN IF NOT EXISTS owner_token VARCHAR(64),
    ADD COLUMN IF NOT EXISTS fingerprint BYTEA;

-- +goose Down
ALTER TABLE idempotency_keys
    DROP COLUMN IF EXISTS owner_token,
    DROP COLUMN IF EXISTS fingerprint;
