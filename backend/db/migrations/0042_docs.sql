-- Docs v1 (D-81, D-86, stage 4 session 2): a project's pages in a tree,
-- every save a version, the services a page's blocks name with what the
-- page last saw of each (for the out-of-date warning), the issues, pull
-- requests and pages a page names, and the docs_read and docs_write API key
-- scopes.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Everything is added or widened (D-63): v0.8.1 runs on it and ignores it.

ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_scope_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scope_check CHECK (
    (project_id IS NULL AND cardinality(scopes) = 0)
    OR (project_id IS NOT NULL AND cardinality(scopes) > 0
        AND scopes <@ ARRAY['deploy', 'variables', 'code_read', 'code_write', 'checks', 'issues_read', 'issues_write',
                            'docs_read', 'docs_write']::TEXT[])
);

-- A project's pages, in a tree by parent_id and position: markdown with
-- Toft's blocks fenced in it (toft-service, toft-run, toft-issue,
-- toft-pull). title and body are the current version's, which is version;
-- verified_at is the last Mark as verified. A deleted page stays 30 days
-- (deleted_at) and comes back with its versions. search serves ⌘K and the
-- tree's search.
CREATE TABLE IF NOT EXISTS doc_pages (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    parent_id   UUID        REFERENCES doc_pages(id) ON DELETE CASCADE,
    position    INTEGER     NOT NULL DEFAULT 0,
    title       TEXT        NOT NULL CHECK (char_length(title) >= 1 AND char_length(title) <= 200 AND title !~ '[[:cntrl:]]'),
    body        TEXT        NOT NULL DEFAULT '' CHECK (octet_length(body) <= 262144),
    version     INTEGER     NOT NULL DEFAULT 1 CHECK (version > 0),
    created_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    updated_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    verified_at TIMESTAMPTZ,
    verified_by UUID        REFERENCES users(id) ON DELETE SET NULL,
    deleted_at  TIMESTAMPTZ,
    search      TSVECTOR    GENERATED ALWAYS AS (setweight(to_tsvector('simple', title), 'A') || setweight(to_tsvector('simple', body), 'B')) STORED,
    CONSTRAINT doc_pages_parent_not_self CHECK (parent_id <> id)
);
CREATE INDEX IF NOT EXISTS idx_doc_pages_project ON doc_pages (project_id, parent_id, position);
CREATE INDEX IF NOT EXISTS idx_doc_pages_search ON doc_pages USING GIN (search);
CREATE INDEX IF NOT EXISTS idx_doc_pages_deleted ON doc_pages (deleted_at) WHERE deleted_at IS NOT NULL;

-- Every version of a page (D-86), the current one included.
CREATE TABLE IF NOT EXISTS doc_page_versions (
    page_id    UUID        NOT NULL REFERENCES doc_pages(id) ON DELETE CASCADE,
    version    INTEGER     NOT NULL CHECK (version > 0),
    title      TEXT        NOT NULL CHECK (char_length(title) >= 1 AND char_length(title) <= 200 AND title !~ '[[:cntrl:]]'),
    body       TEXT        NOT NULL CHECK (octet_length(body) <= 262144),
    author_id  UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (page_id, version)
);

-- The services a page's blocks name (D-86), each with what the page last
-- saw of it: its state when the page was verified, or when a save first
-- named it. The page is out of date once the service's state differs
-- (internal/doc says what counts).
CREATE TABLE IF NOT EXISTS doc_page_services (
    page_id    UUID        NOT NULL REFERENCES doc_pages(id) ON DELETE CASCADE,
    service_id UUID        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    seen       JSONB       NOT NULL CHECK (jsonb_typeof(seen) = 'object' AND octet_length(seen::text) <= 65536),
    seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (page_id, service_id)
);
CREATE INDEX IF NOT EXISTS idx_doc_page_services_service ON doc_page_services (service_id);

-- The issues, pull requests and pages a page's current version names
-- (D-86): their Referenced in docs, and a page's Referenced by.
CREATE TABLE IF NOT EXISTS doc_refs (
    page_id         UUID NOT NULL REFERENCES doc_pages(id) ON DELETE CASCADE,
    issue_id        UUID REFERENCES issues(id) ON DELETE CASCADE,
    pull_request_id UUID REFERENCES pull_requests(id) ON DELETE CASCADE,
    to_page_id      UUID REFERENCES doc_pages(id) ON DELETE CASCADE,
    CONSTRAINT doc_refs_one_target CHECK (num_nonnulls(issue_id, pull_request_id, to_page_id) = 1)
);
CREATE INDEX IF NOT EXISTS idx_doc_refs_page ON doc_refs (page_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_doc_refs_issue ON doc_refs (issue_id, page_id) WHERE issue_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_doc_refs_pull ON doc_refs (pull_request_id, page_id) WHERE pull_request_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_doc_refs_page ON doc_refs (to_page_id, page_id) WHERE to_page_id IS NOT NULL;

-- Rollback (reversible; it loses every page, version and link, and the
-- keys holding a docs scope lose it, or go with it if it was their only
-- one):
--   DROP TABLE IF EXISTS doc_refs;
--   DROP TABLE IF EXISTS doc_page_services;
--   DROP TABLE IF EXISTS doc_page_versions;
--   DROP TABLE IF EXISTS doc_pages;
--   UPDATE api_keys SET scopes = array_remove(array_remove(scopes, 'docs_read'), 'docs_write') WHERE project_id IS NOT NULL;
--   DELETE FROM api_keys WHERE project_id IS NOT NULL AND cardinality(scopes) = 0;
--   ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_scope_check;
--   ALTER TABLE api_keys ADD CONSTRAINT api_keys_scope_check CHECK (
--       (project_id IS NULL AND cardinality(scopes) = 0)
--       OR (project_id IS NOT NULL AND cardinality(scopes) > 0
--           AND scopes <@ ARRAY['deploy', 'variables', 'code_read', 'code_write', 'checks', 'issues_read', 'issues_write']::TEXT[])
--   );
