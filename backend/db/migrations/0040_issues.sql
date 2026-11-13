-- Issues v1 (D-81, stage 4 session 1): issues with their labels, comments,
-- history and links to branches and pull requests, saved views, each
-- project's issue key and numbers, the events outbox's issues mark, ⌘K's
-- search on pull requests, and the issues_read and issues_write API key
-- scopes.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Everything is added or widened (D-63): v0.6.1 runs on it. It ignores the
-- new tables and columns; the events it writes keep issues_at NULL, so the
-- issues module applies them once v0.7.0 runs again.

ALTER TABLE projects ADD COLUMN IF NOT EXISTS issue_key TEXT CHECK (issue_key ~ '^[A-Z][A-Z0-9]{1,4}$');
ALTER TABLE projects ADD COLUMN IF NOT EXISTS next_issue_number INTEGER NOT NULL DEFAULT 1 CHECK (next_issue_number > 0);
CREATE UNIQUE INDEX IF NOT EXISTS uq_projects_issue_key ON projects (org_id, issue_key) WHERE issue_key IS NOT NULL;

ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_scope_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scope_check CHECK (
    (project_id IS NULL AND cardinality(scopes) = 0)
    OR (project_id IS NOT NULL AND cardinality(scopes) > 0
        AND scopes <@ ARRAY['deploy', 'variables', 'code_read', 'code_write', 'checks', 'issues_read', 'issues_write']::TEXT[])
);

ALTER TABLE git_events ADD COLUMN IF NOT EXISTS issues_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_git_events_issues ON git_events (id) WHERE issues_at IS NULL;

ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS search TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple', title)) STORED;
CREATE INDEX IF NOT EXISTS idx_pull_requests_search ON pull_requests USING GIN (search);

