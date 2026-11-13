-- name: CreateServer :one
INSERT INTO servers (
    user_id,
    name,
    ip_address,
    "user",
    port,
    ssh_key,
    status
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
) RETURNING id, org_id, user_id, name, ip_address, "user", port, ssh_key, status,
            provisioned_at,
            total_cpu_cores, total_memory_mb, cpu_cores_override, memory_mb_override,
            host_key, host_key_pinned_at,
            created_at, updated_at;

-- name: GetServer :one
SELECT id, org_id, user_id, name, ip_address, "user", port, ssh_key, status,
       provisioned_at,
       total_cpu_cores, total_memory_mb, cpu_cores_override, memory_mb_override,
       host_key, host_key_pinned_at,
       created_at, updated_at,
       connection, agent_key, agent_frozen_at
FROM servers
WHERE id = $1 LIMIT 1;

-- name: ListServersByUser :many
SELECT id, org_id, user_id, name, ip_address, "user", port, ssh_key, status,
       provisioned_at,
       total_cpu_cores, total_memory_mb, cpu_cores_override, memory_mb_override,
       host_key, host_key_pinned_at,
       created_at, updated_at
FROM servers
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: UpdateServerStatus :one
UPDATE servers
SET status = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING id, org_id, user_id, name, ip_address, "user", port, ssh_key, status,
          provisioned_at,
          total_cpu_cores, total_memory_mb, cpu_cores_override, memory_mb_override,
          host_key, host_key_pinned_at,
          created_at, updated_at,
       connection, agent_key;

-- name: MarkServerProvisioned :one
UPDATE servers
SET provisioned_at = NOW(),
    updated_at = NOW()
WHERE id = $1
RETURNING id, org_id, user_id, name, ip_address, "user", port, ssh_key, status,
          provisioned_at,
          total_cpu_cores, total_memory_mb, cpu_cores_override, memory_mb_override,
          host_key, host_key_pinned_at,
          created_at, updated_at,
       connection, agent_key;

-- name: UpdateServerDiscoveredCapacity :execrows
-- The capacity read from the server; the manual overrides are left as they are.
UPDATE servers
SET total_cpu_cores = $2,
    total_memory_mb = $3,
    updated_at = NOW()
WHERE id = $1;

-- name: DeleteServer :exec
DELETE FROM servers
WHERE id = $1;

-- name: PinServerHostKey :one
-- Compare-and-set: pins the key only while none is pinned. No row means a
-- key was already pinned (or the server is gone); read it with
-- GetServerHostKey.
UPDATE servers
SET host_key = $2,
    host_key_pinned_at = NOW(),
    updated_at = NOW()
WHERE id = $1 AND host_key IS NULL
RETURNING host_key;

-- name: GetServerHostKey :one
SELECT host_key FROM servers
WHERE id = $1;

-- name: ReplaceServerHostKey :execrows
-- Unconditional: an instance operator confirmed this key's fingerprint.
UPDATE servers
SET host_key = $2,
    host_key_pinned_at = NOW(),
    updated_at = NOW()
WHERE id = $1;

-- name: GetServerDNSProvider :one
SELECT server_id, provider, credentials, updated_at FROM server_dns_providers WHERE server_id = $1;

-- name: UpsertServerDNSProvider :one
INSERT INTO server_dns_providers (server_id, provider, credentials)
VALUES ($1, $2, $3)
ON CONFLICT (server_id) DO UPDATE SET provider = EXCLUDED.provider, credentials = EXCLUDED.credentials, updated_at = NOW()
RETURNING server_id, provider, credentials, updated_at;

-- name: DeleteServerDNSProvider :exec
DELETE FROM server_dns_providers WHERE server_id = $1;

-- ─── Shares ───────────────────────────────────────────────────────────────────

-- name: ListServerShares :many
SELECT sh.org_id, o.name AS org_name, sh.cpu_cores::float8 AS cpu_cores, sh.memory_mb, sh.created_at
FROM server_shares sh
JOIN organizations o ON o.id = sh.org_id
WHERE sh.server_id = $1
ORDER BY o.name;

-- name: GetServerShare :one
SELECT cpu_cores::float8 AS cpu_cores, memory_mb FROM server_shares WHERE server_id = $1 AND org_id = $2;

