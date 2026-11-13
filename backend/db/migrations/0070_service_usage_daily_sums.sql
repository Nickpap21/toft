-- 0070: the Service page's 7d range draws the same quantity as 1h and 24h
-- (7g, D-156). service_usage_daily keeps each compose service's largest
-- single container's memory peak (what a per-container limit is sized
-- against); this table keeps the peak and CPU p95 of the summed samples,
-- per compose service and, under compose_service '', the whole service.
-- The summariser fills it from the samples (kept 48 hours), so yesterday
-- and today get rows on the first run; older days stay without one and the
-- 7d range marks them until they age out of its 6 days.
-- schema.sql is the source of truth; Atlas applies it. Additive (D-63): a
-- new table the previous release never reads.

CREATE TABLE IF NOT EXISTS service_usage_daily_sums (
    service_id      UUID          NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    compose_service TEXT          NOT NULL DEFAULT '',
    day             DATE          NOT NULL,
    memory_peak_mb  NUMERIC(10,2) NOT NULL CHECK (memory_peak_mb >= 0),
    cpu_p95_percent NUMERIC(7,2)  NOT NULL CHECK (cpu_p95_percent >= 0),
    PRIMARY KEY (service_id, compose_service, day)
);

-- Rollback (reversible; 7d marks the days again until the summariser
-- refills yesterday and today):
--   DROP TABLE IF EXISTS service_usage_daily_sums;
-- A rollback of the release alone needs none: the previous code ignores it.
