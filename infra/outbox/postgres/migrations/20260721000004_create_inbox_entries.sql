-- +goose Up
-- The transactional inbox is deliberately co-published with the outbox:
-- consumers can atomically claim an inbound delivery, mutate local state, and
-- enqueue an outbound event in one PostgreSQL transaction.
CREATE TABLE IF NOT EXISTS inbox_entries (
    consumer_name VARCHAR(128) NOT NULL,
    message_id    VARCHAR(255) NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (consumer_name, message_id)
);

-- Retention pruning deletes by received_at. This index bounds that janitor
-- path by the expired tail rather than scanning every retained receipt.
CREATE INDEX IF NOT EXISTS idx_inbox_entries_received_at
    ON inbox_entries (received_at);

-- +goose Down
DROP TABLE IF EXISTS inbox_entries;
