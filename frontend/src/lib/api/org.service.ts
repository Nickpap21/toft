import type { components } from "./schema";
import { ApiError, BASE_URL, fetchAPI, fetchPage } from "./client";

export type OrgMember = components["schemas"]["Member"];

export type OrgInvite = components["schemas"]["Invite"];

/** A suggestion for "Has an account here" (D-58). */
export type AddableAccount = components["schemas"]["AddableAccount"];

export type Organization = components["schemas"]["Organization"];

export type OrgMembership = components["schemas"]["OrgMembership"];

/** What an audit entry is about, resolved to names by the API. */
export interface AuditTarget {
    kind: "service" | "deployment" | "server" | "project" | "environment" | "user" | "apikey" | "invite" | "org" | "instance" | string;
    id: string;
    name: string;
    /** The object is gone: name is what the entry recorded, or its ID. */
    deleted: boolean;
    project_id?: string;
    service_id?: string;
    environment?: string;
    /** A server share's other workspace. */
    other_workspace?: string;
}

export interface AuditEntry {
    id: string;
    org_id: string;
    actor_id: string;
    actor: string;
    actor_email?: string;
    action: string;
    /** What the entry is about, by name (target.name). */
    resource: string;
    created_at: string;
    target: AuditTarget;
    /** What the action recorded with it (role, rules, key scope…). */
    detail: Record<string, unknown>;
    /** The agent connection that acted for the actor (D-100); null for a
     *  person's own action. */
    agent_id?: string | null;
    agent_name?: string | null;
    /** The API key the actor used; null for a session. */
    api_key_id?: string | null;
    /** The key's name; null once the key is revoked. */
    api_key_name?: string | null;
}

export interface AuditLogFilter {
    action?: string;
    /** Any of these actions. */
    actions?: string[];
    /** A user ID. */
    actor?: string;
    q?: string;
    /** "any" (agents only), "none" (people only) or a connection ID. */
    agent?: string;
    /** "any" (made with an API key) or "none" (made without one). */
    api_key?: "any" | "none";
    /** One resource exactly, "service:<id>". */
    resource?: string;
    /** RFC 3339: entries at or after it. */
    since?: string;
    /** The previous page's `next`; the first page without it (D-59). */
    cursor?: string;
    limit?: number;
}

export type InviteValidation = components["schemas"]["InviteValidation"];

export type OrgSettings = components["schemas"]["OrgSettings"];

/** Where the workspace's services can run Sandboxed (D-95). */
export type OrgIsolation = components["schemas"]["OrgIsolation"];

function auditQuery(filter: AuditLogFilter): string {
    const params = new URLSearchParams();
    if (filter.action) params.set("action", filter.action);
    if (filter.actions?.length) params.set("actions", filter.actions.join(","));
    if (filter.actor) params.set("actor", filter.actor);
    if (filter.q) params.set("q", filter.q);
    if (filter.agent) params.set("agent", filter.agent);
    if (filter.api_key) params.set("api_key", filter.api_key);
    if (filter.resource) params.set("resource", filter.resource);
    if (filter.since) params.set("since", filter.since);
    if (filter.cursor) params.set("cursor", filter.cursor);
    if (filter.limit) params.set("limit", String(filter.limit));
    const qs = params.toString();
    return qs ? `?${qs}` : "";
}

