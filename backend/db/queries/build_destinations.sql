-- name: LockBuildDestinations :exec
-- One change to a server's list at a time, until the transaction ends: the
-- whole list is written to the server inside it. '' is the panel's server.
SELECT pg_advisory_xact_lock(hashtextextended('build_destinations:' || sqlc.arg(server)::text, 0));

-- name: ListBuildDestinations :many
-- A server's list (NULL: the panel's server), in the order its rules are
-- written.
SELECT id, cidr, port, note, created_at
FROM build_destinations
WHERE server_id IS NOT DISTINCT FROM sqlc.narg(server_id)::uuid
ORDER BY cidr, port NULLS FIRST;

-- name: AddBuildDestination :one
INSERT INTO build_destinations (server_id, cidr, port, note, created_by)
VALUES (sqlc.narg(server_id)::uuid, sqlc.arg(cidr)::cidr, sqlc.narg(port)::integer, sqlc.arg(note), sqlc.narg(created_by)::uuid)
RETURNING id, cidr, port, note, created_at;

-- name: RemoveBuildDestination :one
DELETE FROM build_destinations
WHERE id = sqlc.arg(id)::uuid AND server_id IS NOT DISTINCT FROM sqlc.narg(server_id)::uuid
RETURNING id, cidr, port, note, created_at;
