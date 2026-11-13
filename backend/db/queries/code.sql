-- Git hosting (D-71): repositories and SSH keys.

-- name: ListProjectsWithoutRepository :many
-- Projects that have no repository yet, with what naming one needs.
SELECT p.id AS project_id, p.org_id::uuid AS org_id, p.name AS project_name,
       o.name AS org_name, o.slug AS org_slug
FROM projects p
JOIN organizations o ON o.id = p.org_id
WHERE NOT EXISTS (SELECT 1 FROM repositories r WHERE r.project_id = p.id)
ORDER BY p.created_at;

-- name: ListOrgSlugs :many
SELECT slug::text FROM organizations WHERE slug IS NOT NULL;

-- name: ListRepositoryNamesIn :many
SELECT org_id, name FROM repositories WHERE org_id = ANY(sqlc.arg(org_ids)::uuid[]);

-- name: SetOrgSlugs :exec
-- Only workspaces that have none: a slug, once given, is in clone URLs.
UPDATE organizations o SET slug = v.slug
FROM (SELECT unnest(sqlc.arg(ids)::uuid[]) AS id,
             unnest(sqlc.arg(slugs)::text[]) AS slug) v
WHERE o.id = v.id AND o.slug IS NULL;

-- name: InsertRepositories :exec
INSERT INTO repositories (org_id, project_id, name)
SELECT unnest(sqlc.arg(org_ids)::uuid[]), unnest(sqlc.arg(project_ids)::uuid[]), unnest(sqlc.arg(names)::text[]);

-- name: ListProjectRepositories :many
SELECT r.id, r.org_id, r.name, r.created_at, r.protect_default_branch, r.protected_branch, o.slug::text AS org_slug,
       (SELECT count(*) FROM pull_requests p WHERE p.repository_id = r.id AND p.state = 'open') AS open_pulls,
       EXISTS (SELECT 1 FROM repository_mirrors m WHERE m.repository_id = r.id AND m.direction = 'pull') AS mirrored
FROM repositories r
JOIN organizations o ON o.id = r.org_id
WHERE r.project_id = $1
ORDER BY r.created_at, r.name;

-- name: GetRepositoryByPath :one
-- The repository at <workspace slug>/<name>, for git's front doors, with
-- the URL it pulls from while it mirrors one (read-only then, D-79).
SELECT r.id, r.org_id, r.project_id, r.protect_default_branch, r.protected_branch,
       COALESCE((SELECT m.url FROM repository_mirrors m WHERE m.repository_id = r.id AND m.direction = 'pull'), '')::text AS mirror_url
FROM repositories r
JOIN organizations o ON o.id = r.org_id
WHERE o.slug = sqlc.arg(org_slug) AND r.name = sqlc.arg(name);

-- name: CreateRepository :one
INSERT INTO repositories (org_id, project_id, name)
VALUES (sqlc.arg(org_id), sqlc.arg(project_id), sqlc.arg(name))
RETURNING id, created_at;

-- name: DeleteRepository :execrows
-- Never a project's last (D-75): the backend would make it again.
DELETE FROM repositories r
WHERE r.id = sqlc.arg(id) AND r.project_id = sqlc.arg(project_id)
  AND (SELECT count(*) FROM repositories o WHERE o.project_id = sqlc.arg(project_id)) > 1;

-- name: SetRepositoryProtection :exec
-- Protection on or off, with the protected branch's name (L2): NULL when
-- off, or on while the repository has no branch yet.
UPDATE repositories SET protect_default_branch = sqlc.arg(protect), protected_branch = sqlc.narg(branch)
WHERE id = sqlc.arg(id);