-- name: UpsertServerShare :exec
INSERT INTO server_shares (server_id, org_id, cpu_cores, memory_mb)
VALUES ($1, $2, sqlc.arg(cpu_cores)::float8, $3)
ON CONFLICT (server_id, org_id) DO UPDATE SET cpu_cores = EXCLUDED.cpu_cores, memory_mb = EXCLUDED.memory_mb;

-- name: DeleteServerShare :execrows
DELETE FROM server_shares WHERE server_id = $1 AND org_id = $2;

-- name: CountOrgServicesOnServer :one
-- The services of a workspace (by their project's workspace, or their own
-- for workspace-shared ones) on a server.
SELECT count(*) FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
WHERE s.server_id = sqlc.arg(server_id) AND COALESCE(p.org_id, s.scoped_org_id) = sqlc.arg(org_id);

-- name: CountSharedServicesOnServer :one
-- Services on a server that belong to a workspace it is shared with.
SELECT count(*) FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
JOIN server_shares sh ON sh.server_id = s.server_id AND sh.org_id = COALESCE(p.org_id, s.scoped_org_id)
WHERE s.server_id = $1;

-- name: ListOrgServicesOnServer :many
-- A workspace's services on a server (by their project's workspace, or
-- their own for workspace-shared ones), for the server's page. Other
-- workspaces' services there show only as what they reserve.
SELECT s.id,
       s.name,
       COALESCE(p.name, '')::text          AS project_name,
       COALESCE(e.project_id, s.project_id) AS project_id,
       COALESCE(e.name, '')::text          AS environment_name,
       s.scope,
       COALESCE(s.cpu_limit, '')::text     AS cpu_limit,
       COALESCE(s.memory_limit, '')::text  AS memory_limit,
       s.runtime_status,
       COALESCE(s.isolation, o.default_isolation, 'standard')::text AS effective_isolation
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
LEFT JOIN organizations o ON o.id = COALESCE(p.org_id, s.scoped_org_id)
WHERE s.server_id = sqlc.arg(server_id) AND COALESCE(p.org_id, s.scoped_org_id) = sqlc.arg(org_id)
ORDER BY p.name NULLS LAST, s.name, e.name;

-- ─── Provision runs (D-48) ───────────────────────────────────────────────────

-- name: StartProvisionRun :exec
-- Replaces the server's last run with a new, running one.
INSERT INTO server_provision_runs (server_id, started_by)
VALUES (sqlc.arg(server_id), sqlc.narg(started_by))
ON CONFLICT (server_id) DO UPDATE
SET started_by = EXCLUDED.started_by, started_at = NOW(), finished_at = NULL, ok = NULL, error = '', log = '', warnings = '{}';

-- name: AppendProvisionLog :exec
-- One line of the running log; the oldest text goes past 256 KiB.
UPDATE server_provision_runs
SET log = right(log || sqlc.arg(line)::text || E'\n', 262144)
WHERE server_id = sqlc.arg(server_id);

-- name: FinishProvisionRun :exec
UPDATE server_provision_runs
SET finished_at = NOW(), ok = sqlc.arg(ok), error = left(sqlc.arg(error)::text, 2000), warnings = sqlc.arg(warnings)::text[]
WHERE server_id = sqlc.arg(server_id);

-- name: CloseUnfinishedProvisionRuns :execrows
-- At startup: no run survives a backend restart.
UPDATE server_provision_runs
SET finished_at = NOW(), ok = false, error = 'The panel restarted during the run, so it stopped without finishing.'
WHERE finished_at IS NULL;

-- name: GetProvisionRun :one
SELECT r.started_at, r.finished_at, r.ok, r.error, r.log, r.warnings, COALESCE(u.name, '')::text AS started_by_name
FROM server_provision_runs r
LEFT JOIN users u ON u.id = r.started_by
WHERE r.server_id = $1;

-- name: ListProvisionWarningsByOrg :many
-- The workspace's servers whose last run ended ok with warnings, for the
-- server list (Needs attention).
SELECT r.server_id, r.warnings
FROM server_provision_runs r
JOIN servers s ON s.id = r.server_id
WHERE s.org_id = $1 AND r.ok AND cardinality(r.warnings) > 0;

-- ─── gVisor installs (D-95), as the provision runs above ─────────────────────

-- name: StartGvisorRun :exec
INSERT INTO server_gvisor_runs (server_id, release, started_by)
VALUES (sqlc.arg(server_id), sqlc.arg(release), sqlc.narg(started_by))
ON CONFLICT (server_id) DO UPDATE
SET release = EXCLUDED.release, started_by = EXCLUDED.started_by, started_at = NOW(), finished_at = NULL, ok = NULL, error = '', log = '';

