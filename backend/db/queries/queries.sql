-- =============================================================================
-- sqlc Query Definitions for control-panel-paas
-- Run: sqlc generate  (from backend/)
-- =============================================================================

-- name: CreateUser :one
INSERT INTO users (email, name, password_hash)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users
WHERE email = $1 LIMIT 1;

-- name: GetUserByID :one
SELECT * FROM users
WHERE id = $1 LIMIT 1;

-- name: UpdateLastLogin :exec
UPDATE users
SET last_login_at = NOW()
WHERE id = $1;

-- name: UpdateUserProfile :one
UPDATE users
SET name = $2, updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdateUserPassword :exec
UPDATE users
SET password_hash = $2, tokens_valid_from = NOW(), updated_at = NOW()
WHERE id = $1;

-- name: UpdateUserTheme :exec
UPDATE users SET theme = $2, updated_at = NOW() WHERE id = $1;

-- name: RevokeUserSessions :exec
-- "Sign out other browsers": every session token issued before now is
-- refused, as after a password change.
UPDATE users SET tokens_valid_from = NOW(), updated_at = NOW() WHERE id = $1;

-- name: RevokeSession :exec
-- Signs one session token out; the expired ones go at the same time.
WITH gone AS (DELETE FROM revoked_sessions WHERE expires_at < NOW())
INSERT INTO revoked_sessions (token_hash, expires_at) VALUES ($1, $2)
ON CONFLICT (token_hash) DO NOTHING;

-- name: GetUserIsActive :one
SELECT is_active FROM users
WHERE id = $1 LIMIT 1;

-- name: GetRequestIdentity :one
-- Everything the auth middleware needs per request in one round trip: the
-- account's state, the moment before which its session tokens are refused,
-- and its role in the workspace the request works in: org_id when the
-- request names one (X-Toft-Org, D-52), else the one the account last
-- switched to. The role is NULL when there is none or the user is not a
-- member of it; the middleware then refuses a named workspace, and falls
-- back to the earliest membership otherwise (org.Service.GetForUser).
-- session_revoked: the session token with this sha256 was signed out.
SELECT u.is_active, u.tokens_valid_from,
       COALESCE(sqlc.narg(org_id)::uuid, u.active_org_id)::uuid AS org_id, m.role,
       EXISTS (SELECT 1 FROM revoked_sessions r WHERE r.token_hash = sqlc.narg(session_hash)::text)::bool AS session_revoked
FROM users u
LEFT JOIN org_members m ON m.org_id = COALESCE(sqlc.narg(org_id)::uuid, u.active_org_id) AND m.user_id = u.id
WHERE u.id = sqlc.arg(id) LIMIT 1;

-- ─── Users ────────────────────────────────────────────────────────────────────

-- name: FetchUserByID :one
SELECT * FROM users WHERE id = $1 LIMIT 1;

-- name: FetchUserByEmail :one
SELECT * FROM users WHERE email = $1 LIMIT 1;

-- name: SaveNewUser :one
INSERT INTO users (email, name, password_hash, role)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateUserLastLogin :exec
UPDATE users SET last_login_at = NOW() WHERE id = $1;

-- name: DeactivateUser :exec
UPDATE users SET is_active = FALSE, updated_at = NOW() WHERE id = $1;

-- ─── Projects ─────────────────────────────────────────────────────────────────

-- name: ListAllProjectsForUser :many
SELECT * FROM projects
WHERE user_id = $1
ORDER BY updated_at DESC;

-- name: FetchProjectByID :one
SELECT * FROM projects WHERE id = $1 LIMIT 1;

-- name: ListOrgProjects :many
-- A workspace's projects, last changed first, with their open issues and
-- open pull requests (D-81), and their pages (D-86).
SELECT p.id, p.org_id, p.user_id, p.name, p.description, p.icon, p.created_at, p.updated_at,
       COALESCE(p.issue_key, '')::text AS issue_key,
       (SELECT count(*) FROM issues i WHERE i.project_id = p.id AND i.status NOT IN ('shipped', 'cancelled')) AS open_issues,
       (SELECT count(*) FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
        WHERE r.project_id = p.id AND pr.state = 'open') AS open_pulls,
       (SELECT count(*) FROM doc_pages d WHERE d.project_id = p.id AND d.deleted_at IS NULL) AS doc_pages
FROM projects p
WHERE p.org_id = $1
ORDER BY p.updated_at DESC;

-- name: GetProjectWorkCounts :one
SELECT COALESCE(p.issue_key, '')::text AS issue_key,
       (SELECT count(*) FROM issues i WHERE i.project_id = p.id AND i.status NOT IN ('shipped', 'cancelled')) AS open_issues,
       (SELECT count(*) FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
        WHERE r.project_id = p.id AND pr.state = 'open') AS open_pulls,
       (SELECT count(*) FROM doc_pages d WHERE d.project_id = p.id AND d.deleted_at IS NULL) AS doc_pages
FROM projects p
WHERE p.id = $1;

-- name: CreateProject :one
INSERT INTO projects (org_id, user_id, name, description, icon)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UpdateProject :one
UPDATE projects
SET name = $2, description = $3, icon = $4, updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteProject :exec
-- Its services go by cascade: their servers' spec_generation rises (D-111).
WITH bumped AS (
    UPDATE servers v SET spec_generation = v.spec_generation + 1
    WHERE v.id IN (SELECT s.server_id FROM services s WHERE s.project_id = $1
                   OR s.environment_id IN (SELECT e.id FROM environments e WHERE e.project_id = $1))
)
DELETE FROM projects p WHERE p.id = $1;

-- ─── Services ─────────────────────────────────────────────────────────────────

-- name: ListAllServicesForProject :many
SELECT * FROM services
WHERE project_id = $1
ORDER BY created_at DESC;

-- name: FetchServiceByID :one
SELECT * FROM services WHERE id = $1 LIMIT 1;

-- name: SaveNewService :one
INSERT INTO services (
    project_id, name, type, source_type,
    git_repo_url, git_branch, base_directory,
    docker_image,
    install_command, build_command, start_command, build_pack,
    port, replicas, cpu_limit, memory_limit
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
RETURNING *;

-- name: UpdateServiceConfig :one
UPDATE services
SET
    name            = $2,
    git_repo_url    = $3,
    git_branch      = $4,
    base_directory  = $5,
    docker_image    = $6,
    install_command = $7,
    build_command   = $8,
    start_command   = $9,
    build_pack      = $10,
    port            = $11,
    replicas        = $12,
    cpu_limit       = $13,
    memory_limit    = $14,
    updated_at      = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdateServiceRuntimeStatus :exec
-- A change to what runs raises its server's spec_generation (D-111).
WITH changed AS (
    SELECT s.server_id FROM services s
    WHERE s.id = sqlc.arg(id)
      AND (s.runtime_status IS DISTINCT FROM sqlc.arg(runtime_status)::text
           OR s.active_deployment_id IS DISTINCT FROM sqlc.narg(active_deployment_id)::uuid)
), updated AS (
    UPDATE services s
    SET runtime_status = sqlc.arg(runtime_status), active_deployment_id = sqlc.narg(active_deployment_id), updated_at = NOW()
    WHERE s.id = sqlc.arg(id)
)
UPDATE servers v SET spec_generation = v.spec_generation + 1 WHERE v.id IN (SELECT server_id FROM changed);

-- name: ListDesiredServices :many
-- The services on a server, as its agent's desired state (D-111), each
-- with the Loki its agent ships its logs to (D-113): its project's, else
-- its workspace's, as GetLokiFor picks (observability.LokiTarget.ShipTo
-- decides from these).
SELECT s.id, s.source_type, s.runtime_status, s.active_deployment_id,
       COALESCE(l.loki_url, '')::text AS loki_url, l.loki_service_id, l.loki_server_id, l.loopback_port
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
LEFT JOIN LATERAL (
    SELECT o.loki_url, ls.id AS loki_service_id, ls.server_id AS loki_server_id, ls.loopback_port
    FROM observability_configs o
    LEFT JOIN services ls ON ls.id = o.loki_service_id
    WHERE o.org_id = COALESCE(p.org_id, s.scoped_org_id)
      AND (o.project_id = p.id OR o.project_id IS NULL)
      AND (COALESCE(o.loki_url, '') <> '' OR ls.loopback_port IS NOT NULL)
    ORDER BY o.project_id NULLS LAST
    LIMIT 1
) l ON true
WHERE s.server_id = $1
ORDER BY s.id;