-- name: FillProtectedBranch :exec
-- The backfill of 0080: HEAD's name at a push, only while protection is on
-- and no name is stored (an owner's change in between wins).
UPDATE repositories SET protected_branch = sqlc.arg(branch)
WHERE id = sqlc.arg(id) AND protect_default_branch AND protected_branch IS NULL;

-- ─── Pull requests (D-75) ────────────────────────────────────────────────────

-- name: NextPullNumber :one
UPDATE repositories SET next_pull_number = next_pull_number + 1
WHERE id = $1
RETURNING (next_pull_number - 1)::int AS number;

-- name: InsertPullRequest :one
INSERT INTO pull_requests (repository_id, number, title, body, author_id, base_branch, head_branch, head_sha, opened_with_key, api_key_id,
                           agent_connection_id)
VALUES (sqlc.arg(repository_id), sqlc.arg(number), sqlc.arg(title), sqlc.arg(body), sqlc.arg(author_id),
        sqlc.arg(base_branch), sqlc.arg(head_branch), sqlc.arg(head_sha), sqlc.arg(opened_with_key), sqlc.narg(api_key_id),
        sqlc.narg(agent_connection_id))
RETURNING id;

-- name: GetPullRequest :one
SELECT p.id, p.repository_id, p.number, p.title, p.body, p.author_id, p.base_branch, p.head_branch, p.head_sha,
       p.state, p.merge_method, p.merge_sha, p.merge_base_sha, p.merged_by, p.merged_at, p.closed_at,
       p.created_at, p.updated_at,
       COALESCE(u.name, '')::text AS author_name, COALESCE(u.email, '')::text AS author_email,
       COALESCE(m.name, '')::text AS merged_by_name,
       (SELECT count(*) FROM pull_request_comments c WHERE c.pull_request_id = p.id) AS comments,
       COALESCE(ac.name, '')::text AS agent_name
FROM pull_requests p
LEFT JOIN users u ON u.id = p.author_id
LEFT JOIN users m ON m.id = p.merged_by
LEFT JOIN agent_connections ac ON ac.id = p.agent_connection_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.number = sqlc.arg(number);

-- name: ListPullRequests :many
-- Newest first; before is the number the previous page ended at.
SELECT p.id, p.repository_id, p.number, p.title, p.body, p.author_id, p.base_branch, p.head_branch, p.head_sha,
       p.state, p.merge_method, p.merge_sha, p.merge_base_sha, p.merged_by, p.merged_at, p.closed_at,
       p.created_at, p.updated_at,
       COALESCE(u.name, '')::text AS author_name, COALESCE(u.email, '')::text AS author_email,
       COALESCE(m.name, '')::text AS merged_by_name,
       (SELECT count(*) FROM pull_request_comments c WHERE c.pull_request_id = p.id) AS comments,
       COALESCE(ac.name, '')::text AS agent_name
FROM pull_requests p
LEFT JOIN users u ON u.id = p.author_id
LEFT JOIN users m ON m.id = p.merged_by
LEFT JOIN agent_connections ac ON ac.id = p.agent_connection_id
WHERE p.repository_id = sqlc.arg(repository_id) AND p.state = sqlc.arg(state)
  AND (sqlc.narg(before)::int IS NULL OR p.number < sqlc.narg(before)::int)
ORDER BY p.number DESC
LIMIT sqlc.arg(row_limit);

-- name: CountPullRequests :many
SELECT state, count(*) AS n FROM pull_requests WHERE repository_id = $1 GROUP BY state;

-- name: UpdatePullRequestText :exec
UPDATE pull_requests SET title = sqlc.arg(title), body = sqlc.arg(body), updated_at = NOW() WHERE id = sqlc.arg(id);

-- name: SetPullRequestState :execrows
-- open ↔ closed only; a merged pull request stays merged.
UPDATE pull_requests
SET state = sqlc.arg(state), closed_at = CASE WHEN sqlc.arg(state) = 'closed' THEN NOW() ELSE NULL END, updated_at = NOW()
WHERE id = sqlc.arg(id) AND state = sqlc.arg(from_state);

-- name: MarkPullRequestMerged :execrows
UPDATE pull_requests
SET state = 'merged', merge_method = sqlc.arg(merge_method), merge_sha = sqlc.arg(merge_sha),
    merge_base_sha = sqlc.arg(merge_base_sha), head_sha = sqlc.arg(head_sha),
    merged_by = sqlc.arg(merged_by), merged_at = NOW(), updated_at = NOW()
WHERE id = sqlc.arg(id) AND state = 'open';

-- name: SetPullRequestHead :exec
UPDATE pull_requests SET head_sha = sqlc.arg(head_sha), updated_at = NOW() WHERE id = sqlc.arg(id);

-- name: ListOpenPullRequestsByHead :many
-- The open pull requests whose head is one of these branches, with what
-- their events carry (the base and title).
SELECT id, number, title, base_branch, head_branch, head_sha FROM pull_requests
WHERE repository_id = sqlc.arg(repository_id) AND state = 'open' AND head_branch = ANY(sqlc.arg(branches)::text[]);

-- name: InsertPullRequestComment :one
INSERT INTO pull_request_comments (pull_request_id, author_id, body, agent_connection_id)
VALUES (sqlc.arg(pull_request_id), sqlc.arg(author_id), sqlc.arg(body), sqlc.narg(agent_connection_id))
RETURNING id, created_at;

-- name: GetPullRequestComment :one
SELECT c.id, c.author_id, c.body, c.created_at, c.updated_at, COALESCE(u.name, '')::text AS author_name,
       COALESCE(ac.name, '')::text AS agent_name
FROM pull_request_comments c
LEFT JOIN users u ON u.id = c.author_id
LEFT JOIN agent_connections ac ON ac.id = c.agent_connection_id
WHERE c.id = sqlc.arg(id) AND c.pull_request_id = sqlc.arg(pull_request_id);

-- name: UpdatePullRequestComment :exec
UPDATE pull_request_comments SET body = sqlc.arg(body), updated_at = NOW() WHERE id = sqlc.arg(id);

-- name: DeletePullRequestComment :exec
DELETE FROM pull_request_comments WHERE id = sqlc.arg(id);

-- name: InsertPullRequestEvent :exec
INSERT INTO pull_request_events (pull_request_id, actor_id, kind, data)
VALUES (sqlc.arg(pull_request_id), sqlc.narg(actor_id), sqlc.arg(kind), sqlc.arg(data));

-- name: ListPullRequestTimeline :many
-- The conversation after its description: comments and events, oldest
-- first; the cursor is the last item's time and id.
SELECT t.item, t.id, t.actor_id, t.actor_name, t.agent_name, t.body, t.kind, t.data, t.created_at, t.updated_at
FROM (
    SELECT 'comment'::text AS item, c.id, c.author_id AS actor_id, COALESCE(u.name, '')::text AS actor_name,
           COALESCE(ac.name, '')::text AS agent_name,
           c.body, ''::text AS kind, '{}'::jsonb AS data, c.created_at, c.updated_at
    FROM pull_request_comments c LEFT JOIN users u ON u.id = c.author_id
    LEFT JOIN agent_connections ac ON ac.id = c.agent_connection_id
    WHERE c.pull_request_id = sqlc.arg(pull_request_id)
    UNION ALL
    SELECT 'event'::text, e.id, e.actor_id, COALESCE(u.name, '')::text, COALESCE(e.data->>'agent', '')::text,
           ''::text, e.kind, e.data, e.created_at, e.created_at
    FROM pull_request_events e LEFT JOIN users u ON u.id = e.actor_id
    WHERE e.pull_request_id = sqlc.arg(pull_request_id)
) t
WHERE sqlc.narg(after_at)::timestamptz IS NULL
   OR (t.created_at, t.id) > (sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::uuid)
ORDER BY t.created_at, t.id
LIMIT sqlc.arg(row_limit);

-- name: ListSSHKeys :many
SELECT id, user_id, title, fingerprint, key_type, last_used_at, created_at
FROM ssh_keys WHERE user_id = $1 ORDER BY created_at DESC;

-- name: CountSSHKeys :one
SELECT count(*) FROM ssh_keys WHERE user_id = $1;

-- name: CreateSSHKey :one
INSERT INTO ssh_keys (user_id, title, public_key, fingerprint, key_type)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, user_id, title, fingerprint, key_type, last_used_at, created_at;

-- name: DeleteSSHKey :one
DELETE FROM ssh_keys WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id)
RETURNING id, user_id, title, fingerprint, key_type, last_used_at, created_at;

