-- 0048: agents over MCP with OAuth (stage 6 session 1, D-100). Additive.
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it.

-- =============================================================================
-- Agents over MCP with OAuth (0048, D-100). Toft is the authorization server:
-- an agent (an OAuth client) connects as a person, to one workspace, with the
-- abilities the person allowed, and acts under the workspace's agent policy.
-- =============================================================================

-- OAuth clients: a Client ID Metadata Document's URL ('cimd', fetched and
-- cached) or a dynamically registered public client ('dcr').
CREATE TABLE IF NOT EXISTS oauth_clients (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id     TEXT        NOT NULL UNIQUE,
    kind          TEXT        NOT NULL CHECK (kind IN ('cimd', 'dcr')),
    name          TEXT        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    redirect_uris TEXT[]      NOT NULL CHECK (cardinality(redirect_uris) BETWEEN 1 AND 10),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- When a CIMD client's document was last fetched (it is refetched after
    -- a day); a DCR client's registration.
    fetched_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- A connection is one consent: who, which workspace, which projects, what.
-- It ends at expires_at (90 days) or when revoked; its tokens end with it.
CREATE TABLE IF NOT EXISTS agent_connections (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_id    UUID        NOT NULL REFERENCES oauth_clients(id),
    -- The client's name when the person allowed it.
    name         TEXT        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- Every project of the workspace, new ones included; otherwise those in
    -- agent_connection_projects.
    all_projects BOOLEAN     NOT NULL,
    scopes       TEXT[]      NOT NULL CHECK (scopes <@ ARRAY['read', 'deploy', 'code', 'issues', 'variables', 'runbooks']::TEXT[]
                                             AND 'read' = ANY (scopes)),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    revoked_by   UUID        REFERENCES users(id) ON DELETE SET NULL,
    last_used_at TIMESTAMPTZ,
    last_tool    TEXT,
    CHECK (expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS idx_agent_connections_org ON agent_connections (org_id, created_at);
CREATE INDEX IF NOT EXISTS idx_agent_connections_client ON agent_connections (client_id);

CREATE TABLE IF NOT EXISTS agent_connection_projects (
    connection_id UUID NOT NULL REFERENCES agent_connections(id) ON DELETE CASCADE,
    project_id    UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    PRIMARY KEY (connection_id, project_id)
);
CREATE INDEX IF NOT EXISTS idx_agent_connection_projects_project ON agent_connection_projects (project_id);

-- Authorization codes: single use, 10 minutes, PKCE S256. The consent's
-- choices ride on the code until the token exchange makes the connection.
CREATE TABLE IF NOT EXISTS oauth_codes (
    code_hash      TEXT        PRIMARY KEY,
    client_id      UUID        NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    user_id        UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id         UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    all_projects   BOOLEAN     NOT NULL,
    project_ids    UUID[]      NOT NULL DEFAULT '{}',
    scopes         TEXT[]      NOT NULL,
    redirect_uri   TEXT        NOT NULL,
    code_challenge TEXT        NOT NULL,
    expires_at     TIMESTAMPTZ NOT NULL,
    used_at        TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (all_projects OR cardinality(project_ids) > 0)
);

-- Access tokens (1 hour) and refresh tokens (rotated; a reused one revokes
-- its connection), stored as SHA-256 hashes.
CREATE TABLE IF NOT EXISTS agent_tokens (
    token_hash    TEXT        PRIMARY KEY,
    connection_id UUID        NOT NULL REFERENCES agent_connections(id) ON DELETE CASCADE,
    kind          TEXT        NOT NULL CHECK (kind IN ('access', 'refresh')),
    expires_at    TIMESTAMPTZ NOT NULL,
    -- A refresh token's use (rotation): a second use is a stolen token.
    used_at       TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (kind = 'refresh' OR used_at IS NULL)
);
CREATE INDEX IF NOT EXISTS idx_agent_tokens_connection ON agent_tokens (connection_id);
CREATE INDEX IF NOT EXISTS idx_agent_tokens_expires ON agent_tokens (expires_at);

-- The workspace's policy: a missing row is the default (agent.DefaultPolicy).
CREATE TABLE IF NOT EXISTS agent_policies (
    org_id     UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    action     TEXT        NOT NULL CHECK (action IN ('deploy', 'code', 'issues', 'variables', 'secrets', 'runbooks')),
    env_class  TEXT        NOT NULL CHECK (env_class IN ('preview', 'other', 'protected')),
    decision   TEXT        NOT NULL CHECK (decision IN ('allow', 'ask', 'never')),
    updated_by UUID        REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (org_id, action, env_class)
);

-- Ask a person: an agent's call waiting for an owner or developer. request
-- is the operation Approve runs (method, path, body); it never holds a
-- secret. A waiting request past expires_at has expired.
CREATE TABLE IF NOT EXISTS agent_approvals (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    connection_id  UUID        NOT NULL REFERENCES agent_connections(id) ON DELETE CASCADE,
    user_id        UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    action         TEXT        NOT NULL CHECK (action IN ('deploy', 'code', 'issues', 'variables', 'secrets', 'runbooks')),
    tool           TEXT        NOT NULL,
    service_id     UUID        REFERENCES services(id) ON DELETE CASCADE,
    environment_id UUID        REFERENCES environments(id) ON DELETE CASCADE,
    target         TEXT        NOT NULL,
    request        JSONB       NOT NULL,
    request_hash   TEXT        NOT NULL,
    reason         TEXT        NOT NULL CHECK (length(reason) BETWEEN 1 AND 2000),
    plan           TEXT        NOT NULL DEFAULT '' CHECK (length(plan) <= 4000),
    state          TEXT        NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting', 'approved', 'denied', 'cancelled')),
    expires_at     TIMESTAMPTZ NOT NULL,
    decided_by     UUID        REFERENCES users(id) ON DELETE SET NULL,
    decided_at     TIMESTAMPTZ,
    note           TEXT        NOT NULL DEFAULT '' CHECK (length(note) <= 2000),
    executed_at    TIMESTAMPTZ,
    result         JSONB,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((state = 'waiting') = (decided_at IS NULL)),
    CHECK (executed_at IS NULL OR state = 'approved')
);
CREATE INDEX IF NOT EXISTS idx_agent_approvals_waiting ON agent_approvals (org_id, created_at) WHERE state = 'waiting';
CREATE INDEX IF NOT EXISTS idx_agent_approvals_connection ON agent_approvals (connection_id, request_hash);

ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS agent_connection_id UUID REFERENCES agent_connections(id) ON DELETE SET NULL;
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS api_key_id UUID;
CREATE INDEX IF NOT EXISTS idx_audit_log_agent ON audit_log (org_id, agent_connection_id, created_at DESC) WHERE agent_connection_id IS NOT NULL;
ALTER TABLE organizations ADD COLUMN IF NOT EXISTS agents_paused_at TIMESTAMPTZ;
ALTER TABLE organizations ADD COLUMN IF NOT EXISTS agents_paused_by UUID REFERENCES users(id) ON DELETE SET NULL;

-- Rollback (reversible; it loses every agent connection, its tokens, the
-- workspaces' agent policies, waiting and decided approvals, which audit
-- entries an agent or a key made, and the pause switches; connected agents
-- must sign in again afterwards):
--   ALTER TABLE organizations DROP COLUMN IF EXISTS agents_paused_by, DROP COLUMN IF EXISTS agents_paused_at;
--   DROP INDEX IF EXISTS idx_audit_log_agent;
--   ALTER TABLE audit_log DROP COLUMN IF EXISTS api_key_id, DROP COLUMN IF EXISTS agent_connection_id;
--   DELETE FROM instance_settings WHERE key = 'agents_paused';
--   DROP TABLE IF EXISTS agent_approvals, agent_policies, agent_tokens, oauth_codes,
--       agent_connection_projects, agent_connections, oauth_clients;
