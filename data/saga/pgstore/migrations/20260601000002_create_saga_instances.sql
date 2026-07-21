-- +goose Up
CREATE TABLE IF NOT EXISTS saga_instances (
    id            VARCHAR(64)  PRIMARY KEY,
    definition    VARCHAR(128) NOT NULL,
    state         VARCHAR(32)  NOT NULL,
    current_step  INT          NOT NULL DEFAULT 0,
    compensated   JSONB        NOT NULL DEFAULT '[]'::jsonb,
    input         BYTEA,
    step_results  JSONB        NOT NULL DEFAULT '[]'::jsonb,
    last_error    TEXT         NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_saga_instances_resumable
    ON saga_instances (state, updated_at)
    WHERE state IN ('pending', 'running', 'compensating');

-- +goose Down
DROP TABLE IF EXISTS saga_instances;
