-- +goose Up
CREATE TABLE IF NOT EXISTS cron_schedules (
    name        VARCHAR(128) PRIMARY KEY,
    spec        VARCHAR(128) NOT NULL,
    enabled     BOOLEAN      NOT NULL DEFAULT TRUE,
    description TEXT,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS cron_schedules;
