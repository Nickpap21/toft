-- PR previews (D-97).

-- name: GetPreviewSettings :one
SELECT p.id, p.org_id, p.preview_environment_id, COALESCE(p.preview_domain, '')::text AS preview_domain,
       COALESCE(e.name, '')::text AS environment_name, p.preview_max
FROM projects p
LEFT JOIN environments e ON e.id = p.preview_environment_id
WHERE p.id = $1;

-- name: SetPreviewSettings :exec
-- max NULL keeps the project's limit.
UPDATE projects SET preview_environment_id = sqlc.narg(environment_id), preview_domain = sqlc.narg(domain),
    preview_max = COALESCE(sqlc.narg(max)::smallint, preview_max), updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: GetPreviewByPull :one
SELECT v.id, v.pull_request_id, v.project_id, v.source_environment_id, v.environment_id, v.untrusted, v.head_sha, v.approved_sha, v.approved_by, v.approved_at, v.state, v.stopped_reason, v.note, v.created_at, v.updated_at,
       COALESCE(u.name, '')::text AS approved_by_name, v.migration_files, v.migrations_unchecked
FROM previews v LEFT JOIN users u ON u.id = v.approved_by
WHERE v.pull_request_id = $1;

-- name: GetPreviewByEnvironment :one
SELECT id, pull_request_id, project_id, source_environment_id, environment_id, untrusted, head_sha, approved_sha, approved_by, approved_at, state, stopped_reason, note, created_at, updated_at
FROM previews WHERE environment_id = $1;

-- name: InsertPreview :one
INSERT INTO previews (pull_request_id, project_id, source_environment_id, untrusted, head_sha, state)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, pull_request_id, project_id, source_environment_id, environment_id, untrusted, head_sha, approved_sha, approved_by, approved_at, state, stopped_reason, note, created_at, updated_at;

-- name: UpdatePreview :exec
-- updated_at moves only when something did: the reconcile saves a
-- preview it found unchanged, as at every start.
UPDATE previews SET environment_id = sqlc.narg(environment_id), untrusted = sqlc.arg(untrusted), head_sha = sqlc.arg(head_sha),
       approved_sha = sqlc.narg(approved_sha), approved_by = sqlc.narg(approved_by), approved_at = sqlc.narg(approved_at),
       state = sqlc.arg(state), stopped_reason = sqlc.narg(stopped_reason), note = sqlc.arg(note),
       updated_at = CASE WHEN (environment_id, untrusted, head_sha, approved_sha, approved_by, approved_at, state, stopped_reason, note)
                              IS DISTINCT FROM (sqlc.narg(environment_id)::uuid, sqlc.arg(untrusted)::boolean, sqlc.arg(head_sha)::text,
                                                sqlc.narg(approved_sha)::text, sqlc.narg(approved_by)::uuid, sqlc.narg(approved_at)::timestamptz,
                                                sqlc.arg(state)::text, sqlc.narg(stopped_reason)::text, sqlc.arg(note)::text)
                         THEN NOW() ELSE updated_at END
WHERE id = sqlc.arg(id);

-- name: SetPreviewMigrationFiles :exec
-- The migrations the pull request's diff touches, which the preview skips
-- (D-144), and whether the diff was too large to check.
UPDATE previews SET migration_files = sqlc.arg(files)::text[], migrations_unchecked = sqlc.arg(unchecked)::boolean
WHERE id = sqlc.arg(id);

-- name: DeletePreview :exec
DELETE FROM previews WHERE id = $1;

-- name: CountHoldingPreviews :one
-- The previews that count toward a project's limit (preview_max): started ones, running,
-- failed or stopped.
SELECT count(*) FROM previews WHERE project_id = $1 AND state IN ('active', 'stopped', 'removing');

-- name: ListPreviewPullsWaitingRoom :many
SELECT pull_request_id FROM previews WHERE project_id = $1 AND state = 'waiting_room' ORDER BY created_at, id;

-- name: ListPreviewPullsOfSource :many
SELECT pull_request_id FROM previews WHERE source_environment_id = $1 ORDER BY created_at;

-- name: ListPreviewPullsOfProject :many
SELECT pull_request_id FROM previews WHERE project_id = $1 ORDER BY created_at;

-- name: ListPreviewPullsOfRepository :many
SELECT v.pull_request_id FROM previews v JOIN pull_requests p ON p.id = v.pull_request_id
WHERE p.repository_id = $1 ORDER BY v.created_at;

