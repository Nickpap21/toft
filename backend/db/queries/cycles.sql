-- ─── Cycles (D-90) ───────────────────────────────────────────────────────────
-- Part of the issues module: a workspace's cycles, made from its schedule,
-- what each carried over, and its daily counts.

-- name: GetOrgIssueSettings :one
SELECT issue_key_digits, cycle_weeks, cycle_start_day, cycle_time_zone, issue_estimates FROM organizations WHERE id = $1;

-- name: SetOrgCycleSchedule :exec
UPDATE organizations SET cycle_weeks = sqlc.narg(weeks), cycle_start_day = sqlc.arg(start_day), cycle_time_zone = sqlc.arg(time_zone)
WHERE id = sqlc.arg(id);

-- name: SetOrgIssueEstimates :exec
UPDATE organizations SET issue_estimates = sqlc.arg(estimates) WHERE id = sqlc.arg(id);

-- name: ListCycleSchedules :many
-- The workspaces that use cycles, for the issues module's reconcile loop.
SELECT id, cycle_weeks::int AS weeks, cycle_start_day, cycle_time_zone FROM organizations WHERE cycle_weeks IS NOT NULL;

-- name: ListCycles :many
SELECT id, number, name, starts_on, ends_on, closed_at,
       final_scope, final_shipped, final_cancelled, final_carried, final_points, final_shipped_points
FROM cycles WHERE org_id = $1 ORDER BY number;

-- name: InsertCycle :execrows
INSERT INTO cycles (org_id, number, starts_on, ends_on) VALUES (sqlc.arg(org_id), sqlc.arg(number), sqlc.arg(starts_on), sqlc.arg(ends_on))
ON CONFLICT (org_id, number) DO NOTHING;

-- name: MoveUpcomingCycle :execrows
-- A new schedule redraws only the cycles that haven't started.
UPDATE cycles SET starts_on = sqlc.arg(starts_on), ends_on = sqlc.arg(ends_on)
WHERE id = sqlc.arg(id) AND starts_on > sqlc.arg(today)::date AND closed_at IS NULL;

-- name: RenameCycle :execrows
UPDATE cycles SET name = sqlc.arg(name) WHERE org_id = sqlc.arg(org_id) AND number = sqlc.arg(number);

-- name: CycleCounts :many
-- Each of the workspace's cycles' counts now (an ended cycle keeps its
-- final ones). Scope leaves cancelled issues out; started is in progress,
-- in review or shipped.
SELECT i.cycle_id,
       count(*) FILTER (WHERE i.status <> 'cancelled')::int AS scope,
       count(*) FILTER (WHERE i.status IN ('in_progress', 'in_review', 'shipped'))::int AS started,
       count(*) FILTER (WHERE i.status = 'shipped')::int AS shipped,
       count(*) FILTER (WHERE i.status = 'cancelled')::int AS cancelled,
       COALESCE(sum(i.estimate) FILTER (WHERE i.status <> 'cancelled'), 0)::int AS points,
       COALESCE(sum(i.estimate) FILTER (WHERE i.status IN ('in_progress', 'in_review', 'shipped')), 0)::int AS started_points,
       COALESCE(sum(i.estimate) FILTER (WHERE i.status = 'shipped'), 0)::int AS shipped_points
FROM issues i JOIN cycles c ON c.id = i.cycle_id
WHERE c.org_id = sqlc.arg(org_id) AND (sqlc.narg(project_id)::uuid IS NULL OR i.project_id = sqlc.narg(project_id)::uuid)
GROUP BY i.cycle_id;

-- name: CloseCycle :execrows
-- Marks a cycle ended with what it held; the caller then carries its
-- unfinished issues over, in the same transaction.
UPDATE cycles c SET closed_at = NOW(),
       final_scope = x.scope, final_shipped = x.shipped, final_cancelled = x.cancelled, final_carried = x.carried,
       final_points = x.points, final_shipped_points = x.shipped_points
FROM (
    SELECT count(*) FILTER (WHERE status <> 'cancelled')::int AS scope,
           count(*) FILTER (WHERE status = 'shipped')::int AS shipped,
           count(*) FILTER (WHERE status = 'cancelled')::int AS cancelled,
           count(*) FILTER (WHERE status NOT IN ('shipped', 'cancelled'))::int AS carried,
           COALESCE(sum(estimate) FILTER (WHERE status <> 'cancelled'), 0)::int AS points,
           COALESCE(sum(estimate) FILTER (WHERE status = 'shipped'), 0)::int AS shipped_points
    FROM issues WHERE cycle_id = sqlc.arg(id)
) x
WHERE c.id = sqlc.arg(id) AND c.closed_at IS NULL;