-- name: AppendGvisorLog :exec
UPDATE server_gvisor_runs
SET log = right(log || sqlc.arg(line)::text || E'\n', 262144)
WHERE server_id = sqlc.arg(server_id);

-- name: FinishGvisorRun :exec
UPDATE server_gvisor_runs
SET finished_at = NOW(), ok = sqlc.arg(ok), error = left(sqlc.arg(error)::text, 2000)
WHERE server_id = sqlc.arg(server_id);

-- name: CloseUnfinishedGvisorRuns :execrows
UPDATE server_gvisor_runs
SET finished_at = NOW(), ok = false, error = 'The panel restarted during the install, so it stopped without finishing.'
WHERE finished_at IS NULL;

-- name: GetGvisorRun :one
SELECT r.release, r.started_at, r.finished_at, r.ok, r.error, r.log, COALESCE(u.name, '')::text AS started_by_name
FROM server_gvisor_runs r
LEFT JOIN users u ON u.id = r.started_by
WHERE r.server_id = $1;

-- ─── Capabilities (D-93) ─────────────────────────────────────────────────────

-- name: UpsertServerCapabilities :exec
INSERT INTO server_capabilities (server_id, kernel, arch, cgroup_v2, virtualization, kvm, docker_version, apparmor, seccomp, runtimes, error, preview_firewall, dns_relay, address_pools, probed_at)
VALUES (sqlc.arg(server_id), sqlc.arg(kernel), sqlc.arg(arch), sqlc.arg(cgroup_v2), sqlc.arg(virtualization), sqlc.arg(kvm),
        sqlc.arg(docker_version), sqlc.arg(apparmor), sqlc.arg(seccomp), sqlc.arg(runtimes), sqlc.arg(error), sqlc.arg(preview_firewall), sqlc.arg(dns_relay),
        sqlc.narg(address_pools), NOW())
ON CONFLICT (server_id) DO UPDATE
SET kernel = EXCLUDED.kernel, arch = EXCLUDED.arch, cgroup_v2 = EXCLUDED.cgroup_v2, virtualization = EXCLUDED.virtualization,
    kvm = EXCLUDED.kvm, docker_version = EXCLUDED.docker_version, apparmor = EXCLUDED.apparmor, seccomp = EXCLUDED.seccomp,
    runtimes = EXCLUDED.runtimes, error = EXCLUDED.error, preview_firewall = EXCLUDED.preview_firewall,
    dns_relay = EXCLUDED.dns_relay, address_pools = EXCLUDED.address_pools, probed_at = NOW();

-- name: SetServerCapabilitiesError :exec
-- A probe that failed keeps what the last one read, with its error.
INSERT INTO server_capabilities (server_id, error, probed_at) VALUES (sqlc.arg(server_id), sqlc.arg(error), NOW())
ON CONFLICT (server_id) DO UPDATE SET error = EXCLUDED.error, probed_at = NOW();

-- name: GetServerCapabilities :one
SELECT server_id, kernel, arch, cgroup_v2, virtualization, kvm, docker_version, apparmor, seccomp, runtimes, error, probed_at,
       preview_firewall, dns_relay, address_pools
FROM server_capabilities WHERE server_id = $1;

-- name: ListStaleCapabilityServers :many
-- Servers whose probe is older than a day or never ran, for the daily probe.
SELECT s.id FROM servers s LEFT JOIN server_capabilities c ON c.server_id = s.id
WHERE c.server_id IS NULL OR c.probed_at < NOW() - INTERVAL '1 day';

-- ─── Updates (D-133) ─────────────────────────────────────────────────────────

-- name: UpsertServerUpdates :exec
INSERT INTO server_updates (server_id, report, error, checked_at) VALUES (sqlc.arg(server_id), sqlc.arg(report), '', NOW())
ON CONFLICT (server_id) DO UPDATE SET report = EXCLUDED.report, error = '', checked_at = NOW();

-- name: SetServerUpdatesError :exec
-- A read that failed keeps what the last one read, with its error.
INSERT INTO server_updates (server_id, error, checked_at) VALUES (sqlc.arg(server_id), sqlc.arg(error), NOW())
ON CONFLICT (server_id) DO UPDATE SET error = EXCLUDED.error, checked_at = NOW();

