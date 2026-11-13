-- 0065: a domain's certificate, seen (D-150). A web service gets an address
-- when it is created; under sslip.io, Let's Encrypt's limit is shared by
-- everyone, so a certificate can take hours. The panel checks each routed
-- name over TLS every few minutes and records when a valid certificate first
-- answered; until then the name shows "certificate pending", and a temporary
-- one answers over HTTP.
-- schema.sql is the source of truth; Atlas applies it. Additive (D-63): a
-- nullable column with no default rewrites nothing, and the previous release
-- never reads it.

ALTER TABLE domains ADD COLUMN IF NOT EXISTS certificate_at TIMESTAMPTZ;

-- Rollback (reversible; the next checks fill it again):
--   ALTER TABLE domains DROP COLUMN IF EXISTS certificate_at;
