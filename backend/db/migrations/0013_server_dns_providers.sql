-- DNS providers for wildcard domains (the founders' notes from the first pilot, item 11;
-- design: private/specs/2026-10-11-wildcard-domains-design.md).
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Additive: a new table. A remote server with a row here gets a second ACME
-- resolver (letsencrypt-dns, DNS-01) at its next Provision, which wildcard
-- domains (*.example.com) need. credentials is a JSON object of the
-- provider's variables, encrypted with ENCRYPTION_KEY; the API returns only
-- their names. Deleting the server deletes its row. Rolling the binary back
-- is safe: older code never reads the table (and refuses wildcard domains).
--
-- Reversible: DROP TABLE server_dns_providers;
CREATE TABLE IF NOT EXISTS server_dns_providers (
    server_id   UUID        PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    provider    TEXT        NOT NULL CHECK (provider ~ '^[a-z0-9]{1,40}$'),
    credentials TEXT        NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