-- name: DeleteService :exec
-- Raises its server's spec_generation (D-111).
WITH deleted AS (
    DELETE FROM services s WHERE s.id = $1 RETURNING s.server_id
)
UPDATE servers v SET spec_generation = v.spec_generation + 1 WHERE v.id IN (SELECT server_id FROM deleted);

-- name: ListRunningServicesForMetrics :many
-- The services the metrics collector samples, which it groups by server
-- (a NULL server_id is the control plane's own Docker host). routed: it has
-- a domain, so its server's Traefik measures its requests (D-156).
SELECT id, name, server_id, source_type, port,
       health_check_path, health_check_timeout, health_check_retries, container_health,
       (preview_of IS NOT NULL)::bool AS preview,
       EXISTS (SELECT 1 FROM domains d WHERE d.service_id = services.id)::bool AS routed
FROM services
WHERE runtime_status = 'running'
ORDER BY server_id NULLS FIRST, id;

-- ─── Domains ──────────────────────────────────────────────────────────────────

-- name: ListDomainsForService :many
-- A service's domains, oldest first, with the name of who added each.
SELECT d.id, d.service_id, d.hostname, d.tls_enabled, d.status, d.created_at, d.updated_at,
       d.routed_at, d.certificate_at, COALESCE(u.name, '')::text AS created_by_name
FROM domains d
LEFT JOIN users u ON u.id = d.created_by
WHERE d.service_id = $1
ORDER BY d.hostname LIKE '%.sslip.io', d.created_at ASC; -- a person's own domains before a temporary one (D-150)

-- name: ListOrgDomains :many
-- Every domain of a workspace's services, for the Domains page (D-48):
-- environment services by their project's workspace, project and
-- workspace services by their own. server_id is NULL on the panel host.
SELECT d.id, d.hostname, d.created_at, d.routed_at, d.certificate_at, COALESCE(u.name, '')::text AS created_by_name,
       s.id AS service_id, s.name AS service_name, s.scope, s.server_id,
       COALESCE(e.project_id, s.project_id) AS project_id,
       COALESCE(p.name, '')::text AS project_name,
       COALESCE(e.name, '')::text AS environment_name
FROM domains d
JOIN services s ON s.id = d.service_id
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
LEFT JOIN users u ON u.id = d.created_by
WHERE COALESCE(p.org_id, s.scoped_org_id) = sqlc.arg(org_id)::uuid
ORDER BY p.name NULLS LAST, s.name, d.hostname;

-- name: FetchDomainByHostname :one
SELECT * FROM domains WHERE hostname = $1 LIMIT 1;

-- name: SaveNewDomain :one
INSERT INTO domains (service_id, hostname, tls_enabled, created_by)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListDomainsAwaitingCertificate :many
-- The routed names no valid certificate has answered for yet (D-150), for
-- the certificate check; wildcards get theirs over DNS and are left out.
SELECT id, service_id, hostname FROM domains
WHERE routed_at IS NOT NULL AND certificate_at IS NULL AND hostname NOT LIKE '*.%'
ORDER BY routed_at;

-- name: SetDomainCertificate :execrows
-- 1 row the first time only: whoever records it (the 5-minute check or a
-- person's Check) is the one that has the service redeployed (D-150).
UPDATE domains SET certificate_at = NOW(), updated_at = NOW() WHERE id = $1 AND certificate_at IS NULL;

-- name: HostnameTaken :one
SELECT EXISTS (SELECT 1 FROM domains WHERE hostname = $1)::bool;

-- name: RenameTemporaryDomains :execrows
-- A moved service's sslip.io names under its old server's address follow
-- it to the new one's (suffixes ".a-b-c-d.sslip.io", domain.MoveTemporary,
-- D-150). The move's deploy routed the new names; each waits for its
-- certificate again.
UPDATE domains
SET hostname = left(hostname, -length(sqlc.arg(old_suffix)::text)) || sqlc.arg(new_suffix)::text,
    certificate_at = NULL, updated_at = NOW()
WHERE service_id = sqlc.arg(service_id)
  AND right(hostname, length(sqlc.arg(old_suffix)::text)) = sqlc.arg(old_suffix)::text;

-- name: UpdateDomainStatus :exec
UPDATE domains SET status = $2, updated_at = NOW() WHERE id = $1;

-- name: DeleteDomain :one
-- Only the service's own domain: no row when the ID is another service's.
DELETE FROM domains WHERE id = $1 AND service_id = $2 RETURNING hostname;

-- name: ActivateDomainsForService :exec
-- After a successful deploy: every domain of the service is routed, and the
-- first such deploy is when it started.
UPDATE domains
SET status = 'verified', routed_at = COALESCE(routed_at, NOW()), updated_at = NOW()
WHERE service_id = $1;

-- ─── Environment Variables ────────────────────────────────────────────────────

-- name: ListEnvVarsForService :many
-- A service's variables and secrets by key, with the name of who set each
-- (secret values come back encrypted).
SELECT e.id, e.service_id, e.key, e.value, e.is_secret, e.masked, e.created_at, e.updated_at,
       COALESCE(u.name, '')::text AS updated_by_name, COALESCE(ac.name, '')::text AS updated_by_agent
FROM env_vars e
LEFT JOIN users u ON u.id = e.updated_by
-- The agent's person must be who set it: a row a release without the
-- column changed keeps a stale agent (0079).
LEFT JOIN agent_connections ac ON ac.id = e.agent_connection_id AND ac.user_id = e.updated_by
WHERE e.service_id = $1
ORDER BY e.key ASC;

-- name: UpsertEnvVar :one
-- Sets a plain variable. Its updated_at and updated_by change only when its
-- value or mask does, so they say when it last changed. It never touches a
-- secret with the same key: then no row is returned.
INSERT INTO env_vars (service_id, key, value, is_secret, masked, updated_by, agent_connection_id)
VALUES ($1, $2, $3, false, $4, $5, $6)
ON CONFLICT (service_id, key) DO UPDATE SET
    value      = EXCLUDED.value,
    masked     = EXCLUDED.masked,
    updated_at = CASE WHEN (env_vars.value, env_vars.masked) IS DISTINCT FROM (EXCLUDED.value, EXCLUDED.masked)
                      THEN NOW() ELSE env_vars.updated_at END,
    updated_by = CASE WHEN (env_vars.value, env_vars.masked) IS DISTINCT FROM (EXCLUDED.value, EXCLUDED.masked)
                      THEN EXCLUDED.updated_by ELSE env_vars.updated_by END,
    agent_connection_id = CASE WHEN (env_vars.value, env_vars.masked) IS DISTINCT FROM (EXCLUDED.value, EXCLUDED.masked)
                      THEN EXCLUDED.agent_connection_id ELSE env_vars.agent_connection_id END
WHERE env_vars.is_secret = false
RETURNING id, service_id, key, value, is_secret, masked, created_at, updated_at;

-- name: SwapEnvVar :exec
-- Sets a service's own plain variable to value only while it is still
-- from_value (deployment.followLive); it never creates one, nor touches a
-- secret or the environment's.
UPDATE env_vars SET value = sqlc.arg(value), updated_at = NOW(), updated_by = NULL, agent_connection_id = NULL
WHERE service_id = sqlc.arg(service_id) AND key = sqlc.arg(key) AND is_secret = false
  AND value = sqlc.arg(from_value)::text;

-- name: UnsetEnvVar :exec
-- Deletes a service's own plain variable only while it is still value (a
-- Deploy a version that started nothing); never a secret.
DELETE FROM env_vars
WHERE service_id = sqlc.arg(service_id) AND key = sqlc.arg(key) AND is_secret = false
  AND value = sqlc.arg(value)::text;

-- name: DeleteEnvVar :exec
DELETE FROM env_vars WHERE service_id = $1 AND key = $2;

