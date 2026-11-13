-- Operator-issued password resets (D-56). An instance operator issues a
-- one-time link for an account that forgot its password; the holder sets a
-- new password within an hour, every other session of the account ends
-- (users.tokens_valid_from, 0024) and they are signed in. The URL carries
-- the raw token; the table stores only its sha256. Issuing a new link
-- deletes the account's earlier unused rows, so one link is live at a time.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Additive. A backend from before 0032 ignores the table.

CREATE TABLE IF NOT EXISTS password_resets (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_by UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT        NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Rollback (reversible; unused links stop working):
--   DROP TABLE IF EXISTS password_resets;
