-- Home (the Main board). A workspace's services are its environments',
-- its projects' shared ones and its own shared ones:
-- COALESCE(p.org_id, s.scoped_org_id), as in ListWorkspaceServices.

-- name: DashboardDeploys24h :one
-- The workspace's deploys of the last 24 hours, and the median time, start
-- to finish, of those that went live (0 when none did).
SELECT COUNT(*)                                   AS total,
       COUNT(*) FILTER (WHERE d.status = 'running') AS succeeded,
       COUNT(*) FILTER (WHERE d.status = 'failed')  AS failed,
       COALESCE(EXTRACT(EPOCH FROM percentile_cont(0.5) WITHIN GROUP (ORDER BY d.finished_at - d.started_at)
           FILTER (WHERE d.status = 'running' AND d.started_at IS NOT NULL AND d.finished_at IS NOT NULL)), 0)::float8
           AS median_seconds
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
JOIN deployments d ON d.service_id = s.id AND d.created_at > NOW() - INTERVAL '24 hours'
WHERE COALESCE(p.org_id, s.scoped_org_id) = sqlc.arg(org_id)::uuid;

-- name: ListDashboardFailedDeploys :many
-- The workspace's services whose latest deploy failed, most recent first.
SELECT d.id, s.id AS service_id, s.name AS service_name,
       COALESCE(e.project_id, s.project_id) AS project_id,
       COALESCE(p.name, '')::text           AS project_name,
       COALESCE(e.name, '')::text           AS environment_name,
       COALESCE(sv.name, '')::text          AS server_name,
       COALESCE(d.error_summary, '')::text  AS error_summary,
       COALESCE(d.commit_sha, '')::text     AS commit_sha,
       COALESCE(d.finished_at, d.created_at)::timestamptz AS failed_at
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
LEFT JOIN servers sv ON sv.id = s.server_id
JOIN LATERAL (
    SELECT dd.id, dd.status, dd.error_summary, dd.commit_sha, dd.finished_at, dd.created_at
    FROM deployments dd WHERE dd.service_id = s.id
    ORDER BY dd.created_at DESC LIMIT 1
) d ON TRUE
WHERE COALESCE(p.org_id, s.scoped_org_id) = sqlc.arg(org_id)::uuid AND d.status = 'failed'
  -- A preview's failures are its pull request's (D-97).
  AND s.preview_of IS NULL
ORDER BY failed_at DESC
LIMIT 20;

-- name: ListDashboardUnprotectedPublic :many
-- The workspace's image services with an address and neither basic auth
-- nor an IP allowlist, each with its first address. The inbox keeps the
-- admin tools among them (resources.NewKindOf), which SQL can't tell.
SELECT s.id, s.name, s.addon_slug, s.docker_image,
       COALESCE(e.project_id, s.project_id) AS project_id,
       COALESCE(p.name, '')::text           AS project_name,
       COALESCE(e.name, '')::text           AS environment_name,
       dm.hostname, dm.created_at
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
JOIN LATERAL (
    SELECT d.hostname, d.created_at FROM domains d WHERE d.service_id = s.id
    ORDER BY d.created_at, d.hostname LIMIT 1
) dm ON TRUE
WHERE COALESCE(p.org_id, s.scoped_org_id) = sqlc.arg(org_id)::uuid
  AND s.source_type = 'docker-image'
  AND s.basic_auth_user = ''
  AND s.ip_allowlist IN ('', '[]', 'null')
  AND s.preview_of IS NULL
ORDER BY s.name, s.id;

-- name: ListDashboardTemporaryLogins :many
-- The workspace's services made from one of slugs (the catalog's apps
-- whoever opens first becomes the admin of) with the temporary login Toft
-- put in front of them still on (basic auth with its user, not one the
-- owner chose). Scans the workspace's services as ListDashboardUnprotectedPublic.
SELECT s.id, s.name, s.addon_slug,
       COALESCE(e.project_id, s.project_id) AS project_id,
       COALESCE(p.name, '')::text           AS project_name,
       COALESCE(e.name, '')::text           AS environment_name,
       s.created_at
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
WHERE COALESCE(p.org_id, s.scoped_org_id) = sqlc.arg(org_id)::uuid
  AND s.addon_slug = ANY(sqlc.arg(slugs)::text[])
  AND s.basic_auth_user = sqlc.arg(login_user)::text
  AND s.preview_of IS NULL
