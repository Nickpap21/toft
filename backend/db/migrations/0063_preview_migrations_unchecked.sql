-- 0063: a preview says when its pull request's diff was too large to check
-- for migrations (D-147).
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it. Additive (D-63): the previous release never
-- reads the column, and its update of migration_files leaves it as it was.
--
-- previews.migrations_unchecked: true when the diff at the commit the
-- preview last started or updated to changes over 3,000 files, so
-- migration_files may be incomplete; written with migration_files, and false
-- again once a later diff fits. NOT NULL with a constant default, so adding
-- it rewrites nothing.

ALTER TABLE previews ADD COLUMN IF NOT EXISTS migrations_unchecked BOOLEAN NOT NULL DEFAULT false;

-- Rollback (reversible; it loses which previews were unchecked):
--   ALTER TABLE previews DROP COLUMN IF EXISTS migrations_unchecked;
