-- 0074: server_capabilities.address_pools.
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it.
--
-- The capability probe reads Docker's default-address-pools (docker info),
-- so a server's Firewall card shows the pool Docker on it uses, or that it
-- has none, rather than the pool the panel writes on fresh servers. NULL
-- until a probe read them: rows from before this release say "not read
-- yet" until the next probe (after provisioning, daily, or Check again).
--
-- Nullable, no default: the ADD COLUMN is a catalog change; its CHECK reads
-- the table once (one row per server) with every row NULL. The previous
-- release never names the column (sqlc lists its columns), so it runs on
-- this schema; its probes leave the column as the last one here wrote it.

ALTER TABLE server_capabilities ADD COLUMN IF NOT EXISTS address_pools JSONB
    CHECK (jsonb_typeof(address_pools) = 'array' AND octet_length(address_pools::text) <= 4096);

-- Rollback (reversible; nothing to run for the previous release, which works
-- on this schema). To drop it, once no release writes it, in contract.sql:
--   ALTER TABLE server_capabilities DROP COLUMN IF EXISTS address_pools;