ORDER BY s.name, s.id;

-- name: ListDashboardHTTPSRedeploys :many
-- The workspace's running services with a temporary (sslip.io) name whose
-- certificate was recorded after their live deployment started (D-159):
-- that deployment routes the name over plain HTTP, and Toft's own redeploy
-- didn't run (a protected environment, settings saved since, or a stack it
-- can't bring up unchanged). One row per service, its first such name; not
-- while a deploy begun after the certificate is under way. behind_login:
-- its basic auth is on, so that deployment redirects HTTP to HTTPS already
-- and only its TOFT_URL still says http:// (as of its settings now).
SELECT s.id, s.name,
       (s.basic_auth_user <> '' AND s.basic_auth_password <> '')::bool AS behind_login,
       COALESCE(e.project_id, s.project_id) AS project_id,
       COALESCE(p.name, '')::text           AS project_name,
       COALESCE(e.name, '')::text           AS environment_name,
       dm.hostname, dm.certificate_at::timestamptz AS certificate_at
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
JOIN deployments live ON live.id = s.active_deployment_id
JOIN LATERAL (
    SELECT d.hostname, d.certificate_at FROM domains d
    WHERE d.service_id = s.id AND d.hostname LIKE '%.sslip.io' AND d.certificate_at > live.created_at
    ORDER BY d.certificate_at, d.hostname LIMIT 1
) dm ON TRUE
WHERE COALESCE(p.org_id, s.scoped_org_id) = sqlc.arg(org_id)::uuid
  AND s.runtime_status = 'running'
  AND s.preview_of IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM deployments nd
      WHERE nd.service_id = s.id AND nd.created_at > dm.certificate_at
        AND nd.status IN ('pending', 'queued', 'building')
  )
ORDER BY s.name, s.id;

-- name: ListDashboardUnbackedServices :many
-- The workspace's services on a server nothing backs up (D-43): the panel
-- host (server_id NULL) without its destination, or a server without one
-- when no other entry for the same machine (the same host key, or address
-- and port: server.sameMachine) has one either. Each with its own volumes
-- and the shared volumes it mounts, and a compose stack with its inline
-- file or whether its file is in a repository (not stored); the inbox
-- tells the databases among them (resources.KindOf), which SQL can't.
SELECT s.id, s.addon_slug, s.docker_image, s.server_id,
       COALESCE(srv.name, '')::text AS server_name,
       (CASE WHEN jsonb_typeof(NULLIF(s.volumes, '')::jsonb) = 'array'
             THEN jsonb_array_length(s.volumes::jsonb) ELSE 0 END)::int AS volumes,
       ARRAY(SELECT ssv.shared_volume_id::text FROM service_shared_volumes ssv
             WHERE ssv.service_id = s.id)::text[] AS shared_volume_ids,
       (s.source_type = 'docker-compose')::bool AS compose,
       (CASE WHEN s.source_type = 'docker-compose' THEN COALESCE(s.compose_content, '') ELSE '' END)::text AS compose_content,
       (s.source_type = 'docker-compose' AND (s.compose_repo_url <> '' OR s.repository_id IS NOT NULL))::bool AS compose_from_repo,
       s.created_at
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
LEFT JOIN servers srv ON srv.id = s.server_id
WHERE COALESCE(p.org_id, s.scoped_org_id) = sqlc.arg(org_id)::uuid
  AND s.preview_of IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM backup_destinations b
      LEFT JOIN servers o ON o.id = b.server_id
      WHERE b.server_id IS NOT DISTINCT FROM s.server_id
         OR (srv.id IS NOT NULL AND o.id IS NOT NULL AND (
                (COALESCE(srv.host_key, '') <> '' AND o.host_key = srv.host_key)
             OR (lower(btrim(o.ip_address)) = lower(btrim(srv.ip_address)) AND o.port = srv.port)))
  )
ORDER BY s.server_id NULLS FIRST, s.created_at, s.id;

-- name: DashboardPanelHostBackedUp :one
-- Whether the panel host has a backup destination (D-43).
SELECT EXISTS (SELECT 1 FROM backup_destinations WHERE server_id IS NULL);