-- name: GetSSHKeyByFingerprint :one
SELECT id, user_id FROM ssh_keys WHERE fingerprint = $1;

-- name: TouchSSHKey :exec
UPDATE ssh_keys SET last_used_at = NOW() WHERE id = $1;

-- ─── Deploy on push and checks (D-77) ────────────────────────────────────────

-- name: InsertGitEvent :one
-- Writes an event, its webhook deliveries (one for each active webhook of
-- the repository subscribed to each of hook_events) and, with push_mirrors,
-- marks the repository's push mirrors due in 5 s: one statement, so none of
-- them is lost (D-79). An event that deploys nothing is written dispatched,
-- so no dispatcher claims it.
WITH e AS (
    INSERT INTO git_events (repository_id, kind, action, branch, before_sha, after_sha, actor_id, pull_request_id, subject, commits, forced, with_key,
                            agent_connection_id, dispatched_at)
    VALUES (sqlc.arg(repository_id), sqlc.arg(kind), sqlc.narg(action), sqlc.arg(branch), sqlc.arg(before_sha), sqlc.arg(after_sha),
            sqlc.narg(actor_id), sqlc.narg(pull_request_id), sqlc.arg(subject), sqlc.arg(commits), sqlc.arg(forced), sqlc.arg(with_key),
            sqlc.narg(agent_connection_id),
            CASE WHEN sqlc.arg(deploys)::bool THEN NULL ELSE NOW() END)
    RETURNING id, repository_id
), d AS (
    INSERT INTO webhook_deliveries (webhook_id, git_event_id, event)
    SELECT w.id, e.id, ev.name
    FROM e
    JOIN repository_webhooks w ON w.repository_id = e.repository_id AND w.active
    CROSS JOIN unnest(sqlc.arg(hook_events)::text[]) AS ev(name)
    WHERE ev.name = ANY (w.events)
), m AS (
    UPDATE repository_mirrors SET next_sync_at = NOW() + interval '5 seconds'
    WHERE repository_mirrors.repository_id = sqlc.arg(repository_id) AND repository_mirrors.direction = 'push' AND sqlc.arg(push_mirrors)::bool
)
SELECT id FROM e;

