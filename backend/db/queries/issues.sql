-- ─── Issues (D-81) ───────────────────────────────────────────────────────────
-- The issues module reads pull_requests, git_events, services and
-- deployments to link and move issues; it writes only its own tables and
-- git_events.issues_at.

-- name: GetIssueProject :one
SELECT p.id, p.org_id, p.name, p.issue_key, p.next_issue_number, p.default_issue_type_id, p.issue_key_digits, p.failed_deploy_issues,
       COALESCE(p.issue_key_digits, o.issue_key_digits)::int AS digits, o.issue_key_digits AS org_digits,
       o.cycle_weeks, o.cycle_time_zone, o.issue_estimates
FROM projects p JOIN organizations o ON o.id = p.org_id
WHERE p.id = $1;

-- name: ListOrgIssueKeys :many
-- Every key the workspace holds: its projects' and their former ones.
SELECT p.issue_key::text AS key FROM projects p WHERE p.org_id = sqlc.arg(org_id) AND p.issue_key IS NOT NULL
UNION
SELECT k.key FROM issue_key_aliases k WHERE k.org_id = sqlc.arg(org_id);

-- name: ListProjectKeyAliases :many
SELECT key FROM issue_key_aliases WHERE project_id = $1 ORDER BY created_at, key;

-- name: IssueKeyHolder :one
-- The project that holds key in the workspace, as its key or a former one.
SELECT p.id FROM projects p WHERE p.org_id = sqlc.arg(org_id) AND p.issue_key = sqlc.arg(key)::text
UNION
SELECT k.project_id FROM issue_key_aliases k WHERE k.org_id = sqlc.arg(org_id) AND k.key = sqlc.arg(key)::text
LIMIT 1;

-- name: ReplaceProjectIssueKey :exec
UPDATE projects SET issue_key = sqlc.arg(issue_key) WHERE id = sqlc.arg(id);

-- name: AddIssueKeyAlias :exec
INSERT INTO issue_key_aliases (org_id, key, project_id) VALUES (sqlc.arg(org_id), sqlc.arg(key), sqlc.arg(project_id))
ON CONFLICT (org_id, key) DO NOTHING;

-- name: DeleteIssueKeyAlias :exec
DELETE FROM issue_key_aliases WHERE org_id = sqlc.arg(org_id) AND key = sqlc.arg(key) AND project_id = sqlc.arg(project_id);

-- name: SetProjectIssueSettings :exec
UPDATE projects SET issue_key_digits = sqlc.narg(digits), default_issue_type_id = sqlc.narg(default_type_id) WHERE id = sqlc.arg(id);

-- name: SetOrgIssueDigits :exec
UPDATE organizations SET issue_key_digits = sqlc.arg(digits) WHERE id = sqlc.arg(id);

-- name: GetOrgIssueDigits :one
SELECT issue_key_digits FROM organizations WHERE id = $1;

-- name: ListHiddenIssueTypes :many
SELECT type_id FROM project_hidden_issue_types WHERE project_id = $1;

-- name: LockProjectForReplace :one
-- A replace of its hidden types' first step, as LockIssueForReplace.
SELECT id FROM projects WHERE id = $1 FOR UPDATE;

-- name: ListHiddenIssueTypesForReplace :many
-- What a replace of a project's hidden types can delete, locked until it
-- commits; its version is computed from these.
SELECT type_id FROM project_hidden_issue_types
WHERE project_id = $1
ORDER BY type_id
FOR UPDATE;

-- name: DeleteHiddenIssueTypes :exec
DELETE FROM project_hidden_issue_types WHERE project_id = $1;

-- name: InsertHiddenIssueTypes :exec
INSERT INTO project_hidden_issue_types (project_id, type_id)
SELECT sqlc.arg(project_id), unnest(sqlc.arg(type_ids)::uuid[])
ON CONFLICT DO NOTHING;

-- ─── Types (D-83) ────────────────────────────────────────────────────────────

-- name: ListIssueTypes :many
SELECT id, name, kind, icon, color, position, archived_at FROM issue_types
WHERE org_id = $1 ORDER BY position, created_at, id;

-- name: InsertIssueType :one
INSERT INTO issue_types (org_id, name, kind, icon, color, position)
VALUES (sqlc.arg(org_id), sqlc.arg(name), sqlc.arg(kind), sqlc.arg(icon), sqlc.arg(color), sqlc.arg(position))
ON CONFLICT (org_id, name) DO NOTHING
RETURNING id;

-- name: UpdateIssueType :execrows
UPDATE issue_types SET name = sqlc.arg(name), icon = sqlc.arg(icon), color = sqlc.arg(color), position = sqlc.arg(position)
WHERE id = sqlc.arg(id) AND org_id = sqlc.arg(org_id);

-- name: SetIssueTypeArchived :execrows
UPDATE issue_types SET archived_at = CASE WHEN sqlc.arg(archived)::bool THEN COALESCE(archived_at, NOW()) END
WHERE id = sqlc.arg(id) AND org_id = sqlc.arg(org_id);

-- name: SetProjectIssueKey :execrows
UPDATE projects SET issue_key = sqlc.arg(issue_key) WHERE id = sqlc.arg(id) AND issue_key IS NULL;

-- name: NextIssueNumber :one
UPDATE projects SET next_issue_number = next_issue_number + 1
WHERE id = $1
RETURNING (next_issue_number - 1)::int AS number;

