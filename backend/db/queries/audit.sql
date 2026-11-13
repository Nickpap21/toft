-- name: WriteAuditEntry :exec
INSERT INTO audit_log (org_id, actor_id, action, resource, metadata, agent_connection_id, api_key_id)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListAuditEntries :many
-- The workspace's audit log, newest first, 50 at a time behind a
-- (created_at, id) cursor, with what each entry is about resolved to names
-- (D-46). A resource is "<kind>:<id>", optionally followed by " org:<id>"
-- (a server share's other workspace). total counts every entry the filters
-- match, ignoring the cursor and the limit. The org filter and order are
-- served by idx_audit_log_org_created. agent is '' (everyone), 'any' (agents
-- only), 'none' (people only) or a connection's ID (D-100). api_key is ''
-- (every entry), 'any' (made with a key) or 'none' (made without one).
WITH entries AS (
    SELECT a.id, a.actor_id, a.agent_connection_id, a.api_key_id, a.action, COALESCE(a.resource, '')::text AS resource,
           COALESCE(a.metadata, '{}'::jsonb) AS metadata, a.created_at,
           split_part(COALESCE(a.resource, ''), ':', 1) AS kind,
           split_part(split_part(COALESCE(a.resource, ''), ' ', 1), ':', 2) AS target,
           split_part(split_part(COALESCE(a.resource, ''), ' ', 2), ':', 2) AS other
    FROM audit_log a
    WHERE a.org_id = sqlc.arg(org_id)::uuid
      AND (sqlc.narg(since)::timestamptz IS NULL OR a.created_at >= sqlc.narg(since)::timestamptz)
      AND (cardinality(sqlc.arg(actions)::text[]) = 0 OR a.action = ANY(sqlc.arg(actions)::text[]))
      AND (sqlc.narg(actor_id)::uuid IS NULL OR a.actor_id = sqlc.narg(actor_id)::uuid)
      AND (sqlc.arg(resource)::text = '' OR a.resource = sqlc.arg(resource)::text)
      AND (sqlc.arg(agent)::text = ''
           OR (sqlc.arg(agent)::text = 'any' AND a.agent_connection_id IS NOT NULL)
           OR (sqlc.arg(agent)::text = 'none' AND a.agent_connection_id IS NULL)
           OR a.agent_connection_id::text = sqlc.arg(agent)::text)
      AND (sqlc.arg(api_key)::text = ''
           OR (sqlc.arg(api_key)::text = 'any' AND a.api_key_id IS NOT NULL)
           OR (sqlc.arg(api_key)::text = 'none' AND a.api_key_id IS NULL))
), typed AS (
    SELECT e.id, e.actor_id, e.agent_connection_id, e.api_key_id, e.action, e.resource, e.metadata, e.created_at, e.kind, e.target,
           CASE WHEN e.target ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN e.target::uuid END AS target_id,
           CASE WHEN e.other ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN e.other::uuid END AS other_id
    FROM entries e
), resolved AS (
    SELECT t.id, t.actor_id, COALESCE(u.name, 'Toft')::text AS actor_name, COALESCE(u.email, '')::text AS actor_email, t.agent_connection_id,
           ag.name AS agent_name, t.api_key_id, ak.name AS via_key_name, t.action, t.resource, t.metadata, t.created_at,
           t.kind, t.target,
           s.id AS service_id, s.name AS service_name,
           COALESCE(se.project_id, s.project_id) AS service_project_id,
           sp.name AS service_project_name, se.name AS service_environment,
           sv.name AS server_name, pr.name AS project_name,
           en.name AS environment_name, enp.id AS environment_project_id, enp.name AS environment_project_name,
           tu.name AS user_name, k.name AS key_name, oo.name AS other_org_name,
           rp.name AS repository_name, rpp.id AS repository_project_id, rpp.name AS repository_project_name,
           dp.title AS doc_page_title, dpp.id AS doc_page_project_id, dpp.name AS doc_page_project_name
    FROM typed t
    LEFT JOIN users u ON u.id = t.actor_id -- none: Toft itself (D-135)
    LEFT JOIN agent_connections ag ON ag.id = t.agent_connection_id
    LEFT JOIN api_keys ak ON ak.id = t.api_key_id
    LEFT JOIN deployments d ON t.kind = 'deployment' AND d.id = t.target_id
    LEFT JOIN services s ON s.id = CASE t.kind WHEN 'service' THEN t.target_id WHEN 'deployment' THEN d.service_id END
    LEFT JOIN environments se ON se.id = s.environment_id
    LEFT JOIN projects sp ON sp.id = COALESCE(se.project_id, s.project_id)
    LEFT JOIN servers sv ON t.kind = 'server' AND sv.id = t.target_id
    LEFT JOIN projects pr ON t.kind = 'project' AND pr.id = t.target_id
    LEFT JOIN environments en ON t.kind = 'environment' AND en.id = t.target_id
    LEFT JOIN projects enp ON enp.id = en.project_id
    LEFT JOIN users tu ON t.kind = 'user' AND tu.id = t.target_id
    LEFT JOIN api_keys k ON t.kind = 'apikey' AND k.id = t.target_id
    LEFT JOIN organizations oo ON oo.id = t.other_id
    LEFT JOIN repositories rp ON t.kind = 'repository' AND rp.id = t.target_id
    LEFT JOIN projects rpp ON rpp.id = rp.project_id
    LEFT JOIN doc_pages dp ON t.kind = 'doc_page' AND dp.id = t.target_id
    LEFT JOIN projects dpp ON dpp.id = dp.project_id
), matched AS (
    SELECT r.*, count(*) OVER () AS total
    FROM resolved r
    WHERE sqlc.arg(q)::text = ''
       OR r.actor_name ILIKE '%' || sqlc.arg(q)::text || '%'
       OR r.agent_name ILIKE '%' || sqlc.arg(q)::text || '%'
       OR concat_ws(' ', r.service_project_name, r.service_name, r.server_name, r.project_name,
                    r.environment_project_name, r.environment_name, r.user_name, r.key_name,
                    r.repository_project_name, r.repository_name, r.doc_page_project_name, r.doc_page_title,
                    r.metadata->>'name', r.metadata->>'project_name', r.target) ILIKE '%' || sqlc.arg(q)::text || '%'
)
SELECT m.id, m.actor_id, m.actor_name, m.actor_email, m.agent_connection_id, m.agent_name, m.api_key_id, m.via_key_name,
       m.action, m.resource, m.metadata, m.created_at,
       m.kind, m.target, m.service_id, m.service_name, m.service_project_id, m.service_project_name,
       m.service_environment, m.server_name, m.project_name, m.environment_name, m.environment_project_id,
       m.environment_project_name, m.user_name, m.key_name, m.other_org_name,
       m.repository_name, m.repository_project_id, m.repository_project_name,
       m.doc_page_title, m.doc_page_project_id, m.doc_page_project_name, m.total
FROM matched m
WHERE sqlc.narg(before_at)::timestamptz IS NULL
   OR (m.created_at, m.id) < (sqlc.narg(before_at)::timestamptz, sqlc.narg(before_id)::uuid)
ORDER BY m.created_at DESC, m.id DESC
LIMIT sqlc.arg(row_limit)::int;