-- name: GetServerUpdates :one
SELECT report, error, checked_at FROM server_updates WHERE server_id = $1;

-- name: ListServerUpdates :many
-- Every server's last read, for the alerts.
SELECT server_id, report, error, checked_at FROM server_updates;

-- name: ListStaleUpdateServers :many
-- Servers whose last read is 6 hours old or never ran.
SELECT s.id FROM servers s LEFT JOIN server_updates u ON u.server_id = s.id
WHERE u.server_id IS NULL OR u.checked_at < NOW() - INTERVAL '6 hours';

-- name: SetServerConnection :execrows
UPDATE servers SET connection = $2, updated_at = NOW() WHERE id = $1;

-- name: SetServerSSHCredentials :execrows
-- Moving a server back to SSH with a new key or user (D-106).
UPDATE servers SET "user" = $2, port = $3, ssh_key = $4, updated_at = NOW() WHERE id = $1;

-- name: GetServerAgentKey :one
SELECT agent_key FROM servers WHERE id = $1;

-- name: CreateServerJoinToken :exec
INSERT INTO server_join_tokens (server_id, token_hash, replaces_key, created_by, expires_at)
VALUES ($1, $2, $3, $4, $5);

-- name: UseServerJoinToken :one
-- Single use: marks the token used only while it is unused and unexpired.
UPDATE server_join_tokens
SET used_at = NOW()
WHERE token_hash = $1 AND used_at IS NULL AND expires_at > NOW()
RETURNING server_id, replaces_key, created_by;

