-- +goose NO TRANSACTION
-- +goose Up
-- pg_trgm is a trusted extension (PostgreSQL 13+): a role with CREATE on
-- the database installs it. A migrating role without that privilege needs
-- the extension created beforehand by the database owner; IF NOT EXISTS
-- then succeeds without the privilege. CONCURRENTLY keeps appends running
-- while the index builds. If a build is interrupted, PostgreSQL leaves an
-- INVALID index of this name: drop it and run the migration again.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_audit_log_events_search
    ON audit_log_events USING gin (search_text gin_trgm_ops);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_audit_log_events_search;