-- A project's issues, numbered from projects.next_issue_number. Events from
-- the git_events outbox and deploys going live move status forward
-- (issue_events says how); people move it any way. search serves ⌘K.
CREATE TABLE IF NOT EXISTS issues (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id        UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    number            INTEGER     NOT NULL CHECK (number > 0),
    title             TEXT        NOT NULL CHECK (char_length(title) >= 1 AND char_length(title) <= 256 AND title !~ '[[:cntrl:]]'),
    description       TEXT        NOT NULL DEFAULT '' CHECK (octet_length(description) <= 65536),
    status            TEXT        NOT NULL DEFAULT 'backlog' CHECK (status IN ('backlog', 'todo', 'in_progress', 'in_review', 'shipped', 'cancelled')),
    priority          TEXT        NOT NULL DEFAULT 'none' CHECK (priority IN ('none', 'low', 'medium', 'high', 'urgent')),
    assignee_id       UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_by        UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status_changed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    search            TSVECTOR    GENERATED ALWAYS AS (to_tsvector('simple', title)) STORED,
    UNIQUE (project_id, number)
);
CREATE INDEX IF NOT EXISTS idx_issues_project_status ON issues (project_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_issues_assignee ON issues (assignee_id, status) WHERE assignee_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_issues_search ON issues USING GIN (search);

-- An issue's labels: lower case, free text per project (D-81). A label
-- named after one of the project's services links the issue to it.
CREATE TABLE IF NOT EXISTS issue_labels (
    issue_id UUID NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    name     TEXT NOT NULL CHECK (char_length(name) >= 1 AND char_length(name) <= 40 AND name = lower(btrim(name)) AND name !~ '[[:cntrl:],]'),
    PRIMARY KEY (issue_id, name)
);
CREATE INDEX IF NOT EXISTS idx_issue_labels_name ON issue_labels (name, issue_id);

CREATE TABLE IF NOT EXISTS issue_comments (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id   UUID        NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    author_id  UUID        REFERENCES users(id) ON DELETE SET NULL,
    body       TEXT        NOT NULL CHECK (char_length(body) >= 1 AND octet_length(body) <= 65536),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    edited_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_issue_comments_issue ON issue_comments (issue_id, created_at, id);

-- An issue's history. automatic marks what an event did (git_event_id, or
-- deployment_id for a deploy going live), with the git event's actor; one
-- row per issue, git event and kind, so applying an event twice adds
-- nothing. from_value and to_value are the field's values (a status, a
-- priority, an assignee's id, labels joined by commas, a title).
CREATE TABLE IF NOT EXISTS issue_events (
    id              BIGSERIAL   PRIMARY KEY,
    issue_id        UUID        NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    kind            TEXT        NOT NULL CHECK (kind IN ('opened', 'status', 'assigned', 'priority', 'labels', 'title', 'linked', 'merged')),
    actor_id        UUID        REFERENCES users(id) ON DELETE SET NULL,
    automatic       BOOLEAN     NOT NULL DEFAULT false,
    from_value      TEXT        CHECK (char_length(from_value) <= 300),
    to_value        TEXT        CHECK (char_length(to_value) <= 300),
    git_event_id    BIGINT      REFERENCES git_events(id) ON DELETE SET NULL,
    pull_request_id UUID        REFERENCES pull_requests(id) ON DELETE SET NULL,
    deployment_id   UUID        REFERENCES deployments(id) ON DELETE SET NULL,
    branch          TEXT        CHECK (char_length(branch) BETWEEN 1 AND 255),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_issue_events_issue ON issue_events (issue_id, created_at, id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_issue_events_git_event ON issue_events (issue_id, git_event_id, kind) WHERE git_event_id IS NOT NULL;

-- The branches and pull requests that name an issue's key (D-81), found by
-- the issues module in the git_events outbox: a branch by its name, a pull
-- request by its head branch, title or description.
CREATE TABLE IF NOT EXISTS issue_links (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id        UUID        NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    repository_id   UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    pull_request_id UUID        REFERENCES pull_requests(id) ON DELETE CASCADE,
    branch          TEXT        CHECK (char_length(branch) BETWEEN 1 AND 255),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((pull_request_id IS NULL) <> (branch IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_issue_links_pull ON issue_links (issue_id, pull_request_id) WHERE pull_request_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_issue_links_branch ON issue_links (issue_id, repository_id, branch) WHERE branch IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_issue_links_pull ON issue_links (pull_request_id) WHERE pull_request_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_issue_links_branch ON issue_links (repository_id, branch) WHERE branch IS NOT NULL;

-- Saved views of a project's issues, shared with the project (D-81): the
-- list's filters, grouping and order as the panel's URL holds them.
CREATE TABLE IF NOT EXISTS issue_views (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_id   UUID        REFERENCES users(id) ON DELETE SET NULL,
    name       TEXT        NOT NULL CHECK (char_length(name) >= 1 AND char_length(name) <= 60 AND name !~ '[[:cntrl:]]'),
    filters    JSONB       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(filters) = 'object' AND octet_length(filters::text) <= 4096),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (project_id, name)
);

-- Rollback (reversible; it loses every issue with its comments, history,
-- links and views, and the issue keys). The scope CHECK narrows only once
-- no key holds the new scopes:
--   DROP TABLE IF EXISTS issue_views;
--   DROP TABLE IF EXISTS issue_links;
--   DROP TABLE IF EXISTS issue_events;
--   DROP TABLE IF EXISTS issue_comments;
--   DROP TABLE IF EXISTS issue_labels;
--   DROP TABLE IF EXISTS issues;
--   DROP INDEX IF EXISTS idx_pull_requests_search;
--   ALTER TABLE pull_requests DROP COLUMN IF EXISTS search;
--   DROP INDEX IF EXISTS idx_git_events_issues;
--   ALTER TABLE git_events DROP COLUMN IF EXISTS issues_at;
--   DELETE FROM api_keys WHERE scopes && ARRAY['issues_read', 'issues_write']::TEXT[];
--   ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_scope_check;
--   ALTER TABLE api_keys ADD CONSTRAINT api_keys_scope_check CHECK (
--       (project_id IS NULL AND cardinality(scopes) = 0)
--       OR (project_id IS NOT NULL AND cardinality(scopes) > 0
--           AND scopes <@ ARRAY['deploy', 'variables', 'code_read', 'code_write', 'checks']::TEXT[]));
--   DROP INDEX IF EXISTS uq_projects_issue_key;
--   ALTER TABLE projects DROP COLUMN IF EXISTS next_issue_number;
--   ALTER TABLE projects DROP COLUMN IF EXISTS issue_key;
