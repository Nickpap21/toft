-- The part of a stored service the deploy policy checks, with where it
-- belongs (deploypolicy.StoredService): its workspace (an org-shared
-- service's own, else its project's) and its server's name ("" is the
-- control-plane server).

-- name: GetPolicyService :one
SELECT s.id::text AS id, s.name, s.source_type, COALESCE(s.compose_content, '')::text AS compose_content,
       s.compose_repo_url, s.volumes, s.pre_deploy_commands,
       COALESCE(s.docker_image, '')::text AS docker_image, COALESCE(s.git_repo_url, '')::text AS git_repo_url,
       COALESCE(s.repository_id::text, '')::text AS repository_id,
       COALESCE(s.server_id::text, '')::text AS server_id, COALESCE(sv.name, '')::text AS server_name,
       COALESCE(o.id::text, '')::text AS org_id, COALESCE(o.name, '')::text AS org_name,
       COALESCE(p.id::text, '')::text AS project_id, COALESCE(p.name, '')::text AS project_name,
       COALESCE(e.name, '')::text AS environment_name,
       COALESCE((SELECT array_agg(d.hostname ORDER BY d.created_at) FROM domains d WHERE d.service_id = s.id), '{}')::text[] AS hostnames
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
LEFT JOIN organizations o ON o.id = CASE WHEN s.scope = 'org' THEN s.scoped_org_id ELSE p.org_id END
LEFT JOIN servers sv ON sv.id = s.server_id
WHERE s.id = $1;

-- name: ListPolicyServices :many
SELECT s.id::text AS id, s.name, s.source_type, COALESCE(s.compose_content, '')::text AS compose_content,
       s.compose_repo_url, s.volumes, s.pre_deploy_commands,
       COALESCE(s.docker_image, '')::text AS docker_image, COALESCE(s.git_repo_url, '')::text AS git_repo_url,
       COALESCE(s.repository_id::text, '')::text AS repository_id,
       COALESCE(s.server_id::text, '')::text AS server_id, COALESCE(sv.name, '')::text AS server_name,
       COALESCE(o.id::text, '')::text AS org_id, COALESCE(o.name, '')::text AS org_name,
       COALESCE(p.id::text, '')::text AS project_id, COALESCE(p.name, '')::text AS project_name,
       COALESCE(e.name, '')::text AS environment_name,
       COALESCE((SELECT array_agg(d.hostname ORDER BY d.created_at) FROM domains d WHERE d.service_id = s.id), '{}')::text[] AS hostnames
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
LEFT JOIN organizations o ON o.id = CASE WHEN s.scope = 'org' THEN s.scoped_org_id ELSE p.org_id END
LEFT JOIN servers sv ON sv.id = s.server_id
ORDER BY o.name NULLS LAST, p.name NULLS LAST, s.name;

-- A service's deploy policy exception, with who granted it. allowed_values
-- is NULL for an exception granted before 0055 (it covers by rule).

-- name: GetPolicyException :one
SELECT x.service_id::text AS service_id, x.allowed_rules, x.reason, COALESCE(x.granted_by::text, '')::text AS granted_by,
       COALESCE(u.name, '')::text AS granted_by_name, COALESCE(u.email, '')::text AS granted_by_email,
       x.created_at, x.updated_at, x.allowed_values
FROM service_deploy_policy_exceptions x
LEFT JOIN users u ON u.id = x.granted_by
WHERE x.service_id = $1;

-- name: ListPolicyExceptions :many
SELECT x.service_id::text AS service_id, x.allowed_rules, x.reason, COALESCE(x.granted_by::text, '')::text AS granted_by,
       COALESCE(u.name, '')::text AS granted_by_name, COALESCE(u.email, '')::text AS granted_by_email,
       x.created_at, x.updated_at, x.allowed_values
FROM service_deploy_policy_exceptions x
LEFT JOIN users u ON u.id = x.granted_by;

-- name: GetPolicyExceptionForReplace :one
-- What a grant replaces, locked until it commits; its version is computed
-- from this. After LockServiceForReplace, which also holds off the first grant.
SELECT allowed_rules, updated_at FROM service_deploy_policy_exceptions
WHERE service_id = $1
FOR UPDATE;

-- name: UpsertPolicyException :exec
-- Granting again replaces the rules, the reason and the pinned values.
INSERT INTO service_deploy_policy_exceptions (service_id, allowed_rules, reason, granted_by, allowed_values)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (service_id) DO UPDATE
SET allowed_rules  = EXCLUDED.allowed_rules,
    reason         = EXCLUDED.reason,
    granted_by     = EXCLUDED.granted_by,
    allowed_values = EXCLUDED.allowed_values,
    updated_at     = NOW();

-- name: RepinPolicyValues :execrows
-- Rewrites pinned fingerprints a deploy matched by their plain SHA-256 to
-- their HMAC (D-126): the values approved stay the same, so the rest of the
-- exception, its updated_at included, does too. Only a pin still holding
-- the old fingerprint changes.
UPDATE service_deploy_policy_exceptions e
SET allowed_values = (
    SELECT jsonb_agg(CASE WHEN m.new_fp IS NULL THEN a.v ELSE jsonb_set(a.v, '{fingerprint}', to_jsonb(m.new_fp)) END ORDER BY a.ord)
    FROM jsonb_array_elements(e.allowed_values) WITH ORDINALITY AS a(v, ord)
    LEFT JOIN (SELECT unnest(sqlc.arg(old_fps)::text[]) AS old_fp, unnest(sqlc.arg(new_fps)::text[]) AS new_fp) m
           ON m.old_fp = a.v->>'fingerprint'
)
WHERE e.service_id = sqlc.arg(service_id)
  AND EXISTS (SELECT 1 FROM jsonb_array_elements(e.allowed_values) AS x(v) WHERE x.v->>'fingerprint' = ANY(sqlc.arg(old_fps)::text[]));

-- name: GetPolicyCheck :one
-- The last deploy-time check of a service's compose file from a repository.
SELECT commit_sha, checked_at, violations FROM service_deploy_policy_checks WHERE service_id = $1;

-- name: UpsertPolicyCheck :exec
INSERT INTO service_deploy_policy_checks (service_id, commit_sha, checked_at, violations)
VALUES ($1, $2, NOW(), $3)
ON CONFLICT (service_id) DO UPDATE
SET commit_sha = EXCLUDED.commit_sha, checked_at = EXCLUDED.checked_at, violations = EXCLUDED.violations;

-- name: ListPolicyChecks :many
SELECT service_id::text AS service_id, commit_sha, checked_at, violations FROM service_deploy_policy_checks;
