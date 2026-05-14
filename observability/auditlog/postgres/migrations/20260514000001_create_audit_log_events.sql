-- +goose Up
CREATE TABLE IF NOT EXISTS audit_log_events (
    id            VARCHAR(36) PRIMARY KEY,
    -- seq is the monotonic append-order index produced by Postgres at
    -- insert time. RangeChain (used by VerifyChain) iterates by seq ASC,
    -- so chain integrity is independent of the caller-supplied
    -- occurred_at — a backfilled or clock-skewed event stays verifiable.
    seq           BIGSERIAL UNIQUE NOT NULL,
    -- TIMESTAMPTZ (not TIMESTAMP) so the round-trip preserves UTC
    -- regardless of session timezone. The HMAC signs a canonical
    -- encoding of the event; a session-local TIMESTAMP would corrupt
    -- the signed bytes after a round trip on drivers that interpret
    -- the column literally.
    occurred_at   TIMESTAMPTZ NOT NULL,
    actor         VARCHAR(255) NOT NULL,
    action        VARCHAR(255) NOT NULL,
    -- Matches auditlog.MaxResourceBytes.
    resource      VARCHAR(2048) NOT NULL DEFAULT '',
    -- Matches auditlog.MaxStatusBytes; auditlog only persists success /
    -- failure / denied (validated at the Logger boundary).
    status        VARCHAR(32) NOT NULL,
    -- Optional. Empty string when not set so we don't have to deal with
    -- NULL distinctness in queries / index plans.
    ip_address    VARCHAR(64) NOT NULL DEFAULT '',
    -- Optional 32-char lowercase hex trace id (matches OpenTelemetry).
    trace_id      VARCHAR(32) NOT NULL DEFAULT '',
    -- Caller-supplied structured context, bounded by
    -- auditlog.MaxMetadataBytes at the Logger boundary.
    metadata      JSONB,
    -- prev_hmac links to the previous chain entry (empty for the first
    -- row in the chain — represented as the empty byte string, not
    -- NULL, so a chain reader does not have to special-case NULL).
    prev_hmac     BYTEA NOT NULL DEFAULT '\x',
    -- hmac is the per-event tamper-evident tag computed at append time
    -- by the Logger over a canonical encoding of the event.
    hmac          BYTEA NOT NULL
);

-- Query() returns events ordered by (occurred_at DESC, id DESC).
CREATE INDEX IF NOT EXISTS idx_audit_log_events_occurred
    ON audit_log_events (occurred_at DESC, id DESC);

-- Filter helpers — exact-match for actor/action/ip and prefix-match for
-- resource (auditlog.Filter.Resource is documented as a prefix).
CREATE INDEX IF NOT EXISTS idx_audit_log_events_actor
    ON audit_log_events (actor);
CREATE INDEX IF NOT EXISTS idx_audit_log_events_action
    ON audit_log_events (action);
CREATE INDEX IF NOT EXISTS idx_audit_log_events_ip
    ON audit_log_events (ip_address);
-- text_pattern_ops lets `resource LIKE 'prefix%'` use the index.
CREATE INDEX IF NOT EXISTS idx_audit_log_events_resource_prefix
    ON audit_log_events (resource text_pattern_ops);

-- +goose Down
DROP TABLE IF EXISTS audit_log_events;