-- name: PeekServerJoinToken :one
-- Whether a token would still join (the install script's download).
SELECT server_id FROM server_join_tokens
WHERE token_hash = $1 AND used_at IS NULL AND expires_at > NOW();

-- name: ExpireServerJoinTokens :exec
-- A new token makes the server's older unused ones useless.
UPDATE server_join_tokens SET expires_at = NOW()
WHERE server_id = $1 AND used_at IS NULL AND expires_at > NOW();

-- name: DeleteUnusedServerJoinTokens :exec
-- A new token replaces the server's unused ones: a command that can never
-- join is no row to keep.
DELETE FROM server_join_tokens WHERE server_id = $1 AND used_at IS NULL;

-- name: PinServerAgentKey :execrows
-- A first join pins only while no key is pinned; a rejoin (replace) pins over it.
UPDATE servers
SET agent_key = $2, agent_next_key = NULL, agent_key_pinned_at = NOW(), agent_revoked_at = NULL, updated_at = NOW()
WHERE id = $1 AND (agent_key IS NULL OR sqlc.arg(replace)::boolean);

-- name: SetServerAgentNextKey :execrows
-- A rotation's new key, admitted beside the pinned one (D-107).
UPDATE servers SET agent_next_key = $2, updated_at = NOW()
WHERE id = $1 AND agent_key IS NOT NULL;

-- name: PromoteServerAgentNextKey :execrows
-- The agent presented the rotation's new key: pin it in place of the old.
UPDATE servers
SET agent_key = agent_next_key, agent_next_key = NULL, agent_key_pinned_at = NOW(), updated_at = NOW()
WHERE id = $1 AND agent_next_key = sqlc.arg(key)::text;

-- name: RevokeServerAgent :execrows
-- A revoked agent has no state to choose (D-113): its freeze ends with it.
UPDATE servers
SET agent_key = NULL, agent_next_key = NULL, agent_revoked_at = NOW(), updated_at = NOW(),
    agent_frozen_at = NULL, agent_frozen = NULL
WHERE id = $1 AND agent_key IS NOT NULL;

-- name: RecordAgentConnected :exec
UPDATE servers
SET agent_version = $2, agent_arch = $3, agent_boot_id = $4, agent_from_ip = $5,
    agent_connected_at = NOW(), agent_last_seen_at = NOW()
WHERE id = $1;

-- name: TouchAgentSeen :exec
UPDATE servers SET agent_last_seen_at = NOW() WHERE id = $1;

-- name: GetServerAgent :one
SELECT s.id, s.connection, s.agent_key, s.agent_next_key, s.agent_key_pinned_at, s.agent_version, s.agent_arch,
       s.agent_boot_id, s.agent_connected_at, s.agent_last_seen_at, s.agent_from_ip, s.agent_revoked_at,
       s.spec_generation, s.observed_generation,
       -- The drift less other workspaces' services: they show on this
       -- server only as what they reserve (ListOrgServicesOnServer).
       (SELECT COALESCE(jsonb_agg(d), '[]'::jsonb) FROM jsonb_array_elements(s.agent_drift) d
        WHERE NOT EXISTS (
          SELECT 1 FROM services sv
          LEFT JOIN environments e ON e.id = sv.environment_id
          LEFT JOIN projects p ON p.id = COALESCE(e.project_id, sv.project_id)
          WHERE sv.id::text = d->>'serviceId' AND COALESCE(p.org_id, sv.scoped_org_id) IS DISTINCT FROM s.org_id))::jsonb AS agent_drift,
       s.agent_drift_at, s.agent_routes, s.agent_frozen_at, s.agent_frozen,
       (SELECT t.expires_at FROM server_join_tokens t
         WHERE t.server_id = s.id AND t.used_at IS NULL AND t.expires_at > NOW()
         ORDER BY t.created_at DESC LIMIT 1)::timestamptz AS token_expires_at
FROM servers s
WHERE s.id = $1;

-- name: ListServerAgentsByOrg :many
SELECT s.id, s.connection, s.agent_key, s.agent_next_key, s.agent_key_pinned_at, s.agent_version, s.agent_arch,
       s.agent_boot_id, s.agent_connected_at, s.agent_last_seen_at, s.agent_from_ip, s.agent_revoked_at,
       s.spec_generation, s.observed_generation,
       -- The drift less other workspaces' services: they show on this
       -- server only as what they reserve (ListOrgServicesOnServer).
       (SELECT COALESCE(jsonb_agg(d), '[]'::jsonb) FROM jsonb_array_elements(s.agent_drift) d
        WHERE NOT EXISTS (
          SELECT 1 FROM services sv
          LEFT JOIN environments e ON e.id = sv.environment_id
          LEFT JOIN projects p ON p.id = COALESCE(e.project_id, sv.project_id)
          WHERE sv.id::text = d->>'serviceId' AND COALESCE(p.org_id, sv.scoped_org_id) IS DISTINCT FROM s.org_id))::jsonb AS agent_drift,
       s.agent_drift_at, s.agent_routes, s.agent_frozen_at, s.agent_frozen,
       (SELECT t.expires_at FROM server_join_tokens t
         WHERE t.server_id = s.id AND t.used_at IS NULL AND t.expires_at > NOW()
         ORDER BY t.created_at DESC LIMIT 1)::timestamptz AS token_expires_at
FROM servers s
WHERE s.org_id = $1;

-- name: GetAgentPanelKey :one
SELECT value FROM instance_settings WHERE key = 'agent_panel_key';

-- name: InsertAgentPanelKey :exec
-- First writer wins; a racing start reads the stored one back.
INSERT INTO instance_settings (key, value) VALUES ('agent_panel_key', $1)
ON CONFLICT (key) DO NOTHING;

-- name: CreateAgentServer :one
-- A server joined by token (D-106): no SSH key, an address the agent's
-- connection may fill in.
-- name_automatic: Add a server named it server-<n> (D-153).
INSERT INTO servers (org_id, user_id, name, ip_address, "user", port, ssh_key, status, connection, name_automatic)
VALUES ($1, $2, $3, $4, 'root', 22, '', 'provisioning', 'agent', $5)
RETURNING id;

-- name: SetServerAddressIfEmpty :exec
UPDATE servers SET ip_address = $2, updated_at = NOW() WHERE id = $1 AND ip_address = '';

-- name: EnsurePanelEpoch :one
-- The panel's epoch (D-111), made on its first start. install.sh --restore
-- deletes it, so a restored panel starts a new one.
WITH made AS (
    INSERT INTO instance_settings (key, value)
    VALUES ('panel_epoch', jsonb_build_object('id', gen_random_uuid()::text))
    ON CONFLICT (key) DO NOTHING
    RETURNING value
)
SELECT (m.value->>'id')::text AS id FROM made m
UNION ALL
SELECT (i.value->>'id')::text FROM instance_settings i WHERE i.key = 'panel_epoch'
LIMIT 1;

-- name: GetAgentGeneration :one
SELECT spec_generation, observed_generation FROM servers WHERE id = $1;

-- name: RecordAgentObserved :exec
-- The agent's answer to the desired state (D-111): the generation it holds,
-- never above the panel's, its drift and its routes (D-113). An answer under
-- the panel's epoch ends a freeze.
UPDATE servers
SET observed_generation = GREATEST(0, LEAST(sqlc.arg(generation)::bigint, spec_generation)),
    agent_drift = sqlc.arg(drift), agent_drift_at = NOW(), agent_routes = sqlc.narg(routes),
    agent_frozen_at = NULL, agent_frozen = NULL
WHERE id = sqlc.arg(id);

-- name: RecordAgentFrozen :exec
-- The agent holds another panel epoch's state (D-113): what runs there.
UPDATE servers
SET agent_frozen_at = COALESCE(agent_frozen_at, NOW()), agent_frozen = sqlc.arg(running),
    agent_routes = sqlc.narg(routes)
WHERE id = sqlc.arg(id);

-- name: GetAgentFrozen :one
-- What runs on a frozen server (NULL when it isn't).
SELECT agent_frozen FROM servers WHERE id = $1;

-- name: ListAgentServersFrozen :many
-- Servers that use an agent with a pinned key, and whether it is frozen:
-- the agent_frozen alert's.
SELECT id, (agent_frozen_at IS NOT NULL)::boolean AS frozen FROM servers WHERE connection = 'agent' AND agent_key IS NOT NULL;

-- name: ListAgentServerIDs :many
-- Servers that use an agent with a pinned key: the offline alert's.
SELECT id FROM servers WHERE connection = 'agent' AND agent_key IS NOT NULL;

-- name: ListServerSSHKeys :many
-- The stored SSH private keys, encrypted or, from before encryption, still
-- in plain text.
SELECT id, ssh_key FROM servers WHERE ssh_key <> '';

-- name: ReplaceServerSSHKeys :execrows
-- A row changes only while it still holds old_value, so a key saved in
-- between is kept.
UPDATE servers s SET ssh_key = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::uuid[]) AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE s.id = v.id AND s.ssh_key = v.old_value;

-- ─── Update runs (D-134) ─────────────────────────────────────────────────────

-- name: StartUpdateRun :execrows
-- Takes the server's run for a new one unless one is unfinished: 0 rows is
-- a run in progress.
INSERT INTO server_update_runs (server_id, run_id, action, started_by)
VALUES (sqlc.arg(server_id), sqlc.arg(run_id), sqlc.arg(action), sqlc.narg(started_by))
ON CONFLICT (server_id) DO UPDATE
SET run_id = EXCLUDED.run_id, action = EXCLUDED.action, started_by = EXCLUDED.started_by, started_at = NOW(),
    finished_at = NULL, outcome = 'running', packages = 0, error = '', output = ''
WHERE server_update_runs.finished_at IS NOT NULL;

-- name: FinishUpdateRun :execrows
-- Finishes the run once: 1 row is the caller's to write to the audit log.
UPDATE server_update_runs
SET finished_at = sqlc.arg(finished_at), outcome = sqlc.arg(outcome), packages = sqlc.arg(packages),
    error = left(sqlc.arg(error)::text, 2000), output = right(sqlc.arg(output)::text, 131072)
WHERE server_id = sqlc.arg(server_id) AND run_id = sqlc.arg(run_id) AND finished_at IS NULL;

-- name: GetUpdateRun :one
SELECT r.run_id, r.action, r.started_by, r.started_at, r.finished_at, r.outcome, r.packages, r.error, r.output,
       COALESCE(u.name, '')::text AS started_by_name
FROM server_update_runs r
LEFT JOIN users u ON u.id = r.started_by
WHERE r.server_id = $1;

-- name: ListUnfinishedUpdateRuns :many
SELECT server_id FROM server_update_runs WHERE finished_at IS NULL;

-- name: ServerUpdating :one
-- A Docker update or reboot runs on the server: its deploys wait.
SELECT EXISTS (
    SELECT 1 FROM server_update_runs
    WHERE server_id = $1 AND finished_at IS NULL AND action IN ('docker', 'reboot')
)::bool;

-- ── Add a server (D-153) ─────────────────────────────────────────────────────
-- "Waiting": an agent server no agent ever joined (no key pinned, none
-- revoked, no token used) and never provisioned.

-- name: NextAutomaticServerNumber :one
-- The n of the next automatic name, server-<n>, in the workspace.
SELECT (COALESCE(MAX(substring(name FROM '^server-([0-9]{1,9})$')::int), 0) + 1)::int AS n
FROM servers WHERE org_id = $1;

-- name: FindWaitingAgentServer :one
-- The caller's own newest waiting server Add a server made (an automatic
-- name: one made with a name is someone's command elsewhere), reused.
-- Touching updated_at keeps the sweep (DeleteUnjoinedServers) off it: a
-- delete already under way wins, and then no row comes back.
UPDATE servers SET updated_at = NOW()
WHERE servers.id = (
  SELECT w.id FROM servers w
  WHERE w.org_id = $1 AND w.user_id = $2 AND w.name_automatic AND w.connection = 'agent' AND w.agent_key IS NULL AND w.agent_revoked_at IS NULL AND w.provisioned_at IS NULL
    AND NOT EXISTS (SELECT 1 FROM server_join_tokens t WHERE t.server_id = w.id AND t.used_at IS NOT NULL)
  ORDER BY w.created_at DESC LIMIT 1)
RETURNING servers.id;

-- name: GetWaitingServerSSHKey :one
-- The SSH key of a waiting server ('' when none was made); no row when it
-- isn't waiting.
SELECT servers.ssh_key FROM servers WHERE servers.id = $1 AND servers.connection = 'agent' AND servers.agent_key IS NULL AND servers.agent_revoked_at IS NULL AND servers.provisioned_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM server_join_tokens t WHERE t.server_id = servers.id AND t.used_at IS NOT NULL);