-- name: InsertIssue :one
INSERT INTO issues (project_id, number, title, description, status, priority, assignee_id, created_by, type_id, parent_id,
                    cycle_id, start_on, due_on, estimate, agent_connection_id)
VALUES (sqlc.arg(project_id), sqlc.arg(number), sqlc.arg(title), sqlc.arg(description), sqlc.arg(status),
        sqlc.arg(priority), sqlc.narg(assignee_id), sqlc.narg(created_by), sqlc.narg(type_id), sqlc.narg(parent_id),
        sqlc.narg(cycle_id), sqlc.narg(start_on), sqlc.narg(due_on), sqlc.narg(estimate), sqlc.narg(agent_connection_id))
RETURNING id;

-- name: LockIssueForReplace :one
-- A replace of its labels' first step: FOR UPDATE conflicts with the FOR KEY
-- SHARE a new label's foreign key takes.
SELECT id FROM issues WHERE id = $1 FOR UPDATE;

-- name: ListIssueLabelsForReplace :many
-- What a replace of an issue's labels can delete, locked until it commits;
-- its version is computed from these.
SELECT name FROM issue_labels WHERE issue_id = $1 ORDER BY name FOR UPDATE;

-- name: DeleteIssueLabels :exec
DELETE FROM issue_labels WHERE issue_id = $1;

-- name: InsertIssueLabels :exec
INSERT INTO issue_labels (issue_id, name)
SELECT sqlc.arg(issue_id), unnest(sqlc.arg(names)::text[])
ON CONFLICT DO NOTHING;

-- name: GetIssue :one
SELECT i.id, i.project_id, i.number, i.title, i.description, i.status, i.priority, i.type_id, i.parent_id,
       i.assignee_id, COALESCE(a.name, '')::text AS assignee_name, COALESCE(a.email, '')::text AS assignee_email,
       i.created_by, COALESCE(c.name, '')::text AS created_by_name,
       i.created_at, i.updated_at, i.status_changed_at,
       COALESCE((SELECT array_agg(l.name ORDER BY l.name) FROM issue_labels l WHERE l.issue_id = i.id), '{}')::text[] AS labels,
       (SELECT count(*) FROM issue_comments m WHERE m.issue_id = i.id) AS comments,
       i.cycle_id, i.start_on, i.due_on, i.estimate,
       COALESCE(cy.number, 0)::int AS cycle_number, COALESCE(cy.name, '')::text AS cycle_name, cy.starts_on AS cycle_starts_on, cy.ends_on AS cycle_ends_on,
       COALESCE((SELECT fc.number FROM cycle_carryovers cc JOIN cycles fc ON fc.id = cc.cycle_id
                 WHERE cc.issue_id = i.id AND cc.to_cycle_id = i.cycle_id ORDER BY fc.number DESC LIMIT 1), 0)::int AS carried_from,
       COALESCE((SELECT sum(k.estimate) FROM issues k WHERE k.parent_id = i.id), 0)::int AS children_points,
       COALESCE(ac.name, '')::text AS created_by_agent
FROM issues i
LEFT JOIN users a ON a.id = i.assignee_id
LEFT JOIN users c ON c.id = i.created_by
LEFT JOIN cycles cy ON cy.id = i.cycle_id
LEFT JOIN agent_connections ac ON ac.id = i.agent_connection_id
WHERE i.project_id = sqlc.arg(project_id) AND i.number = sqlc.arg(number);

-- name: LockIssueStatus :one
SELECT status FROM issues WHERE id = $1 FOR UPDATE;

-- name: UpdateIssue :exec
-- The service merges what changed into the row it read; status_changed_at
-- moves only when the status does.
UPDATE issues
SET title = sqlc.arg(title), description = sqlc.arg(description), priority = sqlc.arg(priority),
    assignee_id = sqlc.narg(assignee_id), type_id = sqlc.narg(type_id), parent_id = sqlc.narg(parent_id),
    cycle_id = sqlc.narg(cycle_id), start_on = sqlc.narg(start_on), due_on = sqlc.narg(due_on), estimate = sqlc.narg(estimate),
    status_changed_at = CASE WHEN status <> sqlc.arg(status) THEN NOW() ELSE status_changed_at END,
    status = sqlc.arg(status), updated_at = NOW()
WHERE id = sqlc.arg(id);

-- name: MoveIssue :execrows
-- An event's move: only from the statuses it moves forward from.
UPDATE issues SET status = sqlc.arg(status), status_changed_at = NOW(), updated_at = NOW()
WHERE id = sqlc.arg(id) AND status = ANY(sqlc.arg(from_statuses)::text[]);

-- name: TouchIssue :exec
UPDATE issues SET updated_at = NOW() WHERE id = $1;

