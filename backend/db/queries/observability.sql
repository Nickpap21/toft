-- Observability settings (D-48). The rest of the repository predates sqlc.

-- name: GetLokiFor :one
-- The Loki a service's logs go to: its project's, else its workspace's. A
-- NULL project matches only the workspace's row. Its address when one is
-- set, else the catalog Loki's server and 127.0.0.1 port (D-148).
SELECT COALESCE(o.loki_url, '')::text AS loki_url, ls.id AS loki_service_id, ls.server_id AS loki_server_id, ls.loopback_port,
       COALESCE(sv.name, '')::text AS loki_server_name
FROM observability_configs o
LEFT JOIN services ls ON ls.id = o.loki_service_id
LEFT JOIN servers sv ON sv.id = ls.server_id
WHERE o.org_id = sqlc.arg(org_id)
  AND (o.project_id = sqlc.narg(project_id) OR o.project_id IS NULL)
  AND (COALESCE(o.loki_url, '') <> '' OR ls.loopback_port IS NOT NULL)
ORDER BY o.project_id NULLS LAST
LIMIT 1;

-- name: ListProjectObservability :many
-- Each of the workspace's projects with its own Loki and Grafana, if set.
SELECT p.id, p.name,
       COALESCE(o.loki_url, '')::text    AS loki_url,
       COALESCE(o.grafana_url, '')::text AS grafana_url
FROM projects p
LEFT JOIN observability_configs o ON o.project_id = p.id
WHERE p.org_id = sqlc.arg(org_id)
ORDER BY p.name;

-- name: UnregisterWorkspaceObservability :exec
-- Forgets the workspace's Loki, Grafana or Prometheus: its service and the
-- address set for it (D-162). The service itself is untouched.
UPDATE observability_configs
SET loki_service_id       = CASE WHEN sqlc.arg(kind)::text = 'loki' THEN NULL ELSE loki_service_id END,
    loki_url              = CASE WHEN sqlc.arg(kind)::text = 'loki' THEN NULL ELSE loki_url END,
    grafana_service_id    = CASE WHEN sqlc.arg(kind)::text = 'grafana' THEN NULL ELSE grafana_service_id END,
    grafana_url           = CASE WHEN sqlc.arg(kind)::text = 'grafana' THEN NULL ELSE grafana_url END,
    prometheus_service_id = CASE WHEN sqlc.arg(kind)::text = 'prometheus' THEN NULL ELSE prometheus_service_id END,
    prometheus_url        = CASE WHEN sqlc.arg(kind)::text = 'prometheus' THEN NULL ELSE prometheus_url END,
    updated_at            = NOW()
WHERE org_id = sqlc.arg(org_id) AND project_id IS NULL;
