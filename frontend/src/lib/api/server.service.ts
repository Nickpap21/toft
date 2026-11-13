import { apiOrigin, fetchAPI, reauthHeader, reauthProof, saveReauth, streamEvents, tabOrg } from "./client";
import type { components } from "./schema";

export interface SystemStats {
    cpu_percent: number;
    mem_used_mb: number;
    mem_total_mb: number;
    disk_used_mb: number;
    disk_total_mb: number;
    load_1: string;
    load_5: string;
    load_15: string;
    uptime: string;
    docker_version?: string;
}

/** A workspace a server is shared with, and its share. */
export interface ServerShare {
    org_id: string;
    org_name: string;
    cpu_cores: number;
    memory_mb: number;
    created_at: string;
}

export interface Server {
    /** Shared with this workspace by another one (shared_by): the address,
     *  user and port are not given, and total_* is this workspace's share. */
    shared?: boolean;
    shared_by?: string;
    /** The owning workspace of a shared server. */
    shared_by_org_id?: string;
    id: string;
    /** The owning workspace; absent on a server shared with this one. */
    org_id?: string;
    user_id: string;
    name: string;
    ip_address: string;
    user: string;
    port: number;
    status: string;
    // Set once Provision has completed successfully; null means never
    // provisioned, even if status is "active" (a bare SSH test-connection
    // also sets status to "active").
    provisioned_at: string | null;
    // The pinned SSH host key: its type (e.g. "ssh-ed25519") and SHA256
    // fingerprint. All three are null until the first connection pins it
    // (trust on first use). A pinned_at without a fingerprint means the
    // stored key could not be read and must be replaced.
    host_key_type: string | null;
    host_key_fingerprint: string | null;
    host_key_pinned_at: string | null;
    /** The server's effective capacity (overrides applied), or this
     *  workspace's share of a shared server. */
    total_cpu_cores?: number;
    total_memory_mb?: number;
    /** Owner-set capacity in place of the scan's (absent when none). */
    cpu_cores_override?: number;
    memory_mb_override?: number;
    /** What provisioning sets up for app networks (GET /servers/:id only);
     *  the pool Docker uses there is ServerCapabilities.address_pools. */
    app_networks?: "isolated" | "legacy";
    /** How the panel reaches the server (D-106), its agent (null for an SSH
     *  server that never had one), and whether an SSH key is kept. */
    connection?: "ssh" | "agent";
    agent?: ServerAgent | null;
    ssh_key_stored?: boolean;
    /** GET /servers only: what the last provisioning run, which ended ok,
     *  left out (ProvisionRun.warnings); absent when nothing. */
    provision_warnings?: string[];
    created_at: string;
    updated_at: string;
}

/** A server's agent as the panel sees it now (D-106). */
export type ServerAgent = components["schemas"]["ServerAgent"];
export type AgentStatus = ServerAgent["status"];
/** A one-time join command, shown once. */
export type AgentJoin = components["schemas"]["AgentJoin"];

export interface ConnectionResult {
    host: string;
    latency_ms: number;
    /** The SSH host key the server presented. */
    host_key_type: string;
    host_key_fingerprint: string;
    /** True when this connection was the first and pinned the key. */
    host_key_newly_pinned: boolean;
}

/** What a credential-free SSH handshake with the server found. */
export interface HostKeyScan {
    key_type: string;
    presented_fingerprint: string;
    /** Null while no key is pinned. */
    pinned_fingerprint: string | null;
    pinned_key_type: string | null;
    matches_pinned: boolean;
}

/** The data of a 409 from resetHostKey: the key the server presents now. */
export interface HostKeyChanged {
    key_type: string;
    presented_fingerprint: string;
}

export interface CapacitySummary {
    server_id: string;
    effective_cpu_cores: number;
    effective_memory_mb: number;
    allocated_cpu_cores: number;
    allocated_memory_mb: number;
    live_cpu_percent: number;
    live_memory_mb: number;
    capacity_discovered: boolean;
    /** The figures are this workspace's share of a server another workspace owns. */
    share: boolean;
    /** The owner's view only; absent when the server could not be read. */
    disk_used_mb?: number;
    disk_total_mb?: number;
    docker_version?: string;
    uptime_seconds?: number;
    /** The server's paas-traefik container: "running", "exited", …; absent
     *  when there is none or it wasn't read. */
    traefik_status?: string;
    traefik_image?: string;
    /** What each workspace's running services reserve (owner's view only). */
    workspaces?: WorkspaceAllocation[];
}

