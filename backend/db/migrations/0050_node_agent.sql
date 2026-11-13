-- 0050: the node agent, phase 1 (stage 7 session 1, D-106). Additive.
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it.
--
-- The previous release (v0.16.x) ignores the new columns and table. It reads
-- every server as SSH: one moved from SSH keeps working over SSH (its key and
-- sshd are still there); one joined by token has ssh_key = '' and fails to
-- connect until the panel is upgraded again, while its apps keep serving.

ALTER TABLE servers ADD COLUMN IF NOT EXISTS connection TEXT NOT NULL DEFAULT 'ssh';
ALTER TABLE servers ADD COLUMN IF NOT EXISTS agent_key TEXT;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS agent_key_pinned_at TIMESTAMPTZ;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS agent_next_key TEXT;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS agent_version TEXT;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS agent_arch TEXT;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS agent_boot_id TEXT;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS agent_connected_at TIMESTAMPTZ;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS agent_last_seen_at TIMESTAMPTZ;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS agent_from_ip TEXT;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS agent_revoked_at TIMESTAMPTZ;
ALTER TABLE servers ADD CONSTRAINT servers_connection_check CHECK (connection IN ('ssh', 'agent'));
-- Every existing row has a key, and v0.16.x always writes one with the
-- default connection, so the previous release never meets this check.
ALTER TABLE servers ADD CONSTRAINT servers_ssh_key_check CHECK (connection = 'agent' OR ssh_key <> '');

CREATE TABLE IF NOT EXISTS server_join_tokens (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id    UUID        NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    token_hash   BYTEA       NOT NULL UNIQUE,
    replaces_key BOOLEAN     NOT NULL DEFAULT FALSE,
    created_by   UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL,
    used_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_server_join_tokens_server ON server_join_tokens (server_id, created_at DESC);

-- The agent_offline alert (a CHECK only widens): the previous release never
-- writes it and lists an open one like any other.
ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN ('service_down', 'service_restarting',
    'service_unhealthy', 'container_exited', 'server_unreachable', 'disk_full',
    'volume_full', 'backup_failed', 'deploy_failed', 'oom_killed',
    'panel_container', 'panel_certificate', 'panel_upgrade_failed', 'mirror_failing',
    'agent_offline'));

-- The panel's own agent key lives in instance_settings ('agent_panel_key',
-- encrypted with ENCRYPTION_KEY): no schema change.

-- Undo (not run by toft upgrade, which never drops; for a development
-- database only). Token-joined servers must be removed first, since without
-- the agent they have no way in:
--   DELETE FROM servers WHERE connection = 'agent' AND ssh_key = '';
--   DELETE FROM alerts WHERE kind = 'agent_offline';  (then the CHECK without it)
--   DROP TABLE IF EXISTS server_join_tokens;
--   ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_ssh_key_check;
--   ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_connection_check;
--   ALTER TABLE servers DROP COLUMN IF EXISTS agent_revoked_at, DROP COLUMN IF EXISTS agent_from_ip,
--     DROP COLUMN IF EXISTS agent_last_seen_at, DROP COLUMN IF EXISTS agent_connected_at,
--     DROP COLUMN IF EXISTS agent_boot_id, DROP COLUMN IF EXISTS agent_arch,
--     DROP COLUMN IF EXISTS agent_version, DROP COLUMN IF EXISTS agent_key_pinned_at,
--     DROP COLUMN IF EXISTS agent_next_key,
--     DROP COLUMN IF EXISTS agent_key, DROP COLUMN IF EXISTS connection;
