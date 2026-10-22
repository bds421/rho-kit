-- +goose NO TRANSACTION
-- +goose Up
-- Batches of 5000 rows, each committed on its own, so appends (which only
-- insert) never wait for the whole table.

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

-- +goose StatementBegin
DO $$
DECLARE
    batch CONSTANT BIGINT := 5000;
    low BIGINT := 0;
    top BIGINT;
BEGIN
    SELECT coalesce(max(seq), 0) INTO top FROM audit_log_events;
    WHILE low < top LOOP
        UPDATE audit_log_events
           SET search_text = rtrim(concat_ws(E'\n', actor, action, resource,
                   pg_temp.audit_log_metadata_values(signed_metadata, metadata)), E'\n')
         WHERE seq > low AND seq <= low + batch AND search_text = '';
        COMMIT;
        low := low + batch;
    END LOOP;
END $$;
-- +goose StatementEnd

DROP FUNCTION pg_temp.audit_log_metadata_values(BYTEA, JSONB);

-- +goose Down
SELECT 1;
