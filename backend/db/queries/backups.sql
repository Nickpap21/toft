-- Backup destinations (D-43). server_id NULL is the panel host.

-- name: GetBackupDestination :one
SELECT * FROM backup_destinations WHERE server_id IS NOT DISTINCT FROM sqlc.narg(server_id)::uuid;

-- name: ListBackupDestinations :many
SELECT * FROM backup_destinations ORDER BY created_at;

-- name: InsertBackupDestination :one
INSERT INTO backup_destinations (id, server_id, endpoint, bucket, access_key, secret_key, repo_password, schedule)
VALUES (sqlc.arg(id)::uuid, sqlc.narg(server_id)::uuid, $1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateBackupDestination :one
-- The secret key is kept when secret_key is empty.
UPDATE backup_destinations
SET endpoint   = $2,
    bucket     = $3,
    access_key = $4,
    secret_key = CASE WHEN sqlc.arg(secret_key)::text = '' THEN secret_key ELSE sqlc.arg(secret_key)::text END,
    schedule   = $5,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteBackupDestination :exec
DELETE FROM backup_destinations WHERE id = $1;

-- name: RecordBackupRun :exec
-- The last run a server reported; last_ok_at moves only on a success.
UPDATE backup_destinations
SET last_run_at = sqlc.arg(run_at),
    last_ok     = sqlc.arg(ok),
    last_ok_at  = CASE WHEN sqlc.arg(ok)::bool THEN sqlc.arg(run_at) ELSE last_ok_at END,
    last_bytes  = sqlc.arg(bytes),
    last_error  = sqlc.arg(error)
WHERE id = sqlc.arg(id);