-- name: SetWaitingServerSSHKey :execrows
-- The key Toft made for a waiting server, once.
UPDATE servers SET ssh_key = $2, updated_at = NOW()
WHERE servers.id = $1 AND servers.ssh_key = '' AND servers.connection = 'agent' AND servers.agent_key IS NULL AND servers.agent_revoked_at IS NULL AND servers.provisioned_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM server_join_tokens t WHERE t.server_id = servers.id AND t.used_at IS NOT NULL);

-- name: UseSSHForWaitingServer :execrows
-- A waiting server reached over SSH instead, as root (D-153).
UPDATE servers SET connection = 'ssh', ip_address = $2, port = $3, "user" = 'root', ssh_key = $4, updated_at = NOW()
WHERE servers.id = $1 AND servers.connection = 'agent' AND servers.agent_key IS NULL AND servers.agent_revoked_at IS NULL AND servers.provisioned_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM server_join_tokens t WHERE t.server_id = servers.id AND t.used_at IS NOT NULL);

-- name: RenameServer :execrows
UPDATE servers SET name = $2, name_automatic = false, updated_at = NOW() WHERE id = $1;

-- name: NameServerFromHostname :execrows
-- The hostname replaces the automatic name, only while it is automatic.
UPDATE servers SET name = $2, name_automatic = false, updated_at = NOW() WHERE id = $1 AND name_automatic;

