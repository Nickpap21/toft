-- 0085: the logs API key scope (issue #11). schema.sql is the source of
-- truth; Atlas applies it. This file records the change and how to undo it.
--
-- A project key with logs follows its services' live logs (toft logs), and
-- lists its project and services to resolve their names. It is a scope of
-- its own, not part of deploy: a service's runtime output can hold what the
-- app prints, and a key issued before this release must not gain it on
-- upgrade. A key limited to some services (K5) may hold it too.
--
-- Both CHECKs only widen (D-63): the previous release runs on this schema,
-- never creates a logs key, and ignores the scope on one it reads.
--
-- Locks: each constraint is replaced, which validates api_keys (a few
-- hundred rows at most) under ACCESS EXCLUSIVE.

ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_service_ids_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_service_ids_check CHECK (
    cardinality(service_ids) = 0
    OR (project_id IS NOT NULL AND scopes <@ ARRAY['deploy', 'variables', 'logs']::TEXT[])
);
ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_scope_check;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_scope_check CHECK (
    (project_id IS NULL AND cardinality(scopes) = 0)
    OR (project_id IS NOT NULL AND cardinality(scopes) > 0
        AND scopes <@ ARRAY['deploy', 'variables', 'code_read', 'code_write', 'checks', 'issues_read', 'issues_write',
                            'docs_read', 'docs_write', 'logs']::TEXT[])
);

-- Rollback (narrows: first delete the keys whose only scope is logs, then
-- take it from the rest; checked on a scratch database):
--   DELETE FROM api_keys WHERE scopes = ARRAY['logs']::TEXT[];
--   UPDATE api_keys SET scopes = array_remove(scopes, 'logs') WHERE 'logs' = ANY (scopes);
--   then the two constraints above without 'logs'.
