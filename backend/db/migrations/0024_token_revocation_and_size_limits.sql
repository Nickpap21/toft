-- Session revocation on password change, and size limits the database
-- enforces (security audit of 2026-10-15, plans S4 and S5).
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- users.tokens_valid_from: a session token (JWT) whose iat is before this
-- moment is refused by the auth middleware. A password change sets it to
-- NOW(), so every other session of the account ends; the API answers the
-- change with a fresh token for the session that made it. Existing rows get
-- NOW() at migration, which is before every token they hold... except
-- tokens issued earlier: those are refused once, and the user signs in
-- again. Deploy at a quiet moment or accept one re-sign-in per user.
--
-- The CHECKs match the API's limits, so a client that bypasses the handlers
-- still cannot store more: compose files and config files up to 1 MiB,
-- variable values up to 32 KiB (Docker's limit for one environment entry).
-- A pre-check against production found no row over any limit.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS tokens_valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE services
    ADD CONSTRAINT services_compose_content_size
    CHECK (compose_content IS NULL OR length(compose_content) <= 1048576);
ALTER TABLE env_vars
    ADD CONSTRAINT env_vars_value_size CHECK (length(value) <= 32768);
ALTER TABLE environment_env_vars
    ADD CONSTRAINT environment_env_vars_value_size CHECK (length(value) <= 32768);
ALTER TABLE service_config_files
    ADD CONSTRAINT service_config_files_content_size CHECK (length(content) <= 1048576);

-- Rollback (reversible; drops no data):
-- ALTER TABLE service_config_files DROP CONSTRAINT service_config_files_content_size;
-- ALTER TABLE environment_env_vars DROP CONSTRAINT environment_env_vars_value_size;
-- ALTER TABLE env_vars DROP CONSTRAINT env_vars_value_size;
-- ALTER TABLE services DROP CONSTRAINT services_compose_content_size;
-- ALTER TABLE users DROP COLUMN tokens_valid_from;
