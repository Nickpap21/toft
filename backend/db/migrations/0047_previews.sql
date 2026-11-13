-- PR previews (D-97, stage 5 session 3): each project's preview setting,
-- the previews themselves, a preview's copies of services, pull requests
-- and pushes made with an API key, the previews module's mark on the events
-- outbox, and the preview kinds of a pull request's history.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Everything is added or widened (D-63): v0.13.2 runs on it and ignores it.
-- While it runs, a preview's environment shows as an ordinary environment
-- named pr-<n>, its containers keep running as they are, and nothing removes
-- it on merge or close; pushes never deploy it (Deploy on push is off on a
-- preview's services). The events it writes keep previews_at NULL, so
-- v0.14.0 applies them once it runs again, and removes the previews of pull
-- requests no longer open at its start.

ALTER TABLE projects ADD COLUMN IF NOT EXISTS preview_environment_id UUID;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS preview_domain TEXT CHECK (char_length(preview_domain) <= 200
    AND preview_domain ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$');
ALTER TABLE projects ADD CONSTRAINT fk_projects_preview_environment
    FOREIGN KEY (preview_environment_id) REFERENCES environments(id) ON DELETE SET NULL;

ALTER TABLE services ADD COLUMN IF NOT EXISTS preview_of UUID;
ALTER TABLE services ADD CONSTRAINT fk_services_preview_of
    FOREIGN KEY (preview_of) REFERENCES services(id);
CREATE INDEX IF NOT EXISTS idx_services_preview_of ON services (preview_of) WHERE preview_of IS NOT NULL;

ALTER TABLE server_capabilities ADD COLUMN IF NOT EXISTS preview_firewall BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS opened_with_key BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS api_key_id UUID REFERENCES api_keys(id) ON DELETE SET NULL;

ALTER TABLE git_events ADD COLUMN IF NOT EXISTS with_key BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE git_events ADD COLUMN IF NOT EXISTS previews_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_git_events_previews ON git_events (id) WHERE previews_at IS NULL;

CREATE TABLE IF NOT EXISTS previews (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    pull_request_id       UUID        NOT NULL UNIQUE REFERENCES pull_requests(id),
    project_id            UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_environment_id UUID        NOT NULL REFERENCES environments(id),
    environment_id        UUID        UNIQUE REFERENCES environments(id) ON DELETE CASCADE,
    untrusted             BOOLEAN     NOT NULL DEFAULT false,
    head_sha              TEXT        NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    approved_sha          TEXT        CHECK (approved_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    approved_by           UUID        REFERENCES users(id) ON DELETE SET NULL,
    approved_at           TIMESTAMPTZ,
    state                 TEXT        NOT NULL
        CHECK (state IN ('waiting_approval', 'waiting_room', 'active', 'stopped', 'removing')),
    stopped_reason        TEXT        CHECK (stopped_reason IN ('room', 'manual')),
    note                  TEXT        NOT NULL DEFAULT '' CHECK (char_length(note) <= 1000),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((state = 'stopped') = (stopped_reason IS NOT NULL)),
    CHECK (state <> 'active' OR environment_id IS NOT NULL),
    CHECK ((approved_sha IS NULL) = (approved_at IS NULL))
);
CREATE INDEX IF NOT EXISTS idx_previews_project ON previews (project_id, created_at);
CREATE INDEX IF NOT EXISTS idx_previews_source ON previews (source_environment_id);

ALTER TABLE pull_request_events DROP CONSTRAINT IF EXISTS pull_request_events_kind_check;
ALTER TABLE pull_request_events ADD CONSTRAINT pull_request_events_kind_check CHECK (kind IN ('pushed', 'closed', 'reopened',
    'merged', 'head_deleted', 'title_changed', 'preview_started', 'preview_waiting', 'preview_approved', 'preview_updated',
    'preview_stopped', 'preview_removed'));

-- Rollback (reversible; it loses every preview's record, the projects'
-- preview settings and which pull requests and pushes were made with a key;
-- remove the previews first, by turning previews off in each project, or
-- their environments stay as ordinary ones):
--   ALTER TABLE pull_request_events DROP CONSTRAINT IF EXISTS pull_request_events_kind_check;
--   DELETE FROM pull_request_events WHERE kind LIKE 'preview_%';
--   ALTER TABLE pull_request_events ADD CONSTRAINT pull_request_events_kind_check CHECK (kind IN ('pushed', 'closed',
--       'reopened', 'merged', 'head_deleted', 'title_changed'));
--   DROP TABLE IF EXISTS previews;
--   DROP INDEX IF EXISTS idx_git_events_previews;
--   ALTER TABLE git_events DROP COLUMN IF EXISTS previews_at, DROP COLUMN IF EXISTS with_key;
--   ALTER TABLE pull_requests DROP COLUMN IF EXISTS api_key_id, DROP COLUMN IF EXISTS opened_with_key;
--   ALTER TABLE server_capabilities DROP COLUMN IF EXISTS preview_firewall;
--   ALTER TABLE services DROP CONSTRAINT IF EXISTS fk_services_preview_of;
--   ALTER TABLE services DROP COLUMN IF EXISTS preview_of;
--   ALTER TABLE projects DROP CONSTRAINT IF EXISTS fk_projects_preview_environment;
--   ALTER TABLE projects DROP COLUMN IF EXISTS preview_domain, DROP COLUMN IF EXISTS preview_environment_id;
