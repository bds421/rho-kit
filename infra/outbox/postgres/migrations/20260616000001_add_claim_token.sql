-- +goose Up
-- claim_token fences outcome updates (MarkPublished / MarkFailed /
-- IncrementAttempts) by claim ownership. FetchPending stamps a fresh
-- per-row token on every claim and the relay remembers id->token in
-- process; the outcome UPDATEs add `AND claim_token = $token` so a late
-- update from a relay whose claim was stale-reset and re-claimed by
-- another relay cannot resurrect or duplicate the row (the ABA race).
--
-- Nullable with no default so the column is safe to add to a live,
-- already-populated table: existing rows get NULL, and the partial
-- index below stays small. FetchPending always sets the token when it
-- transitions a row to 'processing', so any row the relay acts on
-- carries a non-NULL token; legacy NULL rows simply have no in-flight
-- claim to fence.
ALTER TABLE outbox_entries
    ADD COLUMN IF NOT EXISTS claim_token UUID;

-- The outcome UPDATEs match on (id, status='processing', claim_token).
-- A partial index on the live processing set keeps the fenced lookup
-- bounded by in-flight rows rather than the whole table.
CREATE INDEX IF NOT EXISTS idx_outbox_processing_claim_token
    ON outbox_entries (claim_token)
    WHERE status = 'processing';

-- +goose Down
DROP INDEX IF EXISTS idx_outbox_processing_claim_token;
ALTER TABLE outbox_entries
    DROP COLUMN IF EXISTS claim_token;