-- name: LockServiceForReplace :one
-- A replace's first step. FOR UPDATE conflicts with the FOR KEY SHARE that a
-- new variable's foreign key takes, so no key can appear between the
-- replace's check and its delete.
SELECT id FROM services WHERE id = $1 FOR UPDATE;

-- name: ListPlainEnvVarsForReplace :many
-- What a replace can delete or overwrite, locked until it commits; its
-- version is computed from these.
SELECT key, updated_at FROM env_vars
WHERE service_id = $1 AND is_secret = false
ORDER BY key
FOR UPDATE;

-- name: DeleteEnvVarsExcept :exec
-- Deletes the service's plain variables whose key is not in keys: what a
-- save of the whole list removed.
DELETE FROM env_vars
WHERE service_id = sqlc.arg(service_id) AND is_secret = false AND NOT (key = ANY(sqlc.arg(keys)::text[]));

-- name: DeleteAllEnvVarsForService :exec
DELETE FROM env_vars WHERE service_id = $1;

-- name: CreateEnvVarSecret :one
-- Write-once: an existing key (secret or plain) is a unique violation.
INSERT INTO env_vars (service_id, key, value, is_secret, updated_by, agent_connection_id)
VALUES ($1, $2, $3, true, $4, $5)
RETURNING id, service_id, key, created_at, updated_at;

-- name: UpdateEnvVarSecret :execrows
UPDATE env_vars SET value = $3, updated_at = NOW(), updated_by = $4, agent_connection_id = $5
WHERE service_id = $1 AND key = $2 AND is_secret = true;

-- name: DeleteEnvVarSecret :execrows
DELETE FROM env_vars WHERE service_id = $1 AND key = $2 AND is_secret = true;

-- ─── Environment (shared) variables ──────────────────────────────────────────

-- name: ListEnvironmentEnvVars :many
SELECT e.id, e.environment_id, e.key, e.value, e.is_secret, e.created_at, e.updated_at,
       COALESCE(u.name, '')::text AS updated_by_name, COALESCE(ac.name, '')::text AS updated_by_agent
FROM environment_env_vars e
LEFT JOIN users u ON u.id = e.updated_by
-- The agent's person must be who set it: a row a release without the
-- column changed keeps a stale agent (0079).
LEFT JOIN agent_connections ac ON ac.id = e.agent_connection_id AND ac.user_id = e.updated_by
WHERE e.environment_id = $1
ORDER BY e.key ASC;

-- UpsertEnvironmentEnvVar sets a plain variable, changing updated_at and
-- updated_by only when its value does. It never touches a secret with the
-- same key: then no row is returned.
-- name: UpsertEnvironmentEnvVar :one
INSERT INTO environment_env_vars (environment_id, key, value, is_secret, updated_by, agent_connection_id)
VALUES ($1, $2, $3, false, $4, $5)
ON CONFLICT (environment_id, key) DO UPDATE SET
    value      = EXCLUDED.value,
    updated_at = CASE WHEN environment_env_vars.value IS DISTINCT FROM EXCLUDED.value
                      THEN NOW() ELSE environment_env_vars.updated_at END,
    updated_by = CASE WHEN environment_env_vars.value IS DISTINCT FROM EXCLUDED.value
                      THEN EXCLUDED.updated_by ELSE environment_env_vars.updated_by END,
    agent_connection_id = CASE WHEN environment_env_vars.value IS DISTINCT FROM EXCLUDED.value
                      THEN EXCLUDED.agent_connection_id ELSE environment_env_vars.agent_connection_id END
WHERE environment_env_vars.is_secret = false
RETURNING id, environment_id, key, value, is_secret, created_at, updated_at;

-- name: DeleteEnvironmentEnvVar :execrows
DELETE FROM environment_env_vars
WHERE environment_id = $1 AND key = $2 AND is_secret = false;

-- name: LockEnvironmentForReplace :one
-- As LockServiceForReplace, for an environment's variables.
SELECT id FROM environments WHERE id = $1 FOR UPDATE;

-- name: ListPlainEnvironmentEnvVarsForReplace :many
-- As ListPlainEnvVarsForReplace, for an environment's variables.
SELECT key, updated_at FROM environment_env_vars
WHERE environment_id = $1 AND is_secret = false
ORDER BY key
FOR UPDATE;

-- name: DeleteEnvironmentEnvVarsExcept :exec
-- Deletes the environment's plain variables whose key is not in keys.
DELETE FROM environment_env_vars
WHERE environment_id = sqlc.arg(environment_id) AND is_secret = false AND NOT (key = ANY(sqlc.arg(keys)::text[]));

-- CreateEnvironmentSecret is write-once: an existing key (secret or plain)
-- is a unique violation.
-- name: CreateEnvironmentSecret :one
INSERT INTO environment_env_vars (environment_id, key, value, is_secret, updated_by, agent_connection_id)
VALUES ($1, $2, $3, true, $4, $5)
RETURNING id, environment_id, key, value, is_secret, created_at, updated_at;

-- name: UpdateEnvironmentSecret :execrows
UPDATE environment_env_vars SET value = $3, updated_at = NOW(), updated_by = $4, agent_connection_id = $5
WHERE environment_id = $1 AND key = $2 AND is_secret = true;

-- name: DeleteEnvironmentSecret :execrows
DELETE FROM environment_env_vars
WHERE environment_id = $1 AND key = $2 AND is_secret = true;

-- ListEnvVarsForDeployment returns a service's variables over its
-- environment's: for a key set on both, the service's row wins. Secret
-- values come back encrypted; environment_id is set on the environment's
-- rows, whose secrets are sealed for that environment (D-126).
-- name: ListEnvVarsForDeployment :many
SELECT DISTINCT ON (v.key) v.key, v.value, v.is_secret, v.environment_id
FROM (
    SELECT ev.key, ev.value, ev.is_secret, NULL::uuid AS environment_id, 0 AS precedence
    FROM env_vars ev
    WHERE ev.service_id = $1
    UNION ALL
    SELECT ee.key, ee.value, ee.is_secret, ee.environment_id, 1 AS precedence
    FROM environment_env_vars ee
    JOIN services s ON s.environment_id = ee.environment_id
    WHERE s.id = $1
) v
ORDER BY v.key, v.precedence;

-- ─── Deployments ──────────────────────────────────────────────────────────────

-- name: CreateDeployment :one
-- git_event_id is the push or merge that started it (D-77): one deployment
-- per service and event (uq_deployments_git_event).
INSERT INTO deployments (service_id, status, commit_sha, commit_message, triggered_by, queued_at, git_event_id)
VALUES ($1, 'pending', $2, $3, $4, NOW(), sqlc.narg(git_event_id))
RETURNING *;

-- name: SetDeploymentSettingsFingerprint :exec
-- What a deploy that went live shipped of its service's settings (D-159).
UPDATE deployments SET settings_fingerprint = sqlc.narg(settings_fingerprint) WHERE id = $1;

-- name: SetDeploymentReservedMemory :exec
-- The memory a deploy that went live reserved on its server, in MiB (D-166).
UPDATE deployments SET reserved_memory_mb = sqlc.arg(reserved_memory_mb)::float8 WHERE id = $1;

-- name: ListDeploymentPulls :many
-- The pull requests whose merges started these deployments (D-77).
SELECT d.id AS deployment_id, p.number, p.repository_id
FROM deployments d
JOIN git_events e ON e.id = d.git_event_id
JOIN pull_requests p ON p.id = e.pull_request_id
WHERE d.id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListDeploymentIssues :many
-- The issues linked to those pull requests (D-81): what the deploy ships.
SELECT d.id AS deployment_id, COALESCE(pj.issue_key, '')::text AS issue_key, i.number, i.title, i.status,
       COALESCE(pj.issue_key_digits, o.issue_key_digits)::int AS digits
FROM deployments d
JOIN git_events e ON e.id = d.git_event_id
JOIN issue_links l ON l.pull_request_id = e.pull_request_id
JOIN issues i ON i.id = l.issue_id
JOIN projects pj ON pj.id = i.project_id
JOIN organizations o ON o.id = pj.org_id
WHERE d.id = ANY(sqlc.arg(ids)::uuid[])
ORDER BY i.number;

