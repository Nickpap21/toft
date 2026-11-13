-- The inbox (D-88): a person's Needs you (computed), their updates
-- (notifications), and the notifier's sources.

-- name: InboxAssignedUrgent :many
-- Open urgent or high issues assigned to a person and not yet started.
SELECT i.id, i.number, i.title, i.priority, i.status, i.updated_at, i.project_id, p.name AS project_name,
       COALESCE(p.issue_key, '')::text AS issue_key, COALESCE(p.issue_key_digits, o.issue_key_digits)::int AS digits
FROM issues i
JOIN projects p ON p.id = i.project_id
JOIN organizations o ON o.id = p.org_id
WHERE p.org_id = sqlc.arg(org_id) AND i.assignee_id = sqlc.arg(user_id)
  AND i.priority IN ('urgent', 'high') AND i.status IN ('backlog', 'todo')
ORDER BY CASE i.priority WHEN 'urgent' THEN 0 ELSE 1 END, i.updated_at DESC
LIMIT 20;

-- name: InboxAssignedDue :many
-- Open issues assigned to a person and due by tomorrow, in the workspace's
-- time zone (D-90).
SELECT i.id, i.number, i.title, i.status, i.priority, i.due_on, i.updated_at, i.project_id, p.name AS project_name,
       COALESCE(p.issue_key, '')::text AS issue_key, COALESCE(p.issue_key_digits, o.issue_key_digits)::int AS digits,
       (NOW() AT TIME ZONE o.cycle_time_zone)::date AS today
FROM issues i
JOIN projects p ON p.id = i.project_id
JOIN organizations o ON o.id = p.org_id
WHERE p.org_id = sqlc.arg(org_id) AND i.assignee_id = sqlc.arg(user_id)
  AND i.status NOT IN ('shipped', 'cancelled') AND i.due_on <= (NOW() AT TIME ZONE o.cycle_time_zone)::date + 1
ORDER BY i.due_on, i.number
LIMIT 20;

-- name: ListInboxDone :many
SELECT item_key, done_at FROM inbox_done WHERE user_id = $1;

-- name: MarkInboxDone :exec
INSERT INTO inbox_done (user_id, item_key)
SELECT sqlc.arg(user_id), k FROM unnest(sqlc.arg(keys)::text[]) AS k
ON CONFLICT (user_id, item_key) DO UPDATE SET done_at = now();

-- name: UnmarkInboxDone :exec
DELETE FROM inbox_done WHERE user_id = sqlc.arg(user_id) AND item_key = ANY(sqlc.arg(keys)::text[]);

-- name: ListNotifications :many
-- A person's updates in a workspace, newest first, with what they are
-- about as it is now.
SELECT n.id, n.kind, n.detail, n.created_at, n.read_at, n.done_at,
       n.actor_id, COALESCE(a.name, '')::text AS actor_name,
       n.project_id, COALESCE(p.name, '')::text AS project_name,
       n.issue_id, COALESCE(i.number, 0)::int AS issue_number, COALESCE(i.title, '')::text AS issue_title,
       COALESCE(i.status, '')::text AS issue_status, COALESCE(p.issue_key, '')::text AS issue_key,
       COALESCE(p.issue_key_digits, o.issue_key_digits, 0)::int AS digits,
       n.pull_request_id, COALESCE(pr.number, 0)::int AS pull_number, COALESCE(pr.title, '')::text AS pull_title,
       COALESCE(pr.state, '')::text AS pull_state, COALESCE(r.name, '')::text AS repository,
       n.page_id, COALESCE(d.title, '')::text AS page_title
FROM notifications n
LEFT JOIN users a ON a.id = n.actor_id
LEFT JOIN projects p ON p.id = n.project_id
LEFT JOIN organizations o ON o.id = n.org_id
LEFT JOIN issues i ON i.id = n.issue_id
LEFT JOIN pull_requests pr ON pr.id = n.pull_request_id
LEFT JOIN repositories r ON r.id = pr.repository_id
LEFT JOIN doc_pages d ON d.id = n.page_id
WHERE n.user_id = sqlc.arg(user_id) AND n.org_id = sqlc.arg(org_id)
  AND (n.done_at IS NOT NULL) = sqlc.arg(done)::bool
  AND (NOT sqlc.arg(done)::bool OR n.done_at > now() - interval '30 days')
  AND (NOT sqlc.arg(unread)::bool OR n.read_at IS NULL)
  AND (cardinality(sqlc.arg(kinds)::text[]) = 0 OR n.kind = ANY(sqlc.arg(kinds)::text[]))
  AND (NOT sqlc.arg(has_cursor)::bool OR (n.created_at, n.id) < (sqlc.arg(cursor_at)::timestamptz, sqlc.arg(cursor_id)::bigint))
