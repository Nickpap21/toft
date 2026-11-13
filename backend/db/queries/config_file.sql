-- Config file queries. The configfile package uses raw pgx for the others
-- (documentation only); DeleteConfigFile is used.

-- name: ListConfigFiles :many
SELECT * FROM service_config_files WHERE service_id = $1 ORDER BY path;

-- name: UpsertConfigFile :one
INSERT INTO service_config_files (service_id, path, content)
VALUES ($1, $2, $3)
ON CONFLICT (service_id, path) DO UPDATE
SET content = EXCLUDED.content, updated_at = NOW()
RETURNING *;

-- name: DeleteConfigFile :one
-- Used by the configfile package: the deleted file's path, for the audit log.
DELETE FROM service_config_files WHERE id = $1 AND service_id = $2 RETURNING path;
