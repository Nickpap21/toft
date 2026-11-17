-- 0084: a health check passes on an answer below health_check_pass_below
-- (issue #12). schema.sql is the source of truth; Atlas applies it. This
-- file records the change and how to undo it.
--
-- A new web app checks its health over HTTP from its first deploy (GET /,
-- five attempts), and an answer below 500 passes: a 404 is an app that is
-- up, not one that is down. The stricter rule, below 400 (what curl -f
-- did, and what every check did before), stays one setting away.
--
-- Rows from before: premigrate.sql (step 0084) adds the column with 400,
-- so their checks judge as they did; Atlas then makes 500 the default for
-- new rows and adds the CHECK (a new column: nothing it narrows). The same
-- step brings a timeout outside 1 to 60 s or an attempt count outside 0 to
-- 10 into range, each in its workspace's audit log
-- (service.health_check_clamped, no actor, the values before and after).
-- The CHECKs on those two follow one release later (contract.sql).
--
-- Locks: ADD COLUMN with a constant default changes the catalog only, no
-- rewrite; the clamp updates the few rows out of range. services is
-- hundreds of rows at most.
--
-- The previous release runs on this schema: it never names the column. A
-- service it creates gets 500, which its own checks (curl -f) ignore.

ALTER TABLE services ADD COLUMN IF NOT EXISTS health_check_pass_below SMALLINT NOT NULL DEFAULT 500
    CHECK (health_check_pass_below IN (400, 500));

-- Rollback (reversible, and nothing to run for the previous release). The
-- clamped values stay; the audit log has what each was.
--   ALTER TABLE services DROP COLUMN IF EXISTS health_check_pass_below;