export interface WorkspaceAllocation {
    org_id: string;
    org_name: string;
    projects: string[];
    services: number;
    cpu_cores: number;
    memory_mb: number;
}

/** Whether a service with given limits fits for this workspace, decided as
 *  the deploy capacity check decides, and the room it was measured against:
 *  this workspace's share, or for the owner the server less its shares. */
export interface FitResult {
    capacity_known: boolean;
    fits: boolean;
    cpu_ok: boolean;
    memory_ok: boolean;
    share: boolean;
    effective_cpu_cores: number;
    effective_memory_mb: number;
    allocated_cpu_cores: number;
    allocated_memory_mb: number;
}

export interface UpdateCapacityInput {
    cpu_cores_override?: number | null;
    memory_mb_override?: number | null;
}

/** One of this workspace's services on a server. */
export interface ServiceOnServer {
    id: string;
    name: string;
    project_name: string;
    /** "" for a workspace service. */
    project_id: string;
    /** "" for a project or workspace service. */
    environment_name: string;
    scope: "environment" | "project" | "org";
    cpu_limit: string;
    memory_limit: string;
    runtime_status: string;
    /** Its own level or its workspace's default (D-95, D-115). */
    effective_isolation: "standard" | "sandboxed" | "sandboxed_strict";
}

/** A server's last provisioning run (D-48); finished_at and ok are null
 *  while it runs. */
export type ProvisionRun = components["schemas"]["ProvisionRun"];

/** One of D-29's server requirements, as the server answered it. */
export interface Requirement {
    id: "os" | "systemd" | "cgroup" | "kernel" | "docker" | "ports" | string;
    label: string;
    found: string;
    met: boolean;
    /** What to do when it isn't met. */
    fix?: string;
}

export interface Requirements {
    met: boolean;
    os: string;
    checks: Requirement[];
}

export interface CreateServerInput {
    name: string;
    ip_address: string;
    user: string;
    port: number;
    ssh_key: string;
}

/** A server's label in pickers: its address, or who shares it with this
 *  workspace (a shared server's address is not given). */
export function serverLabel(s: Pick<Server, "name" | "ip_address" | "shared" | "shared_by">): string {
    return s.shared ? `${s.name} (shared by ${s.shared_by})` : `${s.name} (${s.ip_address})`;
}