-- name: ServerNameAutomatic :one
SELECT name_automatic FROM servers WHERE id = $1;

-- name: LastJoinTokenCreator :one
-- Who made the command the server's agent joined with, in the last hour:
-- a join from before (the old Add a server provisioned on a click) isn't
-- provisioned by a reconnect.
SELECT created_by FROM server_join_tokens
WHERE server_join_tokens.server_id = $1 AND server_join_tokens.used_at > NOW() - INTERVAL '1 hour'
ORDER BY server_join_tokens.used_at DESC LIMIT 1;

-- name: DeleteUnjoinedServers :many
-- Waiting servers Add a server made (an automatic name) whose last
-- command expired over 24 hours ago, untouched since, with nothing on
-- them (services would fall back to the panel's server).
DELETE FROM servers
WHERE servers.name_automatic AND servers.updated_at < NOW() - INTERVAL '25 hours' AND servers.connection = 'agent' AND servers.agent_key IS NULL AND servers.agent_revoked_at IS NULL AND servers.provisioned_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM server_join_tokens t WHERE t.server_id = servers.id AND t.used_at IS NOT NULL)
  AND servers.created_at < NOW() - INTERVAL '25 hours'
  AND NOT EXISTS (SELECT 1 FROM server_join_tokens t WHERE t.server_id = servers.id AND t.expires_at > NOW() - INTERVAL '24 hours')
  AND NOT EXISTS (SELECT 1 FROM services v WHERE v.server_id = servers.id)
  AND NOT EXISTS (SELECT 1 FROM shared_volumes v WHERE v.server_id = servers.id)
RETURNING servers.id, servers.org_id, servers.name;
