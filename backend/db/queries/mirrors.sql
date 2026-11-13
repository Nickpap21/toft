-- Import, mirrors and outbound webhooks (D-79).

-- name: ListRepositoryMirrors :many
-- The repository's mirrors, the pull mirror first.
SELECT m.id, m.repository_id, m.direction, m.url, m.username, (m.password_encrypted IS NOT NULL)::bool AS has_password,
       m.keep, m.created_at, m.created_by, COALESCE(u.name, '')::text AS created_by_name, m.next_sync_at, m.syncing_since,
       m.last_sync_at, m.last_ok_at, m.failures, m.last_error, m.last_moved, m.remote_head, m.remote_branches, m.remote_tags
FROM repository_mirrors m
LEFT JOIN users u ON u.id = m.created_by
WHERE m.repository_id = sqlc.arg(repository_id)
ORDER BY m.direction, m.created_at, m.id;

-- name: ListProjectPullMirrors :many
-- The pull mirrors of a project's repositories, for its repository list.
SELECT m.id, m.repository_id, m.direction, m.url, m.username, (m.password_encrypted IS NOT NULL)::bool AS has_password,
       m.keep, m.created_at, m.created_by, COALESCE(u.name, '')::text AS created_by_name, m.next_sync_at, m.syncing_since,
       m.last_sync_at, m.last_ok_at, m.failures, m.last_error, m.last_moved, m.remote_head, m.remote_branches, m.remote_tags
FROM repository_mirrors m
JOIN repositories r ON r.id = m.repository_id
LEFT JOIN users u ON u.id = m.created_by
WHERE r.project_id = sqlc.arg(project_id) AND m.direction = 'pull';

-- name: CountPushMirrors :one
SELECT count(*) FROM repository_mirrors WHERE repository_id = sqlc.arg(repository_id) AND direction = 'push';

-- name: DeleteFailedImport :exec
-- An import that failed and never succeeded, which a new import replaces.
DELETE FROM repository_mirrors
WHERE repository_id = sqlc.arg(repository_id) AND direction = 'pull' AND last_ok_at IS NULL AND syncing_since IS NULL AND failures > 0;

-- name: InsertMirror :one
-- Due at once. A second pull mirror for the repository breaks
-- uq_repository_mirrors_pull.
INSERT INTO repository_mirrors (id, repository_id, direction, url, username, password_encrypted, keep, created_by, next_sync_at)
VALUES (sqlc.arg(id), sqlc.arg(repository_id), sqlc.arg(direction), sqlc.arg(url), sqlc.arg(username), sqlc.narg(password_encrypted),
        sqlc.arg(keep), sqlc.narg(created_by), NOW())
RETURNING id;

-- name: DeleteMirror :one
DELETE FROM repository_mirrors WHERE repository_id = sqlc.arg(repository_id) AND id = sqlc.arg(id)
RETURNING id, direction, url, last_ok_at;

-- name: RequestMirrorSync :execrows
-- Due now, unless a sync runs.
UPDATE repository_mirrors SET next_sync_at = NOW()
WHERE repository_id = sqlc.arg(repository_id) AND id = sqlc.arg(id) AND syncing_since IS NULL;

-- name: ClaimMirrors :many
-- Up to n due mirrors, marked running; next_sync_at is consumed, so a push
-- arriving while one syncs makes it due again.
UPDATE repository_mirrors m
SET syncing_since = NOW(), next_sync_at = NULL
FROM (SELECT x.id FROM repository_mirrors x
      WHERE x.next_sync_at <= NOW() AND x.syncing_since IS NULL
      ORDER BY x.next_sync_at LIMIT sqlc.arg(n) FOR UPDATE SKIP LOCKED) c,
     repositories r
WHERE m.id = c.id AND r.id = m.repository_id
RETURNING m.id, m.repository_id, r.org_id, m.direction, m.url, m.username, m.password_encrypted, m.keep, m.last_ok_at,
          m.failures, m.created_by;

-- name: SetMirrorRemote :exec
UPDATE repository_mirrors
SET remote_head = sqlc.narg(remote_head), remote_branches = sqlc.arg(remote_branches), remote_tags = sqlc.arg(remote_tags)
WHERE id = sqlc.arg(id);

-- name: FinishMirrorOK :exec
-- A pull mirror is due again after its interval; a push mirror only when a
-- push marked it during the sync.
UPDATE repository_mirrors
SET syncing_since = NULL, last_sync_at = NOW(), last_ok_at = NOW(), failures = 0, last_error = NULL,
    last_moved = sqlc.arg(moved),
    next_sync_at = CASE WHEN direction = 'pull' THEN NOW() + make_interval(secs => sqlc.arg(pull_every)::float8) ELSE next_sync_at END
WHERE id = sqlc.arg(id);

