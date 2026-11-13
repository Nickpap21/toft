-- 0052: routes written by the node agent and the restore freeze (stage 7
-- session 3, D-113). Additive. schema.sql is the source of truth; Atlas
-- applies it. This file records the change and how to undo it.
--
-- The previous release (v0.18.x) ignores the new columns: it never writes
-- them, so agent_routes stays NULL and no server reads as frozen. The CHECK
-- on alerts.kind only widens.

ALTER TABLE servers ADD COLUMN IF NOT EXISTS agent_routes INTEGER CHECK (agent_routes >= 0);
ALTER TABLE servers ADD COLUMN IF NOT EXISTS agent_frozen_at TIMESTAMPTZ;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS agent_frozen JSONB;
ALTER TABLE servers ADD CONSTRAINT servers_agent_frozen_check CHECK ((agent_frozen_at IS NULL) = (agent_frozen IS NULL)
    AND (agent_frozen IS NULL OR (jsonb_typeof(agent_frozen) = 'array' AND octet_length(agent_frozen::text) <= 262144)));

ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN ('service_down', 'service_restarting',
    'service_unhealthy', 'container_exited', 'server_unreachable', 'disk_full',
    'volume_full', 'backup_failed', 'deploy_failed', 'oom_killed',
    'panel_container', 'panel_certificate', 'panel_upgrade_failed', 'mirror_failing',
    'agent_offline', 'agent_frozen'));

-- Undo (not run by toft upgrade, which never drops; for a development
-- database only):
--   DELETE FROM alerts WHERE kind = 'agent_frozen';
--   ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
--   ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN (... 'agent_offline'));
--   ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_agent_frozen_check;
--   ALTER TABLE servers DROP COLUMN IF EXISTS agent_frozen, DROP COLUMN IF EXISTS agent_frozen_at,
--     DROP COLUMN IF EXISTS agent_routes;