-- name: ListAllPreviewPulls :many
SELECT pull_request_id FROM previews ORDER BY created_at;

-- name: ListUnappliedPreviewEvents :many
-- The events the previews module has not applied yet, oldest first: only
-- a pull request's own (opened, pushed, closed, reopened, merged) move a
-- preview; the rest are marked applied with them.
SELECT id, COALESCE(pull_request_id::text, '')::text AS pull_request_id, actor_id
FROM git_events WHERE previews_at IS NULL ORDER BY id LIMIT sqlc.arg(n);

-- name: MarkGitEventsPreviews :exec
UPDATE git_events SET previews_at = NOW() WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: GetPullForPreview :one
-- A pull request as its preview needs it: its state and head, its
-- repository and project, whether an API key opened it (and which), and
-- whether a key, or someone unknown, ever pushed to it: while it was open,
-- or to its head branch before (git_events are kept 90 days; an agent's
-- branch is its for as long as the agent is connected).
SELECT p.id, p.number, p.title, p.state, p.base_branch, p.head_branch, p.head_sha, p.author_id, p.merged_by, p.opened_with_key,
       COALESCE(k.name, '')::text AS api_key_name, COALESCE(u.name, '')::text AS author_name, COALESCE(ac.name, '')::text AS agent_name,
       r.id AS repository_id, r.name AS repository_name, r.project_id, pr.org_id,
       (r.id = (SELECT r2.id FROM repositories r2 WHERE r2.project_id = r.project_id ORDER BY r2.created_at, r2.id LIMIT 1))::bool AS first_repository,
       (EXISTS (SELECT 1 FROM pull_request_events e WHERE e.pull_request_id = p.id AND e.kind = 'pushed'
                AND (e.data->>'with_key')::boolean)
        OR EXISTS (SELECT 1 FROM git_events g WHERE g.repository_id = p.repository_id AND g.kind = 'push'
                   AND g.branch = p.head_branch AND g.with_key)
        OR EXISTS (SELECT 1 FROM agent_branches ab WHERE ab.repository_id = p.repository_id AND ab.name = p.head_branch))::bool AS key_pushed,
       COALESCE(pr.name, '')::text AS project_name
FROM pull_requests p
JOIN repositories r ON r.id = p.repository_id
JOIN projects pr ON pr.id = r.project_id
LEFT JOIN api_keys k ON k.id = p.api_key_id
LEFT JOIN users u ON u.id = p.author_id
LEFT JOIN agent_connections ac ON ac.id = p.agent_connection_id
WHERE p.id = $1;

-- name: GetPullIDByNumber :one
SELECT p.id FROM pull_requests p JOIN repositories r ON r.id = p.repository_id
WHERE r.project_id = sqlc.arg(project_id) AND r.id = sqlc.arg(repository_id) AND p.number = sqlc.arg(number);

-- name: ListPreviewSourceServices :many
-- The services of the environment previews copy, with what a preview needs
-- to decide what to build: the repository each builds from, its build pack,
-- its server, its volumes, whether it mounts shared volumes, and its
-- domains.
SELECT s.id, s.name, s.source_type, COALESCE(s.repository_id::text, '')::text AS repository_id, s.build_pack,
       COALESCE(s.server_id::text, '')::text AS server_id, s.volumes, s.base_directory, s.compose_file_path,
       COALESCE(s.memory_limit, '')::text AS memory_limit, s.type, s.addon_slug,
       EXISTS (SELECT 1 FROM service_shared_volumes v WHERE v.service_id = s.id)::bool AS shared_volumes,
       COALESCE((SELECT array_agg(d.hostname ORDER BY d.created_at) FROM domains d WHERE d.service_id = s.id), '{}')::text[] AS hostnames
FROM services s
WHERE s.environment_id = $1 AND s.preview_of IS NULL
ORDER BY s.name;

-- name: ListPreviewServices :many
-- A preview's services with their originals and their newest deployment.
SELECT s.id, s.name, COALESCE(s.preview_of::text, '')::text AS preview_of, COALESCE(s.server_id::text, '')::text AS server_id,
       COALESCE(sv.name, '')::text AS server_name, s.runtime_status, s.source_type,
       COALESCE(d.id::text, '')::text AS deployment_id, COALESCE(d.status, '')::text AS deployment_status,
       COALESCE(d.commit_sha, '')::text AS deployment_sha, COALESCE(d.error_summary, '')::text AS deployment_error,
       d.created_at AS deployment_created_at, d.finished_at AS deployment_finished_at,
       COALESCE((SELECT array_agg(dm.hostname ORDER BY dm.created_at) FROM domains dm WHERE dm.service_id = s.id), '{}')::text[] AS hostnames,
       COALESCE(s.isolation, o.default_isolation, 'standard')::text AS effective_isolation
