-- +goose Up
-- List pages by (created_at DESC, id DESC) under a mandatory tenant scope.
-- Without this index every tenant-scoped List does a top-N sort over the
-- tenant's full matching set.
CREATE INDEX IF NOT EXISTS idx_approval_requests_tenant_created_id
    ON approval_requests (tenant_id, created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_approval_requests_tenant_created_id;
