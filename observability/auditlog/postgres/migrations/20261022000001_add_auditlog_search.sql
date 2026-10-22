-- +goose Up
-- Filter.Search matches search_text (auditlog.SearchText: actor, action,
-- resource and the metadata values) as a case-insensitive substring.
-- Adding a NOT NULL column with a constant default rewrites nothing, so
-- this step holds its lock only briefly. 20261022000002 fills existing
-- rows in batches and 20261022000003 builds the trigram index without
-- blocking writers. The postgres Store compares search_text with the
-- event's content while it streams the chain for VerifyChain.
ALTER TABLE audit_log_events
    ADD COLUMN IF NOT EXISTS search_text TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE audit_log_events DROP COLUMN IF EXISTS search_text;
