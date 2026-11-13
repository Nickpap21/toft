-- The inbox (D-88, stage 4 session 3): a person's updates (notifications),
-- the Needs you items they marked done, the notifier's place in each of
-- its sources, the failed deploy an issue was opened from, a project's
-- choice to open issues for failed deploys, and the issue history's
-- deploy_failed kind.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Everything is added or widened (D-63): v0.9.1 runs on it and ignores it.

ALTER TABLE projects ADD COLUMN IF NOT EXISTS failed_deploy_issues BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE issues ADD COLUMN IF NOT EXISTS from_deployment_id UUID;
ALTER TABLE issues ADD CONSTRAINT fk_issues_from_deployment FOREIGN KEY (from_deployment_id) REFERENCES deployments(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_issues_from_deployment ON issues (from_deployment_id) WHERE from_deployment_id IS NOT NULL;
ALTER TABLE issue_events DROP CONSTRAINT IF EXISTS issue_events_kind_check;
ALTER TABLE issue_events ADD CONSTRAINT issue_events_kind_check CHECK (kind IN ('opened', 'status', 'assigned', 'priority', 'labels', 'title', 'linked', 'merged',
    'type', 'parent', 'related', 'unrelated', 'unblocked', 'cloned', 'deploy_failed'));

-- A person's updates: what happened on work they opened, are assigned,
-- commented on or were mentioned in, written by the inbox's notifier from
-- the issues', pull requests' and docs' own records (never for the person's
-- own actions). Kept 90 days. Needs you isn't stored: it is computed when
-- read.
CREATE TABLE IF NOT EXISTS notifications (
    id              BIGSERIAL   PRIMARY KEY,
    user_id         UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id          UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    kind            TEXT        NOT NULL CHECK (kind IN ('issue_status', 'issue_assigned', 'issue_comment', 'mention',
                                                         'pull_merged', 'pull_closed', 'pull_comment', 'page_edited')),
    actor_id        UUID        REFERENCES users(id) ON DELETE SET NULL,
    project_id      UUID        REFERENCES projects(id) ON DELETE CASCADE,
    issue_id        UUID        REFERENCES issues(id) ON DELETE CASCADE,
    pull_request_id UUID        REFERENCES pull_requests(id) ON DELETE CASCADE,
    page_id         UUID        REFERENCES doc_pages(id) ON DELETE CASCADE,
    detail          JSONB       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(detail) = 'object' AND octet_length(detail::text) <= 4096),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    read_at         TIMESTAMPTZ,
    done_at         TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_notifications_user ON notifications (user_id, org_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_notifications_unread ON notifications (user_id, org_id) WHERE read_at IS NULL AND done_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_notifications_created ON notifications (created_at);

-- The Needs you items a person marked done (D-88). An item's key names its
-- version (a deploy, an alert, a page's last verification), so a new one
-- comes back. Kept 30 days.
CREATE TABLE IF NOT EXISTS inbox_done (
    user_id  UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_key TEXT        NOT NULL CHECK (char_length(item_key) >= 1 AND char_length(item_key) <= 200),
    done_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, item_key)
);

-- How far the notifier has read each of its sources (D-88): the last
-- row's time and id, so a restart neither repeats nor loses an update.
CREATE TABLE IF NOT EXISTS inbox_cursors (
    source     TEXT        PRIMARY KEY CHECK (source IN ('issue_events', 'issue_comments', 'pull_requests',
                                                         'pull_request_events', 'pull_request_comments', 'doc_page_versions')),
    at         TIMESTAMPTZ NOT NULL,
    last_id    TEXT        NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Rollback (reversible; it loses every update, every Needs you item marked
-- done, which failed deploy an issue came from, and the projects' choice;
-- the history's deploy_failed rows go before the check narrows):
--   DROP TABLE IF EXISTS inbox_cursors;
--   DROP TABLE IF EXISTS inbox_done;
--   DROP TABLE IF EXISTS notifications;
--   DELETE FROM issue_events WHERE kind = 'deploy_failed';
--   ALTER TABLE issue_events DROP CONSTRAINT IF EXISTS issue_events_kind_check;
--   ALTER TABLE issue_events ADD CONSTRAINT issue_events_kind_check CHECK (kind IN ('opened', 'status', 'assigned', 'priority', 'labels', 'title', 'linked', 'merged', 'type', 'parent', 'related', 'unrelated', 'unblocked', 'cloned'));
--   DROP INDEX IF EXISTS idx_issues_from_deployment;
--   ALTER TABLE issues DROP CONSTRAINT IF EXISTS fk_issues_from_deployment;
--   ALTER TABLE issues DROP COLUMN IF EXISTS from_deployment_id;
--   ALTER TABLE projects DROP COLUMN IF EXISTS failed_deploy_issues;
