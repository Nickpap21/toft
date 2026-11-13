-- The theme is saved to the account, so every browser a user signs in on
-- uses it (D-32: "per browser in R1, per account in R2"; D-46). NULL until
-- the user picks one; the browser's own choice is uploaded the first time.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change. A nullable column:
-- the running backend's queries name their columns, so it keeps working
-- before and after.

ALTER TABLE users ADD COLUMN IF NOT EXISTS theme TEXT
    CHECK (theme IN ('system:graphite-paper', 'system:mono-daylight',
                     'graphite', 'paper', 'mono', 'daylight', 'ink', 'contrast'));

-- Rollback (reversible; drops only the saved themes, which each browser
-- still keeps for itself):
-- ALTER TABLE users DROP COLUMN IF EXISTS theme;
