-- name: KeepSecretReveal :exec
-- A person's copy of a service's admin password (D-160); a newer one (a
-- reset) replaces theirs, with its own id: the seal's AD names it.
INSERT INTO secret_reveals (id, service_id, user_id, secret_key, sealed, reason)
VALUES (sqlc.arg(id)::uuid, sqlc.arg(service_id)::uuid, sqlc.arg(user_id)::uuid, sqlc.arg(secret_key), sqlc.arg(sealed), sqlc.arg(reason))
ON CONFLICT (service_id, user_id) DO UPDATE
SET id = EXCLUDED.id, secret_key = EXCLUDED.secret_key, sealed = EXCLUDED.sealed,
    reason = EXCLUDED.reason, created_at = NOW();

-- name: GetSecretReveal :one
-- The person's copy for a service, unless older than since (the clean-up
-- deletes those within the hour).
SELECT id, secret_key, sealed, reason, created_at
FROM secret_reveals
WHERE service_id = sqlc.arg(service_id)::uuid AND user_id = sqlc.arg(user_id)::uuid
  AND created_at > sqlc.arg(since)::timestamptz;

-- name: DeleteSecretReveal :execrows
DELETE FROM secret_reveals
WHERE service_id = sqlc.arg(service_id)::uuid AND user_id = sqlc.arg(user_id)::uuid;

-- name: DeleteOldSecretReveals :execrows
DELETE FROM secret_reveals WHERE created_at <= sqlc.arg(before)::timestamptz;

-- name: ForgetSecretReveals :exec
-- Every person's copy of key (the temporary login's: ''), once it no longer
-- opens the app: the secret was replaced or deleted, or basic auth changed.
DELETE FROM secret_reveals
WHERE service_id = sqlc.arg(service_id)::uuid AND secret_key = sqlc.arg(secret_key);