export const serverService = {
    listShares: (serverId: string) => fetchAPI<ServerShare[]>(`/api/v1/servers/${serverId}/shares`),

    /** Owners only, and only with a workspace the caller also owns. */
    putShare: (serverId: string, orgId: string, cpuCores: number, memoryMb: number) =>
        fetchAPI<ServerShare[]>(`/api/v1/servers/${serverId}/shares/${orgId}`, {
            method: "PUT",
            body: JSON.stringify({ cpu_cores: cpuCores, memory_mb: memoryMb }),
        }),

    deleteShare: (serverId: string, orgId: string) =>
        fetchAPI<void>(`/api/v1/servers/${serverId}/shares/${orgId}`, { method: "DELETE" }),

    list: () =>
        fetchAPI<Server[]>(`/api/v1/servers`),

    create: (input: CreateServerInput) =>
        fetchAPI<Server>(`/api/v1/servers`, {
            method: "POST",
            body: JSON.stringify(input),
        }),

    get: (serverId: string) =>
        fetchAPI<Server>(`/api/v1/servers/${serverId}`),

    /** Adds a server that joins by token (D-106); the answer carries the
     *  one-time join command. Without a name it is the caller's waiting
     *  server, reused with a new command, or a new server-<n> (D-153). */
    createAgent: (name = "", ipAddress?: string) =>
        fetchAPI<Server & { join: AgentJoin }>(`/api/v1/servers`, {
            method: "POST",
            body: JSON.stringify({ connection: "agent", ...(name ? { name } : {}), ...(ipAddress ? { ip_address: ipAddress } : {}) }),
        }),

    rename: (serverId: string, name: string) =>
        fetchAPI<Server>(`/api/v1/servers/${serverId}`, { method: "PATCH", body: JSON.stringify({ name }) }),

    /** The key a waiting server is reached with over SSH, made once: its
     *  authorized_keys line. */
    sshKey: (serverId: string) =>
        fetchAPI<components["schemas"]["ServerSSHKey"]>(`/api/v1/servers/${serverId}/ssh-key`, { method: "POST" }),

    /** Reaches a waiting server over SSH as root instead; without ssh_key,
     *  with the key sshKey made. */
    useSSH: (serverId: string, input: components["schemas"]["UseSSHInput"]) =>
        fetchAPI<Server>(`/api/v1/servers/${serverId}/ssh`, { method: "PUT", body: JSON.stringify(input) }),

    /** A new join command. On a server whose agent key is pinned it is a
     *  rejoin, which needs the password proof. */
    joinToken: (serverId: string) =>
        fetchAPI<AgentJoin>(`/api/v1/servers/${serverId}/agent/join-token`, { method: "POST", headers: reauthHeader() }),

    /** Moves an SSH server to the agent, installing it over SSH; streams
     *  progress like provision. Needs the password proof. */
    installAgent: async (serverId: string, keepKey: boolean, onLine: (line: string) => void): Promise<void> => {
        const ctrl = new AbortController();
        let failed: string | null = null;
        let done = false;
        try {
            await streamEvents(`/api/v1/servers/${serverId}/agent/install${keepKey ? "" : "?keep_ssh_key=false"}`, (type, data) => {
                if (type === "done") { done = true; ctrl.abort(); }
                else if (type === "error") { failed = data || "Installing the agent failed"; ctrl.abort(); }
                else if (data) onLine(data);
            }, ctrl.signal, { method: "POST", headers: reauthHeader() });
        } catch (err) {
            if ((err as Error).name !== "AbortError") throw err;
        }
        if (failed) throw new Error(failed);
        if (!done) throw new Error("The connection to the panel ended before the install finished. Check the server's page to see whether the agent connected.");
    },

    /** Moves an agent server to SSH: an empty key keeps the stored one. */
    moveToSSH: (serverId: string, input: { user?: string; port?: number; ssh_key?: string }) =>
        fetchAPI<Server>(`/api/v1/servers/${serverId}/connection`, {
            method: "POST",
            body: JSON.stringify({ mode: "ssh", ...input }),
            headers: reauthHeader(),
        }),

    /** Ends a frozen server's freeze (D-113): adopt what runs there, or have
     *  the agent take the panel's state. Asks for the password first. */
    chooseAgentState: (serverId: string, adopt: boolean) =>
        fetchAPI<Server>(`/api/v1/servers/${serverId}/agent/${adopt ? "adopt" : "use-panel-state"}`, { method: "POST", headers: reauthHeader() }),

    rotateAgentKey: (serverId: string) =>
        fetchAPI<Server>(`/api/v1/servers/${serverId}/agent/rotate`, { method: "POST" }),

    revokeAgent: (serverId: string, uninstall: boolean) =>
        fetchAPI<Server>(`/api/v1/servers/${serverId}/agent${uninstall ? "?uninstall=true" : ""}`, { method: "DELETE" }),

    /** withServices also deletes the services on it from the panel, for a
     *  server that is gone for good; nothing on the server is touched. That
     *  is the owners', with the password (reauthHeader). */
    delete: (serverId: string, withServices = false) =>
        fetchAPI<void>(`/api/v1/servers/${serverId}${withServices ? "?with_services=true" : ""}`, {
            method: "DELETE",
            headers: withServices ? reauthHeader() : {},
        }),

    testConnection: (serverId: string) =>
        fetchAPI<ConnectionResult>(`/api/v1/servers/${serverId}/test-connection`),

    /** Runs an SSH handshake without credentials and reports the host key the
     *  server presents next to the pinned one. */
    scanHostKey: (serverId: string) =>
        fetchAPI<HostKeyScan>(`/api/v1/servers/${serverId}/host-key/scan`, { method: "POST" }),

    /** Pins the key the server presents now, which must have this fingerprint
     *  (409 with HostKeyChanged otherwise). Instance operators only. */
    resetHostKey: (serverId: string, fingerprint: string) =>
        fetchAPI<Server>(`/api/v1/servers/${serverId}/host-key`, {
            method: "PUT",
            body: JSON.stringify({ fingerprint }),
            headers: reauthHeader(),
        }),

    /** Streams provision progress. Calls onLine for each log line; resolves on
     *  "event: done", rejects on "event: error". Aborts after 10 minutes. Pass
     *  `externalCtrl` to let the caller cancel early (a visible "Cancel" button). */
    provision: async (serverId: string, onLine: (line: string) => void, externalCtrl?: AbortController): Promise<void> => {
        const ctrl = externalCtrl ?? new AbortController();
        let timedOut = false;
        const timeoutId = setTimeout(() => { timedOut = true; ctrl.abort(); }, 10 * 60 * 1000);
        let failed: string | null = null;
        try {
            await streamEvents(`/api/v1/servers/${serverId}/provision`, (type, data) => {
                if (type === "done") ctrl.abort();
                else if (type === "error") { failed = data || "Provisioning failed"; ctrl.abort(); }
                else if (data) onLine(data);
            }, ctrl.signal, { method: "POST" });
        } catch (err) {
            if ((err as Error).name !== "AbortError") throw err;
        } finally {
            clearTimeout(timeoutId);
        }
        if (failed) throw new Error(failed);
        if (timedOut) throw new Error("Provisioning timed out after 10 minutes.");
        if (externalCtrl?.signal.aborted && !ctrl.signal.aborted) throw new Error("Provisioning cancelled.");
    },

    /** Reads the server over SSH and checks it against D-29. */
    requirements: (serverId: string) =>
        fetchAPI<Requirements>(`/api/v1/servers/${serverId}/requirements`),

    /** Null when no run is recorded. */
    lastProvision: (serverId: string) =>
        fetchAPI<ProvisionRun | null>(`/api/v1/servers/${serverId}/provisioning`),

    getSystemStats: (serverId: string) =>
        fetchAPI<SystemStats>(`/api/v1/servers/${serverId}/system-stats`),

    /** A host shell or a host-key replacement needs the password confirmed
     *  (D-53): POST /auth/reauth, and the proof is kept for this tab. */
    confirmPassword: async (password: string): Promise<void> => {
        const p = await fetchAPI<{ token: string; expires_in: number }>("/api/v1/auth/reauth", {
            method: "POST",
            body: JSON.stringify({ password }),
        });
        saveReauth(p.token, p.expires_in);
    },

    /** Returns the WebSocket URL for the PTY terminal on the given server.
     *  The token and the tab's workspace are query params because browsers
     *  cannot set WS headers. */
    getPtyWsUrl: (serverId: string, cols = 120, rows = 30): string => {
        const token = typeof window !== "undefined" ? localStorage.getItem("paas_token") : null;
        const org = tabOrg();
        const reauth = reauthProof();
        const wsBase = apiOrigin().replace(/^http/, "ws");
        const params = new URLSearchParams({
            cols: String(cols),
            rows: String(rows),
            ...(token ? { token } : {}),
            ...(org ? { org } : {}),
            ...(reauth ? { reauth } : {}),
        });
        return `${wsBase}/api/v1/servers/${serverId}/pty?${params}`;
    },

    getCapacitySummary: (serverId: string): Promise<CapacitySummary> =>
        fetchAPI<CapacitySummary>(`/api/v1/servers/${serverId}/capacity-summary`),

    /** Without limits, the room this workspace has left on the server. */
    fit: (serverId: string, cpuCores = 0, memoryMb = 0): Promise<FitResult> =>
        fetchAPI<FitResult>(`/api/v1/servers/${serverId}/fit?cpu_cores=${cpuCores}&memory_mb=${memoryMb}`),

    updateCapacity: (serverId: string, input: UpdateCapacityInput): Promise<Server> =>
        fetchAPI<Server>(`/api/v1/servers/${serverId}/capacity`, {
            method: 'PUT',
            body: JSON.stringify(input),
        }),

    discoverCapacity: (serverId: string): Promise<Server> =>
        fetchAPI<Server>(`/api/v1/servers/${serverId}/discover-capacity`, { method: 'POST' }),

    listServices: (serverId: string): Promise<ServiceOnServer[]> =>
        fetchAPI<ServiceOnServer[]>(`/api/v1/servers/${serverId}/services`),

    exec: (
        serverId: string,
        command: string,
        onLine: (line: string) => void,
        onDone: () => void,
        onError?: (msg: string) => void,
    ): (() => void) => {
        const ctrl = new AbortController();
        streamEvents(`/api/v1/servers/${serverId}/exec`, (type, data) => {
            if (type === "done") ctrl.abort();
            else if (type === "error") { onError?.(data || "Command failed"); ctrl.abort(); }
            else if (data) onLine(data);
        }, ctrl.signal, { method: "POST", body: JSON.stringify({ command }), headers: reauthHeader() })
            .then(onDone, (err: Error) => {
                if (err.name !== "AbortError") onError?.(err.message ?? "Command failed");
                onDone();
            });
        return () => ctrl.abort();
    },
};

