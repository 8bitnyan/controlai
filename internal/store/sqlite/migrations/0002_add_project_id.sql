-- Migration 0002: add project_id tag to tenants and audit_events
-- The ALTER TABLE statements are executed conditionally in Go (see store.go applyMigration0002)
-- to guard against re-running on databases that already have the column.

-- Index is idempotent due to IF NOT EXISTS.
CREATE INDEX IF NOT EXISTS idx_tenants_project_id ON tenants(project_id);

-- Audit events index for project-scoped filtering.
CREATE INDEX IF NOT EXISTS idx_audit_project_id ON audit_events(project_id);