-- name: UpdateDeploymentStatus :one
-- started_at is kept when the argument is NULL: only the move to building
-- sets it, and the terminal statuses that follow must not clear it. So is
-- commit_sha: a deploy started without one never records one.
UPDATE deployments
SET
    status          = sqlc.arg(status),
    image_tag       = sqlc.narg(image_tag),
    commit_sha      = COALESCE(sqlc.narg(commit_sha)::text, commit_sha),
    started_at      = COALESCE(sqlc.narg(started_at)::timestamptz, started_at),
    finished_at     = sqlc.narg(finished_at),
    error_summary   = sqlc.narg(error_summary)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetDeploymentComposeCommit :exec
-- The commit a compose deploy checked its repository out at, recorded right
-- after the checkout.
UPDATE deployments
SET compose_commit_sha = sqlc.arg(compose_commit_sha)::text
WHERE id = sqlc.arg(id);

-- name: SetDeploymentCommit :exec
-- The commit a git build checked out and its subject, recorded right after
-- the clone. They replace a push's or an API call's: the branch may have
-- moved since.
UPDATE deployments
SET commit_sha     = sqlc.arg(commit_sha)::text,
    commit_message = NULLIF(sqlc.arg(commit_message)::text, '')
WHERE id = sqlc.arg(id);

-- name: ListRecentDeploymentsForService :many
SELECT * FROM deployments
WHERE service_id = $1
ORDER BY created_at DESC
LIMIT 25;

-- name: FetchDeploymentByID :one
SELECT * FROM deployments WHERE id = $1 LIMIT 1;

-- name: CancelPendingDeployments :exec
UPDATE deployments
SET status = 'cancelled', finished_at = NOW()
WHERE service_id = $1 AND status IN ('pending', 'queued');

-- name: MarkInterruptedDeployments :many
-- Startup recovery, phase 1: every deployment a restart left in progress is
-- marked failed, except exclude_ids (pipelines running in this process) and
-- those handed to a server's agent (D-111), which the agent finishes.
-- started_at is kept. A NULL exclude_ids excludes nothing.
UPDATE deployments
SET status        = 'failed',
    finished_at   = NOW(),
    error_summary = sqlc.arg(error_summary)::text
WHERE status IN ('pending', 'queued', 'building')
  AND handed_off_at IS NULL
  AND NOT (id = ANY(COALESCE(sqlc.arg(exclude_ids)::uuid[], '{}'::uuid[])))
RETURNING *;

-- name: MarkDeploymentHandedOff :exec
-- The rollout of image_tag goes to the server's agent (D-111), recorded
-- before the request is sent.
UPDATE deployments
SET handed_off_at    = NOW(),
    handed_off_input = sqlc.arg(handed_off_input)::jsonb,
    image_tag        = sqlc.arg(image_tag)::text
WHERE id = sqlc.arg(id) AND status IN ('pending', 'queued', 'building');

-- name: ClearDeploymentHandOff :exec
-- The panel has the agent's result and finishes the deploy itself: a
-- restart from here is an interrupted deploy again.
UPDATE deployments SET handed_off_at = NULL, handed_off_input = NULL WHERE id = $1;

-- name: IsDeploymentHandedOff :one
SELECT (handed_off_at IS NOT NULL)::bool AS handed_off FROM deployments WHERE id = $1;

-- name: ListHandedOffDeployments :many
-- Deploys a server's agent runs that a restart left in progress (D-111).
SELECT * FROM deployments
WHERE status IN ('pending', 'queued', 'building') AND handed_off_at IS NOT NULL
ORDER BY created_at;

-- name: CountAgentLogLines :one
-- How many of the agent's log lines a handed-off deploy has: the lines
-- written since the hand-off, less the panel's own (panel_prefix).
SELECT count(*) FROM deployment_logs l
JOIN deployments d ON d.id = l.deployment_id
WHERE l.deployment_id = sqlc.arg(id)
  AND l.created_at >= d.handed_off_at
  AND NOT starts_with(l.message, sqlc.arg(panel_prefix)::text);

-- name: RecordDeploymentRecovery :exec
-- Startup recovery, phase 2: what was found on the server. image_tag is kept
-- when the argument is NULL.
UPDATE deployments
SET image_tag     = COALESCE(sqlc.narg(image_tag)::text, image_tag),
    error_summary = sqlc.arg(error_summary)::text
WHERE id = sqlc.arg(id);

-- name: ListUnreconciledInterruptedDeployments :many
-- Startup recovery: deployments an earlier start marked interrupted whose
-- phase 2 never finished (a shutdown or crash cut it short), so their error
-- summary is still exactly the marker (every finished check appends what it
-- found), and that are still their service's newest deployment, as
-- LatestDeploymentIDForService counts it.
SELECT * FROM deployments d
WHERE d.status = 'failed'
  AND d.error_summary = sqlc.arg(error_summary)::text
  AND d.id = (
    SELECT l.id FROM deployments l
    WHERE l.service_id = d.service_id
      AND NOT (l.status = 'failed' AND l.started_at IS NULL)
    ORDER BY l.created_at DESC
    LIMIT 1
  );

-- name: LatestDeploymentIDForService :one
-- Startup recovery: the service's newest deployment. One that failed before
-- it started (refused, or interrupted while it waited for the deploy ahead
-- of it) changed nothing on the server and doesn't count.
SELECT id FROM deployments
WHERE service_id = $1
  AND NOT (status = 'failed' AND started_at IS NULL)
ORDER BY created_at DESC
LIMIT 1;

-- ─── Deployment Logs ──────────────────────────────────────────────────────────

-- name: InsertDeploymentLog :one
INSERT INTO deployment_logs (deployment_id, level, message)
VALUES ($1, $2, $3)
RETURNING *;

-- name: FetchAllLogsForDeployment :many
SELECT * FROM deployment_logs
WHERE deployment_id = $1
ORDER BY created_at ASC;

-- name: FetchLogsAfterID :many
SELECT * FROM deployment_logs
WHERE deployment_id = $1 AND id > $2
ORDER BY id ASC;

-- ─── Project webhooks ─────────────────────────────────────────────────────────

-- name: GetProjectWebhookSecret :one
SELECT secret FROM project_webhooks WHERE project_id = $1;

-- name: EnsureProjectWebhookSecret :one
-- Stores secret unless the project has one, and returns the one it has: two
-- first requests at once get the same secret.
INSERT INTO project_webhooks (project_id, secret) VALUES ($1, $2)
ON CONFLICT (project_id) DO UPDATE SET secret = project_webhooks.secret
RETURNING secret;

-- name: GetProjectWebhookLastPush :one
SELECT last_push_at, COALESCE(last_push_repo, '')::text AS last_push_repo FROM project_webhooks WHERE project_id = $1;

-- name: MarkProjectWebhookPushed :exec
UPDATE project_webhooks SET last_push_at = NOW(), last_push_repo = sqlc.narg(repo) WHERE project_id = sqlc.arg(project_id);

-- name: ReplaceProjectWebhookSecret :exec
INSERT INTO project_webhooks (project_id, secret) VALUES ($1, $2)
ON CONFLICT (project_id) DO UPDATE SET secret = EXCLUDED.secret, updated_at = NOW();

-- ─── Shared volumes ───────────────────────────────────────────────────────────

-- name: ListSharedVolumes :many
-- A workspace's shared volumes with the server's name and how many
-- services mount each.
SELECT v.id, v.org_id, v.server_id, v.name, v.size_limit_bytes, v.used_bytes, v.used_at, v.created_at, v.updated_at,
       COALESCE(srv.name, '')::text AS server_name
FROM shared_volumes v
LEFT JOIN servers srv ON srv.id = v.server_id
WHERE v.org_id = $1
ORDER BY v.name;

-- name: GetSharedVolume :one
SELECT id, org_id, server_id, name, size_limit_bytes, used_bytes, used_at, created_at, updated_at
FROM shared_volumes WHERE id = $1 AND org_id = $2;

-- name: CreateSharedVolume :one
INSERT INTO shared_volumes (org_id, server_id, name, size_limit_bytes)
VALUES ($1, $2, $3, $4)
RETURNING id, org_id, server_id, name, size_limit_bytes, used_bytes, used_at, created_at, updated_at;

