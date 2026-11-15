-- 0083: an environment has one service per network alias.
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it.
--
-- The alias is the name an environment's other services reach a service by.
-- The service layer checked it by reading the environment's services and
-- then writing, so two creates (or a create and an edit) at once both
-- passed, and a failed read let anything through. The index holds it now;
-- the repository answers ErrAliasTaken (409 alias_taken) for it, as the
-- check did.
--
-- Rows from before: premigrate.sql (step 0083) renames the later of two
-- services that share an alias to <alias>-<first 8 of its id>, and says so
-- in a NOTICE; the oldest keeps it. A renamed service answers to its new
-- alias from its next deploy. Production had none on 2026-11-15.
--
-- Locks: the build takes SHARE on services (writes wait, reads don't) for
-- one scan of the table, inside Atlas's transaction, so not CONCURRENTLY;
-- services is hundreds of rows at most.
--
-- The previous release runs on this schema: it never names the index. Of
-- two of its requests racing for one alias, the loser now gets 409 with
-- the taken-name message (it reads every unique violation on services as
-- a taken name) where both used to succeed.

CREATE UNIQUE INDEX IF NOT EXISTS uq_services_alias_env
    ON services(environment_id, network_alias)
    WHERE environment_id IS NOT NULL AND network_alias <> '';

-- Rollback (reversible, and nothing to run for the previous release). The
-- aliases premigrate renamed stay renamed.
--   DROP INDEX IF EXISTS uq_services_alias_env;
