-- The panel's own server's usage history (D-68).
--
-- The backend reads the host's CPU, memory and disk every 5 seconds for
-- Instance -> Server's live view and keeps one sample a minute here for its
-- 24-hour and 7-day charts; samples older than 7 days are deleted as new
-- ones arrive.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Additive. A backend from before 0034 ignores the table.

CREATE TABLE IF NOT EXISTS panel_samples (
    taken_at          TIMESTAMPTZ PRIMARY KEY,
    cpu_percent       REAL        NOT NULL CHECK (cpu_percent >= 0),
    memory_used_bytes BIGINT      NOT NULL CHECK (memory_used_bytes >= 0),
    disk_used_bytes   BIGINT      NOT NULL CHECK (disk_used_bytes >= 0)
);

-- Rollback (reversible; the history is lost):
--   DROP TABLE IF EXISTS panel_samples;
