-- 0061: a preview never migrates (D-144). Every preview's copies run
-- neither their migrate nor their pre-deploy commands, not only those of a
-- protected environment, and the pull request says which migrations its
-- diff touches.
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it. Additive (D-63): the previous release (v0.33.x)
-- never reads the column and never writes the new event kind.
--
-- previews.migration_files: the migrations the pull request's diff touches
-- (the first 50 paths), recomputed when the preview starts and at every push
-- it deploys. NOT NULL with a constant default, so adding it rewrites
-- nothing.
-- pull_request_events.kind gains preview_migrations_skipped (data: sha,
-- files), written when that list changes and isn't empty. The CHECK only
-- widens.
--
-- The data step is in premigrate.sql: the copies of previews made before
-- this release lose their migrate and pre-deploy commands. Irreversible by
-- design: a preview's copy is never edited, and its next start copies its
-- original again.
--
-- Contract, the release after (D-63): premigrate runs while the previous
-- backend still serves, and a rollback runs it again, so a preview copy made
-- in either window keeps its commands until the next upgrade. contract.sql
-- then makes the database refuse one: an explicit, idempotent
--   ALTER TABLE services ADD CONSTRAINT services_preview_no_migrate CHECK
--     (preview_of IS NULL OR (migrate_command = '' AND pre_deploy_commands = ''));
-- (check the column that marks a preview copy before writing it). Adding it
-- now would break v0.33.x's copy insert.

ALTER TABLE previews ADD COLUMN IF NOT EXISTS migration_files TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE pull_request_events DROP CONSTRAINT IF EXISTS pull_request_events_kind_check;
ALTER TABLE pull_request_events ADD CONSTRAINT pull_request_events_kind_check CHECK (kind IN ('pushed', 'closed', 'reopened',
    'merged', 'head_deleted', 'title_changed', 'preview_started', 'preview_waiting', 'preview_approved', 'preview_updated',
    'preview_stopped', 'preview_removed', 'preview_migrations_skipped'));

-- Rollback (reversible for the schema; it loses each preview's list and the
-- events that named one):
--   DELETE FROM pull_request_events WHERE kind = 'preview_migrations_skipped';
--   ALTER TABLE pull_request_events DROP CONSTRAINT IF EXISTS pull_request_events_kind_check;
--   ALTER TABLE pull_request_events ADD CONSTRAINT pull_request_events_kind_check CHECK (kind IN ('pushed', 'closed',
--       'reopened', 'merged', 'head_deleted', 'title_changed', 'preview_started', 'preview_waiting', 'preview_approved',
--       'preview_updated', 'preview_stopped', 'preview_removed'));
--   ALTER TABLE previews DROP COLUMN IF EXISTS migration_files;
