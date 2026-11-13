-- Docs v1 (D-86): a project's pages, their versions, the services their
-- blocks name and what they name.

-- name: CountDocPages :one
SELECT count(*) FROM doc_pages WHERE project_id = $1 AND deleted_at IS NULL;

-- name: ListDocTree :many
-- The project's pages, or (deleted) those deleted in the last 30 days;
-- the panel builds the tree from parent_id and position.
SELECT d.id, d.parent_id, d.title, d.position, d.updated_at, d.deleted_at,
       (position('```toft-run' IN d.body) > 0) AS runbook
FROM doc_pages d
WHERE d.project_id = sqlc.arg(project_id)
  AND (d.deleted_at IS NOT NULL) = sqlc.arg(deleted)::bool
  AND (NOT sqlc.arg(deleted)::bool OR d.deleted_at > now() - interval '30 days')
ORDER BY d.parent_id NULLS FIRST, d.position, d.created_at
LIMIT 1000;

-- name: CreateDocPage :one
-- Last among its parent's pages.
INSERT INTO doc_pages (project_id, parent_id, title, body, created_by, updated_by, position)
VALUES (sqlc.arg(project_id), sqlc.narg(parent_id), sqlc.arg(title), sqlc.arg(body), sqlc.arg(actor_id), sqlc.arg(actor_id),
        (SELECT COALESCE(max(s.position) + 1, 0) FROM doc_pages s
         WHERE s.project_id = sqlc.arg(project_id) AND s.parent_id IS NOT DISTINCT FROM sqlc.narg(parent_id)::uuid))
RETURNING id;

-- name: InsertDocVersion :exec
INSERT INTO doc_page_versions (page_id, version, title, body, author_id)
VALUES ($1, $2, $3, $4, $5);

-- name: GetDocPage :one
SELECT d.id, d.project_id, d.parent_id, d.title, d.body, d.version, d.position,
       d.created_by, COALESCE(cu.name, '')::text AS created_by_name,
       d.updated_by, COALESCE(uu.name, '')::text AS updated_by_name,
       d.created_at, d.updated_at, d.verified_at,
       d.verified_by, COALESCE(vu.name, '')::text AS verified_by_name, d.deleted_at
FROM doc_pages d
LEFT JOIN users cu ON cu.id = d.created_by
LEFT JOIN users uu ON uu.id = d.updated_by
LEFT JOIN users vu ON vu.id = d.verified_by
WHERE d.project_id = sqlc.arg(project_id) AND d.id = sqlc.arg(id)
  AND (d.deleted_at IS NULL OR d.deleted_at > now() - interval '30 days');

-- name: GetDocPath :many
-- The pages a page is in, from the top.
WITH RECURSIVE up AS (
    SELECT p.id, p.parent_id, p.title, p.project_id, 0 AS depth FROM doc_pages p WHERE p.id = (SELECT c.parent_id FROM doc_pages c WHERE c.id = $1)
    UNION ALL
    SELECT p.id, p.parent_id, p.title, p.project_id, up.depth + 1 FROM doc_pages p JOIN up ON p.id = up.parent_id WHERE up.depth < 50
)
SELECT up.id, up.title, up.project_id FROM up ORDER BY up.depth DESC;

-- name: SaveDocContent :one
-- The next version, if base is still the current one.
UPDATE doc_pages
SET title = sqlc.arg(title), body = sqlc.arg(body), version = version + 1,
    updated_by = sqlc.arg(actor_id), updated_at = now()
WHERE id = sqlc.arg(id) AND version = sqlc.arg(base_version) AND deleted_at IS NULL
RETURNING version;

-- name: ListDocSiblings :many
SELECT id FROM doc_pages
WHERE project_id = sqlc.arg(project_id) AND parent_id IS NOT DISTINCT FROM sqlc.narg(parent_id)::uuid
  AND deleted_at IS NULL AND id <> sqlc.arg(except_id)
ORDER BY position, created_at;

-- name: PlaceDocPages :exec
-- Puts pages under parent in the order of ids.
UPDATE doc_pages d
SET parent_id = sqlc.narg(parent_id)::uuid, position = array_position(sqlc.arg(ids)::uuid[], d.id) - 1,
    updated_at = CASE WHEN d.id = sqlc.arg(moved_id)::uuid THEN now() ELSE d.updated_at END
WHERE d.id = ANY(sqlc.arg(ids)::uuid[]);

