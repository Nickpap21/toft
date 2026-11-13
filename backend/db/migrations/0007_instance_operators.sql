-- Instance operators and instance-level settings (D-19 Phase 0, D-21 Team mode).
-- Operators are separate from org roles (D-14); users.role is 'admin' on every
-- row and is not used for this.
--
-- No SQL backfill: declarative Atlas cannot run one. On startup the backend's
-- internal/instance Bootstrap creates operators only while the instance has no
-- active operator (break-glass): it grants the accounts listed in
-- TOFT_OPERATOR_EMAILS or, when that is unset and no operator exists at all,
-- the earliest-created active user (logged at WARN). Once an active operator
-- exists, TOFT_OPERATOR_EMAILS is ignored, so an API revoke stays revoked.
--
-- No seed row either: a missing 'registration' row in instance_settings means
-- invite_only. Registration modes and operator sources are validated in Go,
-- not by CHECK constraints, so adding one needs no migration.
--
-- uq_users_email_lower makes emails unique ignoring case. If two accounts
-- already differ only by case, creating it fails and the apply stops: change
-- one of the two emails first. Find them with:
--   SELECT lower(email), array_agg(email) FROM users GROUP BY 1 HAVING count(*) > 1;
--
-- Rolling back to a commit whose schema.sql lacks these objects makes Atlas
-- drop them; re-upgrading runs Bootstrap again.
-- Reversible: DROP TABLE instance_operators; DROP TABLE instance_settings; DROP INDEX uq_users_email_lower;
CREATE TABLE IF NOT EXISTS instance_settings (
    key        TEXT        PRIMARY KEY,
    value      JSONB       NOT NULL,
    updated_by UUID        REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS instance_operators (
    user_id    UUID        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    granted_by UUID        REFERENCES users(id) ON DELETE SET NULL,
    source     TEXT        NOT NULL,           -- first_run | bootstrap | env | api
    granted_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_users_email_lower ON users (lower(email));
