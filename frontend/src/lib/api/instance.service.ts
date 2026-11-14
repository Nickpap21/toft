import type { components } from "./schema";
import { queryOptions } from "@tanstack/react-query";
import { ApiError, BASE_URL, apiOrigin, fetchAPI, fetchPage, reauthHeader, reauthProof, tabOrg } from "./client";

export type RegistrationStatus = components["schemas"]["RegistrationStatus"];

export type InstanceOperator = components["schemas"]["Operator"];

/** One account on the instance, as an operator sees it. */
export type InstanceUser = components["schemas"]["User"];

export type InstanceAuditPerson = components["schemas"]["AuditPerson"];

/** One row of the instance audit log (D-57). */
export type InstanceAuditEntry = components["schemas"]["AuditEntry"];

/** A reset link an operator just issued (D-56): the token appears once. */
export type PasswordReset = components["schemas"]["PasswordReset"];

/** What the reset page shows the holder of a link. */
export type PasswordResetTarget = components["schemas"]["PasswordResetTarget"];

export type InstanceUserUpdate = components["schemas"]["UserUpdate"];

export const instanceService = {
    /** Public endpoint — no auth required. Deliberately a bare fetch rather than
     *  fetchAPI: fetchAPI force-redirects to "/" whenever an expired token sits
     *  in localStorage, which would bounce a logged-out visitor off the sign-up
     *  and login pages. Same reasoning as orgService.validateInvite. */
    getRegistrationStatus: async (): Promise<RegistrationStatus> => {
        const res = await fetch(`${BASE_URL}/api/v1/instance/registration`);
        if (!res.ok) {
            const body: unknown = await res.json().catch(() => ({}));
            const message = (body as { error?: string })?.error;
            throw new Error(message || "Could not read the registration status");
        }
        const json = await res.json();
        return (json?.data !== undefined ? json.data : json) as RegistrationStatus;
    },

    // Settings → Instance → Accounts and operators (D-50). Operator-only on
    // the server; every change carries the operator's own password and is
    // refused with an API key.
    listOperators: () =>
        fetchAPI<InstanceOperator[]>("/api/v1/instance/operators"),

    grantOperator: (email: string, password: string) =>
        fetchAPI<InstanceOperator>("/api/v1/instance/operators", {
            method: "POST",
            body: JSON.stringify({ email, password }),
        }),

    revokeOperator: (userId: string, password: string) =>
        fetchAPI<void>(`/api/v1/instance/operators/${userId}`, {
            method: "DELETE",
            body: JSON.stringify({ password }),
        }),

    /** The accounts, and how many deleted ones the list leaves out. */
    listUsers: () => fetchAPI<{ users: InstanceUser[]; deleted: number }>("/api/v1/instance/users"),

    /** Irreversible (D-57): a deactivated, non-operator account becomes a tombstone. */
    deleteUser: (userId: string, password: string) =>
        fetchAPI<void>(`/api/v1/instance/users/${userId}`, {
            method: "DELETE",
            body: JSON.stringify({ password }),
        }),

    /** A page of the instance audit log, newest first (Instance → Audit log). */
    listAuditPage: (cursor?: string, kind?: "panel" | "accounts") =>
        fetchPage<InstanceAuditEntry[]>(`/api/v1/instance/audit-log?limit=50${kind ? `&kind=${kind}` : ""}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`),

    setUserActive: (userId: string, isActive: boolean, password: string) =>
        fetchAPI<InstanceUserUpdate>(`/api/v1/instance/users/${userId}`, {
            method: "PATCH",
            body: JSON.stringify({ is_active: isActive, password }),
        }),

    /** A one-time reset link for an active account (D-56). */
    issuePasswordReset: (userId: string, password: string) =>
        fetchAPI<PasswordReset>(`/api/v1/instance/users/${userId}/password-resets`, {
            method: "POST",
            body: JSON.stringify({ password }),
        }),

    // The reset page's two calls are public and bare, as getRegistrationStatus
    // and orgService.validateInvite: the holder is signed out, and a stale
    // token in localStorage must not bounce them to sign-in.
    getPasswordReset: (token: string) => publicCall<PasswordResetTarget>(`/api/v1/password-resets/${encodeURIComponent(token)}`),

    redeemPasswordReset: (token: string, password: string, revokeAccess: boolean) =>
        publicCall<{ email: string }>(`/api/v1/password-resets/${encodeURIComponent(token)}`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ password, revoke_access: revokeAccess }),
        }),
};