export const orgService = {
    getOrg: () =>
        fetchAPI<Organization>("/api/v1/org", {}),

    updateOrgName: (name: string) =>
        fetchAPI<Organization>("/api/v1/org", {
            method: "PUT",
            body: JSON.stringify({ name }),
        }),

    /** Deletes the active workspace and everything in it; confirm is its name. */
    deleteOrg: (confirm: string) =>
        fetchAPI<void>("/api/v1/org", { method: "DELETE", body: JSON.stringify({ confirm }) }),

    listOrgs: () =>
        fetchAPI<OrgMembership[]>("/api/v1/org/list", {}),

    switchOrg: (orgId: string) =>
        fetchAPI<{ org_id: string; name: string; role: string }>("/api/v1/org/switch", {
            method: "POST",
            body: JSON.stringify({ org_id: orgId }),
        }),

    createOrg: (name: string) =>
        fetchAPI<Organization>("/api/v1/org", {
            method: "POST",
            body: JSON.stringify({ name }),
        }),

    listMembers: () =>
        fetchAPI<OrgMember[]>("/api/v1/org/members", {}),

    /** Former members, and every account for an operator; owners only (D-58). */
    listAddable: () => fetchAPI<AddableAccount[]>("/api/v1/org/members/addable"),

    /** Adds an existing, active account by email; 404 when none has it (D-56). */
    addMember: (email: string, role: string) =>
        fetchAPI<OrgMember>("/api/v1/org/members", {
            method: "POST",
            body: JSON.stringify({ email, role }),
        }),

    updateMemberRole: (userId: string, role: string) =>
        fetchAPI<void>(`/api/v1/org/members/${userId}/role`, {
            method: "PUT",
            body: JSON.stringify({ role }),
        }),

    removeMember: (userId: string) =>
        fetchAPI<void>(`/api/v1/org/members/${userId}`, {
            method: "DELETE",
        }),

    /** Leaves the active workspace; 409 for its last owner. */
    leaveOrg: () => fetchAPI<void>("/api/v1/org/leave", { method: "POST" }),

    listInvites: () =>
        fetchAPI<OrgInvite[]>("/api/v1/org/invites", {}),

    createInvite: (role: string) =>
        fetchAPI<OrgInvite>("/api/v1/org/invites", {
            method: "POST",
            body: JSON.stringify({ role }),
        }),

    revokeInvite: (inviteId: string) =>
        fetchAPI<void>(`/api/v1/org/invites/${inviteId}`, {
            method: "DELETE",
        }),

    acceptInvite: (token: string) =>
        fetchAPI<Organization>("/api/v1/org/invites/accept", {
            method: "POST",
            body: JSON.stringify({ token }),
        }),

    getSettings: (): Promise<OrgSettings> =>
        fetchAPI<OrgSettings>('/api/v1/org/settings'),

    getIsolation: (): Promise<OrgIsolation> => fetchAPI<OrgIsolation>("/api/v1/org/isolation"),

    updateSettings: (input: OrgSettings): Promise<OrgSettings> =>
        fetchAPI<OrgSettings>('/api/v1/org/settings', {
            method: 'PUT',
            body: JSON.stringify(input),
        }),

    /** Hides Home's Finish setting up checklist for the workspace (7g); owners. */
    hideSetup: (): Promise<OrgSettings> =>
        fetchAPI<OrgSettings>('/api/v1/org/settings', {
            method: 'PATCH',
            body: JSON.stringify({ setup_hidden: true }),
        }),

    /** The first page as a plain list, for the places that show the latest few. */
    listAuditLog: (filter: AuditLogFilter = {}) => fetchAPI<AuditEntry[]>(`/api/v1/org/audit-log${auditQuery(filter)}`),

    /** A page with its `next` cursor (D-59), for the audit log page. */
    listAuditLogPage: (filter: AuditLogFilter = {}) => fetchPage<AuditEntry[]>(`/api/v1/org/audit-log${auditQuery(filter)}`),

    /** How many entries the filter matches, on every page. */
    countAuditLog: (filter: AuditLogFilter = {}) =>
        fetchAPI<{ total: number }>(`/api/v1/org/audit-log/count${auditQuery(filter)}`).then((r) => r.total),

    /** Public endpoint — no auth required. Deliberately uses a bare fetch rather
     *  than fetchAPI: fetchAPI force-redirects to "/" whenever a stale, expired
     *  token happens to be sitting in localStorage, which would wrongly bounce a
     *  visitor off this public invite page just because they'd used the app
     *  before and let an unrelated session lapse. */
    validateInvite: async (token: string): Promise<InviteValidation> => {
        const res = await fetch(`${BASE_URL}/api/v1/org/invites/validate/${token}`);
        if (!res.ok) {
            const body: unknown = await res.json().catch(() => ({}));
            const message = (body as { error?: string })?.error;
            // An ApiError, so the page can read the invite's code.
            throw new ApiError(message || "Invalid or expired invite link", res.status, body);
        }
        const json = await res.json();
        return (json?.data !== undefined ? json.data : json) as InviteValidation;
    },
};
