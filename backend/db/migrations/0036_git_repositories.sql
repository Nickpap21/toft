-- Git hosting, session 1 (D-71): repositories, SSH keys, the workspace's slug
-- in repository URLs, and the API key scopes for code.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Everything is added (D-63): a backend from before 0036 runs on it
-- unchanged. Projects it creates have no repository until the next backend
-- of this release starts (which makes the missing ones), and a project it
-- deletes takes its repository rows with it, leaving the bare repository
-- on the paas-git volume unreferenced.

ALTER TABLE organizations ADD COLUMN IF NOT EXISTS slug TEXT
    CHECK (slug ~ '^[a-z0-9]([a-z0-9-]{0,37}[a-z0-9])?$');
CREATE UNIQUE INDEX IF NOT EXISTS uq_organizations_slug ON organizations (slug);

ALTER TABLE projects ADD CONSTRAINT projects_id_org_id_key UNIQUE (id, org_id);

CREATE TABLE IF NOT EXISTS repositories (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id  UUID        NOT NULL,
    name        TEXT        NOT NULL CHECK (name ~ '^[a-z0-9]([a-z0-9._-]{0,98}[a-z0-9])?$' AND name !~ '\.git$'),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, name),
    FOREIGN KEY (project_id, org_id) REFERENCES projects (id, org_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_repositories_project ON repositories (project_id);

CREATE TABLE IF NOT EXISTS ssh_keys (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title        TEXT        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 100),
    public_key   TEXT        NOT NULL CHECK (char_length(public_key) <= 16384),
    fingerprint  TEXT        NOT NULL UNIQUE,
    key_type     TEXT        NOT NULL,
    last_used_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ssh_keys_user ON ssh_keys (user_id);

-- The CHECK only widens (D-63).
ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_scope_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scope_check CHECK (
    (project_id IS NULL AND cardinality(scopes) = 0)
    OR (project_id IS NOT NULL AND cardinality(scopes) > 0
        AND scopes <@ ARRAY['deploy', 'variables', 'code_read', 'code_write']::TEXT[])
);

-- Rollback (reversible; the repositories on the paas-git volume stay, and
-- API keys with a code scope must go first):
--   DELETE FROM api_keys WHERE scopes && ARRAY['code_read', 'code_write']::TEXT[];
--   ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_scope_check;
--   ALTER TABLE api_keys ADD CONSTRAINT api_keys_scope_check CHECK (
--       (project_id IS NULL AND cardinality(scopes) = 0)
--       OR (project_id IS NOT NULL AND cardinality(scopes) > 0
--           AND scopes <@ ARRAY['deploy', 'variables']::TEXT[]));
--   DROP TABLE IF EXISTS ssh_keys;
--   DROP TABLE IF EXISTS repositories;
--   ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_id_org_id_key;
--   DROP INDEX IF EXISTS uq_organizations_slug;
--   ALTER TABLE organizations DROP COLUMN IF EXISTS slug;
