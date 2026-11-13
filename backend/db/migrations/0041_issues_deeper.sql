-- Issues, deeper (D-83, stage 4 session 1b): issue types per workspace with
-- the types a project hides, each issue's type and parent, relations
-- between issues, a project's former keys, the digits issue numbers show
-- (workspace and project), a project's default type, project keys up to 10
-- characters, and the history's new kinds.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change. premigrate.sql
-- adds organizations.issue_key_digits first, so workspaces from before
-- keep 0 digits and new ones get 3.
--
-- Everything is added or widened (D-63): v0.7.0 runs on it. It ignores the
-- types, parents and relations, shows keys unpadded, and lists issues
-- whatever their type.

ALTER TABLE organizations ADD COLUMN IF NOT EXISTS issue_key_digits SMALLINT NOT NULL DEFAULT 3 CHECK (issue_key_digits BETWEEN 0 AND 6);
ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_issue_key_check;
ALTER TABLE projects ADD CONSTRAINT projects_issue_key_check CHECK (issue_key ~ '^[A-Z][A-Z0-9]{1,9}$');
ALTER TABLE projects ADD COLUMN IF NOT EXISTS issue_key_digits SMALLINT CHECK (issue_key_digits BETWEEN 0 AND 6);
ALTER TABLE projects ADD COLUMN IF NOT EXISTS default_issue_type_id UUID;
ALTER TABLE issues ADD COLUMN IF NOT EXISTS type_id UUID;
ALTER TABLE issues ADD COLUMN IF NOT EXISTS parent_id UUID;
ALTER TABLE issues ADD CONSTRAINT issues_parent_not_self CHECK (parent_id <> id);
ALTER TABLE issue_events DROP CONSTRAINT IF EXISTS issue_events_kind_check;
ALTER TABLE issue_events ADD CONSTRAINT issue_events_kind_check CHECK (kind IN ('opened', 'status', 'assigned', 'priority', 'labels', 'title', 'linked', 'merged',
    'type', 'parent', 'related', 'unrelated', 'unblocked', 'cloned'));

CREATE TABLE IF NOT EXISTS issue_types (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name        TEXT        NOT NULL CHECK (char_length(name) >= 1 AND char_length(name) <= 40 AND name !~ '[[:cntrl:]]'),
    kind        TEXT        NOT NULL CHECK (kind IN ('standard', 'container', 'subtask')),
    icon        TEXT        NOT NULL CHECK (icon IN ('task', 'bug', 'feature', 'epic', 'release', 'subtask', 'story', 'chore', 'incident', 'spike')),
    color       TEXT        NOT NULL CHECK (color IN ('gray', 'blue', 'red', 'green', 'violet', 'amber', 'teal')),
    position    INTEGER     NOT NULL DEFAULT 0,
    archived_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, name)
);
ALTER TABLE issues ADD CONSTRAINT fk_issues_type FOREIGN KEY (type_id) REFERENCES issue_types(id);
ALTER TABLE issues ADD CONSTRAINT fk_issues_parent FOREIGN KEY (parent_id) REFERENCES issues(id) ON DELETE SET NULL;
ALTER TABLE projects ADD CONSTRAINT fk_projects_default_issue_type FOREIGN KEY (default_issue_type_id) REFERENCES issue_types(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_issues_parent ON issues (parent_id) WHERE parent_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS project_hidden_issue_types (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    type_id    UUID NOT NULL REFERENCES issue_types(id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, type_id)
);

CREATE TABLE IF NOT EXISTS issue_relations (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    from_issue_id UUID        NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    to_issue_id   UUID        NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    kind          TEXT        NOT NULL CHECK (kind IN ('blocks', 'relates', 'duplicates', 'clones')),
    created_by    UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (from_issue_id <> to_issue_id),
    UNIQUE (from_issue_id, to_issue_id, kind)
);
CREATE INDEX IF NOT EXISTS idx_issue_relations_to ON issue_relations (to_issue_id, kind);
CREATE UNIQUE INDEX IF NOT EXISTS uq_issue_relations_relates ON issue_relations (LEAST(from_issue_id, to_issue_id), GREATEST(from_issue_id, to_issue_id)) WHERE kind = 'relates';

CREATE TABLE IF NOT EXISTS issue_key_aliases (
    org_id     UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    key        TEXT        NOT NULL CHECK (key ~ '^[A-Z][A-Z0-9]{1,9}$'),
    project_id UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, key)
);
CREATE INDEX IF NOT EXISTS idx_issue_key_aliases_project ON issue_key_aliases (project_id);

-- Rollback (reversible; it loses every type, relation and alias, every
-- issue's type and parent, and the digits). The CHECKs narrow only once no
-- row breaks them:
--   DROP TABLE IF EXISTS issue_key_aliases;
--   DROP TABLE IF EXISTS issue_relations;
--   DROP TABLE IF EXISTS project_hidden_issue_types;
--   ALTER TABLE projects DROP CONSTRAINT IF EXISTS fk_projects_default_issue_type;
--   ALTER TABLE issues DROP CONSTRAINT IF EXISTS fk_issues_parent;
--   ALTER TABLE issues DROP CONSTRAINT IF EXISTS fk_issues_type;
--   ALTER TABLE issues DROP CONSTRAINT IF EXISTS issues_parent_not_self;
--   DROP INDEX IF EXISTS idx_issues_parent;
--   ALTER TABLE issues DROP COLUMN IF EXISTS parent_id;
--   ALTER TABLE issues DROP COLUMN IF EXISTS type_id;
--   DROP TABLE IF EXISTS issue_types;
--   DELETE FROM issue_events WHERE kind IN ('type', 'parent', 'related', 'unrelated', 'unblocked', 'cloned');
--   ALTER TABLE issue_events DROP CONSTRAINT IF EXISTS issue_events_kind_check;
--   ALTER TABLE issue_events ADD CONSTRAINT issue_events_kind_check CHECK (kind IN ('opened', 'status', 'assigned', 'priority', 'labels', 'title', 'linked', 'merged'));
--   ALTER TABLE projects DROP COLUMN IF EXISTS default_issue_type_id;
--   ALTER TABLE projects DROP COLUMN IF EXISTS issue_key_digits;
--   UPDATE projects SET issue_key = NULL WHERE char_length(issue_key) > 5;
--   ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_issue_key_check;
--   ALTER TABLE projects ADD CONSTRAINT projects_issue_key_check CHECK (issue_key ~ '^[A-Z][A-Z0-9]{1,4}$');
--   ALTER TABLE organizations DROP COLUMN IF EXISTS issue_key_digits;
