-- +goose NO TRANSACTION
-- +goose Up
-- Each event links to exactly one predecessor, so no two rows share a
-- prev_hmac: an append that chained onto a stale head fails here instead
-- of forking the chain. A chain that already forked stops this migration
-- before the index is built, naming how many links are shared. An
-- interrupted earlier build left an INVALID index of this name; it is
-- dropped and built again. CONCURRENTLY keeps appends running.
-- +goose StatementBegin
DO $$
DECLARE shared bigint;
BEGIN
    SELECT count(*) INTO shared
      FROM (SELECT 1 FROM audit_log_events GROUP BY prev_hmac HAVING count(*) > 1) AS forks;
    IF shared > 0 THEN
        RAISE EXCEPTION 'audit_log_events: % prev_hmac values are shared by more than one row (a forked chain); resolve them before this migration', shared;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_index WHERE indexrelid = to_regclass('idx_audit_log_events_prev_hmac') AND NOT indisvalid) THEN
        DROP INDEX idx_audit_log_events_prev_hmac;
    END IF;
END $$;
-- +goose StatementEnd

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_audit_log_events_prev_hmac
    ON audit_log_events (prev_hmac);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_audit_log_events_prev_hmac;
