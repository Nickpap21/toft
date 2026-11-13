-- Membership changes that could leave a workspace without an owner run in one
-- transaction that first locks the workspace's row (LockOrganization), so two
-- owners demoting or removing each other at once are serialised and the
-- second sees the first's change.

-- name: LockOrganization :one
SELECT id FROM organizations WHERE id = $1 FOR UPDATE;

-- name: GetMemberRole :one
SELECT role FROM org_members WHERE org_id = $1 AND user_id = $2;

-- name: CountOtherOwners :one
SELECT count(*) FROM org_members
WHERE org_id = $1 AND role = 'owner' AND user_id <> $2;

-- name: SetMemberRole :exec
UPDATE org_members SET role = $3 WHERE org_id = $1 AND user_id = $2;

-- name: DeleteMember :exec
DELETE FROM org_members WHERE org_id = $1 AND user_id = $2;

-- name: ExpireOrgInvites :exec
-- Run when an owner is removed or demoted (changeMember): owners read every
-- pending link (GET /org/invites), so any of them could bring one back.
UPDATE org_invites SET expires_at = NOW()
WHERE org_id = $1 AND used_at IS NULL AND expires_at > NOW();

-- name: InsertInvite :one
-- token is the link's sha256; token_encrypted the link itself, encrypted.
INSERT INTO org_invites (id, org_id, created_by, token, token_encrypted, role, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, org_id, created_by, role, expires_at, used_at, created_at;

-- name: ListPendingInvites :many
SELECT id, org_id, created_by, token_encrypted, role, expires_at, used_at, created_at
FROM org_invites
WHERE org_id = $1 AND used_at IS NULL AND expires_at > NOW()
ORDER BY created_at DESC;

-- name: ListUnhashedInvites :many
-- Invites from before 0054, which hold the link itself in token.
SELECT id, token FROM org_invites WHERE token_encrypted IS NULL;

-- name: HashInvites :execrows
-- A row changes only while it is still unconverted.
UPDATE org_invites i SET token = v.token_hash, token_encrypted = v.token_encrypted
FROM (SELECT unnest(sqlc.arg(ids)::uuid[]) AS id,
             unnest(sqlc.arg(token_hashes)::text[]) AS token_hash,
             unnest(sqlc.arg(tokens_encrypted)::text[]) AS token_encrypted) v
WHERE i.id = v.id AND i.token_encrypted IS NULL;

-- name: GetInviteByToken :one
-- An invite with its creator: an invite from a deactivated account cannot be
-- redeemed, and the invite page names who sent it.
SELECT i.id, i.org_id, i.created_by, i.token, i.role, i.expires_at, i.used_at, i.created_at,
       u.is_active AS creator_active, u.name AS creator_name
FROM org_invites i
JOIN users u ON u.id = i.created_by
WHERE i.token = $1;

-- name: FindUsersByEmailCI :many
-- For adding an existing account to a workspace by email (D-56).
SELECT id, name, email, is_active FROM users WHERE lower(email) = lower(sqlc.arg(email)::text) ORDER BY id;

-- name: ListAddableAccounts :many
-- Accounts an owner can add to the workspace by email (D-58): its former
-- members, read from the audit log's member.removed and member.left rows
-- (latest per person), and, when all_accounts is true (the caller is an
-- instance operator, who sees every account anyway), every other active
-- account. Current members and tombstones are left out. Former members
-- first, most recently gone first.
WITH former AS (
    SELECT DISTINCT ON (a.resource) split_part(a.resource, ':', 2) AS uid,
           a.created_at AS left_at, COALESCE(a.metadata->>'role', '')::text AS role
    FROM audit_log a
    WHERE a.org_id = sqlc.arg(org_id)::uuid AND a.action IN ('member.removed', 'member.left') AND a.resource LIKE 'user:%'
    ORDER BY a.resource, a.created_at DESC
)
SELECT u.id, u.name, u.email, COALESCE(f.role, '')::text AS former_role, f.left_at::timestamptz AS left_at,
       (f.uid IS NOT NULL)::boolean AS former
FROM users u
LEFT JOIN former f ON f.uid = u.id::text
WHERE u.is_active AND u.deleted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM org_members m WHERE m.org_id = sqlc.arg(org_id)::uuid AND m.user_id = u.id)
  AND (f.uid IS NOT NULL OR sqlc.arg(all_accounts)::boolean)
ORDER BY (f.uid IS NULL), f.left_at DESC, u.name;

-- name: GetOrgSettings :one
SELECT resource_bypass_min_role, default_isolation, setup_hidden_at FROM organizations WHERE id = $1;

-- name: UpdateOrgSettings :execrows
UPDATE organizations
SET resource_bypass_min_role = sqlc.arg(resource_bypass_min_role), default_isolation = sqlc.arg(default_isolation), updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: HideOrgSetup :execrows
-- Home's Finish setting up checklist, hidden for the workspace (7g); the
-- first time is kept.
UPDATE organizations SET setup_hidden_at = NOW(), updated_at = NOW() WHERE id = $1 AND setup_hidden_at IS NULL;

-- name: CountDefaultIsolationServices :many
-- The workspace's services that follow its default isolation (D-95), by
-- server, with a row (perhaps 0) for every server it has services on; a
-- NULL server_id is the panel's own server.
SELECT s.server_id, (count(*) FILTER (WHERE s.isolation IS NULL))::int AS services
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
WHERE COALESCE(p.org_id, s.scoped_org_id) = $1
GROUP BY s.server_id;
