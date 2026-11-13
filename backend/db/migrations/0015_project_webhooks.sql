-- One GitHub webhook secret per project (README §7 item 8).
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Additive: a new table. Before, one instance-wide WEBHOOK_SECRET signed every
-- push, every workspace's members could read it, and a push matched services
-- in every workspace. Now each project has its own secret (encrypted with
-- ENCRYPTION_KEY) and webhook URL, and a push deploys only that project's
-- services. A row is created the first time someone opens the project's
-- webhook. Deleting the project deletes its row. Rolling the binary back
-- leaves the table unread; the older backend checks WEBHOOK_SECRET again.
--
-- Reversible: DROP TABLE project_webhooks;
CREATE TABLE IF NOT EXISTS project_webhooks (
    project_id  UUID        PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    secret      TEXT        NOT NULL,  -- encrypted with ENCRYPTION_KEY
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