-- name: CountLiveDocChildren :one
SELECT count(*) FROM doc_pages WHERE parent_id = $1 AND deleted_at IS NULL;

-- name: DeleteDocPage :execrows
UPDATE doc_pages SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: RestoreDocPage :execrows
-- Back under its parent, or at the top when that is deleted too.
UPDATE doc_pages d
SET deleted_at = NULL,
    parent_id = CASE WHEN EXISTS (SELECT 1 FROM doc_pages p WHERE p.id = d.parent_id AND p.deleted_at IS NULL) THEN d.parent_id END
WHERE d.id = $1 AND d.deleted_at IS NOT NULL;

-- name: PurgeDocPages :execrows
-- Deleted pages go after 30 days, with their versions and links.
DELETE FROM doc_pages WHERE deleted_at < now() - interval '30 days';

-- name: VerifyDocPage :exec
UPDATE doc_pages SET verified_at = now(), verified_by = $2 WHERE id = $1;

-- name: ListDocVersions :many
SELECT v.version, v.title, v.author_id, COALESCE(u.name, '')::text AS author_name, v.created_at
FROM doc_page_versions v
LEFT JOIN users u ON u.id = v.author_id
WHERE v.page_id = sqlc.arg(page_id) AND (sqlc.arg(before)::int = 0 OR v.version < sqlc.arg(before)::int)
ORDER BY v.version DESC
LIMIT sqlc.arg(row_limit);

-- name: GetDocVersion :one
SELECT v.version, v.title, v.body, v.author_id, COALESCE(u.name, '')::text AS author_name, v.created_at
FROM doc_page_versions v
LEFT JOIN users u ON u.id = v.author_id
WHERE v.page_id = $1 AND v.version = $2;

-- name: ProjectServiceIDs :many
-- Which of ids are the project's services (its environments' and its own).
SELECT s.id FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
WHERE s.id = ANY(sqlc.arg(ids)::uuid[])
  AND (e.project_id = sqlc.arg(project_id) OR (s.scope = 'project' AND s.project_id = sqlc.arg(project_id)));

-- name: ListDocServiceState :many
-- The services pages name (by page, or every page of a project), each
-- with what the page last saw of it and what it is now (D-86).
SELECT l.page_id, l.service_id, l.seen, l.seen_at,
       s.name, COALESCE(e.name, '')::text AS environment, s.runtime_status, s.source_type,
       COALESCE(s.docker_image, '')::text AS docker_image, COALESCE(s.build_pack, '')::text AS build_pack,
       COALESCE(s.base_directory, '')::text AS base_directory, COALESCE(s.install_command, '')::text AS install_command,
       COALESCE(s.build_command, '')::text AS build_command, COALESCE(s.start_command, '')::text AS start_command,
       COALESCE(s.port, 0)::int AS port, md5(COALESCE(s.compose_content, '')) AS compose_hash,
       COALESCE(s.compose_file_path, '')::text AS compose_file_path,
       COALESCE(s.server_id::text, '')::text AS server_id, COALESCE(sv.name, '')::text AS server_name,
       COALESCE(ld.image_tag, '')::text AS live_image, COALESCE(ld.commit_sha, '')::text AS live_commit, ld.finished_at AS live_at,
       COALESCE((SELECT jsonb_object_agg(v.key, jsonb_build_object('at', v.updated_at, 'secret', v.is_secret))
                 FROM env_vars v WHERE v.service_id = s.id), '{}'::jsonb)::jsonb AS vars,
       nd.id AS since_id, nd.commit_sha AS since_commit, nd.finished_at AS since_at
FROM doc_page_services l
JOIN doc_pages d ON d.id = l.page_id
JOIN services s ON s.id = l.service_id
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN servers sv ON sv.id = s.server_id
LEFT JOIN deployments ld ON ld.id = s.active_deployment_id
LEFT JOIN LATERAL (
    SELECT x.id, x.commit_sha, x.finished_at FROM deployments x
    WHERE x.service_id = s.id AND x.status = 'running' AND x.finished_at > l.seen_at
    ORDER BY x.finished_at DESC LIMIT 1
) nd ON true
WHERE l.page_id = ANY(sqlc.arg(page_ids)::uuid[]) OR d.project_id = sqlc.narg(project_id)::uuid
ORDER BY l.page_id, s.name;