async function publicCall<T>(path: string, init?: RequestInit): Promise<T> {
    const res = await fetch(`${BASE_URL}${path}`, init);
    if (!res.ok) {
        const body: unknown = await res.json().catch(() => ({}));
        const message = (body as { error?: string })?.error;
        throw new ApiError(message || "This link has been used or has expired", res.status, body);
    }
    const json = await res.json();
    return (json?.data !== undefined ? json.data : json) as T;
}

/** react-query options for the public registration status, shared by the
 *  sign-in and sign-up pages so they reuse one cached answer. No retry: both
 *  pages fall back to safe defaults, and the endpoint is rate limited. */
export const registrationStatusQuery = queryOptions({
    queryKey: ["instance", "registration"],
    queryFn: instanceService.getRegistrationStatus,
    retry: false,
});

/** The server the panel runs on, which runs a workspace's services until it
 *  adds a server of its own (GET /instance/panel-host, any member). */
export type PanelHost = components["schemas"]["PanelHost"];

export const panelHostQuery = queryOptions({
    queryKey: ["instance", "panel-host"],
    queryFn: () => fetchAPI<PanelHost>("/api/v1/instance/panel-host"),
    // As a server's fit: what is reserved there moves with every deploy.
    staleTime: 30_000,
});

/** The room on the panel's server, in MiB, as the deploy's capacity check
 *  counts it: what it leaves its apps together (total), what every
 *  workspace's services reserve of that (reserved), what is left, and
 *  whether more is reserved than there is (over), when a service can be
 *  killed for memory before it reaches its own limit. null when it runs no
 *  apps or the room isn't known. */
export function panelRoom(host: PanelHost | undefined) {
    const r = host && host.role !== "control_plane" ? host.reservation : undefined;
    const total = (r?.apps_memory_bytes ?? 0) / 2 ** 20;
    if (!r || total <= 0) return null;
    const reserved = (r.apps_allocated_memory_bytes ?? 0) / 2 ** 20;
    return { total, reserved, left: Math.max(total - reserved, 0), over: reserved > total };
}

// ── The panel itself (D-68). Operators only. ────────────────────────────────

export type PanelStatus = components["schemas"]["PanelStatus"];
export type PanelContainer = components["schemas"]["PanelContainer"];
export type PanelCertificate = components["schemas"]["PanelCertificate"];
export type PanelServer = components["schemas"]["PanelServer"];
export type PanelSample = components["schemas"]["PanelSample"];
export type PanelSystem = components["schemas"]["PanelSystem"];
export type PanelRange = "live" | "24h" | "7d";

/** Whether the panel is well: its containers (live CPU and memory) and its
 *  certificate. Every Instance tab shows its health, so it refreshes while
 *  one is open. */
export const panelStatusQuery = queryOptions({
    queryKey: ["instance", "panel"],
    queryFn: () => fetchAPI<PanelStatus>("/api/v1/instance/panel"),
    refetchInterval: 5_000,
});

/** The panel server's usage over a range: live every 5 seconds, the
 *  averaged ranges every minute (a sample is a minute). */
export const panelServerQuery = (range: PanelRange) =>
    queryOptions({
        queryKey: ["instance", "panel", "server", range],
        queryFn: () => fetchAPI<PanelServer>(`/api/v1/instance/panel/server?range=${range}`),
        refetchInterval: range === "live" ? 5_000 : 60_000,
    });

export type UpgradeStatus = components["schemas"]["UpgradeStatus"];
export type UpgradeRun = components["schemas"]["UpgradeRun"];

/** The panel's version, the newest release and the upgrade in progress or
 *  the last one (D-68). Polled every 2 seconds while an upgrade runs. */
