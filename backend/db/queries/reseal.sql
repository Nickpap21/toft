-- The start-up pass and `server reseal --check` (D-126): every sealed value
-- that isn't v1 under the current key, and a compare-and-swap that writes it
-- again. Each List returns up to 1,000 rows after a cursor, in key order;
-- current is 'v1.<key id>.%'. A Replace changes a row only while it still
-- holds old_value, so a value saved in between is kept (as
-- ReplaceServerSSHKeys). id is the row's key and ident its place's identity,
-- crypto.AD's parts joined by '/'. ORDER BY names the table's column: a bare
-- id is the text alias, which sorts the whole table for every batch.

-- name: ResealListEnvVars :many
SELECT id::text AS id, (service_id::text || '/' || key)::text AS ident, value AS sealed
FROM env_vars
WHERE is_secret AND value <> '' AND value NOT LIKE sqlc.arg(current)::text AND id > sqlc.arg(after)::text::uuid
ORDER BY env_vars.id
LIMIT 1000;

-- name: ResealReplaceEnvVars :execrows
UPDATE env_vars t SET value = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.id = v.id AND t.value = v.old_value;

-- name: ResealListEnvironmentEnvVars :many
SELECT id::text AS id, (environment_id::text || '/' || key)::text AS ident, value AS sealed
FROM environment_env_vars
WHERE is_secret AND value <> '' AND value NOT LIKE sqlc.arg(current)::text AND id > sqlc.arg(after)::text::uuid
ORDER BY environment_env_vars.id
LIMIT 1000;

-- name: ResealReplaceEnvironmentEnvVars :execrows
UPDATE environment_env_vars t SET value = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.id = v.id AND t.value = v.old_value;

-- name: ResealListServiceRegistryPasswords :many
SELECT id::text AS id, id::text AS ident, COALESCE(registry_password, '') AS sealed
FROM services
WHERE registry_password <> '' AND registry_password NOT LIKE sqlc.arg(current)::text AND id > sqlc.arg(after)::text::uuid
ORDER BY services.id
LIMIT 1000;

-- name: ResealReplaceServiceRegistryPasswords :execrows
UPDATE services t SET registry_password = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.id = v.id AND t.registry_password = v.old_value;

-- name: ResealListServiceGitTokens :many
SELECT id::text AS id, id::text AS ident, COALESCE(git_token, '') AS sealed
FROM services
WHERE git_token <> '' AND git_token NOT LIKE sqlc.arg(current)::text AND id > sqlc.arg(after)::text::uuid
ORDER BY services.id
LIMIT 1000;

-- name: ResealReplaceServiceGitTokens :execrows
UPDATE services t SET git_token = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.id = v.id AND t.git_token = v.old_value;

-- name: ResealListServiceBasicAuthPasswords :many
SELECT id::text AS id, id::text AS ident, basic_auth_password AS sealed
FROM services
WHERE basic_auth_password <> '' AND basic_auth_password NOT LIKE sqlc.arg(current)::text AND id > sqlc.arg(after)::text::uuid
ORDER BY services.id
LIMIT 1000;

-- name: ResealReplaceServiceBasicAuthPasswords :execrows
UPDATE services t SET basic_auth_password = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.id = v.id AND t.basic_auth_password = v.old_value;

-- name: ResealListServerSSHKeys :many
SELECT id::text AS id, id::text AS ident, ssh_key AS sealed
FROM servers
WHERE ssh_key <> '' AND ssh_key NOT LIKE sqlc.arg(current)::text AND id > sqlc.arg(after)::text::uuid
ORDER BY servers.id
LIMIT 1000;

-- name: ResealReplaceServerSSHKeys :execrows
UPDATE servers t SET ssh_key = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.id = v.id AND t.ssh_key = v.old_value;

-- name: ResealListServerDNSCredentials :many
SELECT server_id::text AS id, server_id::text AS ident, credentials AS sealed
FROM server_dns_providers
WHERE credentials <> '' AND credentials NOT LIKE sqlc.arg(current)::text AND server_id > sqlc.arg(after)::text::uuid
ORDER BY server_id
LIMIT 1000;

-- name: ResealReplaceServerDNSCredentials :execrows
UPDATE server_dns_providers t SET credentials = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.server_id = v.id AND t.credentials = v.old_value;

-- name: ResealListBackupSecretKeys :many
SELECT id::text AS id, id::text AS ident, secret_key AS sealed
FROM backup_destinations
WHERE secret_key <> '' AND secret_key NOT LIKE sqlc.arg(current)::text AND id > sqlc.arg(after)::text::uuid
ORDER BY backup_destinations.id
LIMIT 1000;

-- name: ResealReplaceBackupSecretKeys :execrows
UPDATE backup_destinations t SET secret_key = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.id = v.id AND t.secret_key = v.old_value;

-- name: ResealListBackupRepoPasswords :many
SELECT id::text AS id, id::text AS ident, repo_password AS sealed
FROM backup_destinations
WHERE repo_password <> '' AND repo_password NOT LIKE sqlc.arg(current)::text AND id > sqlc.arg(after)::text::uuid
ORDER BY backup_destinations.id
LIMIT 1000;

