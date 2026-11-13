-- 0054: fixes from the pre-launch security audit of 2026-10-26
-- (private/reviews/2026-10-26-prelaunch-security-audit.md). schema.sql is the
-- source of truth; Atlas applies it. This file records the change and how to
-- undo it.
--
-- M8: OAuth's dynamic client registration needs no account, and stored any
-- length of redirect URI for a day. The API now refuses a URI over 2048
-- characters; the database bounds the ten at 20480. The previous release
-- (v0.21.x) accepts longer ones and fails on this CHECK with a 500 instead
-- of a 400: no real client registers such URIs. This CHECK narrows, which
-- D-63 otherwise forbids, because anyone could fill the table before it.
-- premigrate.sql first deletes registrations over the bound that no
-- connection uses.

ALTER TABLE oauth_clients ADD CONSTRAINT oauth_clients_redirect_uris_size
    CHECK (length(array_to_string(redirect_uris, '')) <= 20480);

-- L9: signing out ended nothing on the server; a token taken from a shared
-- machine stayed valid for 30 days. Signing out now records the token's
-- sha256 until it expires, and the auth middleware refuses it.

CREATE TABLE IF NOT EXISTS revoked_sessions (
    token_hash TEXT        PRIMARY KEY CHECK (length(token_hash) = 64),
    expires_at TIMESTAMPTZ NOT NULL
);

-- L11: a new workspace's name had no bound (renaming had 100). The API
-- takes 1 to 100 characters everywhere now, and so does the database.
-- premigrate.sql first shortens longer names, which only that gap made.
-- The previous release (v0.21.x) fails on this CHECK with a 500 for a new
-- workspace named longer; the CHECK narrows, as the one above does, so the
-- database holds the bound the API now gives.

ALTER TABLE organizations ADD CONSTRAINT organizations_name_length
    CHECK (length(name) BETWEEN 1 AND 100);

-- L12: invite links sat in the database as they are, so a dump of it
-- joined any workspace with a pending invite. token now holds the link's
-- sha256, and token_encrypted the link under ENCRYPTION_KEY, for owners to
-- copy again. The backend converts older rows at start. The previous release
-- looks links up as they are: on a rollback, links made after this one stop
-- working (owners make new ones) and it lists them wrong.

ALTER TABLE org_invites ADD COLUMN IF NOT EXISTS token_encrypted TEXT;

-- Undo (not run by toft upgrade, which never drops; for a development
-- database only):
--   ALTER TABLE oauth_clients DROP CONSTRAINT IF EXISTS oauth_clients_redirect_uris_size;
--   DROP TABLE IF EXISTS revoked_sessions;
--   ALTER TABLE organizations DROP CONSTRAINT IF EXISTS organizations_name_length;
--   ALTER TABLE org_invites DROP COLUMN IF EXISTS token_encrypted;