-- name: UnlinkDocServices :exec
-- Drops the services the page no longer names.
DELETE FROM doc_page_services WHERE page_id = sqlc.arg(page_id) AND NOT (service_id = ANY(sqlc.arg(keep)::uuid[]));

-- name: SeeDocServices :exec
-- Records what the page saw of each service: new links only, or (all)
-- every one, as Mark as verified does.
INSERT INTO doc_page_services (page_id, service_id, seen)
SELECT sqlc.arg(page_id)::uuid, v.key::uuid, v.value
FROM jsonb_each(sqlc.arg(seen)::jsonb) AS v
ON CONFLICT (page_id, service_id) DO UPDATE SET seen = EXCLUDED.seen, seen_at = now()
WHERE sqlc.arg(all_links)::bool;

-- name: ReplaceDocRefsDelete :exec
DELETE FROM doc_refs WHERE page_id = $1;

-- name: InsertDocRefs :exec
INSERT INTO doc_refs (page_id, issue_id, pull_request_id, to_page_id)
SELECT sqlc.arg(page_id)::uuid AS page_id, x AS issue_id, NULL::uuid AS pull_request_id, NULL::uuid AS to_page_id FROM unnest(sqlc.arg(issue_ids)::uuid[]) AS x
UNION ALL SELECT sqlc.arg(page_id)::uuid, NULL::uuid, x, NULL::uuid FROM unnest(sqlc.arg(pull_ids)::uuid[]) AS x
UNION ALL SELECT sqlc.arg(page_id)::uuid, NULL::uuid, NULL::uuid, x FROM unnest(sqlc.arg(page_ids)::uuid[]) AS x
ON CONFLICT DO NOTHING;

-- name: ResolveDocIssues :many
-- The workspace's issues named as KEY-12 (a project's key or a former one).
SELECT q.ref::text AS ref, i.id
FROM unnest(sqlc.arg(refs)::text[]) AS q(ref)
JOIN projects p ON p.org_id = sqlc.arg(org_id)
     AND (p.issue_key = split_part(q.ref, '-', 1)
          OR EXISTS (SELECT 1 FROM issue_key_aliases k WHERE k.project_id = p.id AND k.key = split_part(q.ref, '-', 1)))
JOIN issues i ON i.project_id = p.id AND i.number = split_part(q.ref, '-', 2)::int;

-- name: ResolveDocPulls :many
-- The project's pull requests named as repository!40.
SELECT q.ref::text AS ref, pr.id
FROM unnest(sqlc.arg(refs)::text[]) AS q(ref)
JOIN repositories r ON r.project_id = sqlc.arg(project_id) AND r.name = split_part(q.ref, '!', 1)
JOIN pull_requests pr ON pr.repository_id = r.id AND pr.number = split_part(q.ref, '!', 2)::int;

-- name: ProjectRepositoryNames :many
SELECT name FROM repositories WHERE project_id = $1 ORDER BY name;

-- name: ExistingDocPages :many
SELECT id FROM doc_pages WHERE project_id = sqlc.arg(project_id) AND id = ANY(sqlc.arg(ids)::uuid[]);

-- name: DocIssues :many
-- Issues as a page shows them.
SELECT i.id, COALESCE(p.issue_key, '')::text AS issue_key, COALESCE(p.issue_key_digits, o.issue_key_digits)::int AS digits,
       i.number, i.title, i.status, i.project_id, i.assignee_id, COALESCE(u.name, '')::text AS assignee_name
FROM issues i
JOIN projects p ON p.id = i.project_id
JOIN organizations o ON o.id = p.org_id
LEFT JOIN users u ON u.id = i.assignee_id
WHERE i.id = ANY(sqlc.arg(ids)::uuid[]);

-- name: DocPulls :many
-- Pull requests as a page shows them, with their head's check states.
SELECT pr.id, r.name AS repository, r.id AS repository_id, pr.number, pr.title, pr.state,
       COALESCE((SELECT array_agg(c.state ORDER BY c.state) FROM commit_checks c
                 WHERE c.repository_id = r.id AND c.sha = pr.head_sha), '{}')::text[] AS check_states
FROM pull_requests pr
JOIN repositories r ON r.id = pr.repository_id
WHERE pr.id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListDocRefIDs :many
-- What the page names, for its blocks.
SELECT issue_id, pull_request_id, to_page_id FROM doc_refs WHERE page_id = $1;