-- name: ListIssues :many
-- A page of a project's issues, in one of four orders, all descending:
-- priority (then last update), last update, creation, or due date (the
-- soonest first, then those with none by last update). The cursor is
-- the last row's sort keys and id. Labels match any of those given; q
-- matches the title's words by prefix (tsq) or the number.
SELECT i.id, i.number, i.title, i.status, i.priority, i.assignee_id, i.type_id,
       COALESCE(a.name, '')::text AS assignee_name, i.created_at, i.updated_at,
       COALESCE((SELECT array_agg(l.name ORDER BY l.name) FROM issue_labels l WHERE l.issue_id = i.id), '{}')::text[] AS labels,
       i.sort_rank, i.sort_at,
       COALESCE(pr.number, 0)::int AS pull_number, COALESCE(pr.state, '')::text AS pull_state,
       pr.repository_id AS pull_repository_id, COALESCE(pr.head_sha, '')::text AS pull_head_sha,
       COALESCE(pr.merge_sha, '')::text AS pull_merge_sha,
       COALESCE(pr.deploys, '')::text AS pull_deploys,
       br.branch AS branch,
       COALESCE(par.key, '')::text AS parent_key, COALESCE(par.title, '')::text AS parent_title,
       COALESCE(kids.total, 0)::int AS children, COALESCE(kids.shipped, 0)::int AS children_shipped,
       COALESCE(kids.done, 0)::int AS children_done,
       COALESCE(blk.keys, '{}')::text[] AS blocked_by,
       i.cycle_id, i.start_on, i.due_on, i.estimate, COALESCE(kids.points, 0)::int AS children_points,
       COALESCE(cy.number, 0)::int AS cycle_number, COALESCE(cy.name, '')::text AS cycle_name, cy.starts_on AS cycle_starts_on, cy.ends_on AS cycle_ends_on
FROM (
    SELECT x.id, x.number, x.title, x.status, x.priority, x.assignee_id, x.type_id, x.parent_id, x.created_at, x.updated_at,
           x.cycle_id, x.start_on, x.due_on, x.estimate,
           (CASE WHEN sqlc.arg(sort)::text = 'priority' THEN
                CASE x.priority WHEN 'urgent' THEN 4 WHEN 'high' THEN 3 WHEN 'medium' THEN 2 WHEN 'low' THEN 1 ELSE 0 END
                 WHEN sqlc.arg(sort)::text = 'due' AND x.due_on IS NOT NULL THEN 1
            ELSE 0 END)::int AS sort_rank,
           (CASE WHEN sqlc.arg(sort)::text = 'created' THEN x.created_at
                 WHEN sqlc.arg(sort)::text = 'due' AND x.due_on IS NOT NULL
                     THEN '2000-01-01'::timestamptz + (DATE '2999-12-31' - x.due_on) * INTERVAL '1 day'
                 ELSE x.updated_at END)::timestamptz AS sort_at
    FROM issues x
    WHERE x.project_id = sqlc.arg(project_id)
      AND (cardinality(sqlc.arg(statuses)::text[]) = 0 OR x.status = ANY(sqlc.arg(statuses)::text[]))
      AND (cardinality(sqlc.arg(priorities)::text[]) = 0 OR x.priority = ANY(sqlc.arg(priorities)::text[]))
      AND (sqlc.arg(assignee)::text = ''
           OR (sqlc.arg(assignee)::text = 'none' AND x.assignee_id IS NULL)
           OR x.assignee_id::text = sqlc.arg(assignee)::text)
      AND (cardinality(sqlc.arg(labels)::text[]) = 0
           OR EXISTS (SELECT 1 FROM issue_labels l WHERE l.issue_id = x.id AND l.name = ANY(sqlc.arg(labels)::text[])))
      AND (NOT sqlc.arg(has_q)::bool
           OR (sqlc.arg(tsq)::text <> '' AND x.search @@ to_tsquery('simple', sqlc.arg(tsq)::text))
           OR x.number = sqlc.arg(q_number)::int)
      AND (cardinality(sqlc.arg(type_ids)::uuid[]) = 0 OR x.type_id = ANY(sqlc.arg(type_ids)::uuid[])
           OR (sqlc.arg(untyped)::bool AND x.type_id IS NULL))
      AND (sqlc.narg(parent_id)::uuid IS NULL OR x.parent_id = sqlc.narg(parent_id)::uuid)
      AND (sqlc.narg(cycle_id)::uuid IS NULL OR x.cycle_id = sqlc.narg(cycle_id)::uuid)
      AND (NOT sqlc.arg(no_cycle)::bool OR x.cycle_id IS NULL)
) i
LEFT JOIN users a ON a.id = i.assignee_id
LEFT JOIN cycles cy ON cy.id = i.cycle_id
LEFT JOIN LATERAL (
    SELECT pp.issue_key || '-' || lpad(pi.number::text, GREATEST(COALESCE(pp.issue_key_digits, po.issue_key_digits), length(pi.number::text)), '0') AS key,
           pi.title
    FROM issues pi JOIN projects pp ON pp.id = pi.project_id JOIN organizations po ON po.id = pp.org_id
    WHERE pi.id = i.parent_id
) par ON true
LEFT JOIN LATERAL (
    SELECT count(*) AS total, count(*) FILTER (WHERE c.status = 'shipped') AS shipped,
           count(*) FILTER (WHERE c.status IN ('shipped', 'cancelled')) AS done,
           sum(c.estimate) AS points
    FROM issues c WHERE c.parent_id = i.id
) kids ON true
LEFT JOIN LATERAL (
    -- The open issues that block it.
    SELECT array_agg(bp.issue_key || '-' || lpad(b.number::text, GREATEST(COALESCE(bp.issue_key_digits, bo.issue_key_digits), length(b.number::text)), '0') ORDER BY b.number) AS keys
    FROM issue_relations r JOIN issues b ON b.id = r.from_issue_id
    JOIN projects bp ON bp.id = b.project_id JOIN organizations bo ON bo.id = bp.org_id
    WHERE r.to_issue_id = i.id AND r.kind = 'blocks' AND b.status NOT IN ('shipped', 'cancelled')
) blk ON true
LEFT JOIN LATERAL (
    -- The pull request the row shows: an open one first, then a merged one.
    SELECT p.number, p.state, p.repository_id, p.head_sha, p.merge_sha,
           (SELECT string_agg(DISTINCT d.status, ',') FROM git_events e JOIN deployments d ON d.git_event_id = e.id
            WHERE e.pull_request_id = p.id AND e.kind = 'merge') AS deploys
    FROM issue_links l JOIN pull_requests p ON p.id = l.pull_request_id
    WHERE l.issue_id = i.id
    ORDER BY (p.state = 'open') DESC, (p.state = 'merged') DESC, p.number DESC
    LIMIT 1
) pr ON true
LEFT JOIN LATERAL (
    SELECT l.branch FROM issue_links l WHERE l.issue_id = i.id AND l.branch IS NOT NULL
    ORDER BY l.created_at DESC LIMIT 1
) br ON true
WHERE sqlc.narg(after_id)::uuid IS NULL
   OR (i.sort_rank, i.sort_at, i.id) < (sqlc.narg(after_rank)::int, sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::uuid)