ORDER BY n.created_at DESC, n.id DESC
LIMIT sqlc.arg(row_limit);

-- name: CountUnreadNotifications :one
SELECT count(*) FROM notifications
WHERE user_id = $1 AND org_id = $2 AND read_at IS NULL AND done_at IS NULL;

-- name: MarkNotificationsRead :exec
-- Reads ids, or (all) every unread one.
UPDATE notifications SET read_at = now()
WHERE user_id = sqlc.arg(user_id) AND org_id = sqlc.arg(org_id) AND read_at IS NULL
  AND (sqlc.arg(all_unread)::bool OR id = ANY(sqlc.arg(ids)::bigint[]));

-- name: SetNotificationsDone :exec
-- Marks ids done (done), or back in the inbox; done ones are read.
UPDATE notifications
SET done_at = CASE WHEN sqlc.arg(done)::bool THEN now() END,
    read_at = COALESCE(read_at, now())
WHERE user_id = sqlc.arg(user_id) AND org_id = sqlc.arg(org_id) AND id = ANY(sqlc.arg(ids)::bigint[]);

-- name: PurgeInbox :exec
-- Updates go after 90 days, Needs you's marks after 30.
WITH gone AS (DELETE FROM notifications WHERE created_at < now() - interval '90 days')
DELETE FROM inbox_done WHERE done_at < now() - interval '30 days';

-- name: InsertNotifications :exec
INSERT INTO notifications (user_id, org_id, kind, actor_id, project_id, issue_id, pull_request_id, page_id, detail, created_at)
SELECT x.user_id, x.org_id, x.kind, x.actor_id, x.project_id, x.issue_id, x.pull_request_id, x.page_id, COALESCE(x.detail, '{}'::jsonb), x.created_at
FROM jsonb_to_recordset(sqlc.arg(rows)::jsonb) AS x(user_id uuid, org_id uuid, kind text, actor_id uuid, project_id uuid,
     issue_id uuid, pull_request_id uuid, page_id uuid, detail jsonb, created_at timestamptz);

-- name: GetInboxCursor :one
SELECT at, last_id FROM inbox_cursors WHERE source = $1;

-- name: SetInboxCursor :exec
INSERT INTO inbox_cursors (source, at, last_id) VALUES ($1, $2, $3)
ON CONFLICT (source) DO UPDATE SET at = EXCLUDED.at, last_id = EXCLUDED.last_id, updated_at = now();

-- The notifier's sources: rows after a cursor (time, id as text), older
-- than a few seconds so a transaction still open can't be passed over.

-- name: InboxIssueEvents :many
SELECT e.id::text AS row_id, e.created_at, e.kind, e.actor_id, e.automatic,
       COALESCE(e.from_value, '')::text AS from_value, COALESCE(e.to_value, '')::text AS to_value,
       i.id AS issue_id, i.project_id, p.org_id, i.created_by, i.description,
       COALESCE((SELECT ac.name FROM agent_connections ac WHERE ac.id = i.agent_connection_id), '')::text AS agent_name
FROM issue_events e
JOIN issues i ON i.id = e.issue_id
JOIN projects p ON p.id = i.project_id
WHERE e.kind IN ('opened', 'status', 'assigned')
  AND (e.created_at, e.id::text) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::text)
  AND e.created_at < now() - interval '3 seconds'
ORDER BY e.created_at, e.id::text
LIMIT 500;

-- name: InboxIssueComments :many
SELECT c.id::text AS row_id, c.created_at, c.author_id, c.body, i.id AS issue_id, i.project_id, p.org_id,
       COALESCE((SELECT ac.name FROM agent_connections ac WHERE ac.id = c.agent_connection_id), '')::text AS agent_name
FROM issue_comments c
JOIN issues i ON i.id = c.issue_id
JOIN projects p ON p.id = i.project_id
WHERE (c.created_at, c.id::text) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::text)
  AND c.created_at < now() - interval '3 seconds'
ORDER BY c.created_at, c.id::text
LIMIT 500;

-- name: InboxPullsOpened :many
SELECT pr.id::text AS row_id, pr.created_at, pr.author_id, pr.body, pr.id AS pull_id, r.project_id, p.org_id,
       COALESCE((SELECT ac.name FROM agent_connections ac WHERE ac.id = pr.agent_connection_id), '')::text AS agent_name
