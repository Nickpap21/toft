-- Data steps Atlas can't express, run before every `atlas schema apply`
-- (`make migrate` and `toft upgrade`). schema.sql stays the
-- source of truth for the schema; each step here must be idempotent, so a
-- second run changes nothing, and must leave the data valid for the
-- schema.sql that follows. A fresh database has none of the tables yet, so
-- each step first checks that its table exists.

-- 0028: one workspace-level observability row per workspace. UNIQUE
-- (org_id, project_id) never matched two NULL project_ids, so each
-- workspace-level PUT /observability/{loki,grafana,prometheus} inserted a
-- row. Keep each workspace's oldest row, give it every setting the rows
-- hold (the most recently updated value where two rows set the same one),
-- and delete the rest, so schema.sql's UNIQUE NULLS NOT DISTINCT applies.
DO $$
BEGIN
    IF to_regclass('public.observability_configs') IS NULL THEN
        RETURN;
    END IF;
    LOCK TABLE observability_configs IN SHARE ROW EXCLUSIVE MODE;

    WITH ranked AS (
        SELECT id, first_value(id) OVER (PARTITION BY org_id ORDER BY created_at, id) AS keep_id
        FROM observability_configs
        WHERE project_id IS NULL
    ), merged AS (
        SELECT r.keep_id,
               (array_agg(o.loki_service_id       ORDER BY o.updated_at DESC) FILTER (WHERE o.loki_service_id IS NOT NULL))[1]       AS loki_service_id,
               (array_agg(o.grafana_service_id    ORDER BY o.updated_at DESC) FILTER (WHERE o.grafana_service_id IS NOT NULL))[1]    AS grafana_service_id,
               (array_agg(o.prometheus_service_id ORDER BY o.updated_at DESC) FILTER (WHERE o.prometheus_service_id IS NOT NULL))[1] AS prometheus_service_id,
               (array_agg(o.loki_url              ORDER BY o.updated_at DESC) FILTER (WHERE o.loki_url <> ''))[1]                    AS loki_url,
               (array_agg(o.grafana_url           ORDER BY o.updated_at DESC) FILTER (WHERE o.grafana_url <> ''))[1]                 AS grafana_url,
               (array_agg(o.prometheus_url        ORDER BY o.updated_at DESC) FILTER (WHERE o.prometheus_url <> ''))[1]              AS prometheus_url,
               max(o.updated_at) AS updated_at
        FROM ranked r
        JOIN observability_configs o ON o.id = r.id
        GROUP BY r.keep_id
        HAVING count(*) > 1
    )
    UPDATE observability_configs c
    SET loki_service_id       = m.loki_service_id,
        grafana_service_id    = m.grafana_service_id,
        prometheus_service_id = m.prometheus_service_id,
        loki_url              = m.loki_url,
        grafana_url           = m.grafana_url,
        prometheus_url        = m.prometheus_url,
        updated_at            = m.updated_at
    FROM merged m
    WHERE c.id = m.keep_id;

    DELETE FROM observability_configs c
    USING (
        SELECT id, first_value(id) OVER (PARTITION BY org_id ORDER BY created_at, id) AS keep_id
        FROM observability_configs
        WHERE project_id IS NULL
    ) r
    WHERE c.id = r.id AND r.id <> r.keep_id;
END $$;

-- 0031: environments get a colour and a protected flag (D-55). Existing
-- rows are set once from their name, the rule new environments follow, so
-- a production environment confirms its changes from the first deploy
-- after the upgrade. Runs only when the columns are missing, so a later
-- choice on the Environment page is never overwritten.
DO $$
BEGIN
    IF to_regclass('public.environments') IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'environments' AND column_name = 'color'
    ) THEN
        ALTER TABLE environments ADD COLUMN color TEXT NOT NULL DEFAULT 'gray'
            CHECK (color IN ('red', 'amber', 'violet', 'blue', 'teal', 'gray'));
        ALTER TABLE environments ADD COLUMN protected BOOLEAN NOT NULL DEFAULT false;
        UPDATE environments SET color = 'red', protected = true WHERE lower(name) IN ('production', 'prod');
        UPDATE environments SET color = 'amber' WHERE lower(name) IN ('staging', 'preprod');
        UPDATE environments SET color = 'violet' WHERE lower(name) IN ('test', 'qa');
        UPDATE environments SET color = 'teal' WHERE lower(name) IN ('dev', 'development');
        UPDATE environments SET color = 'blue' WHERE lower(name) = 'preview';
    END IF;
