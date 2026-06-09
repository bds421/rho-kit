-- +goose Up
CREATE TABLE IF NOT EXISTS api_keys (
    -- id is the public lookup identifier embedded in the token (UUID v7).
    -- It is the primary key, so verification is a single indexed read.
    id            VARCHAR(36) PRIMARY KEY,
    -- prefix is the safe-to-display token prefix (e.g. "rho_018f0a3c")
    -- used to identify a key in a dashboard without revealing the secret.
    prefix        VARCHAR(64) NOT NULL,
    -- hash is the 32-byte SHA-256 of the secret segment. The secret itself
    -- is never stored; only this digest is compared at verification time.
    hash          BYTEA NOT NULL,
    kind          VARCHAR(16) NOT NULL,
    scopes        JSONB NOT NULL DEFAULT '[]'::jsonb,
    owner         VARCHAR(255) NOT NULL,
    -- TIMESTAMPTZ so the round-trip preserves UTC regardless of the
    -- database session timezone. NULL expires_at means the key never
    -- expires; NULL revoked_at means the key is active.
    expires_at    TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ,
    -- rotated_from is the id of the key this one supersedes during a
    -- rotation overlap window; empty when the key was minted fresh.
    rotated_from  VARCHAR(36) NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_api_keys_owner ON api_keys (owner);

-- +goose Down
DROP TABLE IF EXISTS api_keys;
