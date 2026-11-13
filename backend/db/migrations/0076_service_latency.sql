-- 0076: service_latency, each routed service's requests per minute by
-- duration bucket, from its server's Traefik (D-156). The Service page's
-- Overview reads the last 24 hours' p95 from it; the collector writes it,
-- the hourly GC keeps 48 hours. A minute the Traefik could not be read is
-- a row with no buckets that says why (unmeasured).
-- schema.sql is the source of truth; Atlas applies it. Additive (D-63): a
-- new table the previous release never reads.

CREATE TABLE IF NOT EXISTS service_latency (
    service_id   UUID        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    collected_at TIMESTAMPTZ NOT NULL,
    buckets      INT[]       NOT NULL,
    unmeasured   TEXT        NOT NULL DEFAULT '' CHECK (unmeasured IN ('', 'agent', 'adopted', 'traefik')),
    CONSTRAINT service_latency_unmeasured_empty CHECK (unmeasured = '' OR cardinality(buckets) = 0),
    PRIMARY KEY (service_id, collected_at)
);

-- Rollback (reversible; the tile is hidden until 2 minutes of samples
-- refill it):
--   DROP TABLE IF EXISTS service_latency;
-- A rollback of the release alone needs none: the previous code ignores it.
