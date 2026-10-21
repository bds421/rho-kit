-- +goose Up
-- Filter.Search matches search_text (auditlog.SearchText: actor, action,
-- resource and the metadata values) as a case-insensitive substring
-- (ILIKE '%term%'), served by a trigram index. pg_trgm is a trusted
-- extension (PostgreSQL 13+): a role with CREATE on the database installs
-- it. Where it already exists, it must be in the migration's search_path.
-- search_text is derived for searching and not covered by the HMAC chain.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

ALTER TABLE audit_log_events
    ADD COLUMN IF NOT EXISTS search_text TEXT NOT NULL DEFAULT '';

-- +goose StatementBegin
CREATE FUNCTION pg_temp.audit_log_metadata_values(signed BYTEA, legacy JSONB) RETURNS TEXT
LANGUAGE plpgsql AS $$
DECLARE
    doc JSONB := legacy;
BEGIN
    IF signed IS NOT NULL THEN
        IF length(signed) = 0 THEN
            RETURN '';
        END IF;
        doc := convert_from(signed, 'UTF8')::jsonb;
    END IF;
    RETURN coalesce((
        SELECT string_agg(v #>> '{}', E'\n')
          FROM jsonb_path_query(doc, 'strict $.**') AS v
         WHERE jsonb_typeof(v) IN ('string', 'number') AND v #>> '{}' <> ''
    ), '');
EXCEPTION WHEN others THEN
    RETURN '';
END $$;
-- +goose StatementEnd

UPDATE audit_log_events
   SET search_text = rtrim(concat_ws(E'\n', actor, action, resource,
           pg_temp.audit_log_metadata_values(signed_metadata, metadata)), E'\n')
 WHERE search_text = '';

DROP FUNCTION pg_temp.audit_log_metadata_values(BYTEA, JSONB);

CREATE INDEX IF NOT EXISTS idx_audit_log_events_search
    ON audit_log_events USING gin (search_text gin_trgm_ops);

-- +goose Down
DROP INDEX IF EXISTS idx_audit_log_events_search;
ALTER TABLE audit_log_events DROP COLUMN IF EXISTS search_text;
