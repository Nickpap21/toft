-- 0066: Home's Finish setting up checklist, hidden (7g). The welcome layout
-- shows until the checklist is complete or an owner hides it for the
-- workspace.
-- schema.sql is the source of truth; Atlas applies it. Additive (D-63): a
-- nullable column with no default rewrites nothing, and the previous release
-- never reads it.
-- Data: premigrate.sql adds the column first on an upgrade and hides the
-- checklist for every workspace that already has a live service, once, so
-- they don't fall back into the welcome layout.

ALTER TABLE organizations ADD COLUMN IF NOT EXISTS setup_hidden_at TIMESTAMPTZ;

-- Rollback (reversible; the checklist shows again until hidden again):
--   ALTER TABLE organizations DROP COLUMN IF EXISTS setup_hidden_at;
