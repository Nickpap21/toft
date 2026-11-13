-- Who deactivated an account and when (D-50), shown on Settings → Instance →
-- Accounts and operators ("14:06 by admin"). Set by a deactivation, cleared
-- by a reactivation; accounts deactivated before 0030 keep both NULL.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Additive. A backend from before 0030 ignores the columns; its reactivation
-- would leave them set on an active account, which the CHECK refuses, so roll
-- back the backend and the schema together.

ALTER TABLE users ADD COLUMN IF NOT EXISTS deactivated_at TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS deactivated_by UUID REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE users ADD CONSTRAINT users_check
    CHECK (NOT is_active OR (deactivated_at IS NULL AND deactivated_by IS NULL));

-- Rollback (reversible; who deactivated each account is lost):
--   ALTER TABLE users DROP CONSTRAINT IF EXISTS users_check;
--   ALTER TABLE users DROP COLUMN IF EXISTS deactivated_by;
--   ALTER TABLE users DROP COLUMN IF EXISTS deactivated_at;
