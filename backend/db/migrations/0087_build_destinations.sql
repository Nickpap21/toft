-- 0087: the operator's list of private addresses builds may reach (D-170).
-- schema.sql is the source of truth; Atlas applies it. This file records
-- the change and how to undo it.
--
-- build_destinations: per server (server_id NULL is the panel's), an
-- address or CIDR inside the private ranges with one TCP port or all of
-- them. The CHECK keeps loopback, link-local and public addresses out of
-- the table; the unique constraint counts NULLs as equal, so the panel's
-- server and "every port" can't hold the same entry twice. Deleted with its
-- server.
--
-- No index serves the list's read: it matches a server or NULL with IS NOT
-- DISTINCT FROM, which scans the table (EXPLAIN on PostgreSQL 16, with the
-- unique index offered). Nor one for created_by's foreign key: deleting a
-- user scans it too. The table holds at most 100 rows a server.
--
-- Additive (D-63): a new table the previous release never names; it runs on
-- this schema unchanged. A rollback to that release leaves the rows, and
-- the rules they wrote stay in each server's firewall until a release that
-- knows the list removes them.

CREATE TABLE IF NOT EXISTS build_destinations (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id  UUID        REFERENCES servers(id) ON DELETE CASCADE,
    cidr       CIDR        NOT NULL CHECK (family(cidr) = 4 AND (cidr <<= '10.0.0.0/8' OR cidr <<= '172.16.0.0/12'
                               OR cidr <<= '192.168.0.0/16' OR cidr <<= '100.64.0.0/10')),
    port       INTEGER     CHECK (port BETWEEN 1 AND 65535),
    note       TEXT        NOT NULL DEFAULT '' CHECK (char_length(note) <= 200 AND note !~ '[[:cntrl:]]'),
    created_by UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT build_destinations_entry UNIQUE NULLS NOT DISTINCT (server_id, cidr, port)
);

-- Rollback (reversible; the previous release never reads it). Once no
-- release reads it, in contract.sql:
--   DROP TABLE IF EXISTS build_destinations;
