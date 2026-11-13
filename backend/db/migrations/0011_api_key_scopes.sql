-- Project-scoped API keys (the founders' notes from the first pilot, item 15; design:
-- private/specs/2026-10-11-scoped-api-keys-design.md).
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Additive: two columns with defaults that keep every existing key as it is
-- (no project, no scopes: full access in the user's active workspace). A key
-- with a project may only deploy and/or manage variables in that project
-- (internal/apikey/scope.go). Deleting a project deletes its keys.
--
-- Reversible, but the rollback lifts scoped keys' limits, so delete them
-- first:
--   DELETE FROM api_keys WHERE project_id IS NOT NULL;
--   ALTER TABLE api_keys DROP CONSTRAINT api_keys_scope_check,
--                        DROP COLUMN scopes, DROP COLUMN project_id;
ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS scopes     TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scope_check CHECK (
    (project_id IS NULL AND cardinality(scopes) = 0)
    OR (project_id IS NOT NULL AND cardinality(scopes) > 0
        AND scopes <@ ARRAY['deploy', 'variables']::TEXT[])
);
