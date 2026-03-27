-- +goose Up
CREATE TABLE IF NOT EXISTS outbox_entries (
    id             UUID        PRIMARY KEY,
    exchange       TEXT        NOT NULL,
    routing_key    TEXT        NOT NULL,
    message_id     TEXT        NOT NULL,
    message_type   TEXT        NOT NULL,
    payload        JSONB       NOT NULL,
    headers        JSONB,
    schema_version INT         NOT NULL DEFAULT 0,
    status         TEXT        NOT NULL DEFAULT 'pending',
    attempts       INT         NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at   TIMESTAMPTZ,
    last_error     TEXT
);

CREATE INDEX idx_outbox_entries_status_created ON outbox_entries (status, created_at)
    WHERE status = 'pending';

CREATE INDEX idx_outbox_entries_published_at ON outbox_entries (published_at)
    WHERE status = 'published';

-- +goose Down
DROP TABLE IF EXISTS outbox_entries;
