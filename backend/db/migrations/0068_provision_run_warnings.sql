-- 0068: a provisioning run that ends ok but without the firewall (ufw,
-- fail2ban) or the host firewall for app containers (D-37) finishes with
-- warnings naming them, and the Servers page lists the server under Needs
-- attention.
-- schema.sql is the source of truth; Atlas applies it. Additive (D-63): a
-- NOT NULL column with a constant default rewrites nothing, and the previous
-- release never reads it. A run the previous release makes after a rollback
-- leaves the column as it was; the next run of this one sets it again.

ALTER TABLE server_provision_runs ADD COLUMN IF NOT EXISTS warnings TEXT[] NOT NULL DEFAULT '{}' CHECK (cardinality(warnings) <= 10);

-- Rollback (reversible; the runs' warnings are lost):
--   ALTER TABLE server_provision_runs DROP COLUMN IF EXISTS warnings;