-- name: UpdateSharedVolumeLimit :one
UPDATE shared_volumes SET size_limit_bytes = $3, updated_at = NOW()
WHERE id = $1 AND org_id = $2
RETURNING id, org_id, server_id, name, size_limit_bytes, used_bytes, used_at, created_at, updated_at;

-- name: DeleteSharedVolume :execrows
DELETE FROM shared_volumes WHERE id = $1 AND org_id = $2;

-- name: ListSharedVolumeMounts :many
-- Every mount of a workspace's volumes, with the mounting service's name,
-- for the Storage page.
SELECT m.shared_volume_id, m.service_id, s.name AS service_name, m.target, m.read_only
FROM service_shared_volumes m
JOIN shared_volumes v ON v.id = m.shared_volume_id
JOIN services s ON s.id = m.service_id
WHERE v.org_id = $1
ORDER BY s.name, m.target;

-- name: ListServiceSharedMounts :many
-- A service's mounts, with each volume's server, for its settings and its
-- deploys.
SELECT m.shared_volume_id, v.name, v.org_id, v.server_id, m.target, m.read_only
FROM service_shared_volumes m
JOIN shared_volumes v ON v.id = m.shared_volume_id
WHERE m.service_id = $1
ORDER BY m.target;

-- name: ListServiceMountsForReplace :many
-- What a replace of a service's mounts can delete, locked until it commits;
-- its version is computed from these. After LockServiceForReplace.
SELECT shared_volume_id, target, read_only FROM service_shared_volumes
WHERE service_id = $1
ORDER BY target
FOR UPDATE;

-- name: DeleteServiceSharedMounts :exec
DELETE FROM service_shared_volumes WHERE service_id = $1;

-- name: InsertServiceSharedMount :exec
INSERT INTO service_shared_volumes (service_id, shared_volume_id, target, read_only)
VALUES ($1, $2, $3, $4);

-- name: ListAllSharedVolumes :many
-- Every shared volume, for the usage check.
SELECT id, org_id, server_id, name, size_limit_bytes FROM shared_volumes ORDER BY server_id NULLS FIRST, id;

-- name: RecordSharedVolumeUsage :exec
UPDATE shared_volumes SET used_bytes = $2, used_at = NOW() WHERE id = $1;

-- name: GetSharedVolumeAlertContext :one
SELECT name, org_id FROM shared_volumes WHERE id = $1;

-- name: InsertServiceMetric :exec
-- collected_at is the collector cycle's time, shared by the cycle's rows so
-- that ListServiceMetrics can sum a compose stack's rows into one point.
INSERT INTO service_metrics (service_id, compose_service, cpu_percent, memory_mb,
    memory_peak_mb, throttled_pct, cpu_wait_pct, collected_at, restarts)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: NewestServerMetricAt :one
-- The newest sample of a server's services: the collector asks the
-- server's agent for the samples after it (D-113).
SELECT MAX(m.collected_at)::timestamptz AS newest
FROM service_metrics m
JOIN services s ON s.id = m.service_id
WHERE s.server_id = $1;

-- name: ListServiceMetrics :many
-- A service's chart: one point per collector cycle from since on, oldest
-- first. An empty compose_service sums a stack's compose services; a name
-- keeps that one's (idx_service_metrics_service_time). throttled_pct is
-- the most any of them was held back by its CPU limit that minute.
SELECT collected_at,
       SUM(cpu_percent)::float8 AS cpu_percent,
       SUM(memory_mb)::float8 AS memory_mb,
       COALESCE(MAX(throttled_pct), 0)::float8 AS throttled_pct
FROM service_metrics
WHERE service_id = sqlc.arg(service_id)
  AND collected_at >= sqlc.arg(since)
  AND (sqlc.arg(compose_service)::text = '' OR compose_service = sqlc.arg(compose_service)::text)
GROUP BY collected_at
ORDER BY collected_at;

-- name: ListServiceMetricsDaily :many
-- A service's week (range=7d): one point per UTC day, oldest first, of the
-- quantity 1h and 24h draw: the summed samples' memory peak and CPU p95,
-- the whole service's for an empty compose_service, else that compose
-- service's. The 6 days before today come from service_usage_daily_sums
-- (SummariseUsageSums); a day summarised before 0070 has no row there and
-- falls back to service_usage_daily (each compose service's largest
-- container's peak and CPU p95, summed), marked largest_container. Today
-- comes from today's samples. A day without samples (service_usage_daily's
-- kill-only rows) is left out; throttled_pct is the day's highest. The
-- primary keys and idx_service_metrics_service_time serve the parts.
WITH days AS (
    SELECT u.day, SUM(u.cpu_p95_percent)::float8 AS cpu, SUM(u.memory_peak_mb)::float8 AS memory,
           MAX(u.throttled_max_pct)::float8 AS throttled
    FROM service_usage_daily u
    WHERE u.service_id = sqlc.arg(service_id)
      AND u.day >= sqlc.arg(today)::date - 6 AND u.day < sqlc.arg(today)::date
      AND u.samples > 0
      AND (sqlc.arg(compose_service)::text = '' OR u.compose_service = sqlc.arg(compose_service)::text)
    GROUP BY u.day
), today AS (
    SELECT SUM(m.memory_mb) AS memory, SUM(m.cpu_percent) AS cpu, MAX(m.throttled_pct) AS throttled
    FROM service_metrics m
    WHERE m.service_id = sqlc.arg(service_id)
      AND m.collected_at >= (sqlc.arg(today)::date::timestamp AT TIME ZONE 'UTC')
      AND (sqlc.arg(compose_service)::text = '' OR m.compose_service = sqlc.arg(compose_service)::text)
    GROUP BY m.collected_at
)
SELECT (d.day::timestamp AT TIME ZONE 'UTC')::timestamptz AS day_start,
       COALESCE(s.cpu_p95_percent::float8, d.cpu) AS cpu_percent,
       COALESCE(s.memory_peak_mb::float8, d.memory) AS memory_mb,
       COALESCE(d.throttled, 0)::float8 AS throttled_pct,
       (s.day IS NULL)::bool AS largest_container
FROM days d
LEFT JOIN service_usage_daily_sums s
  ON s.service_id = sqlc.arg(service_id) AND s.compose_service = sqlc.arg(compose_service)::text AND s.day = d.day
UNION ALL
-- An aggregate without GROUP BY gives a row even over no samples; HAVING
-- leaves a day without samples out.
SELECT (sqlc.arg(today)::date::timestamp AT TIME ZONE 'UTC')::timestamptz,
       (percentile_cont(0.95) WITHIN GROUP (ORDER BY t.cpu))::float8,
       MAX(t.memory)::float8,
       COALESCE(MAX(t.throttled), 0)::float8,
       false
FROM today t
HAVING COUNT(*) > 0
ORDER BY day_start;

-- name: DeleteServiceMetricsBefore :exec
DELETE FROM service_metrics WHERE collected_at < $1;

-- name: SumAllocatedForServer :one
-- Memory and CPU committed on a server by non-stopped services: a
-- single-container service's limits, and a compose stack's per compose
-- service limits from the panel (file limits are not stored; see the plan).
SELECT
  COALESCE(SUM(CASE WHEN s.source_type = 'docker-compose' THEN COALESCE(c.memory_mb, 0) ELSE 0 END), 0)::bigint AS compose_memory_mb,
  COALESCE(SUM(CASE WHEN s.source_type = 'docker-compose' THEN COALESCE(c.cpu_cores, 0) ELSE 0 END), 0)::float8 AS compose_cpu_cores
FROM services s
LEFT JOIN service_compose_limits c ON c.service_id = s.id
WHERE s.server_id = $1 AND s.runtime_status != 'stopped';

