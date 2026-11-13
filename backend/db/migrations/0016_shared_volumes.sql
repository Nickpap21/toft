-- Shared volumes (D-38; design: private/specs/2026-10-12-shared-volumes-design.md).
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Additive: two new tables, and 'volume_full' added to the alert kinds. A
-- shared volume is storage the platform names (Docker volume
-- paas-shared-<id>), owned by a workspace, which its services on the same
-- server mount instead of bind-mounting a server folder. Deleting a
-- workspace deletes its volumes' rows (not the Docker volumes); a mounted
-- volume, or a server that holds volumes, cannot be deleted. Rolling the
-- binary back leaves the tables unread: services stop mounting their shared
-- volumes at their next deploy, and the data stays in the Docker volumes.
--
-- Reversible:
--   DROP TABLE service_shared_volumes; DROP TABLE shared_volumes;
--   DELETE FROM alerts WHERE kind = 'volume_full';
--   then the alerts kind CHECK without 'volume_full' (schema.sql of 0015).
CREATE TABLE IF NOT EXISTS shared_volumes (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    server_id        UUID        REFERENCES servers(id) ON DELETE RESTRICT,
    name             TEXT        NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,39}$'),
    size_limit_bytes BIGINT      NOT NULL CHECK (size_limit_bytes > 0),
    used_bytes       BIGINT      CHECK (used_bytes >= 0),
    used_at          TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, name)
);

CREATE TABLE IF NOT EXISTS service_shared_volumes (
    service_id       UUID    NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    shared_volume_id UUID    NOT NULL REFERENCES shared_volumes(id) ON DELETE RESTRICT,
    target           TEXT    NOT NULL CHECK (target ~ '^/' AND target !~ '(^|/)\.\.(/|$)'),
    read_only        BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (service_id, target)
);
CREATE INDEX IF NOT EXISTS idx_service_shared_volumes_volume ON service_shared_volumes(shared_volume_id);

ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN ('service_down', 'service_restarting',
    'service_unhealthy', 'container_exited', 'server_unreachable', 'disk_full', 'volume_full'));