-- name: ClaimGitEvents :many
-- Leases up to n due events for 5 minutes, so a dispatcher that stops
-- half-way lets them come due again, with what deploying them needs.
UPDATE git_events e
SET next_attempt_at = NOW() + interval '5 minutes', attempts = e.attempts + 1
FROM (SELECT g.id FROM git_events g
      WHERE g.dispatched_at IS NULL AND g.next_attempt_at <= NOW() AND g.kind IN ('push', 'merge', 'mirror')
      ORDER BY g.id LIMIT sqlc.arg(n) FOR UPDATE SKIP LOCKED) c,
     repositories r
WHERE e.id = c.id AND r.id = e.repository_id
RETURNING e.id, e.repository_id, r.project_id, r.org_id, e.kind, e.branch, e.after_sha, e.subject,
          e.attempts, e.created_at, e.actor_id,
          COALESCE((SELECT u.name FROM users u WHERE u.id = e.actor_id), '')::text AS actor_name,
          COALESCE((SELECT p.number FROM pull_requests p WHERE p.id = e.pull_request_id), 0)::int AS pull_number;

-- name: FinishGitEvent :exec
UPDATE git_events SET dispatched_at = NOW(), last_error = sqlc.narg(last_error) WHERE id = sqlc.arg(id);

-- name: RetryGitEvent :exec
UPDATE git_events SET next_attempt_at = sqlc.arg(at), last_error = sqlc.arg(last_error) WHERE id = sqlc.arg(id);

