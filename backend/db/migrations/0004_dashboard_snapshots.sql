-- Hourly point-in-time counts per org, written by a background goroutine
-- (same pattern as the existing hourly log-GC job in cmd/server/main.go).
-- Lets the dashboard show a real "vs ~24h ago" delta instead of a fake one.
-- Reversible: DROP TABLE dashboard_snapshots;
CREATE TABLE IF NOT EXISTS dashboard_snapshots (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    services_running INT         NOT NULL,
    services_failed  INT         NOT NULL,
    servers_online   INT         NOT NULL,
    servers_total    INT         NOT NULL,
    captured_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_dashboard_snapshots_org_captured ON dashboard_snapshots(org_id, captured_at DESC);