FROM services s
LEFT JOIN servers sv ON sv.id = s.server_id
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = e.project_id
LEFT JOIN organizations o ON o.id = p.org_id
LEFT JOIN LATERAL (
    SELECT id, status, commit_sha, error_summary, created_at, finished_at FROM deployments
    WHERE service_id = s.id ORDER BY created_at DESC LIMIT 1
) d ON true
WHERE s.environment_id = $1
ORDER BY s.name;

-- name: ListProjectPreviews :many
-- A project's previews, oldest first, paged by (created_at, id).
SELECT v.id, v.pull_request_id, v.project_id, v.source_environment_id, v.environment_id, v.untrusted, v.head_sha, v.approved_sha, v.approved_by, v.approved_at, v.state, v.stopped_reason, v.note, v.created_at, v.updated_at,
       COALESCE(u.name, '')::text AS approved_by_name, p.number, p.title, p.head_branch, r.id AS repository_id, r.name AS repository_name,
       COALESCE(e.name, '')::text AS environment_name, v.migration_files, v.migrations_unchecked
FROM previews v
JOIN pull_requests p ON p.id = v.pull_request_id
JOIN repositories r ON r.id = p.repository_id
LEFT JOIN environments e ON e.id = v.environment_id
LEFT JOIN users u ON u.id = v.approved_by
WHERE v.project_id = sqlc.arg(project_id)
  AND (sqlc.narg(after_created)::timestamptz IS NULL OR (v.created_at, v.id) > (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY v.created_at, v.id
LIMIT sqlc.arg(n);

-- name: ListRoomCandidates :many
-- The running previews' services on a server (NULL: the panel's own),
-- oldest preview first: what a deploy that doesn't fit stops first (D-15,
-- D-97).
SELECT v.pull_request_id, s.id AS service_id
FROM previews v
JOIN projects p ON p.id = v.project_id
JOIN services s ON s.environment_id = v.environment_id
WHERE v.state = 'active' AND s.server_id IS NOT DISTINCT FROM sqlc.narg(server_id)::uuid AND s.runtime_status <> 'stopped'
  AND (sqlc.narg(org_id)::uuid IS NULL OR p.org_id = sqlc.narg(org_id)::uuid)
ORDER BY v.created_at, v.id, s.id;

-- name: ListPanelAllocations :many
-- What the panel's own server's services reserve (as SumAllocatedForServer
-- counts them): its ledger is the apps share (D-62, D-97).
SELECT s.source_type, s.cpu_limit, s.memory_limit,
       COALESCE((SELECT SUM(c.memory_mb) FROM service_compose_limits c WHERE c.service_id = s.id), 0)::bigint AS compose_memory_mb
FROM services s
WHERE s.server_id IS NULL AND s.runtime_status != 'stopped';

-- name: CountPreviewCertificates :one
-- Certificates previews ordered for names under a registered domain since
-- a time: the started events that say so (D-97's guard).
SELECT count(*) FROM pull_request_events
WHERE kind = 'preview_started' AND data->>'certificate_domain' = sqlc.arg(domain)::text AND created_at > sqlc.arg(since);

-- name: PreviewOfEnvironment :one
-- The preview whose environment this is, for network plans: the
-- environment it copies and whether it is untrusted.
SELECT source_environment_id, untrusted FROM previews WHERE environment_id = $1;

-- name: ListPreviewNetworkPeers :many
-- A trusted preview's peers from the environment it copies: the
-- environment's services the preview has no copy of, on the same server.
SELECT s.id, s.name, COALESCE(s.network_alias, '')::text AS network_alias, s.source_type
FROM services s
WHERE s.environment_id = sqlc.arg(source_environment_id) AND s.server_id IS NOT DISTINCT FROM sqlc.narg(server_id)::uuid
  AND NOT EXISTS (SELECT 1 FROM services c WHERE c.environment_id = sqlc.arg(preview_environment_id) AND c.preview_of = s.id)
ORDER BY s.id;

-- name: ListPreviewNetworkTargets :many
-- The trusted previews' environments a service of the environment they
-- copy publishes itself into: those without a copy of it.
SELECT v.environment_id::uuid AS environment_id
FROM previews v
WHERE v.source_environment_id = sqlc.arg(environment_id) AND NOT v.untrusted AND v.environment_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM services c WHERE c.environment_id = v.environment_id AND c.preview_of = sqlc.arg(service_id))
ORDER BY v.environment_id;

