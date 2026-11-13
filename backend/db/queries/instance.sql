-- name: AcquireInstanceLock :exec
-- Transaction-scoped advisory lock that serialises first-run registration
-- and operator bootstrap. The key is instance.RegistrationLockKey.
SELECT pg_advisory_xact_lock(sqlc.arg(lock_key)::bigint);

-- name: UsersExist :one
SELECT EXISTS(SELECT 1 FROM users) AS users_exist;

-- name: IsInstanceOperator :one
SELECT EXISTS(SELECT 1 FROM instance_operators WHERE user_id = $1) AS is_operator;

-- name: ListInstanceOperators :many
SELECT o.user_id, u.email, u.name, u.is_active, o.source, o.granted_by, o.granted_at
FROM instance_operators o
JOIN users u ON u.id = o.user_id
ORDER BY o.granted_at, o.user_id;

-- name: InsertInstanceOperator :execrows
INSERT INTO instance_operators (user_id, granted_by, source)
VALUES ($1, $2, $3)
ON CONFLICT (user_id) DO NOTHING;

-- name: LockInstanceOperators :many
-- Locks every operator row (in a fixed order, so concurrent callers cannot
-- deadlock) before a change that could leave the instance without an active
-- operator.
SELECT user_id FROM instance_operators ORDER BY user_id FOR UPDATE;

-- name: CountOtherActiveOperators :one
SELECT count(*) FROM instance_operators o
JOIN users u ON u.id = o.user_id
WHERE u.is_active AND o.user_id <> $1;

-- name: DeleteInstanceOperator :execrows
DELETE FROM instance_operators WHERE user_id = $1;

-- name: PromoteEarliestUserToOperator :one
INSERT INTO instance_operators (user_id, source)
SELECT u.id, 'bootstrap' FROM users u
WHERE u.is_active AND NOT EXISTS (SELECT 1 FROM instance_operators)
ORDER BY u.created_at, u.id
LIMIT 1
ON CONFLICT (user_id) DO NOTHING
RETURNING user_id;

-- name: FindUserIDsByEmailCI :many
SELECT id FROM users WHERE lower(email) = lower(sqlc.arg(email)::text) ORDER BY id;

-- name: UserIDExists :one
SELECT EXISTS(SELECT 1 FROM users WHERE id = $1) AS user_exists;

-- name: GetInstanceSetting :one
SELECT value FROM instance_settings WHERE key = $1;

-- name: MergeInstanceSetting :one
-- Writes the given top-level fields of a setting, keeping the others.
INSERT INTO instance_settings (key, value, updated_by) VALUES ($1, $2, $3)
ON CONFLICT (key) DO UPDATE
SET value = instance_settings.value || EXCLUDED.value,
    updated_by = EXCLUDED.updated_by,
    updated_at = NOW()
RETURNING value;

-- name: CountPanelHostServices :one
-- Services set to run on the panel host (no server of their own).
SELECT count(*) FROM services WHERE server_id IS NULL;

-- name: ListInstanceUsers :many
SELECT u.id, u.email, u.name, u.is_active, u.created_at, u.deactivated_at, u.last_login_at,
       COALESCE(d.name, '')::text AS deactivated_by_name,
       EXISTS(SELECT 1 FROM instance_operators o WHERE o.user_id = u.id) AS is_operator,
       COALESCE((SELECT array_agg(o.name ORDER BY o.name)
                 FROM org_members m JOIN organizations o ON o.id = m.org_id
                 WHERE m.user_id = u.id), '{}')::text[] AS workspaces,
       (SELECT max(r.expires_at) FROM password_resets r
         WHERE r.user_id = u.id AND r.used_at IS NULL AND r.expires_at > NOW())::timestamptz AS reset_expires_at
FROM users u
LEFT JOIN users d ON d.id = u.deactivated_by
WHERE u.deleted_at IS NULL
ORDER BY u.created_at, u.id;

-- name: CountDeletedUsers :one
SELECT count(*) FROM users WHERE deleted_at IS NOT NULL;

-- name: GetUserForDelete :one
SELECT email, is_active, (deleted_at IS NOT NULL)::boolean AS deleted FROM users WHERE id = $1 FOR UPDATE;

-- name: DeleteUserMemberships :exec
DELETE FROM org_members WHERE user_id = $1;

-- name: DeleteUserAPIKeys :exec
DELETE FROM api_keys WHERE user_id = $1;

-- name: TombstoneUser :exec
-- The account's row stays (D-57); what identified the person goes.
UPDATE users SET email = 'deleted-' || id::text || '@deleted.invalid', name = 'Deleted account',
       password_hash = sqlc.arg(password_hash), theme = NULL, tokens_valid_from = NOW(),
       deleted_at = NOW(), deleted_by = sqlc.narg(actor_id)::uuid, updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: WriteInstanceAudit :exec
INSERT INTO instance_audit_log (actor_id, action, target_user_id, detail)
VALUES (sqlc.narg(actor_id)::uuid, sqlc.arg(action), sqlc.narg(target_user_id)::uuid, sqlc.narg(detail)::jsonb);

-- name: ListInstanceAudit :many
SELECT a.id, a.action, COALESCE(a.detail, '{}'::jsonb) AS detail, a.created_at,
       a.actor_id, COALESCE(au.name, '')::text AS actor_name, COALESCE(au.email, '')::text AS actor_email,
       a.target_user_id, COALESCE(tu.name, '')::text AS target_name, COALESCE(tu.email, '')::text AS target_email
