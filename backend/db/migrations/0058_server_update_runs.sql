-- 0058: server updates, phase 2, actions (D-130, D-134;
-- private/plans/2026-10-30-server-updates-actions.md).
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it. Additive (D-63): the previous release (v0.31.x)
-- never reads the table.
--
-- server_update_runs is each server's run of updates in progress, or its
-- last: install updates, update Docker or reboot. The panel's own server
-- keeps its run in instance_settings (panel_update_run), as its read.

CREATE TABLE IF NOT EXISTS server_update_runs (
    server_id   UUID        PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    run_id      TEXT        NOT NULL CHECK (run_id ~ '^[0-9a-f]{32}$'),
    action      TEXT        NOT NULL CHECK (action IN ('install', 'docker', 'reboot')),
    started_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    outcome     TEXT        NOT NULL DEFAULT 'running' CHECK (outcome IN ('running', 'succeeded', 'failed')),
    packages    INTEGER     NOT NULL DEFAULT 0 CHECK (packages >= 0),
    error       TEXT        NOT NULL DEFAULT '' CHECK (char_length(error) <= 2000),
    output      TEXT        NOT NULL DEFAULT '' CHECK (octet_length(output) <= 131072),
    CHECK ((finished_at IS NULL) = (outcome = 'running'))
);

-- Rollback (reversible; it loses the last run of each server, whose
-- outcome is in the audit log; a run still going on a host goes on, and
-- the panel no longer follows it):
--   DROP TABLE IF EXISTS server_update_runs;