END $$;

-- 0041: workspaces from before issue keys had digits keep showing them
-- unpadded (STO-1); new ones get 3 (VLB-001, D-83). The column comes with
-- DEFAULT 0, so every existing workspace has 0, then schema.sql's
-- DEFAULT 3 is set for the rows that follow.
DO $$
BEGIN
    IF to_regclass('public.organizations') IS NULL THEN
        RETURN;
    END IF;
    ALTER TABLE organizations ADD COLUMN IF NOT EXISTS issue_key_digits SMALLINT NOT NULL DEFAULT 0;
    ALTER TABLE organizations ALTER COLUMN issue_key_digits SET DEFAULT 3;
END $$;

-- 0054: OAuth's dynamic client registration needs no account and kept any
-- length of redirect URI; schema.sql now bounds them. Registrations over
-- the bound that no connection uses are deleted, so the CHECK applies (a
-- client a person connected keeps its row, and the apply says why).
DO $$
BEGIN
    IF to_regclass('public.oauth_clients') IS NULL THEN
        RETURN;
    END IF;
    DELETE FROM oauth_clients c
    WHERE c.kind = 'dcr' AND length(array_to_string(c.redirect_uris, '')) > 20480
      AND NOT EXISTS (SELECT 1 FROM agent_connections a WHERE a.client_id = c.id);
END $$;

-- 0054: a workspace's name is 1 to 100 characters; creating one had no
-- bound. Longer names are cut to 100, so the CHECK applies.
DO $$
BEGIN
    IF to_regclass('public.organizations') IS NULL THEN
        RETURN;
    END IF;
    UPDATE organizations SET name = left(name, 100) WHERE length(name) > 100;
END $$;

-- 0060: red left the environment colours (D-142): it means failing, and a
-- protected environment's chip is drawn neutral with the lock. Red
-- environments become gray, keeping their protection. schema.sql's CHECK
-- still allows red, since the previous release writes it for a new
-- production; 0071's contract step tightens it, after this step. Runs every
-- time, so a red one written during the upgrade or a rollback turns gray
-- before schema.sql's CHECK applies.
DO $$
BEGIN
    IF to_regclass('public.environments') IS NULL OR NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'environments' AND column_name = 'color'
    ) THEN
        RETURN;
    END IF;
    UPDATE environments SET color = 'gray' WHERE color = 'red';
END $$;

-- 0061: a preview never migrates (D-144). The copies of previews made
-- before it, of an environment that wasn't protected, kept their migrate
-- and pre-deploy commands, and a push redeploys them as they are: clear
-- them. Runs every time, so a preview a rolled-back release made is
-- cleared at the next upgrade. Every copy (preview_of) too, its preview
-- gone or not, before schema.sql's services_preview_no_migrate applies
-- (0071).
DO $$
BEGIN
    IF to_regclass('public.previews') IS NULL OR to_regclass('public.services') IS NULL OR (
        SELECT count(*) FROM information_schema.columns
        WHERE table_name = 'services' AND column_name IN ('migrate_command', 'pre_deploy_commands', 'preview_of')
    ) < 3 THEN
        RETURN;
    END IF;
    UPDATE services SET migrate_command = '', pre_deploy_commands = ''
    WHERE (environment_id IN (SELECT environment_id FROM previews WHERE environment_id IS NOT NULL) OR preview_of IS NOT NULL)
      AND (migrate_command <> '' OR pre_deploy_commands <> '');
END $$;

-- 0066: Home's Finish setting up checklist (7g, D-151). A workspace that
-- already has a live service when it upgrades is marked hidden at once, so
-- it never falls back into the welcome layout; one that goes live later
-- keeps its checklist until it is complete or an owner hides it. Runs only
-- while the column is missing, so it runs once.
DO $$
BEGIN
    IF to_regclass('public.organizations') IS NULL OR to_regclass('public.deployments') IS NULL OR EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'organizations' AND column_name = 'setup_hidden_at'
    ) THEN
        RETURN;
    END IF;
    -- The old release still serves: give up rather than queue it behind the ALTER.
    PERFORM set_config('lock_timeout', '5s', true);
    ALTER TABLE organizations ADD COLUMN setup_hidden_at TIMESTAMPTZ;
    UPDATE organizations o SET setup_hidden_at = NOW()
    WHERE EXISTS (
        SELECT 1 FROM deployments d
        JOIN services s ON s.id = d.service_id
        LEFT JOIN environments e ON e.id = s.environment_id
        LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
        WHERE d.status = 'running' AND o.id = COALESCE(p.org_id, s.scoped_org_id)
    ) OR EXISTS (
        -- Its running deployment may be gone to log GC (the last 200 kept).
        SELECT 1 FROM services s
        LEFT JOIN environments e ON e.id = s.environment_id
        LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
        WHERE s.runtime_status IN ('running', 'unhealthy', 'stopped') AND o.id = COALESCE(p.org_id, s.scoped_org_id)
    );