-- name: ResealReplaceBackupRepoPasswords :execrows
UPDATE backup_destinations t SET repo_password = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.id = v.id AND t.repo_password = v.old_value;

-- name: ResealListInviteTokens :many
SELECT id::text AS id, id::text AS ident, COALESCE(token_encrypted, '') AS sealed
FROM org_invites
WHERE token_encrypted <> '' AND token_encrypted NOT LIKE sqlc.arg(current)::text AND id > sqlc.arg(after)::text::uuid
ORDER BY org_invites.id
LIMIT 1000;

-- name: ResealReplaceInviteTokens :execrows
UPDATE org_invites t SET token_encrypted = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.id = v.id AND t.token_encrypted = v.old_value;

-- name: ResealListProjectWebhookSecrets :many
SELECT project_id::text AS id, project_id::text AS ident, secret AS sealed
FROM project_webhooks
WHERE secret <> '' AND secret NOT LIKE sqlc.arg(current)::text AND project_id > sqlc.arg(after)::text::uuid
ORDER BY project_id
LIMIT 1000;

-- name: ResealReplaceProjectWebhookSecrets :execrows
UPDATE project_webhooks t SET secret = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.project_id = v.id AND t.secret = v.old_value;

-- name: ResealListMirrorPasswords :many
SELECT id::text AS id, id::text AS ident, COALESCE(password_encrypted, '') AS sealed
FROM repository_mirrors
WHERE password_encrypted <> '' AND password_encrypted NOT LIKE sqlc.arg(current)::text AND id > sqlc.arg(after)::text::uuid
ORDER BY repository_mirrors.id
LIMIT 1000;

-- name: ResealReplaceMirrorPasswords :execrows
UPDATE repository_mirrors t SET password_encrypted = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.id = v.id AND t.password_encrypted = v.old_value;

-- name: ResealListRepositoryWebhookSecrets :many
SELECT id::text AS id, id::text AS ident, secret_encrypted AS sealed
FROM repository_webhooks
WHERE secret_encrypted <> '' AND secret_encrypted NOT LIKE sqlc.arg(current)::text AND id > sqlc.arg(after)::text::uuid
ORDER BY repository_webhooks.id
LIMIT 1000;

-- name: ResealReplaceRepositoryWebhookSecrets :execrows
UPDATE repository_webhooks t SET secret_encrypted = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.id = v.id AND t.secret_encrypted = v.old_value;

-- name: ResealListAlertSigningSecrets :many
SELECT org_id::text AS id, org_id::text AS ident, signing_secret AS sealed
FROM alert_channels
WHERE signing_secret <> '' AND signing_secret NOT LIKE sqlc.arg(current)::text AND org_id > sqlc.arg(after)::text::uuid
ORDER BY org_id
LIMIT 1000;

-- name: ResealReplaceAlertSigningSecrets :execrows
UPDATE alert_channels t SET signing_secret = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.org_id = v.id AND t.signing_secret = v.old_value;

-- name: ResealListAgentApprovalSealed :many
SELECT id::text AS id, id::text AS ident, COALESCE(sealed, '') AS sealed
FROM agent_approvals
WHERE sealed <> '' AND sealed NOT LIKE sqlc.arg(current)::text AND id > sqlc.arg(after)::text::uuid
ORDER BY agent_approvals.id
LIMIT 1000;

-- name: ResealReplaceAgentApprovalSealed :execrows
UPDATE agent_approvals t SET sealed = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.id = v.id AND t.sealed = v.old_value;

-- name: ResealListSecretReveals :many
SELECT id::text AS id, id::text AS ident, sealed
FROM secret_reveals
WHERE sealed NOT LIKE sqlc.arg(current)::text AND id > sqlc.arg(after)::text::uuid
ORDER BY secret_reveals.id
LIMIT 1000;

-- name: ResealReplaceSecretReveals :execrows
UPDATE secret_reveals t SET sealed = v.new_value
FROM (SELECT unnest(sqlc.arg(ids)::text[])::uuid AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.id = v.id AND t.sealed = v.old_value;

-- name: ResealListInstanceKeys :many
-- The instance's own keys, each {"key": <sealed>}. COLLATE "C" so that the
-- first cursor (the zero UUID) sorts before every key.
SELECT key AS id, key AS ident, (value->>'key')::text AS sealed
FROM instance_settings
WHERE key IN ('git_ssh_host_key', 'agent_panel_key', 'hmac_key')
  AND value->>'key' <> '' AND value->>'key' NOT LIKE sqlc.arg(current)::text
  AND key COLLATE "C" > sqlc.arg(after)::text
ORDER BY key COLLATE "C"
LIMIT 1000;

-- name: ResealReplaceInstanceKeys :execrows
UPDATE instance_settings t SET value = jsonb_set(t.value, '{key}', to_jsonb(v.new_value))
FROM (SELECT unnest(sqlc.arg(ids)::text[]) AS id,
             unnest(sqlc.arg(old_values)::text[]) AS old_value,
             unnest(sqlc.arg(new_values)::text[]) AS new_value) v
WHERE t.key = v.id AND t.value->>'key' = v.old_value;
