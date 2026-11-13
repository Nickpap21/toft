-- 0071 (v0.35.0): the contract step for 0060 and 0061 (D-63, D-142, D-144).
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it; contract.sql states it again, explicitly and
-- idempotently, and runs after Atlas on every upgrade.
--
-- - environments.color's CHECK drops red. 0060 stopped writing it, and
--   premigrate.sql turns a red row gray before Atlas runs, so one written
--   by v0.33.x during the upgrade or after a rollback can't fail the apply.
-- - services_preview_no_migrate: a preview's copy (preview_of) has no migrate
--   or pre-deploy command. 0061 stopped copying them, and premigrate.sql
--   clears every copy's before Atlas runs.
--
-- The previous release (v0.34.0) writes neither red nor a copy with
-- commands, so it runs on this schema. Both tables are small (one row per
-- environment, one per service); each ADD CHECK scans its table once under
-- a short lock, with a 5 s lock_timeout in contract.sql.

ALTER TABLE environments DROP CONSTRAINT IF EXISTS environments_color_check;
ALTER TABLE environments ADD CONSTRAINT environments_color_check
    CHECK (color IN ('amber', 'violet', 'blue', 'teal', 'gray'));
ALTER TABLE services DROP CONSTRAINT IF EXISTS services_preview_no_migrate;
ALTER TABLE services ADD CONSTRAINT services_preview_no_migrate
    CHECK (preview_of IS NULL OR (migrate_command = '' AND pre_deploy_commands = ''));

-- Rollback (reversible; nothing to run for v0.34.0, which works on this
-- schema). To widen again for a release older than v0.34.0:
--   ALTER TABLE services DROP CONSTRAINT IF EXISTS services_preview_no_migrate;
--   ALTER TABLE environments DROP CONSTRAINT IF EXISTS environments_color_check;
--   ALTER TABLE environments ADD CONSTRAINT environments_color_check
--       CHECK (color IN ('red', 'amber', 'violet', 'blue', 'teal', 'gray'));
-- The red environments made gray stay gray (0060, irreversible by design).
