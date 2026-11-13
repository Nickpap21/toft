-- 0051: durable rollouts and desired state on the node agent (stage 7
-- session 2, D-111). Additive. schema.sql is the source of truth; Atlas
-- applies it. This file records the change and how to undo it.
--
-- The previous release (v0.17.x) ignores the new columns: it never hands a
-- rollout off, so handed_off_at stays NULL for its deploys, and it never
-- raises spec_generation, which the CHECK allows (observed_generation only
-- moves up to it).

ALTER TABLE deployments ADD COLUMN IF NOT EXISTS handed_off_at TIMESTAMPTZ;
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS handed_off_input JSONB;
ALTER TABLE deployments ADD CONSTRAINT deployments_handed_off_check
    CHECK ((handed_off_at IS NULL) = (handed_off_input IS NULL));

ALTER TABLE servers ADD COLUMN IF NOT EXISTS spec_generation BIGINT NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS observed_generation BIGINT NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS agent_drift JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE servers ADD COLUMN IF NOT EXISTS agent_drift_at TIMESTAMPTZ;
ALTER TABLE servers ADD CONSTRAINT servers_generation_check
    CHECK (observed_generation >= 0 AND observed_generation <= spec_generation);
ALTER TABLE servers ADD CONSTRAINT servers_agent_drift_check CHECK (jsonb_typeof(agent_drift) = 'array');

-- The panel's epoch (a UUID the desired state is versioned under) lives in
-- instance_settings ('panel_epoch'): no schema change.

-- Undo (not run by toft upgrade, which never drops; for a development
-- database only):
--   ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_agent_drift_check;
--   ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_generation_check;
--   ALTER TABLE servers DROP COLUMN IF EXISTS agent_drift_at, DROP COLUMN IF EXISTS agent_drift,
--     DROP COLUMN IF EXISTS observed_generation, DROP COLUMN IF EXISTS spec_generation;
--   ALTER TABLE deployments DROP CONSTRAINT IF EXISTS deployments_handed_off_check;
--   ALTER TABLE deployments DROP COLUMN IF EXISTS handed_off_input, DROP COLUMN IF EXISTS handed_off_at;
--   DELETE FROM instance_settings WHERE key = 'panel_epoch';