-- name: DeleteOldGitEvents :execrows
DELETE FROM git_events WHERE dispatched_at IS NOT NULL AND created_at < sqlc.arg(before);

-- name: ListGitEvents :many
-- A repository's pushes, merges and mirror syncs that moved a branch,
-- newest first, ids below before_id (0: from the newest).
SELECT e.id, e.kind, e.branch, e.before_sha, e.after_sha, e.subject, e.commits, e.forced,
       e.actor_id, COALESCE(u.name, '')::text AS actor_name, p.number AS pull_number,
       e.created_at, e.dispatched_at, e.last_error
FROM git_events e
LEFT JOIN users u ON u.id = e.actor_id
LEFT JOIN pull_requests p ON p.id = e.pull_request_id
WHERE e.repository_id = sqlc.arg(repository_id)
  AND e.kind IN ('push', 'merge', 'mirror') AND e.after_sha !~ '^0+$'
  AND (sqlc.narg(branch)::text IS NULL OR e.branch = sqlc.narg(branch)::text)
  AND (sqlc.arg(before_id)::bigint = 0 OR e.id < sqlc.arg(before_id)::bigint)
ORDER BY e.id DESC
LIMIT sqlc.arg(n);

-- name: UpsertCommitCheck :one
-- Reported again by name, a check is updated: started_at is set when it
-- first runs (again after it had ended), completed_at when it ends.
INSERT INTO commit_checks (repository_id, sha, name, state, description, details_url, deployment_id, reporter_id, started_at, completed_at)
VALUES (sqlc.arg(repository_id), sqlc.arg(sha), sqlc.arg(name), sqlc.arg(state), sqlc.arg(description),
        sqlc.narg(details_url), sqlc.narg(deployment_id), sqlc.narg(reporter_id),
        CASE WHEN sqlc.arg(state)::text = 'pending' THEN NULL ELSE NOW() END,
        CASE WHEN sqlc.arg(state)::text IN ('success', 'failure', 'cancelled', 'skipped') THEN NOW() END)
ON CONFLICT (repository_id, sha, name) DO UPDATE SET
    state         = EXCLUDED.state,
    description   = EXCLUDED.description,
    details_url   = EXCLUDED.details_url,
    deployment_id = EXCLUDED.deployment_id,
    reporter_id   = EXCLUDED.reporter_id,
    started_at    = CASE WHEN EXCLUDED.state = 'pending' THEN NULL
                         WHEN commit_checks.started_at IS NULL OR commit_checks.completed_at IS NOT NULL THEN NOW()
                         ELSE commit_checks.started_at END,
    completed_at  = EXCLUDED.completed_at,
    updated_at    = NOW()
RETURNING created_at, updated_at, started_at, completed_at;

-- name: CountCommitChecks :one
SELECT count(*) FROM commit_checks WHERE repository_id = sqlc.arg(repository_id) AND sha = sqlc.arg(sha);

-- name: CommitCheckExists :one
SELECT EXISTS (SELECT 1 FROM commit_checks WHERE repository_id = sqlc.arg(repository_id) AND sha = sqlc.arg(sha) AND name = sqlc.arg(name));

-- name: ListCommitChecks :many
-- The checks of these commits: Toft's own first, then by name.
SELECT c.sha, c.name, c.state, c.description, c.details_url, c.deployment_id, d.service_id AS deployment_service_id,
       c.reporter_id, COALESCE(u.name, '')::text AS reporter_name,
       c.started_at, c.completed_at, c.created_at, c.updated_at