ORDER BY i.sort_rank DESC, i.sort_at DESC, i.id DESC
LIMIT sqlc.arg(row_limit);

-- name: CountIssuesByStatus :many
-- ListIssues' filters but the status, counted by status.
SELECT x.status, count(*) AS n
FROM issues x
WHERE x.project_id = sqlc.arg(project_id)
  AND (cardinality(sqlc.arg(priorities)::text[]) = 0 OR x.priority = ANY(sqlc.arg(priorities)::text[]))
  AND (sqlc.arg(assignee)::text = ''
       OR (sqlc.arg(assignee)::text = 'none' AND x.assignee_id IS NULL)
       OR x.assignee_id::text = sqlc.arg(assignee)::text)
  AND (cardinality(sqlc.arg(labels)::text[]) = 0
       OR EXISTS (SELECT 1 FROM issue_labels l WHERE l.issue_id = x.id AND l.name = ANY(sqlc.arg(labels)::text[])))
  AND (NOT sqlc.arg(has_q)::bool
       OR (sqlc.arg(tsq)::text <> '' AND x.search @@ to_tsquery('simple', sqlc.arg(tsq)::text))
       OR x.number = sqlc.arg(q_number)::int)
  AND (cardinality(sqlc.arg(type_ids)::uuid[]) = 0 OR x.type_id = ANY(sqlc.arg(type_ids)::uuid[])
       OR (sqlc.arg(untyped)::bool AND x.type_id IS NULL))
  AND (sqlc.narg(parent_id)::uuid IS NULL OR x.parent_id = sqlc.narg(parent_id)::uuid)
  AND (sqlc.narg(cycle_id)::uuid IS NULL OR x.cycle_id = sqlc.narg(cycle_id)::uuid)
  AND (NOT sqlc.arg(no_cycle)::bool OR x.cycle_id IS NULL)
GROUP BY x.status;

-- name: ListCheckStates :many
-- The states of these commits' checks, for the pull requests in a list.
SELECT repository_id, sha, state FROM commit_checks WHERE sha = ANY(sqlc.arg(shas)::text[]);

-- name: ExistingIssueNumbers :many
SELECT number FROM issues WHERE project_id = sqlc.arg(project_id) AND number = ANY(sqlc.arg(numbers)::int[]);

-- ─── Comments and history ────────────────────────────────────────────────────

-- name: InsertIssueComment :one
INSERT INTO issue_comments (issue_id, author_id, body, agent_connection_id)
VALUES (sqlc.arg(issue_id), sqlc.arg(author_id), sqlc.arg(body), sqlc.narg(agent_connection_id))
RETURNING id, created_at;

-- name: GetIssueComment :one
SELECT c.id, c.author_id, c.body, c.created_at, c.edited_at, COALESCE(u.name, '')::text AS author_name,
       COALESCE(ac.name, '')::text AS agent_name
FROM issue_comments c
LEFT JOIN users u ON u.id = c.author_id
LEFT JOIN agent_connections ac ON ac.id = c.agent_connection_id
WHERE c.id = sqlc.arg(id) AND c.issue_id = sqlc.arg(issue_id);

-- name: UpdateIssueComment :exec
UPDATE issue_comments SET body = sqlc.arg(body), edited_at = NOW() WHERE id = sqlc.arg(id);

-- name: DeleteIssueComment :exec
DELETE FROM issue_comments WHERE id = $1;

-- name: InsertIssueEvent :execrows
-- One row per issue, git event and kind: an event applied twice adds none.
INSERT INTO issue_events (issue_id, kind, actor_id, automatic, from_value, to_value, git_event_id, pull_request_id, deployment_id, branch)
VALUES (sqlc.arg(issue_id), sqlc.arg(kind), sqlc.narg(actor_id), sqlc.arg(automatic), sqlc.narg(from_value),
        sqlc.narg(to_value), sqlc.narg(git_event_id), sqlc.narg(pull_request_id), sqlc.narg(deployment_id), sqlc.narg(branch))
ON CONFLICT (issue_id, git_event_id, kind) WHERE git_event_id IS NOT NULL DO NOTHING;