export const upgradeQuery = queryOptions({
    queryKey: ["instance", "upgrade"],
    queryFn: () => fetchAPI<UpgradeStatus>("/api/v1/instance/upgrade"),
    refetchInterval: (q) => (q.state.data?.run?.outcome === "running" ? 2_000 : 60_000),
});

export const upgradeService = {
    /** Starts toft upgrade on the host, with the operator's password. */
    start: (to: string, password: string) =>
        fetchAPI<UpgradeStatus>("/api/v1/instance/upgrade", { method: "POST", body: JSON.stringify({ to, password }) }),
    /** Runs the release check now. */
    check: () => fetchAPI<UpgradeStatus>("/api/v1/instance/upgrade/check", { method: "POST" }),
};

export type Watchdog = components["schemas"]["Watchdog"];

/** The watchdog's URL and its last ping (D-68). */
export const watchdogQuery = queryOptions({
    queryKey: ["instance", "watchdog"],
    queryFn: () => fetchAPI<Watchdog>("/api/v1/instance/watchdog"),
    refetchInterval: 60_000,
});

export const watchdogService = {
    /** "" turns it off. */
    set: (url: string) => fetchAPI<Watchdog>("/api/v1/instance/watchdog", { method: "PUT", body: JSON.stringify({ url }) }),
    test: () => fetchAPI<Watchdog>("/api/v1/instance/watchdog/test", { method: "POST" }),
};

/** The WebSocket URL of a root shell on the panel's server (D-68): the
 *  token, the tab's workspace and the password proof go as query params,
 *  since browsers can't set WebSocket headers. */
export function panelPtyWsUrl(cols: number, rows: number): string {
    const token = typeof window !== "undefined" ? localStorage.getItem("paas_token") : null;
    const org = tabOrg();
    const reauth = reauthProof();
    const params = new URLSearchParams({
        cols: String(cols),
        rows: String(rows),
        ...(token ? { token } : {}),
        ...(org ? { org } : {}),
        ...(reauth ? { reauth } : {}),
    });
    return `${apiOrigin().replace(/^http/, "ws")}/api/v1/instance/panel/pty?${params}`;
}

/** The SSE path that follows one of the panel's containers' logs. */
export const panelLogsPath = (container: string, tail = 200) =>
    `/api/v1/instance/panel/containers/${encodeURIComponent(container)}/logs?tail=${tail}`;

// ── The setup wizard (D-62). Operators only. ─────────────────────────────────

export type Setup = components["schemas"]["Setup"];
export type PanelDomainCheck = components["schemas"]["PanelDomainCheck"];
export type RecoveryKit = components["schemas"]["RecoveryKit"];
export type ServerRole = Setup["role"];

export const setupQuery = queryOptions({
    queryKey: ["instance", "setup"],
    queryFn: () => fetchAPI<Setup>("/api/v1/instance/setup"),
});

export const setupService = {
    patch: (body: { skip?: "backups" | "servers"; kit_acknowledged?: true; fingerprint?: string; finished?: true; domain_checked?: true }) =>
        fetchAPI<Setup>("/api/v1/instance/setup", { method: "PATCH", body: JSON.stringify(body) }),
    /** Needs a password confirmation (useReauth) first. */
    setDomain: (domain: string, domainChecked = false) =>
        fetchAPI<Setup>("/api/v1/instance/setup/domain", {
            method: "PUT",
            body: JSON.stringify(domainChecked ? { domain, domain_checked: true } : { domain }),
            headers: reauthHeader(),
        }),
    checkDomain: (domain: string) =>
        fetchAPI<PanelDomainCheck>("/api/v1/instance/setup/domain-check", {
            method: "POST",
            body: JSON.stringify({ domain }),
        }),
    setRole: (role: ServerRole) =>
        fetchAPI<Setup>("/api/v1/instance/setup/role", { method: "PUT", body: JSON.stringify({ role }) }),
    /** Needs a password confirmation (useReauth) first. */
    recoveryKit: () => fetchAPI<RecoveryKit>("/api/v1/instance/recovery-kit", { headers: reauthHeader() }),
};
