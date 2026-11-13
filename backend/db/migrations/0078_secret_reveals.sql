-- 0078: a catalog app's admin password kept for its creator until dismissed
-- (D-160). schema.sql is the source of truth; Atlas applies it. This file
-- records the change and how to undo it.
--
-- secret_reveals: one sealed copy per person and service, deleted on
-- dismissal, after 7 days (the hourly clean-up), or with the service or the
-- person (ON DELETE CASCADE; idx_secret_reveals_user serves the users FK).
--
-- Additive (D-63): a new table the previous release never names; it runs on
-- this schema unchanged.

CREATE TABLE IF NOT EXISTS secret_reveals (
    id          UUID        PRIMARY KEY,
    service_id  UUID        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    secret_key  TEXT        NOT NULL CHECK (length(secret_key) <= 255),
    sealed      TEXT        NOT NULL,
    reason      TEXT        NOT NULL CHECK (reason IN ('created', 'reset')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT secret_reveals_service_user UNIQUE (service_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_secret_reveals_user ON secret_reveals (user_id);

-- Rollback (reversible; the previous release never reads it). Once no
-- release reads it, in contract.sql:
--   DROP TABLE IF EXISTS secret_reveals;
