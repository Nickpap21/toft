-- 0082 (v1.0.2): deployments.reserved_memory_mb (D-166).
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it.
--
-- Each deploy that goes live records the memory its service's limits
-- reserved (deployment.Service.requestedLimits), in MiB. The room check on
-- the panel's own server lets a service deploy again with no more than its
-- live deployment recorded, also when the server's limits already add up to
-- more than it leaves its apps; a raised limit, or a service that never
-- went live, is new room.
--
-- Nullable, no default: the ADD COLUMN is a catalog change; its CHECK reads
-- the table once (deployments keeps the last 200 per service) with every
-- row NULL. No backfill: a live deployment without the number counts as
-- holding its service's limit as it is, which is what 1.0.1 ran it with
-- unless the limit was changed since. v1.0.1 never names the column (sqlc
-- lists its columns), so it runs on this schema.

ALTER TABLE deployments ADD COLUMN IF NOT EXISTS reserved_memory_mb DOUBLE PRECISION
    CHECK (reserved_memory_mb >= 0);

-- Rollback (reversible; nothing to run for v1.0.1, which works on this
-- schema). To drop it, once no release writes it, in contract.sql:
--   ALTER TABLE deployments DROP COLUMN IF EXISTS reserved_memory_mb;
