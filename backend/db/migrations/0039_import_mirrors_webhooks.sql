-- Git hosting, session 5 (D-79): import, pull and push mirrors, outbound
-- webhooks with their deliveries, and the mirror_failing alert.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Everything is added or widened (D-63): v0.5.1 runs on it. It ignores the
-- new tables, so mirrors stop syncing and webhooks stop sending, and a
-- repository that pulls from another host takes pushes (the next sync after
-- upgrading again overwrites them). It never claims the new event kinds:
-- they are written already dispatched. Its Pipelines tab lists tag and pull
-- request events as pushes (its list has no filter on the kind).

ALTER TABLE git_events DROP CONSTRAINT IF EXISTS git_events_kind_check;
ALTER TABLE git_events ADD CONSTRAINT git_events_kind_check CHECK (kind IN ('push', 'merge', 'mirror', 'tag', 'pull_request'));
ALTER TABLE git_events ADD COLUMN IF NOT EXISTS action TEXT CHECK (action IN ('opened', 'pushed', 'closed', 'reopened'));
ALTER TABLE git_events ADD CONSTRAINT git_events_action CHECK ((kind = 'pull_request') = (action IS NOT NULL));

CREATE TABLE IF NOT EXISTS repository_mirrors (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id      UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    direction          TEXT        NOT NULL CHECK (direction IN ('pull', 'push')),
    url                TEXT        NOT NULL CHECK (url ~ '^https://[^/?#@[:space:]]+/[^?#[:space:]]+$' AND char_length(url) <= 2048),
    username           TEXT        NOT NULL DEFAULT '' CHECK (char_length(username) <= 255 AND username !~ '[[:cntrl:]:]'),
    password_encrypted TEXT,
    keep               BOOLEAN     NOT NULL DEFAULT true,
    created_by         UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    next_sync_at       TIMESTAMPTZ,
    syncing_since      TIMESTAMPTZ,
    last_sync_at       TIMESTAMPTZ,
    last_ok_at         TIMESTAMPTZ,
    failures           INTEGER     NOT NULL DEFAULT 0 CHECK (failures >= 0),
    last_error         TEXT        CHECK (char_length(last_error) <= 1000),
    last_moved         INTEGER     CHECK (last_moved >= 0),
    remote_head        TEXT        CHECK (char_length(remote_head) <= 255),
    remote_branches    INTEGER     CHECK (remote_branches >= 0),
    remote_tags        INTEGER     CHECK (remote_tags >= 0),
    CONSTRAINT repository_mirrors_push_keeps CHECK (direction = 'pull' OR keep)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_repository_mirrors_pull ON repository_mirrors (repository_id) WHERE direction = 'pull';
CREATE INDEX IF NOT EXISTS idx_repository_mirrors_repository ON repository_mirrors (repository_id, direction);
CREATE INDEX IF NOT EXISTS idx_repository_mirrors_due ON repository_mirrors (next_sync_at) WHERE next_sync_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS repository_webhooks (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id     UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    url               TEXT        NOT NULL CHECK (url ~ '^https://[^/?#@[:space:]]+([/?][^#[:space:]]*)?$' AND char_length(url) <= 2048),
    events            TEXT[]      NOT NULL CHECK (((cardinality(events) >= 1) AND (cardinality(events) <= 2)) AND (events <@ ARRAY['push'::text, 'pull_request'::text])),
    active            BOOLEAN     NOT NULL DEFAULT true,
    secret_encrypted  TEXT        NOT NULL,
    created_by        UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    secret_rotated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    secret_rotated_by UUID        REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_repository_webhooks_repository ON repository_webhooks (repository_id);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    webhook_id      UUID        NOT NULL REFERENCES repository_webhooks(id) ON DELETE CASCADE,
    git_event_id    BIGINT      REFERENCES git_events(id) ON DELETE SET NULL,
    event           TEXT        NOT NULL CHECK (event IN ('push', 'pull_request', 'ping')),
    actor_id        UUID        REFERENCES users(id) ON DELETE SET NULL,
    summary         TEXT        NOT NULL DEFAULT '' CHECK (char_length(summary) <= 300),
    payload         TEXT        CHECK (octet_length(payload) <= 262144),
    state           TEXT        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'delivered', 'failed')),
    attempts        INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    response_status INTEGER,
    response_body   TEXT        CHECK (octet_length(response_body) <= 2048),
    error           TEXT        CHECK (char_length(error) <= 500),
    duration_ms     INTEGER     CHECK (duration_ms >= 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at    TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_webhook_deliveries_event ON webhook_deliveries (webhook_id, git_event_id, event) WHERE git_event_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_due ON webhook_deliveries (next_attempt_at) WHERE state = 'pending';
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_webhook ON webhook_deliveries (webhook_id, created_at DESC, id DESC);

ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN ('service_down', 'service_restarting',
    'service_unhealthy', 'container_exited', 'server_unreachable', 'disk_full',
    'volume_full', 'backup_failed', 'deploy_failed', 'oom_killed',
    'panel_container', 'panel_certificate', 'panel_upgrade_failed', 'mirror_failing'));

-- Rollback (reversible; it loses every mirror and its stored token, every
-- webhook and its deliveries, the events that deploy nothing, and the
-- mirror alerts). The CHECKs can narrow only once no row breaks them:
--   DELETE FROM alerts WHERE kind = 'mirror_failing';
--   ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
--   ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN ('service_down', 'service_restarting',
--       'service_unhealthy', 'container_exited', 'server_unreachable', 'disk_full',
--       'volume_full', 'backup_failed', 'deploy_failed', 'oom_killed',
--       'panel_container', 'panel_certificate', 'panel_upgrade_failed'));
--   DROP TABLE IF EXISTS webhook_deliveries;
--   DROP TABLE IF EXISTS repository_webhooks;
--   DROP TABLE IF EXISTS repository_mirrors;
--   DELETE FROM git_events WHERE kind IN ('tag', 'pull_request') OR after_sha ~ '^0+$';
--   UPDATE git_events SET kind = 'push' WHERE kind = 'mirror';
--   ALTER TABLE git_events DROP CONSTRAINT IF EXISTS git_events_action;
--   ALTER TABLE git_events DROP COLUMN IF EXISTS action;
--   ALTER TABLE git_events DROP CONSTRAINT IF EXISTS git_events_kind_check;
--   ALTER TABLE git_events ADD CONSTRAINT git_events_kind_check CHECK (kind IN ('push', 'merge'));