-- name: SumAllocatedForServerOrg :one
-- SumAllocatedForServer for one workspace's services (by their project's
-- workspace, or their own when shared by the workspace).
SELECT
  COALESCE(SUM(CASE WHEN s.source_type = 'docker-compose' THEN COALESCE(c.memory_mb, 0) ELSE 0 END), 0)::bigint AS compose_memory_mb,
  COALESCE(SUM(CASE WHEN s.source_type = 'docker-compose' THEN COALESCE(c.cpu_cores, 0) ELSE 0 END), 0)::float8 AS compose_cpu_cores
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
LEFT JOIN service_compose_limits c ON c.service_id = s.id
WHERE s.server_id = $1 AND s.runtime_status != 'stopped'
  AND COALESCE(p.org_id, s.scoped_org_id) = $2;

-- name: ListServicesOnServerForResources :many
-- What resources reads of each service on a server for the server's
-- suggestions (D-41): the workspace's services on it, as GET
-- /servers/:serverID/services lists them (a shared server carries others').
SELECT s.id, s.name, s.source_type,
       COALESCE(s.memory_limit, '')::text    AS memory_limit,
       COALESCE(s.cpu_limit, '')::text       AS cpu_limit,
       COALESCE(s.addon_slug, '')::text      AS addon_slug,
       COALESCE(s.docker_image, '')::text    AS docker_image,
       COALESCE(s.compose_content, '')::text AS compose_content,
       s.compose_repo_url
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
WHERE s.server_id = sqlc.arg(server_id) AND COALESCE(p.org_id, s.scoped_org_id) = sqlc.arg(org_id)
ORDER BY s.name;

-- name: ListServiceIDsInEnvironment :many
-- The services an environment delete tears down.
SELECT id FROM services WHERE environment_id = $1 ORDER BY id;

-- name: ListServiceIDsInProject :many
-- The services a project delete tears down: every environment's, and the
-- project's shared services.
SELECT s.id FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
WHERE e.project_id = $1 OR s.project_id = $1
ORDER BY s.id;

-- name: ListServiceData :many
-- What deleting these services removes, each one and its previews' copies
-- (D-97): the names its data has on its server.
SELECT s.id, s.name, s.server_id, s.volumes, COALESCE(srv.name, '')::text AS server_name
FROM services s
LEFT JOIN servers srv ON srv.id = s.server_id
LEFT JOIN services p ON p.id = s.preview_of
WHERE s.id = ANY(@ids::uuid[]) OR s.preview_of = ANY(@ids::uuid[])
-- By name, each service's copies right after it.
ORDER BY COALESCE(p.name, s.name), COALESCE(s.preview_of, s.id), s.preview_of NULLS FIRST, s.id;

-- name: ListServiceIDsInOrg :many
-- The services a workspace delete tears down: its projects' and its own
-- workspace-shared services.
SELECT s.id FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
WHERE COALESCE(p.org_id, s.scoped_org_id) = $1
ORDER BY s.id;

-- name: CountServicesOnServer :one
SELECT count(*) FROM services WHERE server_id = $1;

-- name: ListServerAllocations :many
-- Every non-stopped service on a server with its workspace and project and
-- what it reserves (the same figures SumAllocatedForServer adds up): the
-- Servers page splits a server by workspace with it, in one query.
SELECT COALESCE(p.org_id, s.scoped_org_id)::uuid AS org_id,
       o.name AS org_name,
       COALESCE(p.name, '')::text AS project_name,
       s.source_type,
       s.cpu_limit, s.memory_limit,
       COALESCE((SELECT SUM(c.memory_mb) FROM service_compose_limits c WHERE c.service_id = s.id), 0)::bigint AS compose_memory_mb,
       COALESCE((SELECT SUM(c.cpu_cores) FROM service_compose_limits c WHERE c.service_id = s.id), 0)::float8 AS compose_cpu_cores
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
JOIN organizations o ON o.id = COALESCE(p.org_id, s.scoped_org_id)
WHERE s.server_id = $1 AND s.runtime_status != 'stopped'
ORDER BY o.name, project_name;

-- name: ListLimitStringsForServer :many
SELECT cpu_limit, memory_limit FROM services
WHERE server_id = $1 AND runtime_status != 'stopped' AND source_type <> 'docker-compose';

-- name: ListLimitStringsForServerOrg :many
SELECT s.cpu_limit, s.memory_limit FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
WHERE s.server_id = $1 AND s.runtime_status != 'stopped' AND s.source_type <> 'docker-compose'
  AND COALESCE(p.org_id, s.scoped_org_id) = $2;

-- name: ListWorkspaceServices :many
-- Every service of a workspace, for the sidebar's health dots and the command
-- palette, in one request: environment services, project-shared and
-- workspace-shared. project_id is NULL for a workspace-shared service, and
-- server_id for one on the panel host.
-- ponytail: no index serves COALESCE(p.org_id, s.scoped_org_id); a scan of
-- services, fine while the instance has hundreds of them, not millions.
-- deploying, down and unhealthy feed the runtime status it shows
-- (service.PresentedStatus), as GET /services/:id does.
SELECT s.id, s.name, s.scope, s.environment_id,
       COALESCE(e.project_id, s.project_id) AS project_id,
       s.server_id, s.port,
       s.runtime_status, s.source_type,
       COALESCE(s.addon_slug, '')::text   AS addon_slug,
       COALESCE(s.docker_image, '')::text AS docker_image,
       COALESCE(s.git_repo_url, '')::text AS git_repo_url,
       s.git_branch,
       COALESCE(e.name, '')::text AS environment_name,
       EXISTS (SELECT 1 FROM deployments d WHERE d.service_id = s.id
               AND d.status IN ('pending', 'queued', 'building')) AS deploying,
       EXISTS (SELECT 1 FROM alerts a WHERE a.resolved_at IS NULL AND a.subject = s.id::text
               AND a.kind IN ('service_down', 'service_restarting')) AS down,
       EXISTS (SELECT 1 FROM alerts a WHERE a.resolved_at IS NULL AND a.subject = s.id::text
               AND a.kind IN ('service_unhealthy', 'container_exited')) AS unhealthy
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
WHERE COALESCE(p.org_id, s.scoped_org_id) = sqlc.arg(org_id)::uuid
ORDER BY s.name, s.id;

-- name: DeleteAuditLogBefore :execrows
-- Retention (TOFT_AUDIT_RETENTION_DAYS): rows older than the cut-off go.
DELETE FROM audit_log WHERE created_at < $1;

-- name: DeleteOrgAuditLog :exec
-- A deleted workspace's audit log goes with it (audit_log has no cascade).
DELETE FROM audit_log WHERE org_id = $1;

-- name: DeleteOrganization :execrows
-- Its members, invites, settings, alert webhook, shares and the rest go by
-- cascade; its projects, servers and services are deleted before this.
DELETE FROM organizations WHERE id = $1;

-- name: ClearServiceCredentials :exec
-- A save that sends a saved credential somewhere new clears it first
-- (service.credentialsMoved), before the URL it would go to is written.
UPDATE services SET
  git_token = CASE WHEN sqlc.arg(git_token)::bool THEN NULL ELSE git_token END,
  registry_password = CASE WHEN sqlc.arg(registry_password)::bool THEN NULL ELSE registry_password END,
  updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: SetServiceDockerImage :exec
-- A deploy's image override; an image from another registry clears the
-- saved registry password, which a pull would send there.
UPDATE services SET
  docker_image = sqlc.arg(docker_image),
  registry_password = CASE WHEN sqlc.arg(clear_registry_password)::bool THEN NULL ELSE registry_password END,
  updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: SetServiceComposeNetworkService :exec
-- The compose service a successful compose deploy routed ('' none, 0075).
UPDATE services SET compose_network_service = sqlc.arg(compose_network_service)::text
WHERE id = sqlc.arg(id);

-- name: SwapServiceDockerImage :exec
-- A settled deploy's image (deployment.followLive), or the image a Deploy
-- a version that started nothing replaced, only while the saved image is
-- still from_image: a save made meanwhile stays. registry_password, when
-- given, is the sealed password that override cleared, put back with the
-- image only while none has been saved since.
UPDATE services SET
  docker_image = sqlc.arg(docker_image),
  registry_password = COALESCE(sqlc.narg(registry_password)::text, registry_password),
  updated_at = NOW()
WHERE id = sqlc.arg(id) AND docker_image = sqlc.arg(from_image)::text
  AND (sqlc.narg(registry_password)::text IS NULL OR registry_password IS NULL);

-- name: SetServiceSource :exec
-- The project's repository a service builds from (D-77), NULL for a URL,
-- and whether a push deploys it. A repository source keeps no git
-- credentials: they were a URL's.
UPDATE services SET
    repository_id  = CASE WHEN sqlc.arg(set_repository)::boolean THEN sqlc.narg(repository_id)::uuid ELSE repository_id END,
    git_token      = CASE WHEN sqlc.arg(set_repository)::boolean AND sqlc.narg(repository_id)::uuid IS NOT NULL THEN NULL ELSE git_token END,
    git_username   = CASE WHEN sqlc.arg(set_repository)::boolean AND sqlc.narg(repository_id)::uuid IS NOT NULL THEN NULL ELSE git_username END,
    deploy_on_push = COALESCE(sqlc.narg(deploy_on_push)::boolean, deploy_on_push),
    updated_at     = NOW()
WHERE id = sqlc.arg(id);

-- name: RepositoryInScope :one
-- Whether a repository is the project's: the environment's project when
-- environment_id is given (D-77).
SELECT EXISTS (
    SELECT 1 FROM repositories r
    WHERE r.id = sqlc.arg(repository_id)
      AND r.project_id = COALESCE((SELECT e.project_id FROM environments e WHERE e.id = sqlc.narg(environment_id)::uuid),
                                  sqlc.narg(project_id)::uuid));

-- name: SetServiceAccessControl :exec
-- Basic auth and the IP allowlist given when a service is created (the
-- create insert does not carry them; a config save writes them itself).
UPDATE services
SET basic_auth_user = $2, basic_auth_password = $3, ip_allowlist = $4, updated_at = NOW()
WHERE id = $1;

-- name: ListBasicAuthPasswords :many
-- The stored basic auth passwords, encrypted or still in plain text (0019).
SELECT id, basic_auth_password FROM services WHERE basic_auth_password <> '';

-- name: ReplaceBasicAuthPasswords :execrows
-- A row changes only while it still holds old_value, so a password saved in
-- between is kept.
UPDATE services s SET basic_auth_password = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::uuid[]) AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE s.id = v.id AND s.basic_auth_password = v.old_value;

