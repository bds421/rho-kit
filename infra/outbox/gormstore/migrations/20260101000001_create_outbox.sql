-- +goose Up
CREATE TABLE IF NOT EXISTS outbox_entries (
    id             VARCHAR(36)  PRIMARY KEY,
    topic          TEXT         NOT NULL,
    routing_key    TEXT         NOT NULL,
    message_id     TEXT         NOT NULL,
    message_type   TEXT         NOT NULL,
    payload        TEXT         NOT NULL,
    headers        TEXT,
    status         VARCHAR(20)  NOT NULL DEFAULT 'pending',
    attempts       INT          NOT NULL DEFAULT 0,
    last_error     TEXT,
    created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at   TIMESTAMP
);

CREATE INDEX idx_outbox_entries_status_created ON outbox_entries (status, created_at);
CREATE INDEX idx_outbox_entries_published_at ON outbox_entries (published_at);

-- +goose Down
DROP TABLE IF EXISTS outbox_entries;