-- name: ListUntrustedPreviewEnvironments :many
-- Every untrusted preview's environment of a project, or of an org's
-- projects: shared services never publish into them.
SELECT v.environment_id::uuid AS environment_id
FROM previews v JOIN projects p ON p.id = v.project_id
WHERE v.untrusted AND v.environment_id IS NOT NULL
  AND (v.project_id = sqlc.narg(project_id)::uuid OR p.org_id = sqlc.narg(org_id)::uuid);

-- name: CopyServiceForPreview :one
-- A preview's copy of a service of the environment it copies (D-97): its
-- configuration, in the preview's environment, building the pull request's
-- branch, never deploying on push. An untrusted copy runs Sandboxed, with a
-- strict network where it can (untrusted_isolation, D-115), and gets no
-- registry or git credentials and no host paths (refused before).
-- A copy runs neither its migrate nor its pre-deploy commands: a preview
-- never migrates a database it may share (D-144).
INSERT INTO services (scope, environment_id, project_id, server_id, name, type, source_type, git_repo_url, git_branch,
    base_directory, docker_image, install_command, build_command, start_command, build_pack, port, replicas, cpu_limit,
    memory_limit, compose_content, compose_repo_url, compose_file_path, registry_server, registry_username,
    registry_password, addon_slug, network_alias, volumes, git_username, git_token, pre_deploy_commands,
    post_deploy_commands, health_check_path, health_check_timeout, health_check_retries, required_env_vars,
    migrate_command, basic_auth_user, basic_auth_password, ip_allowlist, compose_route_service, deploy_strategy,
    pids_limit, deploy_timeout_minutes, isolation, repository_id, deploy_on_push, preview_of,
    container_health, container_health_command)
SELECT 'environment', sqlc.arg(environment_id), s.project_id, s.server_id, s.name, s.type, s.source_type, s.git_repo_url,
    sqlc.arg(branch), s.base_directory, s.docker_image, s.install_command, s.build_command, s.start_command, s.build_pack,
    s.port, s.replicas, s.cpu_limit, sqlc.arg(memory_limit), s.compose_content, s.compose_repo_url, s.compose_file_path,
    CASE WHEN sqlc.arg(untrusted)::bool THEN NULL ELSE s.registry_server END,
    CASE WHEN sqlc.arg(untrusted)::bool THEN NULL ELSE s.registry_username END,
    CASE WHEN sqlc.arg(untrusted)::bool THEN NULL ELSE s.registry_password END,
    s.addon_slug, s.network_alias, s.volumes,
    CASE WHEN sqlc.arg(untrusted)::bool THEN NULL ELSE s.git_username END,
    CASE WHEN sqlc.arg(untrusted)::bool THEN NULL ELSE s.git_token END,
    '',
    s.post_deploy_commands, s.health_check_path, s.health_check_timeout, s.health_check_retries,
    s.required_env_vars, '', s.basic_auth_user, s.basic_auth_password, s.ip_allowlist,
    s.compose_route_service, s.deploy_strategy, s.pids_limit, s.deploy_timeout_minutes,
    CASE WHEN sqlc.arg(untrusted)::bool THEN sqlc.arg(untrusted_isolation)::text ELSE s.isolation END,
    s.repository_id, false, s.id,
    s.container_health, s.container_health_command
FROM services s WHERE s.id = sqlc.arg(original_id)
RETURNING id;

-- name: GetServiceCredentials :one
-- A preview copy's credentials as CopyServiceForPreview copied them: sealed
-- for the original, so the copy seals them again for itself (D-126).
SELECT registry_password, git_token, basic_auth_password FROM services WHERE id = $1;

-- name: SetServiceCredentials :exec
UPDATE services SET registry_password = $2, git_token = $3, basic_auth_password = $4 WHERE id = $1;

