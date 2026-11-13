-- Cycles, due dates and estimates (D-90, stage 4 session 4): a workspace's
-- cycle schedule and estimate scale, its cycles with what each carried over
-- and its daily counts, and each issue's cycle, start and due dates and
-- estimate, with their history kinds.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Everything is added or widened (D-63): v0.10.0 runs on it and ignores it.

ALTER TABLE organizations ADD COLUMN IF NOT EXISTS cycle_weeks SMALLINT CHECK (cycle_weeks BETWEEN 1 AND 4);
ALTER TABLE organizations ADD COLUMN IF NOT EXISTS cycle_start_day SMALLINT NOT NULL DEFAULT 1 CHECK (cycle_start_day BETWEEN 0 AND 6);
ALTER TABLE organizations ADD COLUMN IF NOT EXISTS cycle_time_zone TEXT NOT NULL DEFAULT 'UTC' CHECK (char_length(cycle_time_zone) BETWEEN 1 AND 64);
ALTER TABLE organizations ADD COLUMN IF NOT EXISTS issue_estimates TEXT NOT NULL DEFAULT 'off' CHECK (issue_estimates IN ('off', 'fibonacci', 'linear', 'tshirt'));

CREATE TABLE IF NOT EXISTS cycles (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id               UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    number               INTEGER     NOT NULL CHECK (number > 0),
    name                 TEXT        NOT NULL DEFAULT '' CHECK (char_length(name) <= 60 AND name !~ '[[:cntrl:]]'),
    starts_on            DATE        NOT NULL,
    ends_on              DATE        NOT NULL,
    closed_at            TIMESTAMPTZ,
    final_scope          INTEGER     CHECK (final_scope >= 0),
    final_shipped        INTEGER     CHECK (final_shipped >= 0),
    final_cancelled      INTEGER     CHECK (final_cancelled >= 0),
    final_carried        INTEGER     CHECK (final_carried >= 0),
    final_points         INTEGER     CHECK (final_points >= 0),
    final_shipped_points INTEGER     CHECK (final_shipped_points >= 0),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (org_id, number),
    CHECK (ends_on >= starts_on)
);

ALTER TABLE issues ADD COLUMN IF NOT EXISTS cycle_id UUID;
ALTER TABLE issues ADD COLUMN IF NOT EXISTS start_on DATE;
ALTER TABLE issues ADD COLUMN IF NOT EXISTS due_on DATE;
ALTER TABLE issues ADD COLUMN IF NOT EXISTS estimate SMALLINT CHECK (estimate BETWEEN 1 AND 100);
ALTER TABLE issues ADD CONSTRAINT issues_dates_in_order CHECK (start_on <= due_on);
ALTER TABLE issues ADD CONSTRAINT fk_issues_cycle FOREIGN KEY (cycle_id) REFERENCES cycles(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_issues_cycle ON issues (cycle_id) WHERE cycle_id IS NOT NULL;

ALTER TABLE issue_events DROP CONSTRAINT IF EXISTS issue_events_kind_check;
ALTER TABLE issue_events ADD CONSTRAINT issue_events_kind_check CHECK (kind IN ('opened', 'status', 'assigned', 'priority', 'labels', 'title', 'linked', 'merged',
    'type', 'parent', 'related', 'unrelated', 'unblocked', 'cloned', 'deploy_failed', 'cycle', 'start', 'due', 'estimate'));

CREATE TABLE IF NOT EXISTS cycle_carryovers (
    cycle_id    UUID NOT NULL REFERENCES cycles(id) ON DELETE CASCADE,
    issue_id    UUID NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    to_cycle_id UUID NOT NULL REFERENCES cycles(id) ON DELETE CASCADE,
    PRIMARY KEY (cycle_id, issue_id)
);
CREATE INDEX IF NOT EXISTS idx_cycle_carryovers_to ON cycle_carryovers (to_cycle_id, issue_id);

CREATE TABLE IF NOT EXISTS cycle_days (
    cycle_id       UUID    NOT NULL REFERENCES cycles(id) ON DELETE CASCADE,
    day            DATE    NOT NULL,
    scope          INTEGER NOT NULL CHECK (scope >= 0),
    started        INTEGER NOT NULL CHECK (started >= 0),
    shipped        INTEGER NOT NULL CHECK (shipped >= 0),
    scope_points   INTEGER NOT NULL DEFAULT 0 CHECK (scope_points >= 0),
    started_points INTEGER NOT NULL DEFAULT 0 CHECK (started_points >= 0),
    shipped_points INTEGER NOT NULL DEFAULT 0 CHECK (shipped_points >= 0),
    PRIMARY KEY (cycle_id, day)
);

-- Rollback (reversible; it loses every cycle, the cycle each issue is in,
-- its start and due dates and estimate, and the workspaces' schedules and
-- scales; the history's new rows go before the check narrows):
--   DROP TABLE IF EXISTS cycle_days;
--   DROP TABLE IF EXISTS cycle_carryovers;
--   DROP INDEX IF EXISTS idx_issues_cycle;
--   ALTER TABLE issues DROP CONSTRAINT IF EXISTS fk_issues_cycle;
--   ALTER TABLE issues DROP CONSTRAINT IF EXISTS issues_dates_in_order;
--   ALTER TABLE issues DROP COLUMN IF EXISTS cycle_id, DROP COLUMN IF EXISTS start_on, DROP COLUMN IF EXISTS due_on, DROP COLUMN IF EXISTS estimate;
--   DROP TABLE IF EXISTS cycles;
--   DELETE FROM issue_events WHERE kind IN ('cycle', 'start', 'due', 'estimate');
--   ALTER TABLE issue_events DROP CONSTRAINT IF EXISTS issue_events_kind_check;
--   ALTER TABLE issue_events ADD CONSTRAINT issue_events_kind_check CHECK (kind IN ('opened', 'status', 'assigned', 'priority', 'labels', 'title', 'linked', 'merged', 'type', 'parent', 'related', 'unrelated', 'unblocked', 'cloned', 'deploy_failed'));
--   ALTER TABLE organizations DROP COLUMN IF EXISTS cycle_weeks, DROP COLUMN IF EXISTS cycle_start_day, DROP COLUMN IF EXISTS cycle_time_zone, DROP COLUMN IF EXISTS issue_estimates;
