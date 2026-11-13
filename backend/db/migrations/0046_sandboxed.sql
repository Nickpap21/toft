-- The Sandboxed level (D-95, stage 5 session 2): each workspace's default
-- isolation, each service's own choice, and the last gVisor install on each
-- server.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Everything is added (D-63): v0.12.0 runs on it and ignores it, so while
-- it runs, a Sandboxed service deploys as Standard (runc).

ALTER TABLE organizations ADD COLUMN IF NOT EXISTS default_isolation TEXT NOT NULL DEFAULT 'standard'
    CHECK (default_isolation IN ('standard', 'sandboxed'));

ALTER TABLE services ADD COLUMN IF NOT EXISTS isolation TEXT CHECK (isolation IN ('standard', 'sandboxed'));

CREATE TABLE IF NOT EXISTS server_gvisor_runs (
    server_id   UUID        PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    release     TEXT        NOT NULL CHECK (char_length(release) BETWEEN 1 AND 40),
    started_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    ok          BOOLEAN,
    error       TEXT        NOT NULL DEFAULT '' CHECK (length(error) <= 2000),
    log         TEXT        NOT NULL DEFAULT '' CHECK (length(log) <= 262144),
    CHECK ((finished_at IS NULL) = (ok IS NULL))
);

-- Rollback (reversible; it loses each workspace's default, each service's
-- choice and the install records; gVisor stays installed on the servers):
--   DROP TABLE IF EXISTS server_gvisor_runs;
--   ALTER TABLE services DROP COLUMN IF EXISTS isolation;
--   ALTER TABLE organizations DROP COLUMN IF EXISTS default_isolation;
