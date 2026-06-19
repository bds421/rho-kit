-- +goose Up
-- Retention: this is a tamper-evident, append-only per-tenant hash chain
-- (prev_hash + seq + signature). Unlike outbox (DeletePublishedBefore /
-- DeleteFailedBefore), idempotency (DeleteExpired), and auditlog
-- (DeleteBefore), the actionlog store ships NO Delete/Prune method — by
-- design, because any row deletion or reordering breaks the per-tenant
-- chain on the next VerifyChain call (there is no watermark-recovery
-- equivalent here). The table therefore grows unbounded for the life of
-- the deployment. Operators who need retention MUST handle it externally
-- and accept that historical chain verification stops at the prune
-- boundary: archive the rows out-of-band first, then either DELETE the
-- oldest contiguous head per tenant (interior deletes are unrecoverable)
-- or, preferred at scale, native range partitioning by occurred_at with
-- whole-partition DETACH/DROP. Treat any prune as making
-- VerifyChain/VerifyChainFrom fail for the pruned range — verify the
-- archived copy, not the live table, for the retained-but-deleted span.
CREATE TABLE IF NOT EXISTS action_log_entries (
    id                VARCHAR(36) PRIMARY KEY,
    tenant_id         VARCHAR(255) NOT NULL,
    actor             VARCHAR(255) NOT NULL,
    action            VARCHAR(255) NOT NULL,
    resource          VARCHAR(500) NOT NULL DEFAULT '',
    outcome           VARCHAR(20)  NOT NULL,
    reason            TEXT NOT NULL DEFAULT '',
    metadata          JSONB,
    -- TIMESTAMPTZ (not TIMESTAMP) so the round-trip preserves UTC
    -- regardless of the database session timezone. The HMAC signing
    -- input formats OccurredAt as RFC3339Nano UTC, so a session-local
    -- TIMESTAMP would cause every signature verification to fail after
    -- a round trip on drivers that interpret the column literally.
    occurred_at       TIMESTAMPTZ NOT NULL,
    signature_key_id  VARCHAR(64) NOT NULL,
    -- seq is the per-tenant monotonic sequence number assigned by
    -- Logger.Append. The unique index on (tenant_id, seq) is the
    -- backstop that keeps two concurrent appends from producing the
    -- same Seq on dialects that elide SELECT FOR UPDATE.
    seq               BIGINT NOT NULL DEFAULT 0,
    -- prev_hash is the hex-encoded plain SHA-256 of the previous
    -- entry's canonical form for this tenant; the first entry stores
    -- 64 zero hex chars. The chain hash is key-free on purpose — the
    -- per-row HMAC signature carries the tamper-evident property and
    -- includes prev_hash in its canonical input, so a key rotation
    -- between two entries does not produce a false ErrChainBroken.
    -- Together with seq, this turns the table into a tamper-evident
    -- append-only log: deletion / reordering / truncation breaks the
    -- chain on the next VerifyChain call.
    prev_hash         VARCHAR(64) NOT NULL DEFAULT '',
    signature         VARCHAR(128) NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_action_log_entries_tenant_occurred
    ON action_log_entries (tenant_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_action_log_entries_actor
    ON action_log_entries (actor);
CREATE INDEX IF NOT EXISTS idx_action_log_entries_action
    ON action_log_entries (action);
CREATE UNIQUE INDEX IF NOT EXISTS idx_action_log_entries_tenant_seq
    ON action_log_entries (tenant_id, seq);

-- +goose Down
DROP TABLE IF EXISTS action_log_entries;