-- ─── Environment overview (the Environment page) ─────────────────────────────

-- name: ListEnvironmentServiceKeys :many
-- How many variables and secrets each service of an environment sets itself,
-- and the keys of those the environment also shares: the service's value
-- wins. Keys only, never values (idx_services_environment_id,
-- idx_env_vars_service_id).
SELECT s.id AS service_id,
       COUNT(*) FILTER (WHERE NOT e.is_secret)::int AS variables,
       COUNT(*) FILTER (WHERE e.is_secret)::int     AS secrets,
       COALESCE(array_agg(e.key ORDER BY e.key) FILTER (WHERE ee.id IS NOT NULL), '{}')::text[] AS overrides
FROM services s
JOIN env_vars e ON e.service_id = s.id
LEFT JOIN environment_env_vars ee ON ee.environment_id = s.environment_id AND ee.key = e.key
WHERE s.environment_id = $1
GROUP BY s.id
ORDER BY s.id;

-- name: ListEnvironmentDomains :many
-- The domains of an environment's services, oldest first per service.
SELECT d.service_id, d.hostname
FROM domains d
JOIN services s ON s.id = d.service_id
WHERE s.environment_id = $1
ORDER BY d.service_id, d.hostname LIKE '%.sslip.io', d.created_at; -- own domains first (D-150)

-- name: ListProjectEnvironmentCounts :many
-- A project's environments with how many shared variables and secrets each
-- has (idx_environments_project_id).
SELECT e.id, e.name, e.color, e.protected,
       COUNT(v.id) FILTER (WHERE NOT v.is_secret) AS variables,
       COUNT(v.id) FILTER (WHERE v.is_secret)     AS secrets
FROM environments e
LEFT JOIN environment_env_vars v ON v.environment_id = e.id
WHERE e.project_id = $1
GROUP BY e.id
ORDER BY e.created_at;

-- name: ListProjectDomains :many
-- The domains of a project's services: its environments' and its own.
SELECT d.service_id, d.hostname
FROM domains d
JOIN services s ON s.id = d.service_id
LEFT JOIN environments e ON e.id = s.environment_id
WHERE e.project_id = sqlc.arg(project_id)::uuid
   OR (s.scope = 'project' AND s.project_id = sqlc.arg(project_id)::uuid)
ORDER BY d.service_id, d.hostname LIKE '%.sslip.io', d.created_at; -- own domains first (D-150)

-- name: ListLatestDeploymentsForProject :many
-- Each of a project's services' latest deployment.
SELECT DISTINCT ON (d.service_id)
       d.service_id, d.id, d.status, COALESCE(d.commit_sha, '')::text AS commit_sha,
       COALESCE(d.image_tag, '')::text AS image_tag, d.created_at, d.finished_at
FROM deployments d
JOIN services s ON s.id = d.service_id
LEFT JOIN environments e ON e.id = s.environment_id
WHERE e.project_id = sqlc.arg(project_id)::uuid
   OR (s.scope = 'project' AND s.project_id = sqlc.arg(project_id)::uuid)
ORDER BY d.service_id, d.created_at DESC;

-- name: ListRecentProjectDeployments :many
-- A project's latest deployments, newest first: row_limit of each
-- environment's, and row_limit of its project services' (environment_name
-- ''), so a busy environment never pushes another's out of its Overview.
SELECT r.id, r.service_id, r.service_name, r.environment_name, r.status,
       r.commit_sha, r.commit_message, r.triggered_by, r.image_tag,
       r.created_at, r.started_at, r.finished_at
FROM (
    SELECT d.id, d.service_id, s.name AS service_name,
           COALESCE(e.name, '')::text           AS environment_name,
           d.status,
           COALESCE(d.commit_sha, '')::text     AS commit_sha,
           COALESCE(d.commit_message, '')::text AS commit_message,
           COALESCE(d.triggered_by, '')::text   AS triggered_by,
           COALESCE(d.image_tag, '')::text      AS image_tag,
           d.created_at, d.started_at, d.finished_at,
           ROW_NUMBER() OVER (PARTITION BY s.environment_id ORDER BY d.created_at DESC) AS n
    FROM deployments d
    JOIN services s ON s.id = d.service_id
    LEFT JOIN environments e ON e.id = s.environment_id
    WHERE e.project_id = sqlc.arg(project_id)::uuid
       OR (s.scope = 'project' AND s.project_id = sqlc.arg(project_id)::uuid)
) r
WHERE r.n <= sqlc.arg(row_limit)::int
ORDER BY r.created_at DESC;

-- name: ListWorkspaceEnvironments :many
-- Every environment of a workspace, for the Projects table and workspace
-- services' reach; preview says it is a pull request's preview (D-97).
SELECT e.id, e.project_id, e.name, e.color, e.protected,
       EXISTS (SELECT 1 FROM previews pv WHERE pv.environment_id = e.id)::bool AS preview
FROM environments e
JOIN projects p ON p.id = e.project_id
WHERE p.org_id = $1
ORDER BY e.created_at;

-- name: CreateEnvironment :one
INSERT INTO environments (project_id, name, color, protected)
VALUES ($1, $2, $3, $4)
RETURNING id, project_id, name, color, protected, created_at, updated_at;

-- name: GetEnvironment :one
SELECT id, project_id, name, color, protected, created_at, updated_at
FROM environments WHERE id = $1 LIMIT 1;

-- name: ListProjectEnvironments :many
SELECT id, project_id, name, color, protected, created_at, updated_at
FROM environments WHERE project_id = $1 ORDER BY created_at ASC;

-- name: UpdateEnvironment :one
-- A PATCH: a NULL argument keeps the column.
UPDATE environments
SET name       = COALESCE(sqlc.narg(name), name),
    color      = COALESCE(sqlc.narg(color), color),
    protected  = COALESCE(sqlc.narg(protected), protected),
    updated_at = NOW()
WHERE id = $1
  -- Set when protection changes: only if it is still what the caller read.
  AND (sqlc.narg(was_protected)::bool IS NULL OR protected = sqlc.narg(was_protected))
