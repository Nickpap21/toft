-- Alerts (the founders' notes from the first pilot, item 13; design:
-- private/specs/2026-10-11-alerts-design.md).
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- Additive: two new tables. alert_channels holds each workspace's webhook
-- (HTTPS only; the signing secret is encrypted with ENCRYPTION_KEY). alerts
-- holds open and recently resolved alerts; alerts_one_open makes "fires
-- once" a database rule, so a restart never re-sends an open alert.
-- Resolved rows older than 30 days are deleted by the monitor. Rolling the
-- binary back is safe: older code never reads either table.
--
-- Reversible: DROP TABLE alerts; DROP TABLE alert_channels;
CREATE TABLE IF NOT EXISTS alert_channels (
    org_id         UUID        PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    webhook_url    TEXT        NOT NULL CHECK (webhook_url ~ '^https://' AND length(webhook_url) <= 2048),
    signing_secret TEXT        NOT NULL,
    enabled        BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS alerts (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    kind        TEXT        NOT NULL CHECK (kind IN ('service_down', 'service_restarting',
                    'service_unhealthy', 'container_exited', 'server_unreachable', 'disk_full')),
    subject     TEXT        NOT NULL,
    detail      TEXT        NOT NULL,
    fired_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ,
    CONSTRAINT alerts_resolved_after_fired CHECK (resolved_at IS NULL OR resolved_at >= fired_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS alerts_one_open ON alerts (kind, subject) WHERE resolved_at IS NULL;
