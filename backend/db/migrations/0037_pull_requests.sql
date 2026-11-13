-- Git hosting, session 3 (D-75): several repositories per project, pull
-- requests, and protecting a repository's default branch.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Everything is added (D-63): v0.3.1 runs on it unchanged. It reads only a
-- project's first repository, ignores the new columns and tables, and a
-- repository it serves pushes to is not protected by it (its git hooks are
-- off).

ALTER TABLE repositories ADD COLUMN IF NOT EXISTS protect_default_branch BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE repositories ADD COLUMN IF NOT EXISTS next_pull_number INTEGER NOT NULL DEFAULT 1 CHECK (next_pull_number > 0);

CREATE TABLE IF NOT EXISTS pull_requests (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id  UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    number         INTEGER     NOT NULL CHECK (number > 0),
    title          TEXT        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 256),
    body           TEXT        NOT NULL DEFAULT '' CHECK (octet_length(body) <= 65536),
    author_id      UUID        REFERENCES users(id) ON DELETE SET NULL,
    base_branch    TEXT        NOT NULL CHECK (char_length(base_branch) BETWEEN 1 AND 255),
    head_branch    TEXT        NOT NULL CHECK (char_length(head_branch) BETWEEN 1 AND 255),
    head_sha       TEXT        NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    state          TEXT        NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed', 'merged')),
    merge_method   TEXT        CHECK (merge_method IN ('merge', 'squash', 'fast_forward')),
    merge_sha      TEXT        CHECK (merge_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    merge_base_sha TEXT        CHECK (merge_base_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    merged_by      UUID        REFERENCES users(id) ON DELETE SET NULL,
    merged_at      TIMESTAMPTZ,
    closed_at      TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (repository_id, number),
    CHECK (base_branch <> head_branch),
    CHECK ((state = 'merged') = (merge_sha IS NOT NULL AND merge_method IS NOT NULL AND merged_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_pull_requests_open_pair ON pull_requests (repository_id, base_branch, head_branch) WHERE state = 'open';
CREATE INDEX IF NOT EXISTS idx_pull_requests_repository_state ON pull_requests (repository_id, state, number DESC);

CREATE TABLE IF NOT EXISTS pull_request_comments (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    pull_request_id  UUID        NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    author_id        UUID        REFERENCES users(id) ON DELETE SET NULL,
    body             TEXT        NOT NULL CHECK (char_length(body) >= 1 AND octet_length(body) <= 65536),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_pull_request_comments_pull ON pull_request_comments (pull_request_id, created_at);

CREATE TABLE IF NOT EXISTS pull_request_events (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    pull_request_id  UUID        NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    actor_id         UUID        REFERENCES users(id) ON DELETE SET NULL,
    kind             TEXT        NOT NULL CHECK (kind IN ('pushed', 'closed', 'reopened', 'merged', 'head_deleted', 'title_changed')),
    data             JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_pull_request_events_pull ON pull_request_events (pull_request_id, created_at);

-- Rollback (reversible; it loses every pull request, its comments and
-- events, and each repository's protection; the refs/pull/<n>/head refs on
-- the paas-git volume stay, hidden from clients):
--   DROP TABLE IF EXISTS pull_request_events;
--   DROP TABLE IF EXISTS pull_request_comments;
--   DROP TABLE IF EXISTS pull_requests;
--   ALTER TABLE repositories DROP COLUMN IF EXISTS next_pull_number;
--   ALTER TABLE repositories DROP COLUMN IF EXISTS protect_default_branch;
