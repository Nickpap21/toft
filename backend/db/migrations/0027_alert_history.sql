-- Settings → Alerts shows the last delivery and the last 30 days of alerts
-- (D-46).
--
-- alert_channels.last_delivery_*: the last message the panel posted to the
-- workspace's webhook, an alert or Send test, and what the receiver
-- answered (its HTTP status, NULL when it gave none, and the error).
--
-- alerts.kind gains deploy_failed and oom_killed: one-off messages, stored
-- resolved as they fire so the list has them. Resolved alerts are already
-- deleted after 30 days (alert.Monitor), these included.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change. Columns are added
-- nullable or with defaults, and the CHECK only widens, so the running
-- backend keeps working before and after.

ALTER TABLE alert_channels
    ADD COLUMN IF NOT EXISTS last_delivery_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_delivery_status INT CHECK (last_delivery_status BETWEEN 100 AND 599),
    ADD COLUMN IF NOT EXISTS last_delivery_error TEXT NOT NULL DEFAULT '' CHECK (length(last_delivery_error) <= 500);

ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN ('service_down', 'service_restarting',
    'service_unhealthy', 'container_exited', 'server_unreachable', 'disk_full',
    'volume_full', 'backup_failed', 'deploy_failed', 'oom_killed'));

-- Rollback (reversible; the second statement deletes the stored one-off
-- messages, which the narrower CHECK no longer allows):
-- ALTER TABLE alert_channels DROP COLUMN IF EXISTS last_delivery_at,
--     DROP COLUMN IF EXISTS last_delivery_status, DROP COLUMN IF EXISTS last_delivery_error;
-- DELETE FROM alerts WHERE kind IN ('deploy_failed', 'oom_killed');
-- ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
-- ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN ('service_down', 'service_restarting',
--     'service_unhealthy', 'container_exited', 'server_unreachable', 'disk_full', 'volume_full', 'backup_failed'));
