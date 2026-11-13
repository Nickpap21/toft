-- The last provisioning run of each server (D-48): who started it, when,
-- how it ended and its log, shown on the server's page.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Additive. A backend from before 0029 ignores the table.

CREATE TABLE IF NOT EXISTS server_provision_runs (
    server_id   UUID        PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    started_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    ok          BOOLEAN,
    error       TEXT        NOT NULL DEFAULT '' CHECK (length(error) <= 2000),
    log         TEXT        NOT NULL DEFAULT '' CHECK (length(log) <= 262144),
    CHECK ((finished_at IS NULL) = (ok IS NULL))
);

-- Rollback (reversible; the recorded runs are lost):
--   DROP TABLE IF EXISTS server_provision_runs;
