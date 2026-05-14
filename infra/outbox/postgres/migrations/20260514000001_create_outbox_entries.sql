-- +goose Up
CREATE TABLE IF NOT EXISTS outbox_entries (
    id            UUID PRIMARY KEY,
    topic         VARCHAR(255) NOT NULL,
    routing_key   VARCHAR(255) NOT NULL,
    message_id    VARCHAR(255) NOT NULL,
    message_type  VARCHAR(255) NOT NULL,
    payload       JSONB NOT NULL,
    -- Headers stored as JSONB object; the kit-side outbox.Writer
    -- marshals an empty map to `null` so the column accepts NULL.
    headers       JSONB,
    -- pending / processing / published / failed (outbox.Status). The
    -- CHECK constraint mirrors the kit's Status enum so a manual SQL
    -- nudge that picks a typo (e.g. "publshed") fails at INSERT/UPDATE
    -- time rather than silently disabling FetchPending.
    status        VARCHAR(20) NOT NULL CHECK (status IN ('pending','processing','published','failed')),
    attempts      INT NOT NULL DEFAULT 0,
    -- TIMESTAMPTZ everywhere so cross-zone round-trips preserve UTC;
    -- the Janitor's retention sweeps compare against time.Now().UTC().
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    -- NULL until MarkPublished; the dedicated partial index keeps the
    -- retention sweep selective without scanning published-recent rows.
    published_at  TIMESTAMPTZ,
    -- NULL means "eligible immediately"; FetchPending honours this so
    -- IncrementAttempts can defer retries via exponential backoff
    -- without spinning the relay loop.
    next_retry_at TIMESTAMPTZ,
    last_error    TEXT
);

-- FetchPending claim path: select status='pending' rows whose
-- next_retry_at is past, oldest first. The partial index keeps the
-- working set small even when published rows accumulate awaiting
-- retention deletion.
CREATE INDEX IF NOT EXISTS idx_outbox_pending_ready
    ON outbox_entries (created_at)
    WHERE status = 'pending';

-- ResetStaleProcessing: find processing rows whose updated_at is
-- older than the stale window. Partial index so the scan is bounded
-- by the in-flight set, not the whole table.
CREATE INDEX IF NOT EXISTS idx_outbox_processing_updated
    ON outbox_entries (updated_at)
    WHERE status = 'processing';

-- DeletePublishedBefore: prune the published tail. Partial index so
-- the sweep is O(rows-to-delete), not O(all-rows).
CREATE INDEX IF NOT EXISTS idx_outbox_published_at
    ON outbox_entries (published_at)
    WHERE status = 'published';

-- DeleteFailedBefore: prune the dead-letter tail.
CREATE INDEX IF NOT EXISTS idx_outbox_failed_updated
    ON outbox_entries (updated_at)
    WHERE status = 'failed';

-- +goose Down
DROP TABLE IF EXISTS outbox_entries;
