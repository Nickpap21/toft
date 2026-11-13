-- Shared servers (private/specs/2026-10-12-shared-servers-design.md).
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Additive: a new table. A server's owner workspace shares it with another
-- workspace, which may then deploy to it within a CPU and memory share. Only
-- under isolated app networks. Deleting the server or either workspace
-- deletes the share. Rolling the binary back leaves the table unread: the
-- other workspace can no longer deploy to the server (its services there
-- are refused at their next deploy), and the owner's capacity is the whole
-- machine again.
--
-- Reversible: DROP TABLE server_shares;
CREATE TABLE IF NOT EXISTS server_shares (
    server_id  UUID        NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    org_id     UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    cpu_cores  NUMERIC     NOT NULL CHECK (cpu_cores > 0),
    memory_mb  BIGINT      NOT NULL CHECK (memory_mb > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (server_id, org_id)
);
CREATE INDEX IF NOT EXISTS idx_server_shares_org ON server_shares(org_id);
