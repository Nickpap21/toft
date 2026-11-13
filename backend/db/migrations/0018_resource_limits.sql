-- Resource limits (D-41, private/specs/2026-10-12-resource-limits-design.md).
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Additive: one column on services, four on service_metrics, four new tables.
-- The previous backend ignores them.
--
-- Rollback (the block below). It drops usage history, OOM kills and the
-- compose limits set in the panel; none of that can be recovered.
--
-- Rollback:
--   DROP TABLE server_usage_daily;
--   DROP TABLE service_usage_daily;
--   DROP TABLE service_oom_kills;
--   DROP TABLE service_compose_limits;
--   DROP INDEX idx_service_metrics_collected_at;
--   ALTER TABLE service_metrics DROP COLUMN cpu_wait_pct, DROP COLUMN throttled_pct,
--       DROP COLUMN memory_peak_mb, DROP COLUMN compose_service;
--   ALTER TABLE services DROP COLUMN memory_limit_grace_until;
--   -- Only before upgrading again after a rollback, so the new upgrade sets grace dates:
--   DELETE FROM instance_settings WHERE key = 'resource_limits_grace_set';

ALTER TABLE services ADD COLUMN IF NOT EXISTS memory_limit_grace_until TIMESTAMPTZ;

ALTER TABLE service_metrics
    ADD COLUMN IF NOT EXISTS compose_service TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS memory_peak_mb  NUMERIC(10,2),
    ADD COLUMN IF NOT EXISTS throttled_pct   NUMERIC(5,2) CHECK (throttled_pct BETWEEN 0 AND 100),
    ADD COLUMN IF NOT EXISTS cpu_wait_pct    NUMERIC(5,2) CHECK (cpu_wait_pct BETWEEN 0 AND 100);

CREATE TABLE IF NOT EXISTS service_compose_limits (
    service_id      UUID    NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    compose_service TEXT    NOT NULL CHECK (compose_service <> ''),
    memory_mb       BIGINT  NOT NULL CHECK (memory_mb >= 64),
    cpu_cores       NUMERIC CHECK (cpu_cores IS NULL OR cpu_cores > 0),
    PRIMARY KEY (service_id, compose_service)
);

CREATE TABLE IF NOT EXISTS service_oom_kills (
    id              BIGSERIAL   PRIMARY KEY,
    service_id      UUID        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    compose_service TEXT        NOT NULL DEFAULT '',
    container       TEXT        NOT NULL,
    killed_at       TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS service_usage_daily (
    service_id        UUID    NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    compose_service   TEXT    NOT NULL DEFAULT '',
    day               DATE    NOT NULL,
    samples           INT     NOT NULL CHECK (samples >= 0),
    memory_peak_mb    NUMERIC(10,2) NOT NULL,
    memory_p95_mb     NUMERIC(10,2) NOT NULL,
    cpu_p95_percent   NUMERIC(7,2)  NOT NULL,
    throttled_max_pct NUMERIC(5,2),
    cpu_wait_avg_pct  NUMERIC(5,2),
    oom_kills         INT     NOT NULL DEFAULT 0 CHECK (oom_kills >= 0),
    PRIMARY KEY (service_id, compose_service, day)
);

CREATE TABLE IF NOT EXISTS server_usage_daily (
    server_id        UUID    NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    day              DATE    NOT NULL,
    samples          INT     NOT NULL CHECK (samples >= 0),
    cpu_wait_sum_pct NUMERIC(12,2) NOT NULL,
    cpu_wait_max_pct NUMERIC(5,2)  NOT NULL,
    PRIMARY KEY (server_id, day)
);

-- SummariseUsageDays and DeleteServiceMetricsBefore scan by time.
CREATE INDEX IF NOT EXISTS idx_service_metrics_collected_at ON service_metrics(collected_at);
-- CountOOMKillsSince and the daily summary read one service's kills by time.
CREATE INDEX IF NOT EXISTS idx_service_oom_kills_service_time ON service_oom_kills(service_id, killed_at);
-- One row per kill: the collector's event windows overlap at their edges and
-- after a restart, and InsertOOMKill skips a kill already recorded.
CREATE UNIQUE INDEX IF NOT EXISTS uq_service_oom_kills_kill ON service_oom_kills(service_id, container, killed_at);
-- DeleteUsageBefore (the primary key leads with service_id).
CREATE INDEX IF NOT EXISTS idx_service_usage_daily_day ON service_usage_daily(day);
