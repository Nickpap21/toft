-- The panel's own alerts (D-68).
--
-- Three kinds about the control plane (subject 'control-plane'):
-- panel_container (a panel container not running or failing its health
-- check), panel_certificate (the panel's certificate not renewing) and
-- panel_upgrade_failed (an upgrade from the panel that rolled back or
-- stopped, a one-off). They go to every workspace an instance operator
-- owns, as the control plane's other alerts do.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change.
--
-- The CHECK only widens (D-63): a backend from before 0035 never writes the
-- new kinds.

ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN ('service_down', 'service_restarting',
    'service_unhealthy', 'container_exited', 'server_unreachable', 'disk_full',
    'volume_full', 'backup_failed', 'deploy_failed', 'oom_killed',
    'panel_container', 'panel_certificate', 'panel_upgrade_failed'));

-- Rollback (reversible once no row has a new kind; delete those first):
--   DELETE FROM alerts WHERE kind IN ('panel_container', 'panel_certificate', 'panel_upgrade_failed');
--   ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_kind_check;
--   ALTER TABLE alerts ADD CONSTRAINT alerts_kind_check CHECK (kind IN ('service_down', 'service_restarting',
--       'service_unhealthy', 'container_exited', 'server_unreachable', 'disk_full',
--       'volume_full', 'backup_failed', 'deploy_failed', 'oom_killed'));
