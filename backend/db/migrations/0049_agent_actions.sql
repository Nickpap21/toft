-- 0049: the remaining agent actions and the policy's scope (stage 6 session 2,
-- D-101, D-102). Additive. schema.sql is the source of truth; Atlas applies
-- it. This file records the change and how to undo it.

-- Code and issues touch no environment: one cell each, class 'any'.
ALTER TABLE agent_policies DROP CONSTRAINT IF EXISTS agent_policies_env_class_check;
ALTER TABLE agent_policies ADD CONSTRAINT agent_policies_env_class_check
    CHECK (env_class IN ('preview', 'other', 'protected', 'any'));

-- A project's own rules: a cell here replaces the workspace's for that
-- project. Its own table, since v0.15.0 reads every agent_policies row of a
-- workspace as the workspace's.
CREATE TABLE IF NOT EXISTS agent_project_policies (
    project_id UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    action     TEXT        NOT NULL CHECK (action IN ('deploy', 'code', 'issues', 'variables', 'secrets', 'runbooks')),
    env_class  TEXT        NOT NULL CHECK (env_class IN ('preview', 'other', 'protected', 'any')),
    decision   TEXT        NOT NULL CHECK (decision IN ('allow', 'ask', 'never')),
    updated_by UUID        REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, action, env_class)
);

-- A request's project (for its project's rules), and set_secret's value,
-- encrypted with ENCRYPTION_KEY, until it is run, turned down or expired.
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES projects(id) ON DELETE CASCADE;
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS sealed TEXT;
ALTER TABLE agent_approvals ADD CONSTRAINT agent_approvals_sealed_check CHECK (sealed IS NULL OR action = 'secrets');
CREATE INDEX IF NOT EXISTS idx_agent_approvals_sealed ON agent_approvals (expires_at) WHERE sealed IS NOT NULL;

-- Branches an agent made, with the push credential for them alone.
CREATE TABLE IF NOT EXISTS agent_branches (
    repository_id    UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    name             TEXT        NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    connection_id    UUID        NOT NULL REFERENCES agent_connections(id) ON DELETE CASCADE,
    user_id          UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash       TEXT        NOT NULL UNIQUE,
    token_expires_at TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (repository_id, name)
);
CREATE INDEX IF NOT EXISTS idx_agent_branches_connection ON agent_branches (connection_id);

-- What an agent made says so.
ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS agent_connection_id UUID;
ALTER TABLE pull_request_comments ADD COLUMN IF NOT EXISTS agent_connection_id UUID;
ALTER TABLE git_events ADD COLUMN IF NOT EXISTS agent_connection_id UUID;
ALTER TABLE issues ADD COLUMN IF NOT EXISTS agent_connection_id UUID;
ALTER TABLE issue_comments ADD COLUMN IF NOT EXISTS agent_connection_id UUID;
ALTER TABLE pull_requests ADD CONSTRAINT fk_pull_requests_agent FOREIGN KEY (agent_connection_id) REFERENCES agent_connections(id) ON DELETE SET NULL;
ALTER TABLE pull_request_comments ADD CONSTRAINT fk_pull_request_comments_agent FOREIGN KEY (agent_connection_id) REFERENCES agent_connections(id) ON DELETE SET NULL;
ALTER TABLE git_events ADD CONSTRAINT fk_git_events_agent FOREIGN KEY (agent_connection_id) REFERENCES agent_connections(id) ON DELETE SET NULL;
ALTER TABLE issues ADD CONSTRAINT fk_issues_agent FOREIGN KEY (agent_connection_id) REFERENCES agent_connections(id) ON DELETE SET NULL;
ALTER TABLE issue_comments ADD CONSTRAINT fk_issue_comments_agent FOREIGN KEY (agent_connection_id) REFERENCES agent_connections(id) ON DELETE SET NULL;

-- Rollback (reversible; it loses every project's own agent rules, the
-- agents' branches' push credentials (the branches stay in git), which pull
-- requests, comments, pushes and issues an agent made, and any set_secret
-- value still waiting; delete code and issues rows first, since the old
-- CHECK refuses 'any'):
--   ALTER TABLE issue_comments DROP CONSTRAINT IF EXISTS fk_issue_comments_agent, DROP COLUMN IF EXISTS agent_connection_id;
--   ALTER TABLE issues DROP CONSTRAINT IF EXISTS fk_issues_agent, DROP COLUMN IF EXISTS agent_connection_id;
--   ALTER TABLE git_events DROP CONSTRAINT IF EXISTS fk_git_events_agent, DROP COLUMN IF EXISTS agent_connection_id;
--   ALTER TABLE pull_request_comments DROP CONSTRAINT IF EXISTS fk_pull_request_comments_agent, DROP COLUMN IF EXISTS agent_connection_id;
--   ALTER TABLE pull_requests DROP CONSTRAINT IF EXISTS fk_pull_requests_agent, DROP COLUMN IF EXISTS agent_connection_id;
--   DROP TABLE IF EXISTS agent_branches, agent_project_policies;
--   DROP INDEX IF EXISTS idx_agent_approvals_sealed;
--   ALTER TABLE agent_approvals DROP CONSTRAINT IF EXISTS agent_approvals_sealed_check, DROP COLUMN IF EXISTS sealed, DROP COLUMN IF EXISTS project_id;
--   DELETE FROM agent_policies WHERE env_class = 'any';
--   ALTER TABLE agent_policies DROP CONSTRAINT IF EXISTS agent_policies_env_class_check;
--   ALTER TABLE agent_policies ADD CONSTRAINT agent_policies_env_class_check CHECK (env_class IN ('preview', 'other', 'protected'));
