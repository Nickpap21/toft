-- Backups (D-43): backup_destinations, and 'backup_failed' in the alert kinds.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Additive. A backend from before 0022 ignores the table, and the servers
-- keep backing up with the env files they have (the timer and script stay;
-- the release note says how to stop them).
--
-- Rollback (the destinations are lost; the backups in the buckets stay, and
-- each needs its repository password to be read):
--   DELETE FROM alerts WHERE kind = 'backup_failed';
--   DROP TABLE IF EXISTS backup_destinations;
--   then the alerts kind CHECK without 'backup_failed' (schema.sql of 0021).

CREATE TABLE IF NOT EXISTS backup_destinations (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id     UUID        UNIQUE REFERENCES servers(id) ON DELETE CASCADE,
    endpoint      TEXT        NOT NULL CHECK (endpoint ~ '^https://[a-z0-9.-]+(:[0-9]{1,5})?$'),
    bucket        TEXT        NOT NULL CHECK (bucket ~ '^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$'),
    access_key    TEXT        NOT NULL CHECK (access_key ~ '^[A-Za-z0-9+/=_.~-]{1,255}$'),
    secret_key    TEXT        NOT NULL,
    repo_password TEXT        NOT NULL,
    schedule      TEXT        NOT NULL DEFAULT '03:00' CHECK (schedule ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    last_run_at   TIMESTAMPTZ,
    last_ok       BOOLEAN,
    last_ok_at    TIMESTAMPTZ,
    last_bytes    BIGINT      CHECK (last_bytes >= 0),
    last_error    TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS backup_destinations_one_panel_host ON backup_destinations ((server_id IS NULL)) WHERE server_id IS NULL;

ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN ('service_down', 'service_restarting',
    'service_unhealthy', 'container_exited', 'server_unreachable', 'disk_full', 'volume_full', 'backup_failed'));
