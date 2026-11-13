-- The disk advisor (D-135).

-- name: DiskHostServices :many
-- The services that run on a host, NULL server_id the panel's own, with
-- their rollback count (0 the default) and their workspace (by their
-- project's, or their own for workspace-shared ones), as the server's page
-- tells a workspace's services from the others'.
SELECT s.id, s.name, COALESCE(s.rollback_images_kept, 0)::int AS rollback_images_kept,
       COALESCE(p.org_id, s.scoped_org_id)::uuid AS org_id
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
WHERE s.server_id IS NOT DISTINCT FROM sqlc.narg(server_id)::uuid
ORDER BY s.name;

-- name: DiskServiceIDPrefixes :many
-- The first 8 characters of every service's ID, on any server: the part
-- volume and compose project names carry. A volume whose prefix is here is
-- some service's, wherever that service runs now.
SELECT DISTINCT left(id::text, 8)::text AS prefix FROM services;

-- name: DiskLiveDeployments :many
-- The deployments of these services that went live with an image, newest
-- first per service, with their rank (1 the newest). Served by
-- idx_deployments_service_id; a service has at most a few hundred.
SELECT id, service_id, image_tag::text AS image_tag, (image_removed_at IS NOT NULL)::boolean AS image_removed,
       row_number() OVER (PARTITION BY service_id ORDER BY created_at DESC)::int AS rank
FROM deployments
WHERE service_id = ANY(sqlc.arg(service_ids)::uuid[])
  AND status = 'running'
  AND image_tag IS NOT NULL AND image_tag <> ''
ORDER BY service_id, rank;

-- name: MarkDeploymentImagesRemoved :exec
-- A clean removed these deployments' images: they can't be rolled back to.
UPDATE deployments SET image_removed_at = NOW()
WHERE id = ANY(sqlc.arg(ids)::uuid[]) AND image_removed_at IS NULL;

-- name: GetServerDiskPolicy :one
SELECT org_id, name, build_cache_limit_bytes, disk_auto_clean, COALESCE(disk_alert_percent, 0)::int AS disk_alert_percent, disk_last_clean,
       EXISTS (SELECT 1 FROM server_shares sh WHERE sh.server_id = servers.id) AS shared
FROM servers WHERE id = $1;

-- name: SetServerDiskPolicy :exec
UPDATE servers SET build_cache_limit_bytes = sqlc.narg(build_cache_limit_bytes),
    disk_auto_clean = sqlc.narg(disk_auto_clean)::text[],
    disk_alert_percent = sqlc.narg(disk_alert_percent)::smallint,
    updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: SetServerDiskLastClean :exec
UPDATE servers SET disk_last_clean = sqlc.arg(disk_last_clean)::jsonb WHERE id = sqlc.arg(id);

-- name: ListServerDiskPolicies :many
-- Every server's alert threshold and automatic clean, for the alert monitor
-- and the daily clean.
SELECT id, org_id, name, build_cache_limit_bytes, disk_auto_clean, COALESCE(disk_alert_percent, 0)::int AS disk_alert_percent,
       EXISTS (SELECT 1 FROM server_shares sh WHERE sh.server_id = servers.id) AS shared
FROM servers ORDER BY id;

-- name: GetServiceRollbackImagesKept :one
-- How many of its last live deployments keep their image (D-135); 0 is the
-- default.
SELECT COALESCE(rollback_images_kept, 0)::int AS rollback_images_kept FROM services WHERE id = $1;

-- name: SetServiceRollbackImagesKept :exec
UPDATE services SET rollback_images_kept = sqlc.narg(rollback_images_kept)::smallint, updated_at = NOW() WHERE id = sqlc.arg(id);