-- name: FinishMirrorFailed :one
-- An import that never succeeded waits for Sync now; otherwise the mirror
-- is due again at retry_at, or sooner if a push marked it meanwhile.
UPDATE repository_mirrors
SET syncing_since = NULL, last_sync_at = NOW(), failures = failures + 1, last_error = sqlc.arg(last_error),
    next_sync_at = CASE WHEN last_ok_at IS NULL AND direction = 'pull' THEN NULL
                        ELSE LEAST(COALESCE(next_sync_at, sqlc.arg(retry_at)), sqlc.arg(retry_at)) END
WHERE id = sqlc.arg(id)
RETURNING failures;

-- name: DeleteImportedOnce :exec
-- An import without keep, once it succeeded: its row and credentials go.
DELETE FROM repository_mirrors WHERE id = sqlc.arg(id) AND direction = 'pull' AND NOT keep;

-- name: ResetSyncingMirrors :exec
-- At start: syncs a stopped backend left marked running run again.
UPDATE repository_mirrors SET syncing_since = NULL, next_sync_at = COALESCE(next_sync_at, NOW())
WHERE syncing_since IS NOT NULL;

-- name: ListFailingMirrors :many
-- For the mirror_failing alert: every mirror, with its failures in a row.
SELECT m.id, m.failures, COALESCE(m.last_error, '')::text AS last_error
FROM repository_mirrors m;

-- name: GetMirrorAlertContext :one
SELECT r.org_id, (o.slug::text || '/' || r.name)::text AS full_name, m.direction, m.url
FROM repository_mirrors m
JOIN repositories r ON r.id = m.repository_id
JOIN organizations o ON o.id = r.org_id
WHERE m.id = sqlc.arg(id);

-- name: ListRepositoryWebhooks :many
SELECT w.id, w.repository_id, w.url, w.events, w.active, w.created_at, w.created_by, COALESCE(cu.name, '')::text AS created_by_name,
       w.secret_rotated_at, w.secret_rotated_by, COALESCE(ru.name, '')::text AS secret_rotated_by_name
FROM repository_webhooks w
LEFT JOIN users cu ON cu.id = w.created_by
LEFT JOIN users ru ON ru.id = w.secret_rotated_by
WHERE w.repository_id = sqlc.arg(repository_id)
ORDER BY w.created_at, w.id;

-- name: CountRepositoryWebhooks :one
SELECT count(*) FROM repository_webhooks WHERE repository_id = sqlc.arg(repository_id);

-- name: InsertRepositoryWebhook :one
INSERT INTO repository_webhooks (id, repository_id, url, events, active, secret_encrypted, created_by, secret_rotated_by)
VALUES (sqlc.arg(id), sqlc.arg(repository_id), sqlc.arg(url), sqlc.arg(events)::text[], sqlc.arg(active), sqlc.arg(secret_encrypted),
        sqlc.narg(created_by), sqlc.narg(created_by))
RETURNING id;

-- name: UpdateRepositoryWebhook :execrows
UPDATE repository_webhooks
SET url = COALESCE(sqlc.narg(url), url), events = COALESCE(sqlc.narg(events)::text[], events),
    active = COALESCE(sqlc.narg(active), active)
WHERE repository_id = sqlc.arg(repository_id) AND id = sqlc.arg(id);

-- name: FailPausedDeliveries :exec
UPDATE webhook_deliveries SET state = 'failed', error = 'the webhook was paused'
WHERE webhook_id = sqlc.arg(webhook_id) AND state = 'pending';

-- name: RotateRepositoryWebhookSecret :execrows
UPDATE repository_webhooks
SET secret_encrypted = sqlc.arg(secret_encrypted), secret_rotated_at = NOW(), secret_rotated_by = sqlc.narg(rotated_by)
WHERE repository_id = sqlc.arg(repository_id) AND id = sqlc.arg(id);

-- name: DeleteRepositoryWebhook :execrows
DELETE FROM repository_webhooks WHERE repository_id = sqlc.arg(repository_id) AND id = sqlc.arg(id);

-- name: InsertPingDelivery :one
INSERT INTO webhook_deliveries (webhook_id, event, actor_id) VALUES (sqlc.arg(webhook_id), 'ping', sqlc.narg(actor_id))
RETURNING id;

-- name: ListWebhookDeliveries :many
-- A webhook's deliveries, newest first, after the cursor (created_at, id)
-- when set.
SELECT d.id, d.event, d.summary, d.state, d.attempts, d.response_status, d.duration_ms, d.error, d.created_at,
       d.delivered_at, d.next_attempt_at, d.actor_id, COALESCE(u.name, '')::text AS actor_name
FROM webhook_deliveries d
LEFT JOIN users u ON u.id = d.actor_id
WHERE d.webhook_id = sqlc.arg(webhook_id)
  AND (sqlc.narg(before_at)::timestamptz IS NULL OR (d.created_at, d.id) < (sqlc.narg(before_at)::timestamptz, sqlc.narg(before_id)::uuid))