/** A server's DNS provider for wildcard certificates (DNS-01): a lego
 *  provider name and the names of its variables. Values are never returned. */
export interface DNSProvider {
    provider: string;
    credential_keys: string[];
    updated_at: string;
}

export const dnsProviderService = {
    /** 404 (ApiError) when the server has none. */
    get: (serverId: string) => fetchAPI<DNSProvider>(`/api/v1/servers/${serverId}/dns-provider`),

    /** Replaces the provider and every variable; applied at the next Provision. */
    put: (serverId: string, provider: string, credentials: Record<string, string>) =>
        fetchAPI<DNSProvider>(`/api/v1/servers/${serverId}/dns-provider`, {
            method: "PUT",
            body: JSON.stringify({ provider, credentials }),
        }),

    delete: (serverId: string) =>
        fetchAPI<void>(`/api/v1/servers/${serverId}/dns-provider`, { method: "DELETE" }),
};

/** A server's backup destination (D-43): an S3-compatible bucket. The
 *  secret key is never returned; repository_password only in the answer
 *  that created the destination. */
export interface BackupDestination {
    endpoint: string;
    bucket: string;
    access_key: string;
    schedule: string;
    repository: string;
    last_run_at: string | null;
    last_ok: boolean | null;
    last_ok_at: string | null;
    last_bytes: number | null;
    last_error: string;
    created_at: string;
    repository_password?: string;
    /** Saved, but not written to the server yet. */
    apply_error?: string;
    /** The panel host only: stored, but the host has no backup settings, so
     *  nothing runs. A restore leaves it so (D-163). */
    paused?: boolean;
    /** When the panel was restored, while no backup ran since: with paused,
     *  and after a resume until this server's first run (D-164). */
    restored_at?: string;
}

