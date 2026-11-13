-- Track whether Docker/Traefik provisioning has actually completed on a server,
-- separate from `status`, which also flips to 'active' on a bare SSH
-- test-connection success with no provisioning ever having run. NULL means
-- "not yet provisioned" — safe default, since Provision is idempotent and can
-- always be re-run for servers that were already set up before this column existed.
-- Reversible: ALTER TABLE servers DROP COLUMN provisioned_at;
ALTER TABLE servers
    ADD COLUMN IF NOT EXISTS provisioned_at TIMESTAMPTZ;
