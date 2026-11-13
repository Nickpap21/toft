-- 0055: S5 of the 2026-10-26 security close-out: keys and exceptions
-- (private/plans/2026-10-26-security-close-out.md, D-124).
-- schema.sql is the source of truth; Atlas applies it. This file records the
-- change and how to undo it. All additive (D-63): the previous release
-- (v0.24.x) never reads the new columns and table.
--
-- K4: API keys never expired. expires_at NULL is a key that never expires,
-- which every existing key keeps; the API gives new keys 90 days unless
-- asked otherwise. On a rollback, expired keys work again.

ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ
    CHECK (expires_at > created_at);

-- K5: a project key reached every service in its project. service_ids
-- limits it to some (empty: the whole project), and only with the deploy
-- and variables scopes. On a rollback, such keys reach their whole project
-- within their scopes: revoke them first.

ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS service_ids UUID[] NOT NULL DEFAULT '{}';
ALTER TABLE api_keys ADD CONSTRAINT api_keys_service_ids_check CHECK (
    cardinality(service_ids) = 0
    OR (project_id IS NOT NULL AND scopes <@ ARRAY['deploy', 'variables']::TEXT[])
);

-- K3: a deploy policy exception covered a rule with any value. It now
-- approves the values the service had when it was granted; NULL for
-- exceptions granted before, which keep covering by rule. The last
-- deploy-time check of a compose file from a repository is kept, so an
-- operator can approve what it found. On a rollback, exceptions cover by
-- rule again.

ALTER TABLE service_deploy_policy_exceptions ADD COLUMN IF NOT EXISTS allowed_values JSONB
    CHECK (allowed_values IS NULL OR jsonb_typeof(allowed_values) = 'array');

CREATE TABLE IF NOT EXISTS service_deploy_policy_checks (
    service_id UUID        PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    commit_sha TEXT        NOT NULL DEFAULT '',
    checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    violations JSONB       NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(violations) = 'array')
);

-- Undo (not run by toft upgrade, which never drops; for a development
-- database only):
--   DROP TABLE IF EXISTS service_deploy_policy_checks;
--   ALTER TABLE service_deploy_policy_exceptions DROP COLUMN IF EXISTS allowed_values;
--   ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_service_ids_check;
--   ALTER TABLE api_keys DROP COLUMN IF EXISTS service_ids;
--   ALTER TABLE api_keys DROP COLUMN IF EXISTS expires_at;
