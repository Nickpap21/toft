-- Account deletion and the instance audit log (D-57).
--
-- Deleting an account keeps its users row as a tombstone: projects.user_id,
-- servers.user_id, api_keys and org_members cascade on a row delete, and
-- audit_log.actor_id and org_invites.created_by would refuse it, so a real
-- DELETE would wipe or be blocked by everything the person ever made.
-- Instead the email, name and password are replaced (the email becomes
-- deleted-<id>@deleted.invalid, which frees the real one), memberships,
-- API keys and reset links are removed, and deleted_at / deleted_by record
-- it. Only a deactivated, non-operator account can be deleted.
--
-- instance_audit_log holds what operators did to accounts and operators,
-- which until now went only to the panel's log: audit_log cannot hold them
-- because its org_id is NOT NULL.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Additive. A backend from before 0033 ignores the columns and the table.

ALTER TABLE users ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS deleted_by UUID REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE users ADD CONSTRAINT users_deleted_inactive CHECK (deleted_at IS NULL OR NOT is_active);

CREATE TABLE IF NOT EXISTS instance_audit_log (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_id       UUID        REFERENCES users(id) ON DELETE SET NULL,
    action         TEXT        NOT NULL,
    target_user_id UUID        REFERENCES users(id) ON DELETE SET NULL,
    detail         JSONB,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_instance_audit_log_created ON instance_audit_log(created_at DESC);

-- Rollback (reversible; the log is lost, tombstones stay as deactivated
-- accounts with their placeholder email):
--   DROP TABLE IF EXISTS instance_audit_log;
--   ALTER TABLE users DROP CONSTRAINT IF EXISTS users_deleted_inactive;
--   ALTER TABLE users DROP COLUMN IF EXISTS deleted_by;
--   ALTER TABLE users DROP COLUMN IF EXISTS deleted_at;
