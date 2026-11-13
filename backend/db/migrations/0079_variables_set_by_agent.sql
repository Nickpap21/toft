-- 0079: a variable or secret an agent set says so (D-100). schema.sql is the
-- source of truth; Atlas applies it. This file records the change and how to
-- undo it.
--
-- set_env_var and set_secret recorded only the person the agent acts for in
-- updated_by, so the Variables tab and an agent request showed the person.
-- The agent's connection now sits beside it; NULL for a person's change.
--
-- Additive (D-63): nullable columns with no default (no rewrite) the
-- previous release never names. No index on them: an agent connection is
-- revoked, and deleted only with its person or workspace, so the FK's
-- ON DELETE SET NULL scans these tables only then (rare).

ALTER TABLE env_vars ADD COLUMN IF NOT EXISTS agent_connection_id UUID;
ALTER TABLE environment_env_vars ADD COLUMN IF NOT EXISTS agent_connection_id UUID;
ALTER TABLE env_vars ADD CONSTRAINT fk_env_vars_agent FOREIGN KEY (agent_connection_id) REFERENCES agent_connections(id) ON DELETE SET NULL;
ALTER TABLE environment_env_vars ADD CONSTRAINT fk_environment_env_vars_agent FOREIGN KEY (agent_connection_id) REFERENCES agent_connections(id) ON DELETE SET NULL;

-- Rollback (reversible; it loses which variables an agent set):
--   ALTER TABLE environment_env_vars DROP CONSTRAINT IF EXISTS fk_environment_env_vars_agent, DROP COLUMN IF EXISTS agent_connection_id;
--   ALTER TABLE env_vars DROP CONSTRAINT IF EXISTS fk_env_vars_agent, DROP COLUMN IF EXISTS agent_connection_id;