END $$;

-- 0073: Add a server's loop made a join command, and a
-- server.agent_token_created row, on every re-render. Each burst keeps its
-- first row: a row goes when an earlier one has the same workspace, actor,
-- server and action within the 60 seconds before it (the DELETE compares
-- the rows as they were, so a burst of any length keeps one). A rejoin
-- command (metadata rejoin: password-confirmed, it replaces the pinned agent
-- key) is never the loop's: it is never deleted, and never the earlier row
-- that deletes another. Only on a database from before v0.36.0: while
-- services.compose_network_service (0075, added below) is missing, so never
-- on a fresh install's tables or after this upgrade, and later rows, a
-- person's own quick reloads, stay. Dropping that column re-arms this step:
-- remove it in the same release. The loop's dead join tokens go in the same
-- step (below). Irreversible: the deleted rows are not kept.
DO $$
BEGIN
    IF to_regclass('public.audit_log') IS NULL OR to_regclass('public.server_join_tokens') IS NULL OR to_regclass('public.services') IS NULL OR EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'services' AND column_name = 'compose_network_service'
    ) THEN
        RETURN;
    END IF;
    DELETE FROM audit_log a
    WHERE a.action = 'server.agent_token_created'
      AND a.metadata->>'rejoin' IS DISTINCT FROM 'true'
      AND EXISTS (
        SELECT 1 FROM audit_log b
        WHERE b.action = a.action AND b.org_id = a.org_id
          AND b.metadata->>'rejoin' IS DISTINCT FROM 'true'
          AND b.actor_id IS NOT DISTINCT FROM a.actor_id
          AND b.resource IS NOT DISTINCT FROM a.resource
          AND b.created_at >= a.created_at - INTERVAL '60 seconds'
          AND (b.created_at, b.id) < (a.created_at, a.id)
      );
    -- Its commands: each server keeps its used tokens and its newest unused
    -- one (whose expiry the sweep and the token_expires_at queries read);
    -- older unused ones, expired, can never join.
    DELETE FROM server_join_tokens t
    WHERE t.used_at IS NULL AND t.expires_at <= NOW()
      AND EXISTS (
        SELECT 1 FROM server_join_tokens n
        WHERE n.server_id = t.server_id AND n.used_at IS NULL
          AND (n.created_at, n.id) > (t.created_at, t.id)
      );
END $$;

-- 0075: services.compose_network_service, the compose service a stack's
-- last successful deploy routed (other services reach the stack by its
-- name). Added here so the running stacks the deploy would have routed get
-- it at once: an explicit compose_route_service, a domain, and running.
-- Every other row stays NULL ("not recorded") until its next compose
-- deploy writes it. Runs only while the column is missing, so once, and
-- never on a fresh install (Atlas creates services with it): later NULLs
-- are the deploys' to fill. It must stay after 0073's step, which reads the
-- column's absence. Reversible: the column is dropped in contract.sql once
-- no release writes it (0075_compose_network_service.sql).
DO $$
BEGIN
    IF to_regclass('public.services') IS NULL OR to_regclass('public.domains') IS NULL OR EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'services' AND column_name = 'compose_network_service'
    ) THEN
        RETURN;
    END IF;
    -- The old release still serves: give up rather than queue it behind the ALTER.
    PERFORM set_config('lock_timeout', '5s', true);
    ALTER TABLE services ADD COLUMN IF NOT EXISTS compose_network_service TEXT;
    UPDATE services s SET compose_network_service = s.compose_route_service
    WHERE s.source_type = 'docker-compose' AND s.compose_route_service <> ''
      AND s.runtime_status = 'running' AND s.compose_network_service IS NULL
      AND EXISTS (SELECT 1 FROM domains d WHERE d.service_id = s.id);
END $$;
