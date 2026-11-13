-- 0081: services.container_health and container_health_command.
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it.
--
-- A service's containers inherited their image's HEALTHCHECK with no way to
-- replace it: a worker built from a web service's Dockerfile was marked
-- unhealthy by an HTTP check on a port it doesn't serve. container_health
-- is 'image' (the image's, as before), 'off' (Docker's NONE) or 'command'
-- (CMD-SHELL container_health_command, at the image's timings).
--
-- Additive (D-63): NOT NULL columns with constant defaults (a catalog
-- change on Postgres 11+, no rewrite). The CHECKs are validated by reading
-- every row of services under ACCESS EXCLUSIVE: one row per service, so
-- milliseconds. Every row is 'image' and '', which the CHECKs accept. The previous release never names the columns (sqlc
-- lists its columns), so it runs on this schema and creates containers
-- with the image's check, as it always did.

ALTER TABLE services ADD COLUMN IF NOT EXISTS container_health TEXT NOT NULL DEFAULT 'image'
    CHECK (container_health IN ('image', 'off', 'command'));
ALTER TABLE services ADD COLUMN IF NOT EXISTS container_health_command TEXT NOT NULL DEFAULT ''
    CHECK (length(container_health_command) <= 1024 AND container_health_command !~ '[\r\n]');
ALTER TABLE services ADD CONSTRAINT services_container_health_command
    CHECK (container_health <> 'command' OR container_health_command <> '');

-- Rollback (reversible; nothing to run for the previous release, which works
-- on this schema). To drop them, once no release reads them, in contract.sql:
--   ALTER TABLE services DROP CONSTRAINT IF EXISTS services_container_health_command;
--   ALTER TABLE services DROP COLUMN IF EXISTS container_health_command;
--   ALTER TABLE services DROP COLUMN IF EXISTS container_health;
