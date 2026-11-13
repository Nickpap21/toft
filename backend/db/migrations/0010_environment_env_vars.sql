-- Shared environment variables (the founders' notes from the first pilot, item 12;
-- design: private/specs/2026-10-11-shared-environment-variables-design.md).
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Additive only: a new table, no change to env_vars. Every service in an
-- environment inherits the environment's variables and secrets on its next
-- deploy; a service's own variable with the same key wins. Secrets are
-- encrypted with ENCRYPTION_KEY, like service secrets, and never read back.
-- Deleting an environment deletes its rows. Rolling the binary back is safe:
-- older code never reads the table.
--
-- The CHECK is the rule envvar.ValidKey applies (letters, digits and _, not
-- starting with a digit). UNIQUE (environment_id, key) also serves the
-- per-environment lookups and the deploy-time merge.
--
-- Reversible: DROP TABLE environment_env_vars;
CREATE TABLE IF NOT EXISTS environment_env_vars (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id UUID        NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    key            TEXT        NOT NULL CHECK (key ~ '^[A-Za-z_][A-Za-z0-9_]*$'),
    value          TEXT        NOT NULL,
    is_secret      BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (environment_id, key)
);
