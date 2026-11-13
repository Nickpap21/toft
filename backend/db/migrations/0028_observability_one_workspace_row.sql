-- One workspace-level observability row per workspace (found while
-- rebuilding Projects, 2026-10-15; README §7).
--
-- UNIQUE (org_id, project_id) never fired for workspace-level rows: their
-- project_id is NULL, and Postgres treats two NULLs as distinct. So the
-- upsert's ON CONFLICT (org_id, project_id) never matched, every
-- workspace-level PUT /observability/{loki,grafana,prometheus} inserted a
-- row, and reads (WHERE project_id IS NULL LIMIT 1) got any one of them:
-- register Loki, then Grafana, and the panel saw only one.
--
-- As with the other numbered files, schema.sql is the source of truth and
-- Atlas applies the diff; this file documents the change. The data step
-- runs first, from premigrate.sql (deploy.sh and `make migrate` run it
-- before Atlas): it merges each workspace's rows into its oldest and
-- deletes the rest. Then Atlas replaces the constraint, which the upsert's
-- ON CONFLICT now matches for NULL project_ids too (Postgres 15+).

-- Data step: premigrate.sql, "0028".

ALTER TABLE observability_configs
    DROP CONSTRAINT observability_configs_org_id_project_id_key,
    ADD CONSTRAINT observability_configs_org_id_project_id_key UNIQUE NULLS NOT DISTINCT (org_id, project_id);

-- Rollback: Atlas, from the previous schema.sql, puts back the plain UNIQUE
-- (org_id, project_id). The merge is irreversible: the deleted rows are
-- gone, though every setting they held lives on in the kept row except
-- where two rows set the same one (the most recent wins). Nothing reads
-- the duplicates, so nothing needs them back.