FROM commit_checks c
LEFT JOIN users u ON u.id = c.reporter_id
LEFT JOIN deployments d ON d.id = c.deployment_id
WHERE c.repository_id = sqlc.arg(repository_id) AND c.sha = ANY(sqlc.arg(shas)::text[])
ORDER BY c.sha, (c.name LIKE 'toft/%') DESC, c.name;

-- name: FailRunningDeployChecks :exec
-- A restart's recovery: the deploy checks of deployments it marked
-- interrupted fail with them.
UPDATE commit_checks
SET state = 'failure', description = sqlc.arg(description), completed_at = NOW(), updated_at = NOW()
WHERE deployment_id = ANY(sqlc.arg(deployment_ids)::uuid[]) AND state IN ('pending', 'running');

-- name: ListDeployTargets :many
-- The services that build from a repository (D-77), with where they run;
-- previews' copies aren't theirs to list (D-97).
SELECT s.id, s.name, s.git_branch, s.base_directory, s.deploy_on_push,
       e.name AS environment_name, e.color AS environment_color, sv.name AS server_name
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN servers sv ON sv.id = s.server_id
WHERE s.repository_id = sqlc.arg(repository_id) AND s.preview_of IS NULL
ORDER BY s.git_branch, e.name NULLS FIRST, s.name;

-- ─── Agents' branches (D-102) ────────────────────────────────────────────────

-- name: GetAgentBranch :one
-- An agent's branch, with the agent and its person (a pull request's About).
-- password_ends_at: when its push password stops working, which is sooner
-- than token_expires_at once the connection expires or is revoked
-- (AgentBranchByToken refuses both).
SELECT b.repository_id, b.name, b.connection_id, b.user_id, b.token_expires_at, b.created_at,
       c.name AS agent_name, COALESCE(u.name, '')::text AS user_name,
       LEAST(b.token_expires_at, c.expires_at, COALESCE(c.revoked_at, 'infinity'))::timestamptz AS password_ends_at
FROM agent_branches b
JOIN agent_connections c ON c.id = b.connection_id
LEFT JOIN users u ON u.id = b.user_id
WHERE b.repository_id = $1 AND b.name = $2;

-- name: UpsertAgentBranch :exec
-- A new push credential for an agent's branch: the first, or the next one
-- create_branch issues on the same branch for the same connection.
INSERT INTO agent_branches (repository_id, name, connection_id, user_id, token_hash, token_expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (repository_id, name) DO UPDATE
SET token_hash = EXCLUDED.token_hash, token_expires_at = EXCLUDED.token_expires_at
WHERE agent_branches.connection_id = EXCLUDED.connection_id;

-- name: AgentBranchByToken :one
-- A live push credential: unexpired, of a connection neither revoked nor
-- expired. paused: its workspace's agents are paused, or the instance's
-- (the agent module's 'agents_paused' setting).
SELECT b.repository_id, b.name, b.connection_id, b.user_id, c.org_id, c.name AS agent_name,
       (o.agents_paused_at IS NOT NULL
        OR EXISTS (SELECT 1 FROM instance_settings i WHERE i.key = 'agents_paused'))::bool AS paused
FROM agent_branches b
JOIN agent_connections c ON c.id = b.connection_id
JOIN organizations o ON o.id = c.org_id
WHERE b.token_hash = $1 AND b.token_expires_at > NOW() AND c.revoked_at IS NULL AND c.expires_at > NOW();

-- name: ListAgentBranches :many
-- A repository's agents' branches, with the agent and its person.
SELECT b.name, c.name AS agent_name, COALESCE(u.name, '')::text AS user_name
FROM agent_branches b
JOIN agent_connections c ON c.id = b.connection_id
LEFT JOIN users u ON u.id = b.user_id
WHERE b.repository_id = $1;

-- name: BranchDeployed :one
-- Whether any service builds from this branch of the repository, deploy on
-- push or not (D-102: an agent never pushes there).
SELECT EXISTS (SELECT 1 FROM services
               WHERE repository_id = $1 AND git_branch = $2 AND preview_of IS NULL)::bool;
