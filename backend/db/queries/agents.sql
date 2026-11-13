-- Agents over MCP with OAuth (D-100).

-- name: GetOAuthClient :one
SELECT id, client_id, kind, name, redirect_uris, created_at, fetched_at
FROM oauth_clients WHERE client_id = $1;

-- name: GetOAuthClientByID :one
SELECT id, client_id, kind, name, redirect_uris, created_at, fetched_at
FROM oauth_clients WHERE id = $1;

-- name: UpsertCIMDClient :one
-- A Client ID Metadata Document, fetched again: its name and redirect URIs
-- follow the document. A DCR client never becomes a CIMD one.
INSERT INTO oauth_clients (client_id, kind, name, redirect_uris)
VALUES ($1, 'cimd', $2, $3)
ON CONFLICT (client_id) DO UPDATE SET name = EXCLUDED.name, redirect_uris = EXCLUDED.redirect_uris, fetched_at = NOW()
WHERE oauth_clients.kind = 'cimd'
RETURNING id, client_id, kind, name, redirect_uris, created_at, fetched_at;

-- name: InsertDCRClient :one
-- Registration needs no account, so the instance holds at most max_unused
-- registrations nobody has finished with (as DeleteUnusedDCRClients counts
-- them: no connection, no code); no row when it is full. A connected client
-- is never counted.
-- ponytail: concurrent registrations each see the same count, so the cap
-- can be passed by as many as arrive at once; a disk bound needs no more.
INSERT INTO oauth_clients (client_id, kind, name, redirect_uris)
SELECT sqlc.arg(client_id)::text, 'dcr', sqlc.arg(name)::text, sqlc.arg(redirect_uris)::text[]
WHERE (SELECT count(*) FROM oauth_clients c
       WHERE c.kind = 'dcr'
         AND NOT EXISTS (SELECT 1 FROM agent_connections a WHERE a.client_id = c.id)
         AND NOT EXISTS (SELECT 1 FROM oauth_codes o WHERE o.client_id = c.id)) < sqlc.arg(max_unused)::bigint
RETURNING id, client_id, kind, name, redirect_uris, created_at, fetched_at;

-- name: DeleteUnusedDCRClients :execrows
-- Registrations nobody finished with: no connection and no code, a day old.
DELETE FROM oauth_clients c
WHERE c.kind = 'dcr' AND c.created_at < $1
  AND NOT EXISTS (SELECT 1 FROM agent_connections a WHERE a.client_id = c.id)
  AND NOT EXISTS (SELECT 1 FROM oauth_codes o WHERE o.client_id = c.id);