-- name: CarryCycleIssues :many
UPDATE issues SET cycle_id = sqlc.arg(to_cycle_id), updated_at = NOW()
WHERE cycle_id = sqlc.arg(cycle_id) AND status NOT IN ('shipped', 'cancelled')
RETURNING id;

-- name: InsertCycleCarryovers :exec
INSERT INTO cycle_carryovers (cycle_id, issue_id, to_cycle_id)
SELECT sqlc.arg(cycle_id), unnest(sqlc.arg(issue_ids)::uuid[]), sqlc.arg(to_cycle_id)
ON CONFLICT DO NOTHING;

-- name: InsertCarryEvents :exec
-- The history line of each carried issue: automatic, from one cycle's
-- number to the next's.
INSERT INTO issue_events (issue_id, kind, automatic, from_value, to_value)
SELECT unnest(sqlc.arg(issue_ids)::uuid[]), 'cycle', true, sqlc.arg(from_value)::text, sqlc.arg(to_value)::text;

-- name: SnapshotCycleDay :exec
INSERT INTO cycle_days (cycle_id, day, scope, started, shipped, scope_points, started_points, shipped_points)
SELECT sqlc.arg(cycle_id), sqlc.arg(day),
       count(*) FILTER (WHERE status <> 'cancelled'),
       count(*) FILTER (WHERE status IN ('in_progress', 'in_review', 'shipped')),
       count(*) FILTER (WHERE status = 'shipped'),
       COALESCE(sum(estimate) FILTER (WHERE status <> 'cancelled'), 0),
       COALESCE(sum(estimate) FILTER (WHERE status IN ('in_progress', 'in_review', 'shipped')), 0),
       COALESCE(sum(estimate) FILTER (WHERE status = 'shipped'), 0)
FROM issues WHERE cycle_id = sqlc.arg(cycle_id)
ON CONFLICT (cycle_id, day) DO UPDATE
SET scope = EXCLUDED.scope, started = EXCLUDED.started, shipped = EXCLUDED.shipped,
    scope_points = EXCLUDED.scope_points, started_points = EXCLUDED.started_points, shipped_points = EXCLUDED.shipped_points;

-- name: ListCycleDays :many
SELECT day, scope, started, shipped, scope_points, started_points, shipped_points FROM cycle_days WHERE cycle_id = $1 ORDER BY day;

-- name: CycleShares :many
-- A cycle's issues by project and by assignee: how many, how many shipped.
SELECT 'project'::text AS by, p.id::text AS ref, p.name AS name,
       count(*) FILTER (WHERE i.status <> 'cancelled')::int AS total, count(*) FILTER (WHERE i.status = 'shipped')::int AS shipped
FROM issues i JOIN projects p ON p.id = i.project_id
WHERE i.cycle_id = sqlc.arg(cycle_id)
   OR EXISTS (SELECT 1 FROM cycle_carryovers cc WHERE cc.cycle_id = sqlc.arg(cycle_id) AND cc.issue_id = i.id)
GROUP BY p.id, p.name
UNION ALL
SELECT 'assignee'::text, COALESCE(i.assignee_id::text, '')::text, COALESCE(u.name, '')::text,
       count(*) FILTER (WHERE i.status <> 'cancelled')::int, count(*) FILTER (WHERE i.status = 'shipped')::int
FROM issues i LEFT JOIN users u ON u.id = i.assignee_id
WHERE i.cycle_id = sqlc.arg(cycle_id)
   OR EXISTS (SELECT 1 FROM cycle_carryovers cc WHERE cc.cycle_id = sqlc.arg(cycle_id) AND cc.issue_id = i.id)
GROUP BY i.assignee_id, u.name;

-- name: CycleMoves :one
-- How many of a cycle's issues came after it started (planned in by a
-- person since its first day), and how many it carried in from the one
-- before.
SELECT (SELECT count(DISTINCT e.issue_id) FROM issue_events e JOIN issues i ON i.id = e.issue_id
        WHERE i.cycle_id = sqlc.arg(cycle_id) AND e.kind = 'cycle' AND NOT e.automatic
          AND e.to_value = sqlc.arg(number)::text AND e.created_at >= sqlc.arg(since)::timestamptz)::int AS added,
       (SELECT count(*) FROM cycle_carryovers cc WHERE cc.to_cycle_id = sqlc.arg(cycle_id))::int AS carried_in;

