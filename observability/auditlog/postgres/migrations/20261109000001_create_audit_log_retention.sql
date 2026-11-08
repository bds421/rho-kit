-- +goose Up
-- What the retention sweeps removed in all (auditlog.RetentionRecord), one
-- row sealed with the retention key, which only the sweeps and the
-- verification hold: whoever can write this table without the key cannot
-- pass a deletion off as retention. Store.ApplyCut replaces the row in the
-- transaction that removes a cut. Grant the verifying roles SELECT and only
-- the role that removes events INSERT and UPDATE.
CREATE TABLE IF NOT EXISTS audit_log_retention (
    id            INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    watermark     BYTEA NOT NULL,
    removed       BIGINT NOT NULL CHECK (removed > 0),
    pruned_before TIMESTAMPTZ NOT NULL,
    pruned_at     TIMESTAMPTZ NOT NULL,
    seal          BYTEA NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS audit_log_retention;
