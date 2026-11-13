-- An environment's chip colour and whether it is protected (D-55): changes
-- to a protected environment (deploy, stop, restart, delete, replace a
-- secret) confirm first and name it. New environments get both from their
-- name (production: red and protected; staging, preprod: amber; test, qa:
-- violet; dev, development: teal; preview: blue; else gray, unprotected),
-- and the operator changes them on the Environment page.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change. Existing rows are
-- backfilled by the same rule once, in premigrate.sql, when the columns are
-- added.
--
-- Additive. A backend from before 0031 ignores the columns.

ALTER TABLE environments ADD COLUMN IF NOT EXISTS color TEXT NOT NULL DEFAULT 'gray'
    CHECK (color IN ('red', 'amber', 'violet', 'blue', 'teal', 'gray'));
ALTER TABLE environments ADD COLUMN IF NOT EXISTS protected BOOLEAN NOT NULL DEFAULT false;

-- Rollback (reversible; each environment's colour and protection is lost):
--   ALTER TABLE environments DROP COLUMN IF EXISTS protected;
--   ALTER TABLE environments DROP COLUMN IF EXISTS color;
