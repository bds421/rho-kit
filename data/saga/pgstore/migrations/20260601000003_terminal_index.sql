-- +goose Up
-- Retention support for terminal instances. The executor leaves
-- completed / failed sagas in place (each row carries input + per-step
-- JSONB results), so the table grows unbounded without a periodic prune.
-- Store.DeleteTerminalBefore(ctx, cutoff) is the sweep; this partial
-- index makes it O(rows-to-delete) by covering exactly the terminal set
-- it scans (state IN ('completed','failed') AND updated_at < cutoff),
-- mirroring the resumable index that covers the in-flight set.
--
-- Additive and safe on a live, already-migrated table: CREATE INDEX
-- IF NOT EXISTS is a no-op on re-run and does not alter the existing
-- idx_saga_instances_resumable.
CREATE INDEX IF NOT EXISTS idx_saga_instances_terminal
    ON saga_instances (state, updated_at)
    WHERE state IN ('completed', 'failed');

-- +goose Down
DROP INDEX IF EXISTS idx_saga_instances_terminal;