-- name: ListIssueTimeline :many
-- History and comments, oldest first; the cursor is the last item's time
-- and id (a comment's uuid, an event's number, as text).
SELECT t.item, t.id, t.actor_id, t.actor_name, t.agent_name, t.body, t.kind, t.automatic, t.from_value, t.to_value,
       t.assignee_name, t.branch, t.pull_number, t.pull_title, t.repository_name, t.service_name,
       t.environment_name, t.commit_sha, t.created_at, t.edited_at
FROM (
    SELECT 'comment'::text AS item, c.id::text AS id, c.author_id AS actor_id, COALESCE(u.name, '')::text AS actor_name,
           COALESCE(ac.name, '')::text AS agent_name, c.body, ''::text AS kind, false AS automatic, ''::text AS from_value, ''::text AS to_value,
           ''::text AS assignee_name, ''::text AS branch, 0::int AS pull_number, ''::text AS pull_title,
           ''::text AS repository_name, ''::text AS service_name, ''::text AS environment_name, ''::text AS commit_sha,
           c.created_at, c.edited_at
    FROM issue_comments c LEFT JOIN users u ON u.id = c.author_id
    LEFT JOIN agent_connections ac ON ac.id = c.agent_connection_id
    WHERE c.issue_id = sqlc.arg(issue_id)
    UNION ALL
    SELECT 'event'::text, e.id::text, e.actor_id, COALESCE(u.name, '')::text, ''::text,
           ''::text, e.kind, e.automatic, COALESCE(e.from_value, '')::text, COALESCE(e.to_value, '')::text,
           COALESCE(au.name, '')::text, COALESCE(e.branch, '')::text, COALESCE(p.number, 0)::int, COALESCE(p.title, '')::text,
           COALESCE(r.name, '')::text, COALESCE(s.name, '')::text, COALESCE(env.name, '')::text, COALESCE(d.commit_sha, '')::text,
           e.created_at, NULL::timestamptz
    FROM issue_events e
    LEFT JOIN users u ON u.id = e.actor_id
    LEFT JOIN users au ON e.kind = 'assigned' AND au.id::text = e.to_value
    LEFT JOIN pull_requests p ON p.id = e.pull_request_id
    LEFT JOIN repositories r ON r.id = p.repository_id
    LEFT JOIN deployments d ON d.id = e.deployment_id
    LEFT JOIN services s ON s.id = d.service_id
    LEFT JOIN environments env ON env.id = s.environment_id
    WHERE e.issue_id = sqlc.arg(issue_id)
) t
WHERE sqlc.narg(after_at)::timestamptz IS NULL
   OR (t.created_at, t.id) > (sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::text)
ORDER BY t.created_at, t.id
LIMIT sqlc.arg(row_limit);

-- ─── Labels and views ────────────────────────────────────────────────────────

-- name: ListProjectIssueLabels :many
SELECT l.name,
       count(*) FILTER (WHERE i.status NOT IN ('shipped', 'cancelled')) AS open,
       count(*) AS total
FROM issue_labels l JOIN issues i ON i.id = l.issue_id
WHERE i.project_id = $1
GROUP BY l.name
ORDER BY l.name;

-- name: ListProjectServiceNames :many
SELECT DISTINCT s.name FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
WHERE s.project_id = $1 OR e.project_id = $1
ORDER BY s.name;

-- name: IssueAssigneeRole :one
SELECT role FROM org_members WHERE org_id = sqlc.arg(org_id) AND user_id = sqlc.arg(user_id);

-- name: ListIssueViews :many
SELECT v.id, v.name, v.filters, v.owner_id, COALESCE(u.name, '')::text AS owner_name, v.created_at, v.updated_at
FROM issue_views v LEFT JOIN users u ON u.id = v.owner_id
WHERE v.project_id = $1
ORDER BY v.created_at, v.id;

-- name: InsertIssueView :one
INSERT INTO issue_views (project_id, owner_id, name, filters)
VALUES (sqlc.arg(project_id), sqlc.arg(owner_id), sqlc.arg(name), sqlc.arg(filters))
RETURNING id;

-- name: GetIssueView :one
SELECT v.id, v.name, v.filters, v.owner_id, COALESCE(u.name, '')::text AS owner_name, v.created_at, v.updated_at
FROM issue_views v LEFT JOIN users u ON u.id = v.owner_id
WHERE v.id = sqlc.arg(id) AND v.project_id = sqlc.arg(project_id);

-- name: UpdateIssueView :exec
UPDATE issue_views SET name = sqlc.arg(name), filters = sqlc.arg(filters), updated_at = NOW() WHERE id = sqlc.arg(id);

-- name: DeleteIssueView :exec
DELETE FROM issue_views WHERE id = $1;

-- ─── Links and moves (the events outbox and deploys) ─────────────────────────

-- name: ListUnappliedGitEvents :many
-- The events the issues module has not applied yet, oldest first, with
-- their pull request.
SELECT e.id, e.repository_id, r.project_id, e.kind, COALESCE(e.action, '')::text AS action, e.branch,
       e.after_sha, e.actor_id, e.pull_request_id, e.created_at,
       COALESCE(p.number, 0)::int AS pull_number, COALESCE(p.title, '')::text AS pull_title,
       COALESCE(p.body, '')::text AS pull_body, COALESCE(p.head_branch, '')::text AS pull_head