-- name: CopyPreviewServiceRows :exec
-- What a trusted preview's copy takes along with it: the original's config
-- files, compose limits, shared volume mounts and deploy-policy exceptions.
WITH f AS (
    INSERT INTO service_config_files (service_id, path, content)
    SELECT sqlc.arg(copy_id), o.path, o.content FROM service_config_files o WHERE o.service_id = sqlc.arg(original_id)
), l AS (
    INSERT INTO service_compose_limits (service_id, compose_service, memory_mb, cpu_cores)
    SELECT sqlc.arg(copy_id), o.compose_service, o.memory_mb, o.cpu_cores FROM service_compose_limits o WHERE o.service_id = sqlc.arg(original_id)
), v AS (
    INSERT INTO service_shared_volumes (service_id, shared_volume_id, target, read_only)
    SELECT sqlc.arg(copy_id), o.shared_volume_id, o.target, o.read_only FROM service_shared_volumes o WHERE o.service_id = sqlc.arg(original_id)
)
INSERT INTO service_deploy_policy_exceptions (service_id, allowed_rules, reason, granted_by)
SELECT sqlc.arg(copy_id), o.allowed_rules, o.reason, o.granted_by FROM service_deploy_policy_exceptions o WHERE o.service_id = sqlc.arg(original_id);

-- name: CopyPreviewComposeLimits :exec
-- An untrusted copy keeps only the original's compose limits (with the
-- sandbox's own memory added to each).
INSERT INTO service_compose_limits (service_id, compose_service, memory_mb, cpu_cores)
SELECT sqlc.arg(copy_id), o.compose_service, o.memory_mb + sqlc.arg(extra_mb)::bigint, o.cpu_cores
FROM service_compose_limits o WHERE o.service_id = sqlc.arg(original_id);

-- name: ListPlainEnvVarsForDeployment :many
-- A service's variables an untrusted preview gets (D-97): plain ones only,
-- neither secret nor masked, its own winning over its environment's.
SELECT DISTINCT ON (v.key) v.key, v.value
FROM (
    SELECT ev.key, ev.value, 0 AS precedence
    FROM env_vars ev
    WHERE ev.service_id = $1 AND NOT ev.is_secret AND NOT ev.masked
    UNION ALL
    SELECT ee.key, ee.value, 1 AS precedence
    FROM environment_env_vars ee
    JOIN services s ON s.environment_id = ee.environment_id
    WHERE s.id = $1 AND NOT ee.is_secret
) v
ORDER BY v.key, v.precedence;

-- name: InsertPreviewDomain :exec
INSERT INTO domains (service_id, hostname) VALUES ($1, $2) ON CONFLICT (hostname) DO NOTHING;

-- name: ListPreviewApprovalsNeeded :many
-- Needs you (D-97): the open pull requests of a workspace whose preview
-- waits for an owner or developer to approve its head.
SELECT v.pull_request_id, v.head_sha, v.updated_at, p.number, p.title, r.id AS repository_id, r.name AS repository_name,
       r.project_id, pr.name AS project_name, COALESCE(k.name, '')::text AS api_key_name, COALESCE(u.name, '')::text AS author_name,
       COALESCE(ac.name, '')::text AS agent_name
FROM previews v
JOIN pull_requests p ON p.id = v.pull_request_id
JOIN repositories r ON r.id = p.repository_id
JOIN projects pr ON pr.id = r.project_id
LEFT JOIN api_keys k ON k.id = p.api_key_id
LEFT JOIN users u ON u.id = p.author_id
LEFT JOIN agent_connections ac ON ac.id = p.agent_connection_id
WHERE pr.org_id = $1 AND p.state = 'open' AND v.untrusted AND v.state <> 'removing'
  AND v.approved_sha IS DISTINCT FROM v.head_sha
ORDER BY v.updated_at DESC;

-- name: ListProjectPreviewEnvironments :many
-- Which of a project's environments are previews, and of which pull
-- request: the environment switcher lists them after the others.
SELECT v.environment_id::uuid AS environment_id, p.number, p.repository_id
FROM previews v JOIN pull_requests p ON p.id = v.pull_request_id
WHERE v.project_id = $1 AND v.environment_id IS NOT NULL;

-- name: GetEnvironmentPreview :one
SELECT p.number, p.repository_id
FROM previews v JOIN pull_requests p ON p.id = v.pull_request_id
WHERE v.environment_id = $1;

-- name: ListServiceCopies :many
-- The previews' copies of a service, removed before it is (D-97).
SELECT id FROM services WHERE preview_of = $1 ORDER BY id;

-- name: IsPreviewCopy :one
SELECT (preview_of IS NOT NULL)::bool FROM services WHERE id = $1;

-- name: IsPreviewEnvironment :one
SELECT EXISTS (SELECT 1 FROM previews WHERE environment_id = $1)::bool;
