import type { components } from "./schema";
import { ApiError, fetchAPI, fetchVersioned, reauthHeader } from "./client";

// Deploy policy (D-19). Every deploy is checked; violations are refused
// (enforce mode) or only reported (warn mode). Instance operators can grant
// a service an audited exception for specific grantable rules.

export type DeployPolicyMode = "enforce" | "warn";

export interface PolicyViolation {
    rule: string;
    /** Compose service the violation is in, when there is one. */
    service?: string;
    /** Path to the offending setting, e.g. "services.web.volumes[0]". */
    field: string;
    /** What the configuration does, e.g. "volume 1 mounts the server folder /srv". */
    message: string;
    /** The rule's explanation, the same for every violation of it. */
    title?: string;
    why?: string;
    fix?: string;
    /** An instance operator can grant an exception for the rule. */
    grantable?: boolean;
    /** What an exception pins (D-124): the host path, the capability list,
     *  the command; secrets redacted. */
    value?: string;
    /** The image the value runs with: an image reference, "build <context>
     *  <dockerfile>", or a git service's repository. */
    image?: string;
    state?: ViolationState;
}

/** What the service's exception makes of a violation: pinned (a value it
 *  approved), granted (any value: control_plane, net_raw, or an exception
 *  from before v0.25.0), pending (not approved; a deploy is refused). */
export type ViolationState = "pinned" | "granted" | "pending" | "not_granted";

/** A violation an exception approved, as it was when granted. */
export interface PinnedValue {
    rule: string;
    service?: string;
    field: string;
    value: string;
    image?: string;
    message: string;
}

/** What the last deploy found in a compose file from a repository. */
export interface DeployCheck {
    commit_sha: string;
    checked_at: string;
    violations: PolicyViolation[];
}

export interface PolicyRule {
    rule: string;
    grantable: boolean;
    description: string;
}

export interface DeployPolicyException {
    service_id: string;
    allowed_rules: string[];
    reason: string;
    granted_by: string;
    granted_by_name: string;
    granted_by_email: string;
    created_at: string;
    updated_at: string;
    /** Null for an exception from before v0.25.0, which covers any value. */
    allowed_values: PinnedValue[] | null;
}

export interface ServiceDeployPolicy {
    /** The version of the exception read here: what a grant must send back. */
    version: string;
    service_id: string;
    mode: DeployPolicyMode;
    violations: PolicyViolation[];
    /** Violations the service's exception covers. */
    allowed: PolicyViolation[];
    exception: DeployPolicyException | null;
    /** Compose files from a repository are only known, and checked, at deploy time. */
    compose_checked_at_deploy: boolean;
    invalid_env_keys: string[];
    rules: PolicyRule[];
    can_manage_exceptions: boolean;
    /**
     * The service has no server, so it deploys to the control-plane server,
     * and the D-19 stopgap applies to it: only instance operators can deploy
     * it, and git-push deploys are refused, until app network isolation is
     * enabled or an operator grants it the control_plane exception.
     */
    control_plane_restricted: boolean;
    /**
     * Service names in the saved inline compose file, sorted. Null for a
     * compose file from a repository, a file that cannot be loaded, and
     * services that are not docker-compose.
     */
    compose_services: string[] | null;
    /** Null unless the compose file comes from a repository and has deployed since v0.25.0. */
    last_deploy_check: DeployCheck | null;
    /** Names the values a grant approves now; the grant sends it back. */
    values_digest: string;
}

export interface DeployPolicyReportEntry {
    service_id: string;
    service_name: string;
    org_id: string;
    org_name: string;
    project_id: string;
    project_name: string;
    environment_name: string;
    server_id: string;
    /** "" for the control-plane server. */
    server_name: string;
    source_type: string;
    violations: PolicyViolation[];
    allowed: PolicyViolation[];
    /** Rules the service's exception allows; empty when it has none. */
    allowed_rules: string[];
    compose_checked_at_deploy: boolean;
    invalid_env_keys: string[];
    /** The reason the exception was granted with; empty when the service has none. */
    exception_reason: string;
    /** Who granted the exception and when; empty and null without one. */
    exception_granted_by: string;
    exception_granted_at: string | null;
    /** As in DeployPolicyException; null without one too. */
    allowed_values: PinnedValue[] | null;
    last_deploy_check: DeployCheck | null;
}

export interface DeployPolicyReport {
    mode: DeployPolicyMode;
    generated_at: string;
    services: DeployPolicyReportEntry[];
    /** Everything the report read, the control-plane server included. */
    services_checked: number;
    servers_checked: number;
    /** Every rule a deploy is checked against. */
    rules: PolicyRule[];
}

export const deployPolicyService = {
    getForService: async (serviceId: string): Promise<ServiceDeployPolicy> => {
        const { data, version } = await fetchVersioned<ServiceDeployPolicy>(`/api/v1/services/${serviceId}/deploy-policy`);
        return { ...data, version };
    },

    /**
     * Instance operators only, with the password confirmed (D-53, D-56). Replaces any existing
     * exception. 409 values_changed when valuesDigest no longer names the service's values, 409
     * changed_since_read when version is no longer the exception's (changedSinceRead).
     */
    grantException: (serviceId: string, allowedRules: string[], reason: string, valuesDigest: string, version: string) => {
        const body: components["schemas"]["GrantExceptionInput"] = { allowed_rules: allowedRules, reason, values_digest: valuesDigest, version };
        return fetchAPI<DeployPolicyException>(`/api/v1/services/${serviceId}/deploy-policy/exception`, {
            method: "POST",
            headers: reauthHeader(),
            body: JSON.stringify(body),
        });
    },

    /** Instance operators only. */
    revokeException: (serviceId: string, reason?: string) =>
        fetchAPI<void>(`/api/v1/services/${serviceId}/deploy-policy/exception`, {
            method: "DELETE",
            headers: reauthHeader(),
            body: JSON.stringify({ reason: reason ?? "" }),
        }),

    /** Instance operators only: every service on the instance that violates the policy. */
    violationsReport: () =>
        fetchAPI<DeployPolicyReport>(`/api/v1/deploy-policy/violations`),
};

/**
 * Returns the violations behind a 422 deploy_policy_violation response, or
 * null when err is anything else. The body is the API envelope:
 * { error, data: { code: "deploy_policy_violation", violations } }.
 */
export function policyViolationsFromError(err: unknown): PolicyViolation[] | null {
    if (!(err instanceof ApiError) || err.status !== 422) return null;
    const body = err.body as { data?: { code?: unknown; violations?: unknown } } | null;
    if (body?.data?.code !== "deploy_policy_violation" || !Array.isArray(body.data.violations)) return null;
    return body.data.violations as PolicyViolation[];
}