-- name: ListDashboardOpenAlerts :many
-- The workspace's open alerts: its services', its servers', its shared
-- volumes', and the control-plane server's when an instance operator owns
-- the workspace (whom the alert webhook tells, ListOperatorOrgIDs).
SELECT a.id, a.kind, a.subject, a.detail, a.fired_at,
       COALESCE(s.name, sv.name, v.name, rr.name, '')::text AS subject_name,
       COALESCE(e.project_id, s.project_id, rr.project_id) AS project_id,
       COALESCE(p.name, '')::text                 AS project_name,
       COALESCE(e.name, '')::text                 AS environment_name
FROM alerts a
LEFT JOIN services s ON a.kind IN ('service_down', 'service_restarting', 'service_unhealthy', 'container_exited')
    AND s.id::text = a.subject
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN repository_mirrors rm ON a.kind = 'mirror_failing' AND rm.id::text = a.subject
LEFT JOIN repositories rr ON rr.id = rm.repository_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id, rr.project_id)
LEFT JOIN servers sv ON a.kind IN ('server_unreachable', 'disk_full', 'backup_failed', 'agent_offline', 'agent_frozen',
    'security_updates', 'reboot_required', 'docker_advisory') AND sv.id::text = a.subject
LEFT JOIN shared_volumes v ON a.kind = 'volume_full' AND v.id::text = a.subject
WHERE a.resolved_at IS NULL
  AND (COALESCE(p.org_id, s.scoped_org_id) = sqlc.arg(org_id)::uuid
       OR sv.org_id = sqlc.arg(org_id)::uuid
       OR v.org_id = sqlc.arg(org_id)::uuid
       OR (a.subject = 'control-plane' AND EXISTS (
           SELECT 1 FROM org_members m JOIN instance_operators o ON o.user_id = m.user_id
           WHERE m.org_id = sqlc.arg(org_id)::uuid AND m.role = 'owner')))
ORDER BY a.fired_at DESC;

-- name: ListDashboardDeploying :many
-- The workspace's deploys in progress, oldest first.
SELECT d.id, d.status, s.id AS service_id, s.name AS service_name, s.source_type,
       s.compose_repo_url,
       COALESCE(e.project_id, s.project_id) AS project_id,
       COALESCE(p.name, '')::text           AS project_name,
       COALESCE(e.name, '')::text           AS environment_name,
       COALESCE(sv.name, '')::text          AS server_name,
       COALESCE(d.commit_sha, '')::text     AS commit_sha,
       d.created_at
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
LEFT JOIN servers sv ON sv.id = s.server_id
JOIN deployments d ON d.service_id = s.id AND d.status IN ('pending', 'queued', 'building')
WHERE COALESCE(p.org_id, s.scoped_org_id) = sqlc.arg(org_id)::uuid
ORDER BY d.created_at;

-- name: ListDashboardProjectDeploys :many
-- Each project's latest deploy, over its environments' and shared services.
SELECT DISTINCT ON (p.id) p.id AS project_id, d.status, d.created_at,
       s.name AS service_name, s.source_type, s.git_branch,
       COALESCE(s.docker_image, '')::text AS docker_image
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
JOIN LATERAL (
    SELECT dd.status, dd.created_at FROM deployments dd WHERE dd.service_id = s.id
    ORDER BY dd.created_at DESC LIMIT 1
) d ON TRUE
WHERE p.org_id = sqlc.arg(org_id)::uuid
ORDER BY p.id, d.created_at DESC;

-- name: ListDashboardRecentDeploys :many
-- The workspace's 20 latest deploys.
SELECT d.id, d.status, s.id AS service_id, s.name AS service_name,
       COALESCE(e.project_id, s.project_id) AS project_id,
       COALESCE(p.name, '')::text           AS project_name,
       COALESCE(e.name, '')::text           AS environment_name,
       d.created_at, d.finished_at
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
JOIN deployments d ON d.service_id = s.id
WHERE COALESCE(p.org_id, s.scoped_org_id) = sqlc.arg(org_id)::uuid
ORDER BY d.created_at DESC
LIMIT 20;

