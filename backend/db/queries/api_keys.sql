-- name: CreateAPIKey :one
-- The key, with its project's name for the audit log.
WITH k AS (
    INSERT INTO api_keys (user_id, name, key_hash, prefix, project_id, scopes, expires_at, service_ids)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
    RETURNING id, user_id, name, prefix, project_id, scopes, last_used_at, created_at, expires_at, service_ids
)
SELECT k.id, k.user_id, k.name, k.prefix, k.project_id, k.scopes, k.last_used_at, k.created_at, k.expires_at, k.service_ids,
       COALESCE(p.name, '')::text AS project_name
FROM k LEFT JOIN projects p ON p.id = k.project_id;

-- name: ListAPIKeysByUser :many
-- A project key's project and workspace are named: the project may be in a
-- workspace other than the one open in the panel.
SELECT k.id, k.user_id, k.name, k.prefix, k.project_id, k.scopes, k.last_used_at, k.created_at, k.expires_at, k.service_ids,
       COALESCE(p.name, '')::text AS project_name,
       COALESCE(o.name, '')::text AS org_name
FROM api_keys k
LEFT JOIN projects p ON p.id = k.project_id
LEFT JOIN organizations o ON o.id = p.org_id
WHERE k.user_id = $1
ORDER BY k.created_at DESC;

-- GetAPIKeyForAuth returns who a key acts for: its user, and for a
-- project-scoped key the project, the project's workspace, the scopes and
-- the services it is limited to; and when it expires.
-- name: GetAPIKeyForAuth :one
SELECT k.id, k.user_id, k.project_id, k.scopes, p.org_id AS project_org_id, k.service_ids, k.expires_at
FROM api_keys k
LEFT JOIN projects p ON p.id = k.project_id
WHERE k.key_hash = $1;

-- name: DeleteAPIKey :one
-- The deleted key, with its project's name and workspace: a project key's
-- revocation goes to that workspace's audit log.
WITH k AS (
    DELETE FROM api_keys AS d WHERE d.id = sqlc.arg(id) AND d.user_id = sqlc.arg(user_id)
    RETURNING d.id, d.user_id, d.name, d.prefix, d.project_id, d.scopes, d.last_used_at, d.created_at, d.expires_at, d.service_ids
)
SELECT k.id, k.user_id, k.name, k.prefix, k.project_id, k.scopes, k.last_used_at, k.created_at, k.expires_at, k.service_ids,
       COALESCE(p.name, '')::text AS project_name, p.org_id AS project_org_id
FROM k LEFT JOIN projects p ON p.id = k.project_id;

-- name: UpdateAPIKeyLastUsed :exec
UPDATE api_keys SET last_used_at = NOW() WHERE id = $1;

-- name: ListAPIKeyServices :many
-- The services a key names that still exist, with their project (as
-- apikey.EnforceScopes resolves it) and environment, for checking a new
-- key's and naming a listed key's.
SELECT s.id, s.name, COALESCE(e.project_id, s.project_id) AS project_id,
       COALESCE(e.name, '')::text AS environment_name
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
WHERE s.id = ANY(sqlc.arg(ids)::uuid[])
ORDER BY environment_name, s.name;

-- name: ListProjectAPIKeys :many
-- A project's keys, newest first, a page at a time: every member's, or
-- user_id's only. Owner is the member's name, or their email without one.
SELECT k.id, k.name, k.prefix, k.scopes, k.user_id, COALESCE(NULLIF(u.name, ''), u.email)::text AS owner_name,
       k.last_used_at, k.created_at, k.expires_at
FROM api_keys k
JOIN users u ON u.id = k.user_id
WHERE k.project_id = sqlc.arg(project_id)
  AND (sqlc.narg(user_id)::uuid IS NULL OR k.user_id = sqlc.narg(user_id)::uuid)
  AND (sqlc.narg(before_at)::timestamptz IS NULL
       OR (k.created_at, k.id) < (sqlc.narg(before_at)::timestamptz, sqlc.narg(before_id)::uuid))
ORDER BY k.created_at DESC, k.id DESC
LIMIT sqlc.arg(lim);