FROM pull_requests pr
JOIN repositories r ON r.id = pr.repository_id
JOIN projects p ON p.id = r.project_id
WHERE (pr.created_at, pr.id::text) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::text)
  AND pr.created_at < now() - interval '3 seconds'
ORDER BY pr.created_at, pr.id::text
LIMIT 500;

-- name: InboxPullEvents :many
SELECT e.id::text AS row_id, e.created_at, e.kind, e.actor_id, pr.id AS pull_id, r.project_id, p.org_id
FROM pull_request_events e
JOIN pull_requests pr ON pr.id = e.pull_request_id
JOIN repositories r ON r.id = pr.repository_id
JOIN projects p ON p.id = r.project_id
WHERE e.kind IN ('merged', 'closed')
  AND (e.created_at, e.id::text) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::text)
  AND e.created_at < now() - interval '3 seconds'
ORDER BY e.created_at, e.id::text
LIMIT 500;

-- name: InboxPullComments :many
SELECT c.id::text AS row_id, c.created_at, c.author_id, c.body, pr.id AS pull_id, r.project_id, p.org_id,
       COALESCE((SELECT ac.name FROM agent_connections ac WHERE ac.id = c.agent_connection_id), '')::text AS agent_name
FROM pull_request_comments c
JOIN pull_requests pr ON pr.id = c.pull_request_id
JOIN repositories r ON r.id = pr.repository_id
JOIN projects p ON p.id = r.project_id
WHERE (c.created_at, c.id::text) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::text)
  AND c.created_at < now() - interval '3 seconds'
ORDER BY c.created_at, c.id::text
LIMIT 500;

-- name: InboxPageVersions :many
SELECT (v.page_id::text || ':' || lpad(v.version::text, 10, '0'))::text AS row_id, v.created_at, v.author_id, v.version,
       d.id AS page_id, d.project_id, p.org_id
FROM doc_page_versions v
JOIN doc_pages d ON d.id = v.page_id
JOIN projects p ON p.id = d.project_id
WHERE v.version > 1
  AND (v.created_at, v.page_id::text || ':' || lpad(v.version::text, 10, '0')) > (sqlc.arg(after_at)::timestamptz, sqlc.arg(after_id)::text)
  AND v.created_at < now() - interval '3 seconds'
ORDER BY v.created_at, v.page_id::text || ':' || lpad(v.version::text, 10, '0')
LIMIT 500;

-- name: InboxIssueParticipants :many
-- Who an issue concerns: its author, its assignee and its commenters.
SELECT i.id AS issue_id, i.created_by AS user_id FROM issues i WHERE i.id = ANY(sqlc.arg(ids)::uuid[]) AND i.created_by IS NOT NULL
UNION
SELECT i.id, i.assignee_id FROM issues i WHERE i.id = ANY(sqlc.arg(ids)::uuid[]) AND i.assignee_id IS NOT NULL
UNION
SELECT c.issue_id, c.author_id FROM issue_comments c WHERE c.issue_id = ANY(sqlc.arg(ids)::uuid[]) AND c.author_id IS NOT NULL;

-- name: InboxPullParticipants :many
-- Who a pull request concerns: its author and its commenters.
SELECT pr.id AS pull_id, pr.author_id AS user_id FROM pull_requests pr WHERE pr.id = ANY(sqlc.arg(ids)::uuid[]) AND pr.author_id IS NOT NULL
UNION
SELECT c.pull_request_id, c.author_id FROM pull_request_comments c WHERE c.pull_request_id = ANY(sqlc.arg(ids)::uuid[]) AND c.author_id IS NOT NULL;

-- name: InboxPageParticipants :many
-- Who a page concerns: whoever wrote or edited it.
SELECT d.id AS page_id, d.created_by AS user_id FROM doc_pages d WHERE d.id = ANY(sqlc.arg(ids)::uuid[]) AND d.created_by IS NOT NULL
UNION
SELECT v.page_id, v.author_id FROM doc_page_versions v WHERE v.page_id = ANY(sqlc.arg(ids)::uuid[]) AND v.author_id IS NOT NULL;

-- name: InboxOrgMembers :many
-- The workspaces' members, for mentions.
SELECT m.org_id, u.id AS user_id, u.email FROM org_members m JOIN users u ON u.id = m.user_id
WHERE m.org_id = ANY(sqlc.arg(org_ids)::uuid[]);

-- name: ListOrgMemberIDs :many
SELECT user_id FROM org_members WHERE org_id = $1;
