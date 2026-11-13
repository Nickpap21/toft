-- 0069: restarts over 24 hours on the service's resources (7g, D-156).
-- Each sample stores its compose service's restarts since the previous
-- sample: the collector compares each container's Docker RestartCount with
-- its last one, so a redeploy's new containers (which start at 0) and a
-- container that drops out of a sample and returns add only their own
-- restarts. GET /services/{id}/resources sums them over the last 24 hours.
-- schema.sql is the source of truth; Atlas applies it. Additive (D-63): a
-- nullable column with no default rewrites nothing (the CHECK scans the
-- table once: service_metrics keeps 48 hours), and the previous release
-- never reads it.

ALTER TABLE service_metrics ADD COLUMN IF NOT EXISTS restarts INT CHECK (restarts >= 0);

-- Rollback (reversible; the counts start again from the next samples):
--   ALTER TABLE service_metrics DROP COLUMN IF EXISTS restarts;
-- A rollback of the release alone needs none: the previous code ignores it.
