-- 0080: branch protection is stored by the branch's name (security audit
-- L2). schema.sql is the source of truth; Atlas applies it. This file
-- records the change and how to undo it.
--
-- pre-receive protected whatever HEAD named at the push, and moving HEAD
-- (default_branch) needed no password, so an owner's API key could move it,
-- rewrite the old default branch (which deploys) and move it back. The hook
-- now protects protected_branch, set when protection is turned on and moved
-- with the default branch, which needs an owner's session and password.
--
-- Backfill: SQL can't read git, so the backend does it. A protected
-- repository whose protected_branch is NULL (all of them, after this
-- change) is protected at each push by HEAD's name as before, and the first
-- push to a repository with a branch stores that name (only while it is
-- still on and NULL). Nothing is unprotected in between: until it is
-- stored, the push reads HEAD as the previous release did, and HEAD moves
-- only through an owner with the password.
--
-- Additive (D-63): a nullable column with no default (no rewrite) the
-- previous release never names. Rolled back to v0.45.4, protect_default_branch
-- still works as it did (HEAD at the push); a turn on or off there leaves
-- protected_branch as it was, which this release corrects when it next
-- turns protection on or moves the default branch.

ALTER TABLE repositories ADD COLUMN IF NOT EXISTS protected_branch TEXT CHECK (protected_branch <> '');

-- Rollback (reversible; it loses which branch is protected, and the hook
-- reads HEAD again). Once no release reads it, in contract.sql:
--   ALTER TABLE repositories DROP COLUMN IF EXISTS protected_branch;
