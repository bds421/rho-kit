-- +goose Up
-- Claim lease columns enable multi-replica Resume without double-executing
-- the same in-flight saga (review-13). ListResumable atomically claims
-- rows with FOR UPDATE SKIP LOCKED and sets claim_until; concurrent
-- resumsers skip claimed rows until the lease expires.
ALTER TABLE saga_instances
    ADD COLUMN IF NOT EXISTS claim_until TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS claim_token TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_saga_instances_claim
    ON saga_instances (claim_until)
    WHERE state IN ('pending', 'running', 'compensating');

-- +goose Down
DROP INDEX IF EXISTS idx_saga_instances_claim;
ALTER TABLE saga_instances
    DROP COLUMN IF EXISTS claim_token,
    DROP COLUMN IF EXISTS claim_until;
