-- 0072 (v0.35.0): deployments.settings_fingerprint (D-159).
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it.
--
-- Each deploy that goes live records an HMAC of what it shipped of its
-- service's settings (deployment.Service.SettingsFingerprint), less its
-- routing; none when a setting changed while it ran. Toft's
-- redeploy for a temporary address's certificate runs only while the live
-- deployment's matches the settings now, so it never ships a change that
-- was saved but not deployed.
--
-- Nullable, no default: the ADD COLUMN is a catalog change; its CHECK reads
-- the table once (deployments keeps the last 200 per service) with every
-- row NULL. v0.34.0 never names the column (sqlc lists its columns), so it
-- runs on this schema.

ALTER TABLE deployments ADD COLUMN IF NOT EXISTS settings_fingerprint TEXT
    CHECK (settings_fingerprint ~ '^[0-9a-f]{64}$');

-- Rollback (reversible; nothing to run for v0.34.0, which works on this
-- schema). To drop it, once no release writes it, in contract.sql:
--   ALTER TABLE deployments DROP COLUMN IF EXISTS settings_fingerprint;
