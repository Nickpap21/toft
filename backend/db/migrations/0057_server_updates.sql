-- 0057: server updates, phase 1, read-only (D-130, D-133;
-- private/plans/2026-10-30-server-updates.md).
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it. Additive (D-63): the previous release (v0.30.x)
-- never reads the table, and never fires the new alert kinds.
--
-- server_updates is each server's last updates read, shaped like the API's
-- HostUpdates (the panel's own server keeps its read in instance_settings,
-- as panel_capabilities). The alerts' kind CHECK widens to three kinds.

CREATE TABLE IF NOT EXISTS server_updates (
    server_id  UUID        PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    report     JSONB       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(report) = 'object' AND octet_length(report::text) <= 262144),
    error      TEXT        NOT NULL DEFAULT '' CHECK (char_length(error) <= 500),
    checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN ('service_down', 'service_restarting',
    'service_unhealthy', 'container_exited', 'server_unreachable', 'disk_full',
    'volume_full', 'backup_failed', 'deploy_failed', 'oom_killed',
    'panel_container', 'panel_certificate', 'panel_upgrade_failed', 'mirror_failing',
    'agent_offline', 'agent_frozen', 'security_updates', 'reboot_required', 'docker_advisory'));

-- Rollback (reversible; it loses the last reads, which the next read
-- writes again, and the three kinds' alerts):
--   DROP TABLE IF EXISTS server_updates;
--   DELETE FROM alerts WHERE kind IN ('security_updates', 'reboot_required', 'docker_advisory');
--   ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
--   ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN (... 'agent_offline', 'agent_frozen'));