FROM git_events e
JOIN repositories r ON r.id = e.repository_id
LEFT JOIN pull_requests p ON p.id = e.pull_request_id
WHERE e.issues_at IS NULL
ORDER BY e.id
LIMIT sqlc.arg(n);

-- name: MarkGitEventIssues :exec
UPDATE git_events SET issues_at = NOW() WHERE id = $1;

-- name: ListIssuesByNumbers :many
SELECT id, number, status, created_at FROM issues
WHERE project_id = sqlc.arg(project_id) AND number = ANY(sqlc.arg(numbers)::int[]);

-- name: ListIssuesForPull :many
SELECT i.id, i.number, i.status, i.created_at FROM issue_links l
JOIN issues i ON i.id = l.issue_id
WHERE l.pull_request_id = $1;

-- name: InsertIssueBranchLink :execrows
INSERT INTO issue_links (issue_id, repository_id, branch)
VALUES (sqlc.arg(issue_id), sqlc.arg(repository_id), sqlc.arg(branch))
ON CONFLICT (issue_id, repository_id, branch) WHERE branch IS NOT NULL DO NOTHING;

-- name: InsertIssuePullLink :execrows
INSERT INTO issue_links (issue_id, repository_id, pull_request_id)
VALUES (sqlc.arg(issue_id), sqlc.arg(repository_id), sqlc.arg(pull_request_id))
ON CONFLICT (issue_id, pull_request_id) WHERE pull_request_id IS NOT NULL DO NOTHING;

-- name: CountOtherOpenLinkedPulls :one
SELECT count(*) FROM issue_links l JOIN pull_requests p ON p.id = l.pull_request_id
WHERE l.issue_id = sqlc.arg(issue_id) AND p.state = 'open' AND p.id <> sqlc.arg(pull_request_id);

-- name: ListIssueLinks :many
SELECT l.id, l.repository_id, r.name AS repository_name, l.branch, l.pull_request_id,
       p.number AS pull_number, p.title AS pull_title, p.state AS pull_state, p.head_branch, p.base_branch,
       p.head_sha, p.merge_sha, p.merged_at, l.created_at
FROM issue_links l
JOIN repositories r ON r.id = l.repository_id
LEFT JOIN pull_requests p ON p.id = l.pull_request_id
WHERE l.issue_id = $1
ORDER BY l.created_at, l.id;

-- name: ListShipCandidates :many
-- Issues not yet shipped or cancelled that are linked to a pull request
-- merged in the last 30 days, with whether another linked one is still
-- open; repository_id narrows them to one repository.
SELECT i.id AS issue_id, i.status, i.created_at AS issue_created_at,
       p.id AS pull_id, p.repository_id, p.number AS pull_number, p.base_branch, p.merge_sha, p.merged_by, p.merged_at,
       EXISTS (SELECT 1 FROM issue_links o JOIN pull_requests op ON op.id = o.pull_request_id
               WHERE o.issue_id = i.id AND op.state = 'open') AS has_open
FROM issues i
JOIN issue_links l ON l.issue_id = i.id
JOIN pull_requests p ON p.id = l.pull_request_id
WHERE i.status IN ('backlog', 'todo', 'in_progress', 'in_review')
  AND p.state = 'merged' AND p.merged_at > NOW() - interval '30 days'
  AND (sqlc.narg(repository_id)::uuid IS NULL OR p.repository_id = sqlc.narg(repository_id)::uuid)
ORDER BY i.id, p.merged_at;

-- name: ListBranchDeployTargets :many
-- The services a merge into this branch deploys (Deploy on push), with the
-- commit each runs live.
SELECT s.id, s.name, COALESCE(e.name, '')::text AS environment_name,
       d.id AS live_deployment_id, d.commit_sha AS live_sha
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN deployments d ON d.id = s.active_deployment_id AND d.status = 'running'
WHERE s.repository_id = sqlc.arg(repository_id) AND s.git_branch = sqlc.arg(branch) AND s.deploy_on_push
ORDER BY e.name NULLS FIRST, s.name;

-- name: LatestDeploymentsSince :many
SELECT DISTINCT ON (d.service_id) d.service_id, d.id, d.status, d.commit_sha, d.created_at
FROM deployments d
WHERE d.service_id = ANY(sqlc.arg(service_ids)::uuid[]) AND d.created_at >= sqlc.arg(since)
ORDER BY d.service_id, d.created_at DESC;

-- name: ServiceRepository :one
SELECT repository_id FROM services WHERE id = $1;

-- ─── Elsewhere in the panel: chips and ⌘K ────────────────────────────────────

-- name: ListPullIssues :many
SELECT l.pull_request_id, i.number, i.title, i.status, COALESCE(p.issue_key, '')::text AS issue_key,
       COALESCE(p.issue_key_digits, o.issue_key_digits)::int AS digits
FROM issue_links l
JOIN issues i ON i.id = l.issue_id
JOIN projects p ON p.id = i.project_id
JOIN organizations o ON o.id = p.org_id
WHERE l.pull_request_id = ANY(sqlc.arg(pull_ids)::uuid[])
ORDER BY i.number;

-- name: ListBranchIssues :many
SELECT l.branch, i.number, i.title, i.status, COALESCE(p.issue_key, '')::text AS issue_key,
       COALESCE(p.issue_key_digits, o.issue_key_digits)::int AS digits
