-- services.deploy_strategy (D-42): how a deploy replaces the container.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- 'rolling' is the blue/green switch every service had: the new container
-- starts and is health-checked while the old one serves. 'recreate' stops
-- the old one first, so two containers never run on the same data, and
-- starts it again when the new one fails. 'auto', the default, is recreate
-- for a database or a cache (resources.KindOf) and rolling for anything
-- else. Compose stacks are not affected: docker compose replaces a changed
-- container in place.
--
-- Additive. A backend from before this release does not read the column and
-- deploys every service rolling, as it did.
--
-- Rollback (the choices made in Settings are lost; auto is what every
-- service had before):
--   ALTER TABLE services DROP COLUMN IF EXISTS deploy_strategy;

ALTER TABLE services ADD COLUMN IF NOT EXISTS deploy_strategy TEXT NOT NULL DEFAULT 'auto'
    CHECK (deploy_strategy IN ('auto', 'rolling', 'recreate'));
