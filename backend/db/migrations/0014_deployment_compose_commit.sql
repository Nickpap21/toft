-- The commit a compose service's repository was checked out at (README §7
-- item 3).
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Additive: a nullable column. A compose deploy from a repository records
-- the commit it checked out, and a rollback checks that commit out again.
-- Before this, a rollback replayed IMAGE_TAG against the branch tip's files.
-- Deployments made before this change keep NULL and still roll back that way.
-- Rolling the binary back is safe: older code never reads the column.
-- Deploying an older commit drops it (deploy.sh applies schema.sql
-- declaratively), which loses only these recorded commits.
--
-- Reversible: ALTER TABLE deployments DROP COLUMN compose_commit_sha;
ALTER TABLE deployments
    ADD COLUMN IF NOT EXISTS compose_commit_sha TEXT
    CHECK (compose_commit_sha ~ '^[0-9a-f]{40}$');