FROM issue_links l
JOIN issues i ON i.id = l.issue_id
JOIN projects p ON p.id = i.project_id
JOIN organizations o ON o.id = p.org_id
WHERE l.repository_id = sqlc.arg(repository_id) AND l.branch = ANY(sqlc.arg(branches)::text[])
ORDER BY i.number;

-- name: SearchIssues :many
-- ⌘K: the workspace's issues whose title matches tsq by prefix, or whose
-- number (and key, when given) matches, or whose project's key (or a former
-- one) is q_prefix; open ones first. Without has_q, the most recently updated.
SELECT i.project_id, p.name AS project_name, COALESCE(p.issue_key, '')::text AS issue_key,
       i.number, i.title, i.status, i.priority, i.type_id,
       COALESCE(p.issue_key_digits, o.issue_key_digits)::int AS digits
FROM issues i
JOIN projects p ON p.id = i.project_id
JOIN organizations o ON o.id = p.org_id
WHERE p.org_id = sqlc.arg(org_id)
  AND (sqlc.narg(project_id)::uuid IS NULL OR i.project_id = sqlc.narg(project_id)::uuid)
  AND (NOT sqlc.arg(has_q)::bool
       OR (sqlc.arg(tsq)::text <> '' AND i.search @@ to_tsquery('simple', sqlc.arg(tsq)::text))
       OR (i.number = sqlc.arg(q_number)::int AND (sqlc.arg(q_key)::text = '' OR p.issue_key = sqlc.arg(q_key)::text
           OR EXISTS (SELECT 1 FROM issue_key_aliases k WHERE k.project_id = p.id AND k.key = sqlc.arg(q_key)::text)))
       OR (sqlc.arg(q_prefix)::text <> '' AND (p.issue_key = sqlc.arg(q_prefix)::text
           OR EXISTS (SELECT 1 FROM issue_key_aliases k WHERE k.project_id = p.id AND k.key = sqlc.arg(q_prefix)::text))))
ORDER BY (i.status IN ('shipped', 'cancelled')), i.updated_at DESC
LIMIT sqlc.arg(row_limit);

-- name: SearchPullRequests :many
SELECT r.project_id, p.name AS project_name, r.name AS repository_name,
       pr.number, pr.title, pr.state
FROM pull_requests pr
JOIN repositories r ON r.id = pr.repository_id
JOIN projects p ON p.id = r.project_id
WHERE r.org_id = sqlc.arg(org_id)
  AND (sqlc.narg(project_id)::uuid IS NULL OR r.project_id = sqlc.narg(project_id)::uuid)
  AND (NOT sqlc.arg(has_q)::bool
       OR (sqlc.arg(tsq)::text <> '' AND pr.search @@ to_tsquery('simple', sqlc.arg(tsq)::text))
       OR pr.number = sqlc.arg(q_number)::int)
ORDER BY (pr.state <> 'open'), pr.updated_at DESC
LIMIT sqlc.arg(row_limit);

-- ─── Parents, children and relations (D-83) ──────────────────────────────────

-- name: IssueRefByID :one
-- An issue as another names it: its project's key and digits, title,
-- status and type.
SELECT i.id, i.project_id, i.number, i.title, i.status, i.type_id, i.parent_id, p.org_id,
       COALESCE(p.issue_key, '')::text AS issue_key, COALESCE(p.issue_key_digits, o.issue_key_digits)::int AS digits,
       i.cycle_id
FROM issues i JOIN projects p ON p.id = i.project_id JOIN organizations o ON o.id = p.org_id
WHERE i.id = $1;

-- name: IssueByKey :one
-- The workspace's issue key-number names (its project's key or a former one).
SELECT i.id
FROM issues i JOIN projects p ON p.id = i.project_id
WHERE p.org_id = sqlc.arg(org_id) AND i.number = sqlc.arg(number)
  AND (p.issue_key = sqlc.arg(key)
       OR EXISTS (SELECT 1 FROM issue_key_aliases k WHERE k.project_id = p.id AND k.key = sqlc.arg(key)));

-- name: ListIssueChildren :many
SELECT i.id, i.project_id, i.number, i.title, i.status, i.type_id, i.assignee_id, COALESCE(a.name, '')::text AS assignee_name,
       COALESCE(p.issue_key, '')::text AS issue_key, COALESCE(p.issue_key_digits, o.issue_key_digits)::int AS digits,
       i.estimate, COALESCE(cy.number, 0)::int AS cycle_number
FROM issues i JOIN projects p ON p.id = i.project_id JOIN organizations o ON o.id = p.org_id
LEFT JOIN users a ON a.id = i.assignee_id
LEFT JOIN cycles cy ON cy.id = i.cycle_id
WHERE i.parent_id = $1
ORDER BY array_position(ARRAY['in_progress', 'in_review', 'todo', 'backlog', 'shipped', 'cancelled']::text[], i.status), i.number;

-- name: ChildCounts :one
SELECT count(*) AS total,
       count(*) FILTER (WHERE status = 'shipped') AS shipped,
       count(*) FILTER (WHERE status IN ('shipped', 'cancelled')) AS done
FROM issues WHERE parent_id = $1;

-- name: ListIssueRelations :many
-- Both ways: outward (this issue is from) and inward (it is to).
SELECT r.id, r.kind, (r.from_issue_id = sqlc.arg(self_id)::uuid)::bool AS outward, o.id AS other_id, o.project_id AS other_project_id,
       o.number, o.title, o.status, o.type_id,
       COALESCE(p.issue_key, '')::text AS issue_key, COALESCE(p.issue_key_digits, og.issue_key_digits)::int AS digits
