-- Variables that are masked on screen, who set each variable and secret,
-- and who added each domain and when it was first routed (D-45).
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- env_vars.masked: a plain variable shown as dots, still stored and returned
-- readable; never a secret too (the CHECK). environment_env_vars has no
-- masked column: the Environment board draws shared variables as plain text.
-- updated_by and created_by are set by the API from 0023 on; rows from
-- before stay NULL, and a deleted user's become NULL. domains.routed_at is
-- set by the first successful deploy after the domain is added.
--
-- Additive, every column nullable or defaulted. A backend from before 0023
-- ignores the columns, and its saves leave masked as it was and updated_by
-- unchanged, except one: its secret insert read the whole row back
-- (RETURNING *), so saving a secret reports a failure although the secret
-- is stored. Saving it again is harmless.
--
-- Rollback (which variables were masked, and the authors, are lost):
--   ALTER TABLE env_vars DROP CONSTRAINT IF EXISTS env_vars_masked_not_secret;
--   ALTER TABLE env_vars DROP COLUMN IF EXISTS masked;
--   ALTER TABLE env_vars DROP COLUMN IF EXISTS updated_by;
--   ALTER TABLE environment_env_vars DROP COLUMN IF EXISTS updated_by;
--   ALTER TABLE domains DROP COLUMN IF EXISTS created_by;
--   ALTER TABLE domains DROP COLUMN IF EXISTS routed_at;

ALTER TABLE env_vars ADD COLUMN IF NOT EXISTS masked BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE env_vars ADD CONSTRAINT env_vars_masked_not_secret CHECK (NOT (masked AND is_secret));
ALTER TABLE env_vars ADD COLUMN IF NOT EXISTS updated_by UUID REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE environment_env_vars ADD COLUMN IF NOT EXISTS updated_by UUID REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE domains ADD COLUMN IF NOT EXISTS created_by UUID REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE domains ADD COLUMN IF NOT EXISTS routed_at TIMESTAMPTZ;
