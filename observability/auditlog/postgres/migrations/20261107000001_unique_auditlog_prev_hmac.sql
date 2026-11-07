-- +goose NO TRANSACTION
-- +goose Up
-- Each event links to exactly one predecessor, so no two rows share a
-- prev_hmac: an append that chained onto a stale head fails here instead
-- of forking the chain. CONCURRENTLY keeps appends running; an interrupted
-- build leaves an INVALID index of this name: drop it and run again.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_audit_log_events_prev_hmac
    ON audit_log_events (prev_hmac);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_audit_log_events_prev_hmac;
