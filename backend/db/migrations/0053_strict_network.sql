-- 0053: Sandboxed with a strict network (stage 7 session 4, D-115).
-- Additive: two CHECKs widen to the new level, sandboxed_strict, and
-- server_capabilities gains dns_relay (NOT NULL DEFAULT false). schema.sql
-- is the source of truth; Atlas applies it. This file records the change and
-- how to undo it.
--
-- The previous release (v0.19.x) reads sandboxed_strict as no level it
-- knows, so while it runs a service at that level deploys as Standard
-- (runc). The release note says to switch such services to Sandboxed before
-- rolling back.

ALTER TABLE organizations DROP CONSTRAINT IF EXISTS organizations_default_isolation_check;
ALTER TABLE organizations ADD CONSTRAINT organizations_default_isolation_check
    CHECK (default_isolation IN ('standard', 'sandboxed', 'sandboxed_strict'));

ALTER TABLE services DROP CONSTRAINT IF EXISTS services_isolation_check;
ALTER TABLE services ADD CONSTRAINT services_isolation_check
    CHECK (isolation IN ('standard', 'sandboxed', 'sandboxed_strict'));

ALTER TABLE server_capabilities ADD COLUMN IF NOT EXISTS dns_relay BOOLEAN NOT NULL DEFAULT false;

-- Undo (not run by toft upgrade, which never drops; for a development
-- database only). Strict services and defaults fall back to Sandboxed:
--   UPDATE services SET isolation = 'sandboxed' WHERE isolation = 'sandboxed_strict';
--   UPDATE organizations SET default_isolation = 'sandboxed' WHERE default_isolation = 'sandboxed_strict';
--   ALTER TABLE server_capabilities DROP COLUMN IF EXISTS dns_relay;
--   ALTER TABLE services DROP CONSTRAINT IF EXISTS services_isolation_check;
--   ALTER TABLE services ADD CONSTRAINT services_isolation_check CHECK (isolation IN ('standard', 'sandboxed'));
--   ALTER TABLE organizations DROP CONSTRAINT IF EXISTS organizations_default_isolation_check;
--   ALTER TABLE organizations ADD CONSTRAINT organizations_default_isolation_check
--     CHECK (default_isolation IN ('standard', 'sandboxed'));
