-- The audit log list reads one workspace's rows newest first
-- (org.PostgresRepository.ListAuditLog: WHERE a.org_id = $1 ORDER BY
-- created_at DESC LIMIT n). idx_audit_log_org_id served the WHERE and left
-- the ORDER BY to a sort; this index serves both and replaces it. Retention
-- (TOFT_AUDIT_RETENTION_DAYS, default 365) is a daily job in the backend,
-- not a schema change (security audit of 2026-10-15, P4 and S11).
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.

CREATE INDEX IF NOT EXISTS idx_audit_log_org_created ON audit_log(org_id, created_at DESC);
DROP INDEX IF EXISTS idx_audit_log_org_id;

-- Rollback (reversible; drops no data):
-- CREATE INDEX IF NOT EXISTS idx_audit_log_org_id ON audit_log(org_id);
-- DROP INDEX IF EXISTS idx_audit_log_org_created;
