-- +goose Up
-- Tenant is optional for single-tenant services. Keep empty-string storage
-- semantics aligned with the other optional scalar audit fields.
ALTER TABLE audit_log_events
    ADD COLUMN IF NOT EXISTS tenant VARCHAR(255) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_audit_log_events_tenant
    ON audit_log_events (tenant);

-- +goose Down
DROP INDEX IF EXISTS idx_audit_log_events_tenant;
ALTER TABLE audit_log_events DROP COLUMN IF EXISTS tenant;