ORDER BY d.created_at DESC, d.id DESC
LIMIT sqlc.arg(n);

-- name: LastWebhookDeliveries :many
-- Each of the repository's webhooks' newest delivery.
SELECT DISTINCT ON (d.webhook_id) d.webhook_id, d.id, d.event, d.summary, d.state, d.attempts, d.response_status,
       d.duration_ms, d.error, d.created_at, d.delivered_at, d.next_attempt_at, d.actor_id, COALESCE(u.name, '')::text AS actor_name
FROM webhook_deliveries d
JOIN repository_webhooks w ON w.id = d.webhook_id
LEFT JOIN users u ON u.id = d.actor_id
WHERE w.repository_id = sqlc.arg(repository_id)
ORDER BY d.webhook_id, d.created_at DESC, d.id DESC;

-- name: GetWebhookDelivery :one
SELECT d.id, d.event, d.summary, d.state, d.attempts, d.response_status, d.duration_ms, d.error, d.created_at,
       d.delivered_at, d.next_attempt_at, d.actor_id, COALESCE(u.name, '')::text AS actor_name, d.payload, d.response_body
FROM webhook_deliveries d
JOIN repository_webhooks w ON w.id = d.webhook_id
LEFT JOIN users u ON u.id = d.actor_id
WHERE w.repository_id = sqlc.arg(repository_id) AND d.webhook_id = sqlc.arg(webhook_id) AND d.id = sqlc.arg(id);

-- name: RedeliverWebhookDelivery :execrows
-- The same delivery again, its retries started over; not while it waits.
UPDATE webhook_deliveries
SET state = 'pending', attempts = 0, next_attempt_at = NOW(), error = NULL, actor_id = sqlc.narg(actor_id)
WHERE webhook_id = sqlc.arg(webhook_id) AND id = sqlc.arg(id) AND state <> 'pending';

-- name: ClaimWebhookDeliveries :many
-- Up to n due deliveries of active webhooks, leased for a minute.
UPDATE webhook_deliveries d
SET next_attempt_at = NOW() + interval '1 minute', attempts = d.attempts + 1
FROM (SELECT x.id FROM webhook_deliveries x
      JOIN repository_webhooks wx ON wx.id = x.webhook_id AND wx.active
      WHERE x.state = 'pending' AND x.next_attempt_at <= NOW()
      ORDER BY x.next_attempt_at LIMIT sqlc.arg(n) FOR UPDATE OF x SKIP LOCKED) c,
     repository_webhooks w
WHERE d.id = c.id AND w.id = d.webhook_id
RETURNING d.id, d.webhook_id, w.repository_id, w.url, w.secret_encrypted, d.git_event_id, d.event, d.actor_id,
          d.payload, d.attempts, d.created_at;

-- name: SetDeliveryPayload :exec
UPDATE webhook_deliveries SET summary = sqlc.arg(summary), payload = sqlc.arg(payload) WHERE id = sqlc.arg(id);

-- name: FinishWebhookDelivery :exec
-- state is delivered, failed, or pending again at next_attempt_at.
UPDATE webhook_deliveries
SET state = sqlc.arg(state), next_attempt_at = sqlc.arg(next_attempt_at), response_status = sqlc.narg(response_status),
    response_body = sqlc.narg(response_body), error = sqlc.narg(error), duration_ms = sqlc.narg(duration_ms),
    delivered_at = NOW()
WHERE id = sqlc.arg(id);

-- name: DeleteOldWebhookDeliveries :execrows
DELETE FROM webhook_deliveries WHERE created_at < sqlc.arg(before) AND state <> 'pending';

-- name: GetGitEvent :one
-- One event, with what a webhook's payload says about it.
SELECT e.id, e.repository_id, e.kind, e.action, e.branch, e.before_sha, e.after_sha, e.actor_id,
       COALESCE(u.name, '')::text AS actor_name, e.pull_request_id, e.commits, e.forced, e.created_at
FROM git_events e
LEFT JOIN users u ON u.id = e.actor_id
WHERE e.id = sqlc.arg(id);

-- name: GetPullRequestByID :one
SELECT p.number, p.title, p.state, p.base_branch, p.head_branch, p.head_sha, p.merge_sha, p.author_id,
       COALESCE(u.name, '')::text AS author_name
FROM pull_requests p
LEFT JOIN users u ON u.id = p.author_id
WHERE p.id = sqlc.arg(id);

-- name: GetRepositoryForWebhook :one
SELECT r.id, r.name, r.project_id, o.slug::text AS org_slug
FROM repositories r
JOIN organizations o ON o.id = r.org_id
WHERE r.id = sqlc.arg(id);
