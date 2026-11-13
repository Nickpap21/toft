-- name: GetAlertChannel :one
SELECT org_id, webhook_url, signing_secret, enabled, last_delivery_at, last_delivery_status, last_delivery_error, created_at, updated_at
FROM alert_channels WHERE org_id = $1;

-- name: CreateAlertChannel :one
INSERT INTO alert_channels (org_id, webhook_url, signing_secret, enabled)
VALUES ($1, $2, $3, $4)
RETURNING org_id, webhook_url, signing_secret, enabled, last_delivery_at, last_delivery_status, last_delivery_error, created_at, updated_at;

-- name: UpdateAlertChannel :one
UPDATE alert_channels SET webhook_url = $2, enabled = $3, updated_at = NOW()
WHERE org_id = $1
RETURNING org_id, webhook_url, signing_secret, enabled, last_delivery_at, last_delivery_status, last_delivery_error, created_at, updated_at;

-- name: RotateAlertChannelSecret :execrows
UPDATE alert_channels SET signing_secret = $2, updated_at = NOW() WHERE org_id = $1;

-- name: RecordAlertDelivery :exec
UPDATE alert_channels
SET last_delivery_at = NOW(), last_delivery_status = $2, last_delivery_error = $3
WHERE org_id = $1;

-- name: DeleteAlertChannel :exec
DELETE FROM alert_channels WHERE org_id = $1;

-- name: ListOpenAlerts :many
SELECT kind, subject FROM alerts WHERE resolved_at IS NULL;

-- OpenAlert inserts an open alert; 0 rows when one is already open.
-- name: OpenAlert :execrows
INSERT INTO alerts (kind, subject, detail) VALUES ($1, $2, $3)
ON CONFLICT (kind, subject) WHERE resolved_at IS NULL DO NOTHING;

-- RecordOneOffAlert stores a failed deploy or a memory kill, resolved as it
-- fires.
-- name: RecordOneOffAlert :exec
INSERT INTO alerts (kind, subject, detail, fired_at, resolved_at) VALUES ($1, $2, $3, $4, $4);

-- name: ListRecentAlerts :many
-- Settings → Alerts: the workspace's alerts open now or fired in the last 30
-- days, newest first, with what they are about (as ListDashboardOpenAlerts
-- decides whose an alert is). Resolved rows live 30 days, so the table stays
-- small and is read whole.
SELECT a.id, a.kind, a.subject, a.detail, a.fired_at, a.resolved_at,
       COALESCE(s.name, sv.name, v.name, rr.name, '')::text AS subject_name,
       COALESCE(e.project_id, s.project_id, rr.project_id) AS project_id,
       COALESCE(p.name, '')::text                        AS project_name,
       COALESCE(e.name, '')::text                        AS environment_name,
       COALESCE(ssv.name, vsv.name, '')::text             AS server_name
FROM alerts a
LEFT JOIN services s ON a.kind IN ('service_down', 'service_restarting', 'service_unhealthy',
        'container_exited', 'deploy_failed', 'oom_killed')
    AND s.id::text = a.subject
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN repository_mirrors rm ON a.kind = 'mirror_failing' AND rm.id::text = a.subject
LEFT JOIN repositories rr ON rr.id = rm.repository_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id, rr.project_id)
LEFT JOIN servers ssv ON ssv.id = s.server_id
LEFT JOIN servers sv ON a.kind IN ('server_unreachable', 'disk_full', 'backup_failed', 'agent_offline', 'agent_frozen',
    'security_updates', 'reboot_required', 'docker_advisory') AND sv.id::text = a.subject
LEFT JOIN shared_volumes v ON a.kind = 'volume_full' AND v.id::text = a.subject
LEFT JOIN servers vsv ON vsv.id = v.server_id
WHERE (a.resolved_at IS NULL OR a.fired_at >= NOW() - INTERVAL '30 days')
  AND (COALESCE(p.org_id, s.scoped_org_id) = sqlc.arg(org_id)::uuid
       OR sv.org_id = sqlc.arg(org_id)::uuid
       OR v.org_id = sqlc.arg(org_id)::uuid
       OR (a.subject = 'control-plane' AND EXISTS (
           SELECT 1 FROM org_members m JOIN instance_operators o ON o.user_id = m.user_id
           WHERE m.org_id = sqlc.arg(org_id)::uuid AND m.role = 'owner')))
ORDER BY a.fired_at DESC
LIMIT 200;

-- SetOpenAlertDetail rewrites an open alert's detail: the disk alert once
-- the automatic clean it started ends (D-135).
-- name: SetOpenAlertDetail :exec
UPDATE alerts SET detail = $3
WHERE kind = $1 AND subject = $2 AND resolved_at IS NULL;

-- name: ResolveAlert :execrows
UPDATE alerts SET resolved_at = NOW()
WHERE kind = $1 AND subject = $2 AND resolved_at IS NULL;

-- name: DeleteResolvedAlertsBefore :exec
DELETE FROM alerts WHERE resolved_at < $1;

-- ListGoneServiceAlerts returns open service alerts whose service was
-- deleted or stopped (a deploying or failed service keeps its alerts).
-- name: ListGoneServiceAlerts :many
SELECT a.kind, a.subject FROM alerts a
LEFT JOIN services s ON s.id::text = a.subject
WHERE a.resolved_at IS NULL
  AND a.kind IN ('service_down', 'service_restarting', 'service_unhealthy', 'container_exited')
  AND (s.id IS NULL OR s.runtime_status IN ('stopped', 'idle'));

-- GetServiceAlertContext names a service for an alert and finds its
-- workspace (through its environment's or its own project, or its
-- workspace for a workspace-shared service).
-- name: GetServiceAlertContext :one
SELECT s.name AS service_name,
       COALESCE(p.name, '')::text AS project_name,
       COALESCE(e.name, '')::text AS environment_name,
       COALESCE(sv.name, '')::text AS server_name,
       COALESCE(p.org_id, s.scoped_org_id) AS org_id
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
LEFT JOIN servers sv ON sv.id = s.server_id
WHERE s.id = $1;

-- name: GetServerAlertContext :one
SELECT name, org_id FROM servers WHERE id = $1;

-- ListOperatorOrgIDs returns the workspaces owned by an instance operator,
-- which hear the control-plane server's alerts.
-- name: ListOperatorOrgIDs :many
SELECT DISTINCT m.org_id FROM org_members m
JOIN instance_operators o ON o.user_id = m.user_id
WHERE m.role = 'owner'
ORDER BY m.org_id;

-- ListServicesOrgIDs returns the workspaces the services belong to, for
-- telling their tabs about alerts in one query per monitor tick.
-- name: ListServicesOrgIDs :many
SELECT DISTINCT COALESCE(p.org_id, s.scoped_org_id)::uuid AS org_id
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
WHERE s.id = ANY(sqlc.arg(ids)::uuid[]) AND COALESCE(p.org_id, s.scoped_org_id) IS NOT NULL;