RETURNING id, project_id, name, color, protected, created_at, updated_at;

-- name: ListLatestDeploymentsForEnvironment :many
-- Each service's latest deployment, for an environment's services
-- (idx_deployments_service_id).
SELECT DISTINCT ON (d.service_id)
       d.service_id, d.id, d.status, COALESCE(d.commit_sha, '')::text AS commit_sha,
       COALESCE(d.image_tag, '')::text AS image_tag, d.created_at, d.finished_at
FROM deployments d
JOIN services s ON s.id = d.service_id
WHERE s.environment_id = $1
ORDER BY d.service_id, d.created_at DESC;

-- name: GetServicePidsLimit :one
-- The hardened runtime's process limit (D-93); 0 is the platform's default.
SELECT COALESCE(pids_limit, 0)::int AS pids_limit FROM services WHERE id = $1;

-- name: SetServicePidsLimit :exec
UPDATE services SET pids_limit = sqlc.narg(pids_limit), updated_at = NOW() WHERE id = sqlc.arg(id);

-- name: GetServiceContainerHealth :one
-- The Docker health check its containers are created with (0081).
SELECT container_health, container_health_command FROM services WHERE id = $1;

-- name: SetServiceContainerHealth :exec
UPDATE services SET container_health = sqlc.arg(container_health), container_health_command = sqlc.arg(container_health_command),
    updated_at = NOW() WHERE id = sqlc.arg(id);

-- name: GetServiceDeployTimeout :one
-- The deploy time limit in minutes (D-125); 0 is the platform's default.
SELECT COALESCE(deploy_timeout_minutes, 0)::int AS deploy_timeout_minutes FROM services WHERE id = $1;

-- name: SetServiceDeployTimeout :exec
UPDATE services SET deploy_timeout_minutes = sqlc.narg(deploy_timeout_minutes), updated_at = NOW() WHERE id = sqlc.arg(id);

-- name: GetServiceIsolation :one
-- The service's own isolation level (D-95; empty follows the workspace's
-- default) and its workspace's default.
SELECT COALESCE(s.isolation, '')::text AS isolation,
       COALESCE(o.default_isolation, 'standard')::text AS workspace_default
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
LEFT JOIN organizations o ON o.id = COALESCE(p.org_id, s.scoped_org_id)
WHERE s.id = $1;

-- name: SetServiceIsolation :exec
UPDATE services SET isolation = sqlc.narg(isolation), updated_at = NOW() WHERE id = sqlc.arg(id);

-- name: AdoptDeployment :one
-- A deployment a server's agent kept running when the panel was restored
-- from an older backup (D-113), for a service on that server: recorded as
-- running when the backup has no record of it. adopted is false when the
-- service isn't on the server or the deployment is another service's.
WITH svc AS (
    SELECT services.id FROM services WHERE services.id = sqlc.arg(service_id) AND services.server_id = sqlc.arg(server_id)
), ins AS (
    INSERT INTO deployments (id, service_id, status, image_tag, triggered_by, queued_at, started_at, finished_at)
    SELECT sqlc.arg(deployment_id)::uuid, svc.id, 'running', sqlc.arg(image_tag), sqlc.arg(triggered_by), sqlc.arg(started_at), sqlc.arg(started_at), sqlc.arg(started_at)
    FROM svc
    ON CONFLICT (id) DO NOTHING
    RETURNING deployments.id
), upd AS (
    UPDATE deployments d SET status = 'running', finished_at = COALESCE(d.finished_at, NOW())
    FROM svc WHERE d.id = sqlc.arg(deployment_id)::uuid AND d.service_id = svc.id AND d.status <> 'running'
    RETURNING d.id
)
SELECT (EXISTS (SELECT 1 FROM ins)
        OR EXISTS (SELECT 1 FROM deployments d JOIN svc ON d.service_id = svc.id WHERE d.id = sqlc.arg(deployment_id)::uuid))::boolean AS adopted;

-- name: DependencyStatuses :many
-- The name and runtime status of each of ids in service_id's own workspace:
-- another workspace's service reads as unknown, never by name.
SELECT d.id, d.name, d.runtime_status
FROM services d
LEFT JOIN environments e ON e.id = d.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, d.project_id)
WHERE d.id = ANY(sqlc.arg(ids)::uuid[])
  AND COALESCE(p.org_id, d.scoped_org_id) = (
      SELECT COALESCE(p2.org_id, s.scoped_org_id)
      FROM services s
      LEFT JOIN environments e2 ON e2.id = s.environment_id
      LEFT JOIN projects p2 ON p2.id = COALESCE(e2.project_id, s.project_id)
      WHERE s.id = sqlc.arg(service_id));

-- name: DeployRunningOnServer :one
-- A deploy is under way to the server, NULL the panel's own: a Docker
-- update or a reboot waits for it (D-134). Runs once per start; indexed
-- through services' key and deployments' service_id (EXPLAIN, 2026-10-30).
SELECT EXISTS (
    SELECT 1 FROM deployments d
    JOIN services s ON s.id = d.service_id
    WHERE d.status IN ('pending', 'queued', 'building')
      AND s.server_id IS NOT DISTINCT FROM sqlc.narg(server_id)::uuid
)::bool;

-- name: AssignLoopbackPort :one
-- The catalog Loki's 127.0.0.1 port (D-148): the one it has, else the
-- lowest one in 39100-39199 no service on the instance holds; NULL when all
-- are taken, no row for any other service (every deploy asks, whatever
-- started it). A concurrent pick of the same port fails on
-- idx_services_loopback_port (the repository picks again).
UPDATE services s SET loopback_port = COALESCE(s.loopback_port, (
    SELECT p FROM generate_series(39100, 39199) AS p
    WHERE NOT EXISTS (SELECT 1 FROM services o WHERE o.loopback_port = p)
    ORDER BY p
    LIMIT 1))
WHERE s.id = $1 AND s.addon_slug = 'loki'
RETURNING loopback_port;

-- ── Move to workspace (D-162) ────────────────────────────────────────────────

-- name: ListEnvironmentVarsNotSetBy :many
-- The variables and secrets an environment shares that the service doesn't
-- set itself: what it inherits, which a move out of the environment copies
-- onto it (idx_env_vars_service_id).
SELECT ee.key, ee.value, ee.is_secret, ee.updated_by, ee.agent_connection_id
FROM environment_env_vars ee
WHERE ee.environment_id = sqlc.arg(environment_id)
  AND NOT EXISTS (SELECT 1 FROM env_vars e WHERE e.service_id = sqlc.arg(service_id) AND e.key = ee.key)
ORDER BY ee.key;

-- name: InsertInheritedEnvVars :exec
-- One row per element of the arrays, in one statement.
INSERT INTO env_vars (service_id, key, value, is_secret, updated_by, agent_connection_id)
SELECT sqlc.arg(service_id), t.k, t.v, t.sec, t.by, t.agent
FROM (SELECT unnest(sqlc.arg(keys)::text[]) AS k, unnest(sqlc.arg(vals)::text[]) AS v,
             unnest(sqlc.arg(secrets)::bool[]) AS sec, unnest(sqlc.arg(updated_by)::uuid[]) AS by,
             unnest(sqlc.arg(agents)::uuid[]) AS agent) t;

-- name: MoveServiceToWorkspace :execrows
-- The service becomes its workspace's own: the name must be free among the
-- workspace's services (uq_services_name_org). Its volumes, containers and
-- domains are keyed on its id and stay.
UPDATE services
SET scope = 'org', environment_id = NULL, project_id = NULL,
    scoped_org_id = sqlc.arg(org_id), updated_at = NOW()
WHERE id = sqlc.arg(id) AND scope <> 'org';

-- name: WorkspaceAliasUser :many
-- Another of the workspace's services with this network alias, in any
-- scope: a workspace service joins every network of the workspace, so a
-- moved one must not share an alias with any (D-162).
SELECT s.name
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(s.project_id, e.project_id)
WHERE s.network_alias = sqlc.arg(alias) AND s.id <> sqlc.arg(id)
  AND (s.scoped_org_id = sqlc.arg(org_id) OR p.org_id = sqlc.arg(org_id))
LIMIT 1;