FROM instance_audit_log a
LEFT JOIN users au ON au.id = a.actor_id
LEFT JOIN users tu ON tu.id = a.target_user_id
WHERE (sqlc.narg(before_at)::timestamptz IS NULL OR (a.created_at, a.id) < (sqlc.narg(before_at)::timestamptz, sqlc.narg(before_id)::uuid))
  AND (sqlc.narg(kind)::text IS NULL OR (sqlc.narg(kind)::text = 'panel') = (a.action ~ '^(panel|recovery_kit|setup|agents)\.'))
ORDER BY a.created_at DESC, a.id DESC
LIMIT sqlc.arg(page_size);

-- name: GetUserActiveForUpdate :one
SELECT is_active FROM users WHERE id = $1 FOR UPDATE;

-- name: SetUserActive :one
-- A deactivation records who made it and when, unless the account was
-- already inactive; a reactivation clears both. A deactivation also ends
-- every session, so reactivating the account revives none of them.
UPDATE users u SET is_active = sqlc.arg(is_active), updated_at = NOW(),
       tokens_valid_from = CASE WHEN sqlc.arg(is_active)::boolean THEN u.tokens_valid_from ELSE NOW() END,
       deactivated_at = CASE WHEN sqlc.arg(is_active)::boolean THEN NULL
                             WHEN u.is_active THEN NOW() ELSE u.deactivated_at END,
       deactivated_by = CASE WHEN sqlc.arg(is_active)::boolean THEN NULL
                             WHEN u.is_active THEN sqlc.narg(actor_id)::uuid ELSE u.deactivated_by END
WHERE u.id = sqlc.arg(id)
RETURNING u.id, u.email, u.name, u.is_active, u.created_at, u.deactivated_at, u.last_login_at,
          COALESCE((SELECT d.name FROM users d WHERE d.id = u.deactivated_by), '')::text AS deactivated_by_name,
          COALESCE((SELECT array_agg(o.name ORDER BY o.name)
                    FROM org_members m JOIN organizations o ON o.id = m.org_id
                    WHERE m.user_id = u.id), '{}')::text[] AS workspaces,
          (SELECT max(r.expires_at) FROM password_resets r
            WHERE r.user_id = u.id AND r.used_at IS NULL AND r.expires_at > NOW())::timestamptz AS reset_expires_at;

-- name: ExpireInvitesExposedToUser :execrows
-- Run when a user is deactivated. Expires every pending invite they could
-- still redeem or hand out: the ones they created, and every pending invite in
-- a workspace they own, because owners can read all pending tokens there
-- (GET /org/invites). Without this, a deactivated owner could register a new
-- active account from one of those links.
UPDATE org_invites SET expires_at = NOW()
WHERE used_at IS NULL
  AND expires_at > NOW()
  AND (created_by = sqlc.arg(user_id)
       OR org_id IN (SELECT m.org_id FROM org_members m
                     WHERE m.user_id = sqlc.arg(user_id) AND m.role = 'owner'));

-- name: CreatePasswordReset :exec
INSERT INTO password_resets (user_id, created_by, token_hash, expires_at)
VALUES ($1, $2, $3, $4);

-- name: DeleteUnusedPasswordResets :exec
DELETE FROM password_resets WHERE user_id = $1 AND used_at IS NULL;

-- name: GetPasswordResetByHash :one
SELECT r.expires_at, r.used_at, u.email, u.name, u.is_active
FROM password_resets r JOIN users u ON u.id = r.user_id
WHERE r.token_hash = $1;

-- name: ClaimPasswordReset :one
-- Marks the link used, only if it is unused, unexpired and its account is
-- active, so two redemptions cannot both succeed.
UPDATE password_resets r SET used_at = NOW()
FROM users u
WHERE r.token_hash = $1 AND r.used_at IS NULL AND r.expires_at > NOW()
  AND u.id = r.user_id AND u.is_active
RETURNING r.user_id, u.email;

-- name: InsertPanelSample :exec
INSERT INTO panel_samples (taken_at, cpu_percent, memory_used_bytes, disk_used_bytes)
VALUES ($1, $2, $3, $4)
ON CONFLICT (taken_at) DO NOTHING;

-- name: ListPanelSamples :many
-- The panel server's samples since a time, averaged into buckets (5 minutes
-- for a day, 30 for a week), so a chart gets a few hundred points.
SELECT date_bin(sqlc.arg(bucket)::interval, taken_at, TIMESTAMPTZ '2000-01-01')::timestamptz AS bucket_at,
       avg(cpu_percent)::float8        AS cpu_percent,
       max(cpu_percent)::float8        AS cpu_peak,
       avg(memory_used_bytes)::bigint  AS memory_used_bytes,
       max(disk_used_bytes)::bigint    AS disk_used_bytes
FROM panel_samples
WHERE taken_at >= sqlc.arg(since)::timestamptz
GROUP BY bucket_at
ORDER BY bucket_at;

-- name: DeletePanelSamplesBefore :execrows
DELETE FROM panel_samples WHERE taken_at < $1;

-- name: PanelDatabaseStats :one
-- The panel's own database: its size, connections and version.
SELECT pg_database_size(current_database())::bigint AS size_bytes,
       (SELECT count(*) FROM pg_catalog.pg_stat_activity WHERE datname = current_database())::int AS connections,
       current_setting('max_connections')::int AS max_connections,
       current_setting('server_version')::text AS version;

-- name: DeleteUserSSHKeys :exec
-- A reset that revokes the account's access (after a compromise), with
-- DeleteUserAPIKeys.
DELETE FROM ssh_keys WHERE user_id = $1;

-- name: RevokeUserAgentConnections :exec
UPDATE agent_connections SET revoked_at = NOW(), revoked_by = $1
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: DeleteUserOAuthCodes :exec
-- An authorization code not yet exchanged would make a new connection.
DELETE FROM oauth_codes WHERE user_id = $1;