-- name: DocsReferencing :many
-- The live pages that name an issue, a pull request or a page.
SELECT DISTINCT d.id, d.title, d.project_id
FROM doc_refs r
JOIN doc_pages d ON d.id = r.page_id
WHERE d.deleted_at IS NULL
  AND (r.issue_id = sqlc.narg(issue_id)::uuid OR r.pull_request_id = sqlc.narg(pull_id)::uuid OR r.to_page_id = sqlc.narg(page_id)::uuid)
ORDER BY d.title
LIMIT 50;

-- name: SearchDocs :many
-- ⌘K and the tree's search: titles and text by word prefix, best first.
SELECT d.id, d.project_id, p.name AS project_name, d.title,
       left(regexp_replace(d.body, '\s+', ' ', 'g'), 400)::text AS body_start
FROM doc_pages d
JOIN projects p ON p.id = d.project_id
WHERE p.org_id = sqlc.arg(org_id) AND d.deleted_at IS NULL
  AND (sqlc.narg(project_id)::uuid IS NULL OR d.project_id = sqlc.narg(project_id)::uuid)
  AND (sqlc.arg(tsq)::text = '' OR d.search @@ to_tsquery('simple', sqlc.arg(tsq)::text))
ORDER BY CASE WHEN sqlc.arg(tsq)::text = '' THEN 0 ELSE ts_rank(d.search, to_tsquery('simple', sqlc.arg(tsq)::text)) END DESC, d.updated_at DESC
LIMIT sqlc.arg(row_limit);

-- name: GetServiceForWork :one
-- A service's project, branch, repository and base directory, for its
-- Connected work.
SELECT s.id, s.name, COALESCE(e.project_id, s.project_id) AS project_id, COALESCE(p.org_id, s.scoped_org_id) AS org_id,
       s.repository_id, COALESCE(s.git_branch, '')::text AS git_branch, COALESCE(s.base_directory, '')::text AS base_directory
FROM services s
LEFT JOIN environments e ON e.id = s.environment_id
LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
WHERE s.id = $1;

-- name: ServiceIssues :many
-- A project's open issues labelled with a service's name (D-81).
SELECT i.id, COALESCE(p.issue_key, '')::text AS issue_key, COALESCE(p.issue_key_digits, o.issue_key_digits)::int AS digits,
       i.number, i.title, i.status, i.priority, i.project_id
FROM issues i
JOIN issue_labels l ON l.issue_id = i.id AND l.name = lower(sqlc.arg(label)::text)
JOIN projects p ON p.id = i.project_id
JOIN organizations o ON o.id = p.org_id
WHERE i.project_id = sqlc.arg(project_id) AND i.status NOT IN ('shipped', 'cancelled')
ORDER BY CASE i.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END, i.updated_at DESC
LIMIT 10;

-- name: ServiceOpenPulls :many
-- A repository's open pull requests into a branch, newest first.
SELECT pr.id, pr.number, pr.title, pr.state, pr.head_sha, r.name AS repository, r.id AS repository_id,
       COALESCE((SELECT array_agg(c.state ORDER BY c.state) FROM commit_checks c
                 WHERE c.repository_id = r.id AND c.sha = pr.head_sha), '{}')::text[] AS check_states
FROM pull_requests pr
JOIN repositories r ON r.id = pr.repository_id
WHERE pr.repository_id = sqlc.arg(repository_id) AND pr.base_branch = sqlc.arg(branch) AND pr.state = 'open'
ORDER BY pr.updated_at DESC
LIMIT 10;

-- name: ServiceDocPages :many
-- The live pages that name a service.
SELECT d.id, d.title, d.verified_at
FROM doc_page_services l
JOIN doc_pages d ON d.id = l.page_id
WHERE l.service_id = $1 AND d.deleted_at IS NULL
ORDER BY d.title
LIMIT 10;

-- name: DocPagesByAuthor :many
-- The live pages a person wrote or edited in a workspace (D-88).
SELECT d.id, d.title, d.project_id, p.name AS project_name, COALESCE(d.verified_at, d.created_at)::timestamptz AS since
FROM doc_pages d
JOIN projects p ON p.id = d.project_id
WHERE p.org_id = sqlc.arg(org_id) AND d.deleted_at IS NULL
  AND (d.created_by = sqlc.arg(user_id) OR EXISTS (SELECT 1 FROM doc_page_versions v WHERE v.page_id = d.id AND v.author_id = sqlc.arg(user_id)))
ORDER BY d.updated_at DESC
LIMIT 200;
