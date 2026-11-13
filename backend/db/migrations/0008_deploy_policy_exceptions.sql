-- Deploy policy exceptions and compose domain routing (D-19, work package B).
--
-- 0006 is reserved by private/plans/2026-09-22-project-roles.md and
-- 0007 is instance operators (package A), so this is 0008. As with the other
-- numbered files, schema.sql is the source of truth and Atlas applies the
-- diff; this file documents the change.
--
-- Additive only: one new table and one new column, no change to existing
-- rows. Every existing service keeps running. A service whose stored
-- configuration now violates the policy is refused on its next deploy (or
-- allowed with a warning when TOFT_DEPLOY_POLICY=warn). Before upgrading, or
-- right after with TOFT_DEPLOY_POLICY=warn, an instance operator can list
-- those services in Settings → Deploy policy report (GET
-- /api/v1/deploy-policy/violations) and fix them or grant exceptions.
--
-- granted_by is SET NULL when the operator's account is deleted; the audit_log
-- row keeps who granted it.
--
-- services.compose_route_service: compose files can no longer set traefik.*
-- labels (reserved), so the platform routes a compose service's domains
-- itself, with labels it generates on one compose service and port. This
-- column holds that choice (Settings → Domain Routing); '' (the default for
-- every existing row) is automatic: the compose file's only service, or no
-- routing when it has several. An existing compose service that routed its
-- domains with its own traefik.* labels is refused on its next deploy until
-- those labels are removed and, for a file with several services, a service
-- is chosen.
--
-- Reversible: DROP TABLE service_deploy_policy_exceptions;
--             ALTER TABLE services DROP COLUMN compose_route_service;
CREATE TABLE IF NOT EXISTS service_deploy_policy_exceptions (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id    UUID        NOT NULL UNIQUE REFERENCES services(id) ON DELETE CASCADE,
    allowed_rules TEXT[]      NOT NULL DEFAULT '{}',
    reason        TEXT        NOT NULL DEFAULT '',
    granted_by    UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE services ADD COLUMN IF NOT EXISTS compose_route_service TEXT NOT NULL DEFAULT '';