FROM issue_relations r
JOIN issues o ON o.id = (CASE WHEN r.from_issue_id = sqlc.arg(self_id)::uuid THEN r.to_issue_id ELSE r.from_issue_id END)
JOIN projects p ON p.id = o.project_id JOIN organizations og ON og.id = p.org_id
WHERE r.from_issue_id = sqlc.arg(self_id)::uuid OR r.to_issue_id = sqlc.arg(self_id)::uuid
ORDER BY r.created_at, r.id;

-- name: InsertIssueRelation :execrows
INSERT INTO issue_relations (from_issue_id, to_issue_id, kind, created_by)
VALUES (sqlc.arg(from_issue_id), sqlc.arg(to_issue_id), sqlc.arg(kind), sqlc.narg(created_by))
ON CONFLICT DO NOTHING;

-- name: GetIssueRelation :one
SELECT r.id, r.from_issue_id, r.to_issue_id, r.kind FROM issue_relations r
WHERE r.id = sqlc.arg(id) AND (r.from_issue_id = sqlc.arg(self_id)::uuid OR r.to_issue_id = sqlc.arg(self_id)::uuid);

-- name: DeleteIssueRelation :exec
DELETE FROM issue_relations WHERE id = $1;

-- name: ListBlockedIssues :many
-- The open issues blocker blocks.
SELECT i.id FROM issue_relations r JOIN issues i ON i.id = r.to_issue_id
WHERE r.from_issue_id = $1 AND r.kind = 'blocks' AND i.status NOT IN ('shipped', 'cancelled');

-- name: CopyIssueLabels :exec
INSERT INTO issue_labels (issue_id, name)
SELECT sqlc.arg(dst_id)::uuid, l.name FROM issue_labels l WHERE l.issue_id = sqlc.arg(src_id)::uuid
ON CONFLICT DO NOTHING;

-- name: CopyIssueRelations :exec
-- A clone's relations: the original's, but not its clone links.
INSERT INTO issue_relations (from_issue_id, to_issue_id, kind, created_by)
SELECT (CASE WHEN r.from_issue_id = sqlc.arg(src_id)::uuid THEN sqlc.arg(dst_id)::uuid ELSE r.from_issue_id END),
       (CASE WHEN r.to_issue_id = sqlc.arg(src_id)::uuid THEN sqlc.arg(dst_id)::uuid ELSE r.to_issue_id END),
       r.kind, sqlc.narg(created_by)::uuid
FROM issue_relations r
WHERE (r.from_issue_id = sqlc.arg(src_id)::uuid OR r.to_issue_id = sqlc.arg(src_id)::uuid) AND r.kind <> 'clones'
ON CONFLICT DO NOTHING;

-- name: SetProjectFailedDeployIssues :exec
UPDATE projects SET failed_deploy_issues = $2 WHERE id = $1;

-- name: MyIssues :many
-- The open issues assigned to a person across the workspace (D-88).
SELECT i.id, i.number, i.title, i.status, i.priority, i.type_id, i.updated_at, i.project_id, p.name AS project_name,
       COALESCE(p.issue_key, '')::text AS issue_key, COALESCE(p.issue_key_digits, o.issue_key_digits)::int AS digits,
       i.due_on, COALESCE(cy.number, 0)::int AS cycle_number
FROM issues i
JOIN projects p ON p.id = i.project_id
JOIN organizations o ON o.id = p.org_id
LEFT JOIN cycles cy ON cy.id = i.cycle_id
WHERE p.org_id = sqlc.arg(org_id) AND i.assignee_id = sqlc.arg(user_id) AND i.status NOT IN ('shipped', 'cancelled')
ORDER BY CASE i.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END, i.updated_at DESC
LIMIT 500;

-- name: GetDeploymentForIssue :one
-- A deploy, with its service and project, for an issue opened from it.
SELECT d.id, d.status, COALESCE(d.error_summary, '')::text AS error_summary, COALESCE(d.commit_sha, '')::text AS commit_sha,
       COALESCE(d.commit_message, '')::text AS commit_message, COALESCE(d.finished_at, d.created_at)::timestamptz AS at,
       s.id AS service_id, s.name AS service_name, COALESCE(e.name, '')::text AS environment_name,
       COALESCE(e.project_id, s.project_id) AS project_id, COALESCE(p.org_id, s.scoped_org_id) AS org_id,
       COALESCE(p.failed_deploy_issues, false)::bool AS failed_deploy_issues
FROM deployments d
JOIN services s ON s.id = d.service_id
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
WHERE d.id = $1;

-- name: OpenFailureIssues :many
-- A service's open issues opened from its failed deploys, newest first.
SELECT i.id, i.number FROM issues i
JOIN deployments d ON d.id = i.from_deployment_id
WHERE d.service_id = $1 AND i.status NOT IN ('shipped', 'cancelled')
ORDER BY i.created_at DESC;

-- name: SetIssueFromDeployment :exec
UPDATE issues SET from_deployment_id = $2 WHERE id = $1;

-- name: ServiceLiveDeployment :one
SELECT COALESCE(active_deployment_id::text, '')::text FROM services WHERE id = $1;
