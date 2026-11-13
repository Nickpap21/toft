-- name: ClaimInviteByToken :one
-- Atomically consumes a live invite. Two concurrent redemptions of the same
-- token cannot both get a row back. An invite whose creator has been
-- deactivated is dead too: deactivation expires those invites, and this
-- guard covers one created concurrently with the deactivation.
UPDATE org_invites SET used_at = NOW()
WHERE token = $1 AND used_at IS NULL AND expires_at > NOW()
  AND EXISTS (SELECT 1 FROM users u WHERE u.id = org_invites.created_by AND u.is_active)
RETURNING id, org_id, role;

-- name: GetInviteStateByToken :one
SELECT used_at, expires_at FROM org_invites WHERE token = $1;

-- name: CreateOrganizationReturningID :one
INSERT INTO organizations (name) VALUES ($1) RETURNING id;

-- name: InsertOrgMember :exec
INSERT INTO org_members (org_id, user_id, role) VALUES ($1, $2, $3);

-- name: SetUserActiveOrg :exec
UPDATE users SET active_org_id = $2 WHERE id = $1;
