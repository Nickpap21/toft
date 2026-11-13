-- 0059: the disk advisor (D-135; private/plans/2026-10-31-disk-advisor.md).
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it. Additive (D-63): every column is nullable with
-- no default, so adding them rewrites no table, and the previous release
-- (v0.32.x) never reads them.
--
-- servers: the disk policy, NULL following the default (the panel's own
-- server keeps the same in instance_settings, panel_disk_policy), and the
-- last disk clean that ended (panel_disk_last_clean there).
-- services: how many of its last live deployments keep their image.
-- deployments: when a disk clean removed its image, so a rollback to it is
-- refused instead of failing on the server.
-- audit_log.actor_id becomes nullable: an automatic clean is Toft's own
-- act, written to the server's workspace log with no person. Dropping NOT
-- NULL rewrites nothing; v0.32.x lists the log with an inner join on the
-- actor, so it doesn't show those entries, and writes none.

ALTER TABLE servers ADD COLUMN IF NOT EXISTS build_cache_limit_bytes BIGINT
    CHECK (build_cache_limit_bytes >= 1000000000);
ALTER TABLE servers ADD COLUMN IF NOT EXISTS disk_auto_clean TEXT[]
    CHECK (disk_auto_clean <@ ARRAY['untagged', 'old_builds', 'unused_pulled']::TEXT[]);
ALTER TABLE servers ADD COLUMN IF NOT EXISTS disk_alert_percent SMALLINT
    CHECK (disk_alert_percent BETWEEN 50 AND 99);
ALTER TABLE servers ADD COLUMN IF NOT EXISTS disk_last_clean JSONB
    CHECK (jsonb_typeof(disk_last_clean) = 'object' AND octet_length(disk_last_clean::text) <= 8192);
ALTER TABLE services ADD COLUMN IF NOT EXISTS rollback_images_kept SMALLINT
    CHECK (rollback_images_kept BETWEEN 1 AND 100);
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS image_removed_at TIMESTAMPTZ;
ALTER TABLE audit_log ALTER COLUMN actor_id DROP NOT NULL;

-- Rollback (reversible; it loses each server's disk policy and last clean,
-- each service's rollback count, which deployments' images were removed,
-- after which a rollback to one fails on the server as before, and the
-- audit entries of automatic cleans):
--   DELETE FROM audit_log WHERE actor_id IS NULL;
--   ALTER TABLE audit_log ALTER COLUMN actor_id SET NOT NULL;
--   ALTER TABLE deployments DROP COLUMN IF EXISTS image_removed_at;
--   ALTER TABLE services DROP COLUMN IF EXISTS rollback_images_kept;
--   ALTER TABLE servers DROP COLUMN IF EXISTS disk_last_clean;
--   ALTER TABLE servers DROP COLUMN IF EXISTS disk_alert_percent;
--   ALTER TABLE servers DROP COLUMN IF EXISTS disk_auto_clean;
--   ALTER TABLE servers DROP COLUMN IF EXISTS build_cache_limit_bytes;
