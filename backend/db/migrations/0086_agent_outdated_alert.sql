-- 0086: the agent_outdated alert. schema.sql is the source of truth; Atlas
-- applies it. This file records the change and how to undo it.
--
-- A server's agent older than the panel, refusing compose deploys, whose
-- upgrade didn't take. The alerts' kind CHECK only widens (D-63): the
-- previous release runs on this schema and never fires the new kind.
--
-- Locks: the constraint is replaced, which validates alerts (open alerts and
-- 30 days of resolved ones) under ACCESS EXCLUSIVE.

ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN ('service_down', 'service_restarting',
    'service_unhealthy', 'container_exited', 'server_unreachable', 'disk_full',
    'volume_full', 'backup_failed', 'deploy_failed', 'oom_killed',
    'panel_container', 'panel_certificate', 'panel_upgrade_failed', 'mirror_failing',
    'agent_offline', 'agent_frozen', 'security_updates', 'reboot_required', 'docker_advisory',
    'agent_outdated'));

-- Rollback (reversible; it loses the kind's alerts):
--   DELETE FROM alerts WHERE kind = 'agent_outdated';
--   ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
--   ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN (... 'reboot_required', 'docker_advisory'));
