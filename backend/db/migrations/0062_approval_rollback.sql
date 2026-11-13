-- 0062: an approved deploy also allows rolling back to what was live, and a
-- plan can come in steps that Toft marks (D-146).
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it. Additive (D-63): the previous release never
-- reads these columns, and its inserts take their defaults.
--
-- agent_approvals.rollback_to: the deployment live when an approved deploy
-- ran (set by the run's claim); deployment_id: the deployment that run
-- started; rollback_used_at: when the agent used the rollback it covers.
-- The rollback is covered once, within 30 minutes of deployment_id going
-- live, while it is still live and the approver is still an owner or
-- developer: all read when the rollback asks, nothing stored for it.
-- A deployment removed by the log GC sets them to NULL, which ends it.
-- plan_steps: the plan as the agent gave it in steps (text, tool,
-- service_id), [] when it gave text only. Constant defaults, so adding the
-- columns rewrites nothing.

ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS plan_steps JSONB NOT NULL DEFAULT '[]';
ALTER TABLE agent_approvals DROP CONSTRAINT IF EXISTS agent_approvals_plan_steps_check;
ALTER TABLE agent_approvals ADD CONSTRAINT agent_approvals_plan_steps_check CHECK (jsonb_typeof(plan_steps) = 'array');
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS rollback_to UUID REFERENCES deployments(id) ON DELETE SET NULL;
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS deployment_id UUID REFERENCES deployments(id) ON DELETE SET NULL;
ALTER TABLE agent_approvals ADD COLUMN IF NOT EXISTS rollback_used_at TIMESTAMPTZ;
-- The deployment log clean-up deletes deployments; these keep its SET NULL
-- from scanning the table. Few rows have either, so plain builds are quick.
CREATE INDEX IF NOT EXISTS idx_agent_approvals_rollback_to ON agent_approvals (rollback_to) WHERE rollback_to IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_agent_approvals_deployment ON agent_approvals (deployment_id) WHERE deployment_id IS NOT NULL;

-- Rollback (reversible; it loses the plans' steps and the allowances):
--   DROP INDEX IF EXISTS idx_agent_approvals_rollback_to;
--   DROP INDEX IF EXISTS idx_agent_approvals_deployment;
--   ALTER TABLE agent_approvals DROP CONSTRAINT IF EXISTS agent_approvals_plan_steps_check;
--   ALTER TABLE agent_approvals DROP COLUMN IF EXISTS plan_steps, DROP COLUMN IF EXISTS rollback_to,
--     DROP COLUMN IF EXISTS deployment_id, DROP COLUMN IF EXISTS rollback_used_at;
