-- 0064: Loki from the catalog is published on a 127.0.0.1 port of its own
-- server, so that server's services send it their logs with no public port
-- (D-148).
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it. Additive (D-63): the previous release never
-- reads or writes the column.
--
-- services.loopback_port: picked from 39100-39199 at the catalog Loki's
-- first deploy and kept (a move included), NULL for every other service.
-- Unique across the instance rather than per server, so a moved Loki never
-- finds its port taken; the ceiling is 100 catalog Lokis per instance, and
-- the CHECK may widen (D-63). A nullable column with no default: adding it
-- rewrites nothing; the inline CHECK scans services once (a small table).
-- The unique index covers only rows that have a port.

ALTER TABLE services ADD COLUMN IF NOT EXISTS loopback_port INTEGER CHECK (loopback_port BETWEEN 39100 AND 39199);
CREATE UNIQUE INDEX IF NOT EXISTS idx_services_loopback_port ON services (loopback_port) WHERE loopback_port IS NOT NULL;

-- Rollback (reversible; Loki gets a new port at its next deploy):
--   DROP INDEX IF EXISTS idx_services_loopback_port;
--   ALTER TABLE services DROP COLUMN IF EXISTS loopback_port;