export interface BackupInput {
    endpoint: string;
    bucket: string;
    access_key: string;
    /** Empty keeps the stored one. */
    secret_key: string;
    schedule: string;
}

/** A server's backup path, or the panel host's (instance operators). */
export const backupPath = (serverId: string | null) =>
    serverId ? `/api/v1/servers/${serverId}/backup` : "/api/v1/instance/backup";

export const backupService = {
    /** Null when there is none. */
    get: (path: string) => fetchAPI<BackupDestination | null>(path),
    put: (path: string, input: BackupInput) =>
        fetchAPI<BackupDestination>(path, { method: "PUT", body: JSON.stringify(input) }),
    delete: (path: string) => fetchAPI<void>(path, { method: "DELETE" }),
};

/** What a server can run, as the capability probe last read it (D-93). */
export type ServerCapabilities = components["schemas"]["ServerCapabilities"];
export type IsolationLevel = ServerCapabilities["levels"][number];

/** Where a server's isolation is read and gVisor installed; serverId null is
 *  the panel's own server, through its host helper (D-97, operators). */
const isolationPath = (serverId: string | null) => (serverId ? `/api/v1/servers/${serverId}` : "/api/v1/instance/panel");

export const capabilityService = {
    /** null when the probe never ran. */
    get: (serverId: string | null) => fetchAPI<ServerCapabilities | null>(`${isolationPath(serverId)}/capabilities`),

    /** Checks again: tens of seconds while a runtime's smoke test pulls its image. */
    probe: (serverId: string | null) => fetchAPI<ServerCapabilities>(`${isolationPath(serverId)}/capabilities`, { method: "POST" }),
};

/** What a server's last updates read found (D-133). */
export type HostUpdates = components["schemas"]["HostUpdates"];
export type DockerAdvisory = HostUpdates["docker"]["advisories"][number];