-- name: DashboardWork :one
-- Home's pull request and issue tiles (D-88): open pull requests and those
-- the person opened; issues in progress, theirs, and those shipped this week;
-- and the current cycle's number and its shipped issues (D-90), 0 when the
-- workspace doesn't use cycles.
SELECT
  (SELECT count(*) FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
   WHERE r.org_id = sqlc.arg(org_id) AND pr.state = 'open') AS pulls_open,
  (SELECT count(*) FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
   WHERE r.org_id = sqlc.arg(org_id) AND pr.state = 'open' AND pr.author_id = sqlc.arg(user_id)) AS pulls_yours,
  (SELECT count(*) FROM issues i JOIN projects p ON p.id = i.project_id
   WHERE p.org_id = sqlc.arg(org_id) AND i.status = 'in_progress') AS issues_in_progress,
  (SELECT count(*) FROM issues i JOIN projects p ON p.id = i.project_id
   WHERE p.org_id = sqlc.arg(org_id) AND i.status = 'in_progress' AND i.assignee_id = sqlc.arg(user_id)) AS issues_yours,
  (SELECT count(DISTINCT e.issue_id) FROM issue_events e JOIN issues i ON i.id = e.issue_id JOIN projects p ON p.id = i.project_id
   WHERE p.org_id = sqlc.arg(org_id) AND e.kind = 'status' AND e.to_value = 'shipped' AND e.created_at > now() - interval '7 days') AS issues_shipped_week,
  COALESCE((SELECT c.number FROM cycles c JOIN organizations o ON o.id = c.org_id
   WHERE c.org_id = sqlc.arg(org_id) AND o.cycle_weeks IS NOT NULL
     AND (now() AT TIME ZONE o.cycle_time_zone)::date BETWEEN c.starts_on AND c.ends_on), 0)::int AS cycle_number,
  (SELECT count(*) FROM issues i JOIN cycles c ON c.id = i.cycle_id JOIN organizations o ON o.id = c.org_id
   WHERE c.org_id = sqlc.arg(org_id) AND o.cycle_weeks IS NOT NULL AND i.status = 'shipped'
     AND (now() AT TIME ZONE o.cycle_time_zone)::date BETWEEN c.starts_on AND c.ends_on) AS issues_shipped_cycle;

-- name: DashboardWorkActivity :many
-- Home's activity beside deploys (D-88): issues opened and shipped, pull
-- requests merged and pages edited, newest first.
SELECT x.kind, x.at, x.actor, x.automatic, x.project_id, x.project_name, x.title, x.issue_key, x.digits, x.number, x.repository, x.page_id
FROM (
  SELECT CASE WHEN e.kind = 'opened' THEN 'issue_opened' ELSE 'issue_shipped' END::text AS kind, e.created_at AS at,
         COALESCE(u.name, '')::text AS actor, e.automatic, i.project_id, p.name AS project_name, i.title,
         COALESCE(p.issue_key, '')::text AS issue_key, COALESCE(p.issue_key_digits, o.issue_key_digits)::int AS digits, i.number,
         ''::text AS repository, NULL::uuid AS page_id
  FROM issue_events e
  JOIN issues i ON i.id = e.issue_id
  JOIN projects p ON p.id = i.project_id
  JOIN organizations o ON o.id = p.org_id
  LEFT JOIN users u ON u.id = e.actor_id
  WHERE p.org_id = sqlc.arg(org_id) AND (e.kind = 'opened' OR (e.kind = 'status' AND e.to_value = 'shipped'))
    AND e.created_at > now() - interval '14 days'
  UNION ALL
  SELECT 'pull_merged'::text, pr.merged_at, COALESCE(u.name, '')::text, false, r.project_id, p.name, pr.title,
         ''::text, 0, pr.number, r.name, NULL::uuid
  FROM pull_requests pr
  JOIN repositories r ON r.id = pr.repository_id
  JOIN projects p ON p.id = r.project_id
  LEFT JOIN users u ON u.id = pr.merged_by
  WHERE r.org_id = sqlc.arg(org_id) AND pr.merged_at > now() - interval '14 days'
  UNION ALL
  SELECT 'page_edited'::text, v.created_at, COALESCE(u.name, '')::text, false, d.project_id, p.name, d.title,
         ''::text, 0, v.version, ''::text, d.id
  FROM doc_page_versions v
  JOIN doc_pages d ON d.id = v.page_id
  JOIN projects p ON p.id = d.project_id
  LEFT JOIN users u ON u.id = v.author_id
  WHERE p.org_id = sqlc.arg(org_id) AND v.version > 1 AND d.deleted_at IS NULL AND v.created_at > now() - interval '14 days'
) x
ORDER BY x.at DESC
LIMIT 15;
