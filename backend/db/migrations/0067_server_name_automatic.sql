-- 0067: Add a server names a new server server-<n> until it first
-- provisions, when its hostname replaces the name (D-153).
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it. Additive (D-63): the previous release never
-- reads the column, and its inserts take the default.
--
-- servers.name_automatic: true while the name is the automatic one Add a
-- server gave it; the hostname swap and any rename set it false. NOT NULL
-- with a constant default, so adding it rewrites nothing.
--
-- Unused servers that never joined are removed 24 hours after their last
-- command expired (an hourly sweep, audited as Toft): no schema for that.

ALTER TABLE servers ADD COLUMN IF NOT EXISTS name_automatic BOOLEAN NOT NULL DEFAULT false;

-- Rollback (reversible; a server still named server-<n> keeps that name at
-- its first provisioning):
--   ALTER TABLE servers DROP COLUMN IF EXISTS name_automatic;
