-- 0088: the lookalike_container alert. schema.sql is the source of truth;
-- Atlas applies it. This file records the change and how to undo it.
--
-- A container on a server that carries both a service's paas.service_id
-- label and a compose project, which Toft never makes. The alerts' kind
-- CHECK only widens (D-63): the previous release runs on this schema and
-- never fires the new kind.
--
-- Locks: the constraint is replaced, which validates alerts (open alerts and
-- 30 days of resolved ones) under ACCESS EXCLUSIVE.

ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN ('service_down', 'service_restarting',
    'service_unhealthy', 'container_exited', 'server_unreachable', 'disk_full',
    'volume_full', 'backup_failed', 'deploy_failed', 'oom_killed',
    'panel_container', 'panel_certificate', 'panel_upgrade_failed', 'mirror_failing',
    'agent_offline', 'agent_frozen', 'security_updates', 'reboot_required', 'docker_advisory',
    'agent_outdated', 'lookalike_container'));

-- Rollback (reversible; it loses the kind's alerts):
--   DELETE FROM alerts WHERE kind = 'lookalike_container';
--   ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
--   ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN (... 'docker_advisory', 'agent_outdated'));