-- name: ListCycleIssues :many
-- A cycle's issues across the workspace's projects, with those it carried
-- over to the next when it ended; in pages by id.
SELECT i.id, i.project_id, p.name AS project_name, i.number, i.title, i.status, i.priority, i.type_id,
       i.assignee_id, COALESCE(a.name, '')::text AS assignee_name, i.estimate, i.due_on,
       COALESCE((SELECT array_agg(l.name ORDER BY l.name) FROM issue_labels l WHERE l.issue_id = i.id), '{}')::text[] AS labels,
       COALESCE(p.issue_key, '')::text AS issue_key, COALESCE(p.issue_key_digits, o.issue_key_digits)::int AS digits,
       COALESCE((SELECT fc.number FROM cycle_carryovers cc JOIN cycles fc ON fc.id = cc.cycle_id
                 WHERE cc.issue_id = i.id AND cc.to_cycle_id = sqlc.arg(cycle_id) ORDER BY fc.number DESC LIMIT 1), 0)::int AS carried_from,
       COALESCE((SELECT tc.number FROM cycle_carryovers cc JOIN cycles tc ON tc.id = cc.to_cycle_id
                 WHERE cc.issue_id = i.id AND cc.cycle_id = sqlc.arg(cycle_id)), 0)::int AS carried_to
FROM issues i
JOIN projects p ON p.id = i.project_id
JOIN organizations o ON o.id = p.org_id
LEFT JOIN users a ON a.id = i.assignee_id
WHERE (i.cycle_id = sqlc.arg(cycle_id)
       OR EXISTS (SELECT 1 FROM cycle_carryovers cc WHERE cc.cycle_id = sqlc.arg(cycle_id) AND cc.issue_id = i.id))
  AND (sqlc.narg(project_id)::uuid IS NULL OR i.project_id = sqlc.narg(project_id)::uuid)
  AND (sqlc.narg(after_id)::uuid IS NULL OR i.id > sqlc.narg(after_id)::uuid)
ORDER BY i.id
LIMIT sqlc.arg(row_limit);

-- name: CycleByNumber :one
SELECT id, starts_on, ends_on FROM cycles WHERE org_id = sqlc.arg(org_id) AND number = sqlc.arg(number);

-- name: ListTimelineIssues :many
-- A project's issues for its Timeline: the open ones and those closed since
-- since, with when each first moved to In progress or later, its labels and
-- its children's points.
SELECT i.id, i.number, i.title, i.status, i.priority, i.type_id, i.parent_id, i.start_on, i.due_on, i.estimate,
       i.assignee_id, COALESCE(a.name, '')::text AS assignee_name, i.status_changed_at,
       (SELECT min(e.created_at) FROM issue_events e
        WHERE e.issue_id = i.id AND e.kind = 'status' AND e.to_value IN ('in_progress', 'in_review', 'shipped'))::timestamptz AS started_at,
       COALESCE((SELECT array_agg(l.name ORDER BY l.name) FROM issue_labels l WHERE l.issue_id = i.id), '{}')::text[] AS labels,
       COALESCE(kids.total, 0)::int AS children, COALESCE(kids.shipped, 0)::int AS children_shipped,
       COALESCE(kids.done, 0)::int AS children_done, COALESCE(kids.points, 0)::int AS children_points
FROM issues i
LEFT JOIN users a ON a.id = i.assignee_id
LEFT JOIN LATERAL (
    SELECT count(*) AS total, count(*) FILTER (WHERE c.status = 'shipped') AS shipped,
           count(*) FILTER (WHERE c.status IN ('shipped', 'cancelled')) AS done, sum(c.estimate) AS points
    FROM issues c WHERE c.parent_id = i.id
) kids ON true
WHERE i.project_id = sqlc.arg(project_id)
  AND (i.status NOT IN ('shipped', 'cancelled') OR i.status_changed_at >= sqlc.arg(since)::timestamptz)
ORDER BY i.number
LIMIT 2000;

-- name: ListProjectBlocks :many
-- The blocks relations between a project's issues.
SELECT r.from_issue_id, r.to_issue_id FROM issue_relations r
JOIN issues f ON f.id = r.from_issue_id JOIN issues t ON t.id = r.to_issue_id
WHERE r.kind = 'blocks' AND f.project_id = sqlc.arg(project_id) AND t.project_id = sqlc.arg(project_id);
