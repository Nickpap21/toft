-- 0075: services.compose_network_service.
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it.
--
-- A compose stack's containers answer to their compose service names, and
-- the stack joins the app network only through the service its domains are
-- routed to, which the deploy picks (deployment.composeRoute): the file's
-- only service when routing is automatic, none without a domain. Each
-- successful compose deploy records its pick here, so connect_to names the
-- address that answers. NULL until a compose deploy recorded it; '' when
-- that deploy routed none.
--
-- Nullable, no default: the ADD COLUMN is a catalog change. premigrate.sql
-- adds it before Atlas runs and fills it once (0075 there) for the running
-- stacks it can name. The previous release never names the column (sqlc
-- lists its columns, its raw queries name theirs), so it runs on this
-- schema; its deploys leave the column as the last one here wrote it.

ALTER TABLE services ADD COLUMN IF NOT EXISTS compose_network_service TEXT;

-- Rollback (reversible; nothing to run for the previous release, which works
-- on this schema). To drop it, once no release writes it, in contract.sql:
--   ALTER TABLE services DROP COLUMN IF EXISTS compose_network_service;
-- premigrate.sql's 0073 and 0075 steps run while this column is missing:
-- remove them in the release that drops it.