-- name: InsertOAuthCode :exec
INSERT INTO oauth_codes (code_hash, client_id, user_id, org_id, all_projects, project_ids, scopes, redirect_uri,
                         code_challenge, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: UseOAuthCode :one
-- A code works once, within its 10 minutes.
UPDATE oauth_codes SET used_at = NOW()
WHERE code_hash = $1 AND used_at IS NULL AND expires_at > NOW()
RETURNING client_id, user_id, org_id, all_projects, project_ids, scopes, redirect_uri, code_challenge;

-- name: DeleteOldOAuthCodes :execrows
DELETE FROM oauth_codes WHERE expires_at < $1;

-- name: InsertAgentConnection :one
INSERT INTO agent_connections (org_id, user_id, client_id, name, all_projects, scopes, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, expires_at;

-- name: InsertAgentConnectionProjects :execrows
-- Only the workspace's own projects; the caller compares the count.
INSERT INTO agent_connection_projects (connection_id, project_id)
SELECT sqlc.arg(connection_id)::uuid, p.id FROM projects p
WHERE p.id = ANY (sqlc.arg(project_ids)::uuid[]) AND p.org_id = sqlc.arg(org_id)::uuid;

-- name: GetAgentConnection :one
SELECT c.id, c.org_id, c.user_id, c.name, c.all_projects, c.scopes, c.created_at, c.expires_at, c.revoked_at,
       c.last_used_at, c.last_tool,
       COALESCE((SELECT array_agg(cp.project_id ORDER BY cp.project_id) FROM agent_connection_projects cp
                 WHERE cp.connection_id = c.id), '{}')::uuid[] AS project_ids
FROM agent_connections c WHERE c.id = $1;

-- name: ListAgentConnections :many
-- The workspace's live connections (not revoked, not expired), newest
-- first, with who made them and the projects they reach. Owners see all;
-- user_id narrows it to one person's.
SELECT c.id, c.user_id, u.name AS user_name, u.email AS user_email, COALESCE(m.role, '')::text AS user_role,
       c.name, c.all_projects, c.scopes, c.created_at, c.expires_at, c.last_used_at, c.last_tool, k.kind AS client_kind,
       k.client_id AS client_ref,
       COALESCE((SELECT array_agg(p.name ORDER BY p.name) FROM agent_connection_projects cp
                 JOIN projects p ON p.id = cp.project_id WHERE cp.connection_id = c.id), '{}')::text[] AS project_names
FROM agent_connections c
JOIN users u ON u.id = c.user_id
JOIN oauth_clients k ON k.id = c.client_id
LEFT JOIN org_members m ON m.org_id = c.org_id AND m.user_id = c.user_id
WHERE c.org_id = sqlc.arg(org_id)::uuid AND c.revoked_at IS NULL AND c.expires_at > NOW()
  AND (sqlc.narg(user_id)::uuid IS NULL OR c.user_id = sqlc.narg(user_id)::uuid)
ORDER BY c.created_at DESC;

-- name: RevokeAgentConnection :one
UPDATE agent_connections SET revoked_at = NOW(), revoked_by = sqlc.narg(revoked_by)::uuid
WHERE id = sqlc.arg(id)::uuid AND revoked_at IS NULL
RETURNING id, org_id, user_id, name;

-- name: TouchAgentConnection :exec
UPDATE agent_connections SET last_used_at = NOW(), last_tool = COALESCE(sqlc.narg(tool)::text, last_tool)
WHERE id = sqlc.arg(id)::uuid;

-- name: InsertAgentToken :exec
INSERT INTO agent_tokens (token_hash, connection_id, kind, expires_at) VALUES ($1, $2, $3, $4);

-- name: ValidateAgentAccessToken :one
-- An access token of a live connection, with what every request needs: its
-- workspace's pause and the projects it reaches. The instance's pause is
-- read separately (instance_settings).
SELECT c.id, c.org_id, c.user_id, c.name, c.all_projects, c.scopes, o.agents_paused_at,
       COALESCE((SELECT array_agg(cp.project_id) FROM agent_connection_projects cp
                 WHERE cp.connection_id = c.id), '{}')::uuid[] AS project_ids
FROM agent_tokens t
JOIN agent_connections c ON c.id = t.connection_id
JOIN organizations o ON o.id = c.org_id
WHERE t.token_hash = $1 AND t.kind = 'access' AND t.expires_at > NOW()
  AND c.revoked_at IS NULL AND c.expires_at > NOW();

-- name: UseRefreshToken :one
-- Rotation: a refresh token is used once. No row means unknown, expired,
-- of a dead connection, or used before (GetAgentToken tells which).
UPDATE agent_tokens t SET used_at = NOW()
FROM agent_connections c
WHERE t.token_hash = $1 AND t.kind = 'refresh' AND t.used_at IS NULL AND t.expires_at > NOW()
  AND c.id = t.connection_id AND c.revoked_at IS NULL AND c.expires_at > NOW()
RETURNING t.connection_id, c.scopes, c.expires_at;

-- name: GetAgentToken :one
SELECT token_hash, connection_id, kind, expires_at, used_at FROM agent_tokens WHERE token_hash = $1;

-- name: DeleteAgentToken :exec
DELETE FROM agent_tokens WHERE token_hash = $1;

-- name: DeleteExpiredAgentTokens :execrows
DELETE FROM agent_tokens WHERE expires_at < $1;

-- name: ListAgentPolicies :many
SELECT action, env_class, decision FROM agent_policies WHERE org_id = $1;

-- name: UpsertAgentPolicy :exec
INSERT INTO agent_policies (org_id, action, env_class, decision, updated_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (org_id, action, env_class) DO UPDATE
SET decision = EXCLUDED.decision, updated_by = EXCLUDED.updated_by, updated_at = NOW();

-- name: ServiceEnvironmentClass :one
-- The policy's environment class of a service (D-100): a preview's,
-- a protected environment's, or another's. A project or workspace service
-- has no environment and counts as protected: it serves every environment.
SELECT s.id, s.environment_id, COALESCE(e.name, '')::text AS environment_name,
       COALESCE(p.name, sp.name, '')::text AS project_name, s.name,
       COALESCE(e.project_id, s.project_id) AS project_id, COALESCE(s.addon_slug, '')::text AS addon_slug,
       COALESCE(s.docker_image, '')::text AS docker_image, s.source_type, s.compose_repo_url,
       COALESCE(s.compose_content, '')::text AS compose_content,
       (CASE WHEN s.preview_of IS NOT NULL OR EXISTS (SELECT 1 FROM previews pv WHERE pv.environment_id = e.id) THEN 'preview'
             WHEN e.id IS NULL OR e.protected THEN 'protected'
             ELSE 'other' END)::text AS env_class
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = e.project_id
LEFT JOIN projects sp ON sp.id = s.project_id
WHERE s.id = $1;

-- name: ListStrictServicesOnServer :many
-- A server's services that run Sandboxed with a strict network (D-115),
-- at their own level or their workspace's default. Preview copies aren't
-- theirs to change: SandboxStrictPreviewCopies moves them.
SELECT s.id, s.name
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
LEFT JOIN organizations o ON o.id = COALESCE(p.org_id, s.scoped_org_id)
WHERE s.server_id = $1 AND COALESCE(s.isolation, o.default_isolation) = 'sandboxed_strict' AND s.preview_of IS NULL
ORDER BY s.name, s.id;

-- name: SandboxStrictPreviewCopies :exec
-- An untrusted preview's copies run strict where their server offers it,
-- Sandboxed elsewhere (D-115): for a server that stops offering it.
UPDATE services SET isolation = 'sandboxed'
WHERE server_id = $1 AND preview_of IS NOT NULL AND isolation = 'sandboxed_strict';

-- name: EnvironmentAgentClass :one
-- The policy's class of an environment (D-102), as ServiceEnvironmentClass.
SELECT e.id, e.name, e.project_id, p.name AS project_name,
       (CASE WHEN EXISTS (SELECT 1 FROM previews pv WHERE pv.environment_id = e.id) THEN 'preview'
             WHEN e.protected THEN 'protected'
             ELSE 'other' END)::text AS env_class
FROM environments e JOIN projects p ON p.id = e.project_id
WHERE e.id = $1;

-- name: ProjectAgentInfo :one
SELECT id, name, org_id FROM projects WHERE id = $1;

-- name: ListAgentProjectPolicies :many
-- A project's own cells (D-101).
SELECT action, env_class, decision FROM agent_project_policies WHERE project_id = $1;

-- name: ListOrgAgentProjectPolicies :many
-- Every project of a workspace with cells of its own, for Settings → Agents.
SELECT pp.project_id, p.name AS project_name, pp.action, pp.env_class, pp.decision
FROM agent_project_policies pp JOIN projects p ON p.id = pp.project_id
WHERE p.org_id = $1
ORDER BY p.name, pp.action, pp.env_class;

-- name: UpsertAgentProjectPolicy :exec
INSERT INTO agent_project_policies (project_id, action, env_class, decision, updated_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (project_id, action, env_class) DO UPDATE
SET decision = EXCLUDED.decision, updated_by = EXCLUDED.updated_by, updated_at = NOW();

-- name: DeleteAgentProjectPolicy :exec
DELETE FROM agent_project_policies WHERE project_id = $1 AND action = $2 AND env_class = $3;

-- name: InsertAgentApproval :one
INSERT INTO agent_approvals (id, org_id, connection_id, user_id, action, tool, service_id, environment_id, project_id, target,
                             request, sealed, request_hash, reason, plan, expires_at, plan_steps)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
RETURNING id, expires_at;

-- name: GetAgentApprovalSealed :one
-- set_secret's encrypted value, for the run Approve starts (D-102).
SELECT sealed FROM agent_approvals WHERE id = $1 AND state = 'approved' AND sealed IS NOT NULL;

-- name: ClearAgentApprovalSealed :exec
UPDATE agent_approvals SET sealed = NULL WHERE id = $1;

-- name: ClearStaleAgentApprovalSealed :execrows
-- A value whose request is over: turned down, expired, run, or approved an
-- hour ago and never run (the panel stopped meanwhile).
UPDATE agent_approvals SET sealed = NULL
WHERE sealed IS NOT NULL
  AND (state IN ('denied', 'cancelled') OR executed_at IS NOT NULL
       OR (state = 'waiting' AND expires_at < sqlc.arg(now)::timestamptz)
       OR (state = 'approved' AND decided_at < sqlc.arg(now)::timestamptz - INTERVAL '1 hour'));

-- name: FindOpenAgentApproval :one
-- The same call again attaches to its request: waiting, or approved and run
-- within its window, so a call that dropped can read the result.
SELECT id FROM agent_approvals
WHERE connection_id = $1 AND request_hash = $2 AND expires_at > NOW()
  AND (state = 'waiting' OR (state = 'approved' AND executed_at IS NOT NULL))
ORDER BY created_at DESC LIMIT 1;

-- name: GetAgentApproval :one
-- With what a deploy's approval allows (D-146): the deployment it rolls
-- back to (stored at the run; while the request waits, what is live now),
-- the approved deployment's state, and the approver's role now.
SELECT a.id, a.org_id, a.connection_id, a.user_id, u.name AS user_name, c.name AS agent_name, a.action, a.tool,
       a.service_id, a.environment_id, a.target, a.request, a.reason, a.plan, a.state, a.expires_at,
       a.decided_by, COALESCE(d.name, '')::text AS decided_by_name, a.decided_at, a.note, a.executed_at, a.result,
       a.created_at, COALESCE(m.role, '')::text AS user_role, a.project_id, a.plan_steps,
       rd.id AS rollback_to, COALESCE(rd.commit_sha, '')::text AS rollback_commit, a.rollback_used_at,
       COALESCE(ad.status, '')::text AS deployment_status, ad.finished_at AS deployment_finished_at,
       COALESCE(ad.commit_sha, '')::text AS deployment_commit,
       COALESCE(s.active_deployment_id = ad.id, false)::bool AS deployment_live,
       COALESCE(dm.role, '')::text AS decided_by_role
FROM agent_approvals a
JOIN users u ON u.id = a.user_id
JOIN agent_connections c ON c.id = a.connection_id
LEFT JOIN users d ON d.id = a.decided_by
LEFT JOIN org_members m ON m.org_id = a.org_id AND m.user_id = a.user_id
LEFT JOIN org_members dm ON dm.org_id = a.org_id AND dm.user_id = a.decided_by
LEFT JOIN services s ON s.id = a.service_id
LEFT JOIN deployments rd ON rd.id = COALESCE(a.rollback_to, CASE WHEN a.state = 'waiting' THEN s.active_deployment_id END)
LEFT JOIN deployments ad ON ad.id = a.deployment_id
WHERE a.id = $1;

-- name: ListWaitingAgentApprovals :many
-- What waits for a person in a workspace, oldest first (Needs you, the
-- Agents page).
SELECT a.id, a.connection_id, a.user_id, u.name AS user_name, c.name AS agent_name, a.action, a.tool, a.service_id,
       a.target, a.reason, a.expires_at, a.created_at
FROM agent_approvals a
JOIN users u ON u.id = a.user_id
JOIN agent_connections c ON c.id = a.connection_id
WHERE a.org_id = $1 AND a.state = 'waiting' AND a.expires_at > NOW()
ORDER BY a.created_at;

-- name: DecideAgentApproval :one
-- Approve or deny once, while it waits and hasn't expired.
UPDATE agent_approvals SET state = sqlc.arg(state)::text, decided_by = sqlc.arg(decided_by)::uuid, decided_at = NOW(),
       note = sqlc.arg(note)::text, sealed = CASE WHEN sqlc.arg(state)::text = 'approved' THEN sealed END
WHERE id = sqlc.arg(id)::uuid AND org_id = sqlc.arg(org_id)::uuid AND state = 'waiting' AND expires_at > NOW()
RETURNING id;

-- name: ClaimApprovalRun :one
-- An approved request runs once: the run claims it before it starts. A
-- deploy's claim keeps what is live now, the rollback it covers (D-146).
UPDATE agent_approvals a SET executed_at = NOW(),
       rollback_to = CASE WHEN a.tool = 'deploy' THEN (SELECT s.active_deployment_id FROM services s WHERE s.id = a.service_id) END
WHERE a.id = $1 AND a.connection_id = $2 AND a.state = 'approved' AND a.executed_at IS NULL
RETURNING a.id, a.service_id, a.action;

-- name: SetAgentApprovalResult :exec
-- What the run answered, and the deployment an approved deploy started.
UPDATE agent_approvals SET result = sqlc.arg(result), deployment_id = sqlc.narg(deployment_id)::uuid WHERE id = sqlc.arg(id)::uuid;

-- name: ClaimApprovalRollback :one
-- The rollback a deploy's approval covers, used once (D-146): the same
-- connection, that service, back to the deployment live when the deploy
-- ran; the approved deployment went live under 30 minutes ago and is still
-- what's live (a failed health check or a rollback since leaves it not
-- live); the approver is still an owner or developer of the workspace.
UPDATE agent_approvals a SET rollback_used_at = NOW()
FROM services s, deployments d, org_members m, users u
WHERE a.connection_id = sqlc.arg(connection_id)::uuid AND a.service_id = sqlc.arg(service_id)::uuid
  AND a.rollback_to = sqlc.arg(rollback_to)::uuid
  AND a.tool = 'deploy' AND a.state = 'approved' AND a.rollback_used_at IS NULL
  AND s.id = a.service_id AND s.active_deployment_id = a.deployment_id
  AND d.id = a.deployment_id AND d.status = 'running' AND d.finished_at > NOW() - INTERVAL '30 minutes'
  AND m.org_id = a.org_id AND m.user_id = a.decided_by AND m.role IN ('owner', 'developer')
  AND u.id = a.decided_by
RETURNING a.id, a.decided_by, u.name AS approver_name;

-- name: ReleaseApprovalRollback :exec
-- A covered rollback that failed before it ran is unused again.
UPDATE agent_approvals SET rollback_used_at = NULL WHERE id = $1;

-- name: CancelConnectionApprovals :execrows
UPDATE agent_approvals SET state = 'cancelled', decided_at = NOW(), sealed = NULL, note = $2
WHERE connection_id = $1 AND state = 'waiting';

-- name: CancelOrgApprovals :execrows
UPDATE agent_approvals SET state = 'cancelled', decided_at = NOW(), sealed = NULL, note = $2
WHERE org_id = $1 AND state = 'waiting';

-- name: CancelAllApprovals :execrows
UPDATE agent_approvals SET state = 'cancelled', decided_at = NOW(), sealed = NULL, note = $1
WHERE state = 'waiting';

-- name: SetOrgAgentsPaused :exec
UPDATE organizations SET agents_paused_at = sqlc.narg(paused_at)::timestamptz, agents_paused_by = sqlc.narg(paused_by)::uuid
WHERE id = sqlc.arg(id)::uuid;

-- name: GetOrgAgentsPaused :one
SELECT o.agents_paused_at, o.agents_paused_by, COALESCE(u.name, '')::text AS paused_by_name
FROM organizations o LEFT JOIN users u ON u.id = o.agents_paused_by
WHERE o.id = $1;

-- name: SetInstanceSetting :exec
INSERT INTO instance_settings (key, value, updated_by) VALUES ($1, $2, $3)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = NOW();

-- name: DeleteInstanceSetting :exec
DELETE FROM instance_settings WHERE key = $1;