export const updatesService = {
    /** null before the first read. */
    get: (serverId: string | null) => fetchAPI<HostUpdates | null>(`${isolationPath(serverId)}/updates`),

    /** Reads again now; a few seconds. Installs nothing. */
    check: (serverId: string | null) => fetchAPI<HostUpdates>(`${isolationPath(serverId)}/updates`, { method: "POST" }),

    /** The run of updates in progress, read from the host, or the last;
     *  null before the first (D-134). Poll it while it runs. */
    run: (serverId: string | null) => fetchAPI<UpdateRun | null>(`${isolationPath(serverId)}/updates/run`),

    /** Starts a run on the host; needs the password confirmed (reauthHeader). */
    start: (serverId: string | null, action: UpdateAction) =>
        fetchAPI<UpdateRun>(`${isolationPath(serverId)}/updates/run`, {
            method: "POST",
            body: JSON.stringify({ action }),
            headers: reauthHeader(),
        }),
};

/** What fills a host's disk, and what is safe to delete (D-135). */
export type DiskReport = components["schemas"]["DiskReport"];
export type DiskClass = components["schemas"]["DiskClass"];
export type DiskVolume = components["schemas"]["DiskVolume"];
export type DiskClean = components["schemas"]["DiskClean"];
export type DiskPolicy = components["schemas"]["DiskPolicy"];
export type DiskPolicyInput = components["schemas"]["DiskPolicyInput"];
export type DiskKind = DiskClean["kinds"][number];

export const diskService = {
    /** Read at most every 5 minutes; fresh reads again now. */
    get: (serverId: string | null, fresh = false) =>
        fetchAPI<DiskReport>(`${isolationPath(serverId)}/disk${fresh ? "?fresh=true" : ""}`),

    /** The clean in progress, or the last; null before the first. Poll it while it runs. */
    clean: (serverId: string | null) => fetchAPI<DiskClean | null>(`${isolationPath(serverId)}/disk/clean`),

    /** Starts a clean; needs the password confirmed (reauthHeader). */
    start: (serverId: string | null, kinds: DiskKind[], buildCacheAll: boolean) =>
        fetchAPI<DiskClean>(`${isolationPath(serverId)}/disk/clean`, {
            method: "POST",
            body: JSON.stringify({ kinds, build_cache_all: buildCacheAll }),
            headers: reauthHeader(),
        }),

    /** Deletes one volume no service owns, with its data (reauthHeader). */
    removeVolume: (serverId: string | null, name: string) =>
        fetchAPI<void>(`${isolationPath(serverId)}/disk/volumes/remove`, {
            method: "POST",
            body: JSON.stringify({ name }),
            headers: reauthHeader(),
        }),

    setPolicy: (serverId: string | null, p: DiskPolicyInput) =>
        fetchAPI<DiskPolicy>(`${isolationPath(serverId)}/disk/policy`, { method: "PUT", body: JSON.stringify(p) }),
};

/** A host's run of updates (D-134). */
export type UpdateRun = components["schemas"]["UpdateRun"];
export type UpdateAction = UpdateRun["action"];

/** A server's last gVisor install (D-95). */
export type GvisorRun = components["schemas"]["GvisorRun"];

export const gvisorService = {
    /** null before the first install. */
    last: (serverId: string | null) => fetchAPI<GvisorRun | null>(`${isolationPath(serverId)}/gvisor`),

    /** Streams the install, as provision does; needs the password confirmed
     *  (reauthHeader). Rejects with the run's error; ctrl stops watching
     *  while the install carries on. */
    install: async (serverId: string | null, onLine: (line: string) => void, ctrl: AbortController): Promise<void> => {
        let failed: string | null = null;
        let done = false;
        try {
            await streamEvents(`${isolationPath(serverId)}/gvisor`, (type, data) => {
                if (type === "done") { done = true; ctrl.abort(); }
                else if (type === "error") { failed = data || "The install failed"; ctrl.abort(); }
                else if (data) onLine(data);
            }, ctrl.signal, { method: "POST", headers: reauthHeader() });
        } catch (err) {
            if ((err as Error).name !== "AbortError") throw err;
        }
        if (failed) throw new Error(failed);
        if (!done) throw new Error("Stopped watching.");
    },
};
