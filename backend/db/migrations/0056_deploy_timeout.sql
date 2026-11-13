-- 0056: S6 of the 2026-10-26 security close-out: deploy limits
-- (private/plans/2026-10-26-security-close-out.md, D-125).
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it. Additive (D-63): the previous release (v0.25.x)
-- never reads the column.
--
-- K10: a deploy had no overall time limit. deploy_timeout_minutes is the
-- service's, NULL for the platform's default of 60, which every existing
-- service keeps. Nullable without a default, so adding it rewrites nothing.

ALTER TABLE services ADD COLUMN IF NOT EXISTS deploy_timeout_minutes INTEGER
    CHECK (deploy_timeout_minutes BETWEEN 5 AND 240);

-- Rollback (reversible; it loses every time limit set):
--   ALTER TABLE services DROP COLUMN IF EXISTS deploy_timeout_minutes;
