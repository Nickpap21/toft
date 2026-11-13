-- 0077: what the reworked project Settings shows (7g step 20).
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it.
--
-- projects.preview_max: how many previews a project runs at once, stopped
-- ones included (was the constant 5, D-93). NOT NULL with a constant
-- default is a catalog change; the CHECK scans projects, a small table.
-- project_webhooks.last_push_at, last_push_repo: when the project's GitHub
-- webhook last delivered a signed branch push, and from which repository.
-- idx_api_keys_project: GET /projects/{id}/api-keys, a project's keys newest
-- first (api_keys is small; a plain build).
--
-- Additive (D-63): the previous release never names these columns (sqlc
-- lists its columns) and runs on this schema with 5 previews a project.

ALTER TABLE projects ADD COLUMN IF NOT EXISTS preview_max SMALLINT NOT NULL DEFAULT 5 CHECK (preview_max BETWEEN 1 AND 20);
ALTER TABLE project_webhooks ADD COLUMN IF NOT EXISTS last_push_at TIMESTAMPTZ;
ALTER TABLE project_webhooks ADD COLUMN IF NOT EXISTS last_push_repo TEXT CHECK (char_length(last_push_repo) <= 200);
CREATE INDEX IF NOT EXISTS idx_api_keys_project ON api_keys (project_id, created_at DESC, id DESC) WHERE project_id IS NOT NULL;

-- Rollback (reversible; nothing to run for the previous release, which works
-- on this schema). To drop them, once no release reads them, in contract.sql:
--   DROP INDEX IF EXISTS idx_api_keys_project;
--   ALTER TABLE project_webhooks DROP COLUMN IF EXISTS last_push_repo, DROP COLUMN IF EXISTS last_push_at;
--   ALTER TABLE projects DROP COLUMN IF EXISTS preview_max;
