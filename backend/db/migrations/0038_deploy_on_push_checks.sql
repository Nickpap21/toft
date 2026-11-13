-- Git hosting, session 4 (D-77): services that build from the project's own
-- repositories, deploy on push through the code module's events outbox, and
-- commit checks.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Everything is added (D-63): v0.4.1 runs on it. It ignores the new columns
-- and tables, so pushes to Toft's repositories don't deploy, a service that
-- builds from one can't deploy (v0.4.1 sees a git service with no URL), and
-- API keys with the checks scope are refused.

ALTER TABLE services ADD COLUMN IF NOT EXISTS repository_id UUID;
ALTER TABLE services ADD COLUMN IF NOT EXISTS deploy_on_push BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE services ADD CONSTRAINT services_repository_source CHECK (repository_id IS NULL OR (
    source_type IN ('git', 'docker-compose') AND COALESCE(git_repo_url, '') = '' AND compose_repo_url = ''));
ALTER TABLE services ADD CONSTRAINT fk_services_repository
    FOREIGN KEY (repository_id) REFERENCES repositories(id);
CREATE INDEX IF NOT EXISTS idx_services_repository ON services (repository_id, git_branch) WHERE repository_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS git_events (
    id              BIGSERIAL   PRIMARY KEY,
    repository_id   UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    kind            TEXT        NOT NULL CHECK (kind IN ('push', 'merge')),
    branch          TEXT        NOT NULL CHECK (char_length(branch) BETWEEN 1 AND 255),
    before_sha      TEXT        NOT NULL CHECK (before_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    after_sha       TEXT        NOT NULL CHECK (after_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    actor_id        UUID        REFERENCES users(id) ON DELETE SET NULL,
    pull_request_id UUID        REFERENCES pull_requests(id) ON DELETE SET NULL,
    subject         TEXT        NOT NULL DEFAULT '' CHECK (char_length(subject) <= 300),
    commits         INTEGER     NOT NULL DEFAULT 0 CHECK (commits >= 0),
    forced          BOOLEAN     NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    dispatched_at   TIMESTAMPTZ,
    attempts        INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error      TEXT        CHECK (char_length(last_error) <= 1000)
);
CREATE INDEX IF NOT EXISTS idx_git_events_repository ON git_events (repository_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_git_events_pending ON git_events (next_attempt_at) WHERE dispatched_at IS NULL;

ALTER TABLE deployments ADD COLUMN IF NOT EXISTS git_event_id BIGINT;
ALTER TABLE deployments ADD CONSTRAINT fk_deployments_git_event
    FOREIGN KEY (git_event_id) REFERENCES git_events(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_deployments_git_event ON deployments (git_event_id, service_id) WHERE git_event_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS commit_checks (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    sha           TEXT        NOT NULL CHECK (sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    name          TEXT        NOT NULL CHECK (char_length(name) >= 1 AND char_length(name) <= 100 AND name !~ '[[:cntrl:]]'),
    state         TEXT        NOT NULL CHECK (state IN ('pending', 'running', 'success', 'failure', 'cancelled', 'skipped')),
    description   TEXT        NOT NULL DEFAULT '' CHECK (char_length(description) <= 300),
    details_url   TEXT        CHECK (details_url ~ '^https?://' AND char_length(details_url) <= 2000),
    deployment_id UUID        REFERENCES deployments(id) ON DELETE SET NULL,
    reporter_id   UUID        REFERENCES users(id) ON DELETE SET NULL,
    started_at    TIMESTAMPTZ,
    completed_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (repository_id, sha, name)
);

-- The checks scope widens the CHECK (only widening, D-63).
ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_scope_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scope_check CHECK (
    (project_id IS NULL AND cardinality(scopes) = 0)
    OR (project_id IS NOT NULL AND cardinality(scopes) > 0
        AND scopes <@ ARRAY['deploy', 'variables', 'code_read', 'code_write', 'checks']::TEXT[])
);

-- Rollback (reversible; it loses every check, the events outbox and which
-- services build from a repository: switch those back to a URL first, or
-- they keep a git source with no URL). The scope CHECK can only narrow once
-- no key has the checks scope, so keys lose it, and keys with nothing else
-- are deleted:
--   DELETE FROM api_keys WHERE scopes = ARRAY['checks']::TEXT[];
--   UPDATE api_keys SET scopes = array_remove(scopes, 'checks') WHERE 'checks' = ANY(scopes);
--   ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_scope_check;
--   ALTER TABLE api_keys ADD CONSTRAINT api_keys_scope_check CHECK (
--       (project_id IS NULL AND cardinality(scopes) = 0)
--       OR (project_id IS NOT NULL AND cardinality(scopes) > 0
--           AND scopes <@ ARRAY['deploy', 'variables', 'code_read', 'code_write']::TEXT[]));
--   DROP TABLE IF EXISTS commit_checks;
--   DROP INDEX IF EXISTS uq_deployments_git_event;
--   ALTER TABLE deployments DROP COLUMN IF EXISTS git_event_id;
--   DROP TABLE IF EXISTS git_events;
--   DROP INDEX IF EXISTS idx_services_repository;
--   ALTER TABLE services DROP CONSTRAINT IF EXISTS fk_services_repository;
--   ALTER TABLE services DROP CONSTRAINT IF EXISTS services_repository_source;
--   ALTER TABLE services DROP COLUMN IF EXISTS deploy_on_push;
--   ALTER TABLE services DROP COLUMN IF EXISTS repository_id;
