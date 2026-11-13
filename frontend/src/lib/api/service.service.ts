import { ApiError, fetchAPI, fetchText, reauthHeader, streamEvents } from "./client";
import type { PolicyViolation } from "./deploypolicy.service";
import type { components } from "./schema";


export type ServiceType = "web" | "worker";
export type SourceType = "git" | "docker-image" | "docker-compose";
/** What a container answered one health-check request; status 0 when
 *  nothing answered, with detail saying why. */
export interface HealthTestResult {
    status: number;
    ms: number;
    detail?: string;
}

/** As the API shows it: "unhealthy" and "error" also come from the alert
 *  monitor (a failing health check; nothing serving or a crash loop). */
export type RuntimeStatus = "idle" | "deploying" | "running" | "unhealthy" | "stopped" | "error";
// ServiceScope controls which deployment context a service belongs to.
// 'environment' = classic per-env (default)
// 'project'     = shared across all environments of a project
// 'org'         = shared across the entire org (e.g. an org-wide Grafana)
export type ServiceScope = "environment" | "project" | "org";

export interface Service {
    id: string;
    scope: ServiceScope;
    project_id: string;
    environment_id?: string; // empty for project/org-scoped services
    scoped_org_id?: string;  // set for org-scoped services
    server_id?: string;
    name: string;
    type: ServiceType;
    source_type: SourceType;
    // git source fields
    git_repo_url?: string;
    git_branch?: string;
    /** One of the project's repositories the service builds from, or takes
     *  its compose files from, instead of a URL; "" for a URL (D-77). */
    repository_id: string;
    /** A push to git_branch deploys it. */
    deploy_on_push: boolean;
    base_directory?: string;
    // docker-image source fields
    docker_image?: string;
    // build config
    install_command?: string;
    build_command?: string;
    start_command?: string;
    build_pack?: string;
    // runtime
    port: number;
    replicas?: number;
    cpu_limit?: string;
    memory_limit?: string;
    runtime_status: RuntimeStatus;
    active_deployment_id?: string;
    /** When the live deployment finished; null when none is live. */
    live_since?: components["schemas"]["Service"]["live_since"];
    /** The latest deploy failed; the previous version may still serve. */
    last_deploy_failed?: boolean;
    // compose source fields
    compose_content?: string;
    compose_repo_url?: string;
    compose_file_path?: string;
    /**
     * Compose service that receives this service's domains on `port`. Empty
     * means automatic: the compose file's only service, when it has exactly
     * one; otherwise the domains are not routed (the deploy log says so).
     */
    compose_route_service?: string;
    /** How a deploy replaces the container (D-42): see DeployStrategy. */
    deploy_strategy?: DeployStrategy;
    /** What deploy_strategy comes to: auto is recreate for a database or
     *  cache, rolling otherwise. */
    effective_deploy_strategy?: "rolling" | "recreate";
    /** The most processes each container may run (D-93); 0 is the default, 2048. */
    pids_limit?: number;
    /** How long a deploy may run, in minutes (D-125); 0 is the default, 60. */
    deploy_timeout_minutes?: number;
    /** How many of its last live deployments keep their image (D-135); 0 is the default, 10. */
    rollback_images_kept?: number;
    /** Its own isolation level (D-95); absent follows the workspace's. */
    isolation?: "standard" | "sandboxed" | "sandboxed_strict";
    /** The level its next deploy runs at. */
    effective_isolation?: "standard" | "sandboxed" | "sandboxed_strict";
    /** The level recommended for it, and why (D-99). */
    isolation_advice: components["schemas"]["Service"]["isolation_advice"];
    /** database, cache, tool or app, by its catalog entry or image's name. */
    kind: components["schemas"]["Service"]["kind"];
    // registry credentials (password never returned from API)
    registry_server?: string;
    registry_username?: string;
    has_registry_password?: boolean;
    // git credentials (token never returned from API)
    git_username?: string;
    has_git_token?: boolean;
    /** The minute probe's last answer to the health check; absent when none
     *  was taken (no check, not running, or the panel restarted since). */
    last_health_check?: { status: number; ms: number; healthy: boolean; detail?: string; at: string; since: string };
    // addon catalog fields
    addon_slug?: string;
    network_alias?: string;
    /** Where other services on its network reach it; show it with connectTo
     *  (lib/network-alias), never network_alias, which a compose stack
     *  doesn't answer to. */
    connect_to?: components["schemas"]["Service"]["connect_to"];
    volumes?: { name: string; target: string; host_path?: string; read_only?: boolean }[];
    pre_deploy_commands?: string;
    post_deploy_commands?: string;
    health_check_path?: string;
    health_check_timeout?: number;
    health_check_retries?: number;
    container_health?: components["schemas"]["Service"]["container_health"];
    container_health_command?: string;
    required_env_vars?: string;
    migrate_command?: string;
    dependencies?: string[];
    basic_auth_user?: string;
    ip_allowlist?: string[];
    // deprecated aliases kept for existing page compatibility
    source_url?: string;
    branch?: string;
    created_at: string;
    updated_at: string;
    /**
     * Only on create and config-save responses in warn mode
     * (TOFT_DEPLOY_POLICY=warn): what the deploy policy would refuse.
     */
    policy_violations?: PolicyViolation[];
}

export interface CreateServiceInput {
    /** Give it the address GET /environments/{id}/address names (D-150). */
    address?: boolean;
    name: string;
    type: ServiceType;
    source_type: SourceType;
    server_id?: string;
    git_repo_url?: string;
    git_branch?: string;
    /** One of the project's repositories instead of a URL (D-77). */
    repository_id?: string;
    deploy_on_push?: boolean;
    base_directory?: string;
    docker_image?: string;
    compose_content?: string;
    compose_repo_url?: string;
    compose_file_path?: string;
    /** The compose service the domain goes to; empty means automatic. */
    compose_route_service?: string;
    registry_server?: string;
    registry_username?: string;
    registry_password?: string;
    port: number;
    start_command?: string;
    git_username?: string;
    git_token?: string;
    addon_slug?: string;
    network_alias?: string;
    volumes?: { name: string; target: string; host_path?: string; read_only?: boolean }[];
    memory_limit?: string;
    /** The create starts its first deploy (D-164): refused with 409 capacity_exceeded, and not created, when it won't fit its server. */
    deploy?: boolean;
    /** With deploy, created past the capacity check by a role that may bypass it. */
    force?: boolean;
    /** Write-once, stored with the service or not at all. */
    secrets?: { key: string; value: string }[];
    /** Plain variables, stored with it; one sent empty stays empty, and a
     *  catalog service gets its template's defaults for those left out. */
    variables?: { key: string; value: string }[];
    basic_auth_user?: string;
    basic_auth_password?: string;
    // kept for legacy form fields
    source_url?: string;
    branch?: string;
}

/** A service as its create answers: with the first deploy the create
 *  started, or why it didn't start (D-164). */
export type CreatedService = Service & Pick<components["schemas"]["CreatedService"], "deployment" | "deploy_error">;

export type ServiceData = components["schemas"]["ServiceData"];
export type DataVolume = components["schemas"]["DataVolume"];

export interface VolumeInfo {
    name:       string;
    mount_name: string;
    target:     string;
    exists:     boolean;
    size_bytes: number;
    type?:      "volume" | "bind";    // "volume" (named) | "bind" (host path)
    host_path?: string;               // set for bind mounts
    read_only?: boolean;              // set for read-only bind mounts
}

export interface UpdateServiceConfigInput {
    name?: string;             // empty = keep existing
    git_repo_url?: string;
    git_branch?: string;
    /** Left out keeps the source; "" switches to the URLs; an id to that
     *  repository of the project (D-77). */
    repository_id?: string;
    deploy_on_push?: boolean;
    base_directory?: string;
    docker_image?: string;
    install_command?: string;
    build_command?: string;
    start_command?: string;
    build_pack?: string;
    port?: number;
    cpu_limit?: string;
    memory_limit?: string;
    compose_content?: string;
    compose_repo_url?: string;
    compose_file_path?: string;
    /** Docker-compose services: see Service.compose_route_service. Empty = automatic. */
    compose_route_service?: string;
    /** 0 goes back to the default (2048); else 256 to 32768. */
    pids_limit?: number;
    /** 0 goes back to the default (60); else 5 to 240. */
    deploy_timeout_minutes?: number;
    /** 0 goes back to the default (10); else 1 to 100. */
    rollback_images_kept?: number;
    /** "default" follows the workspace's (D-95). */
    isolation?: "default" | "standard" | "sandboxed" | "sandboxed_strict";
    deploy_strategy?: DeployStrategy;
    registry_server?: string;
    registry_username?: string;
    registry_password?: string; // write-only; empty = keep existing
    git_username?: string;
    git_token?: string;         // write-only; empty = keep existing
    pre_deploy_commands?: string;
    post_deploy_commands?: string;
    health_check_path?: string;
    health_check_timeout?: number;
    health_check_retries?: number;
    container_health?: components["schemas"]["UpdateConfigInput"]["container_health"];
    container_health_command?: string;
    required_env_vars?: string;
    migrate_command?: string;
    dependencies?: string[];
    volumes?: { name: string; target: string; host_path?: string; read_only?: boolean }[];
    network_alias?: string;
    basic_auth_user?: string;
    basic_auth_password?: string;
    ip_allowlist?: string[];
}

/** rolling: the new container starts while the previous one serves (no
 *  downtime). recreate: the previous one stops first (never two on the same
 *  data). auto: recreate for a database or cache, rolling otherwise. */
export type DeployStrategy = "auto" | "rolling" | "recreate";

/** What the server accepts as compose_route_service (empty = automatic). */
const COMPOSE_ROUTE_SERVICE_RE = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$/;

export function isValidComposeRouteService(name: string): boolean {
    return name === "" || COMPOSE_ROUTE_SERVICE_RE.test(name);
}

/** A service as the shell lists it (GET /workspace/services). project_id is
 *  "" for a workspace-shared service, environment_id "" for a shared one. */
export interface WorkspaceService {
    id: string;
    name: string;
    scope: "environment" | "project" | "org";
    project_id: string;
    environment_id: string;
    /** "" is the panel host. */
    server_id: string;
    /** 0: no port, so no domain. */
    port: number;
    runtime_status: RuntimeStatus;
    source_type: SourceType;
    addon_slug: string;
    docker_image: string;
    git_repo_url: string;
    /** What a deploy builds from (⌘K's "Deploy … from main"). */
    git_branch: string;
    /** "" for project and workspace services. */
    environment_name: string;
}

export type AdminPasswordPending = components["schemas"]["AdminPasswordPending"];
export type AdminPasswordValue = components["schemas"]["AdminPasswordValue"];

export const serviceService = {
    listForProject: (projectId: string) =>
        fetchAPI<Service[]>(`/api/v1/projects/${projectId}/services`),

    listForEnvironment: (environmentId: string) =>
        fetchAPI<Service[]>(`/api/v1/environments/${environmentId}/services`),

    create: (environmentId: string, input: CreateServiceInput) =>
        fetchAPI<CreatedService>(`/api/v1/environments/${environmentId}/services`, {
            method: "POST",
            body: JSON.stringify(input),
        }),

    /** The address a new service would get (D-150), or why it gets none. */
    address: (environmentId: string, q: { name: string; type: ServiceType; server_id: string; addon_slug: string; docker_image: string; port: number }) =>
        fetchAPI<components["schemas"]["ServiceAddress"]>(
            `/api/v1/environments/${environmentId}/address?${new URLSearchParams({ ...q, port: String(q.port) })}`,
        ),

    /** The address a new project's first service would get (7g). */
    newProjectAddress: (q: { name: string; type: ServiceType; server_id: string; addon_slug: string; docker_image: string; port: number }) =>
        fetchAPI<components["schemas"]["ServiceAddress"]>(
            `/api/v1/services/address?${new URLSearchParams({ ...q, port: String(q.port) })}`,
        ),

    get: (serviceId: string) =>
        fetchAPI<Service>(`/api/v1/services/${serviceId}`),

    updateConfig: (serviceId: string, input: UpdateServiceConfigInput) =>
        fetchAPI<Service>(`/api/v1/services/${serviceId}/config`, {
            method: "PUT",
            body: JSON.stringify(input),
        }),

    stop: (serviceId: string) =>
        fetchAPI<void>(`/api/v1/services/${serviceId}/stop`, { method: "POST" }),

    /** Makes a project's or environment's service a workspace service
     *  (D-162); it joins the workspace's network at its next deploy. */
    moveToWorkspace: (serviceId: string) =>
        fetchAPI<Service>(`/api/v1/services/${serviceId}/move-to-workspace`, { method: "POST" }),

    /** Restarts the running containers in place: same version and settings. */
    restart: (serviceId: string) =>
        fetchAPI<void>(`/api/v1/services/${serviceId}/restart`, { method: "POST" }),

    assignServer: (serviceId: string, serverId: string | null) =>
        fetchAPI<Service>(`/api/v1/services/${serviceId}/server`, {
            method: "PUT",
            body: JSON.stringify({ server_id: serverId ?? "" }),
        }),

    /** confirm is the service's name: required when deleting removes data. */
    delete: (serviceId: string, confirm: string) =>
        fetchAPI<void>(`/api/v1/services/${serviceId}?confirm=${encodeURIComponent(confirm)}`, { method: "DELETE" }),

    /** What deleting a service, an environment or a project removes, per service. */
    data: (scope: "services" | "environments" | "projects", id: string, sizes = true) =>
        fetchAPI<ServiceData[]>(`/api/v1/${scope}/${id}/data${sizes ? "" : "?sizes=false"}`),

    listVolumes: (serviceId: string) =>
        fetchAPI<VolumeInfo[]>(`/api/v1/services/${serviceId}/volumes`),

    pruneVolume: (serviceId: string, mountName: string) =>
        fetchAPI<void>(`/api/v1/services/${serviceId}/volumes/${encodeURIComponent(mountName)}`, {
            method: "DELETE",
        }),

    streamLiveLogs: (
        serviceId: string,
        onLine: (line: string) => void,
        onDone: () => void,
        /** status: the HTTP status of the refusal; absent when the connection failed. */
        onError?: (msg: string, status?: number) => void,
        tail = 100,
        container?: string,
        /** timestamps: each line led by Docker's RFC 3339 time and a space.
         *  onContainer: told which container the stream follows, and whether
         *  it runs (one that doesn't gives its last lines, then the stream ends).
         *  onFailure: the server could not follow the log. */
        opts: {
            timestamps?: boolean;
            onContainer?: (name: string, running: boolean) => void;
            onFailure?: (message: string) => void;
        } = {}
    ): (() => void) => {
        const ctrl = new AbortController();
        const fail = (err?: unknown) => {
            if (ctrl.signal.aborted) return;
            if (err instanceof ApiError) onError?.(err.message, err.status);
            else onError?.("Live log stream disconnected.");
            onDone();
        };
        streamEvents(
            `/api/v1/services/${serviceId}/logs/live?tail=${tail}${
                container ? `&container=${encodeURIComponent(container)}` : ""
            }${opts.timestamps ? "&timestamps=1" : ""}`,
            (type, data) => {
                if (type === "done") {
                    ctrl.abort();
                    onDone();
                } else if (type === "container") {
                    try {
                        const c = JSON.parse(data) as { name: string; running: boolean };
                        opts.onContainer?.(c.name, c.running);
                    } catch {
                        opts.onContainer?.(data, true);
                    }
                } else if (type === "error") {
                    opts.onFailure?.(data);
                } else if (type === "message") {
                    onLine(data);
                }
            },
            ctrl.signal
        ).then(() => fail(), fail);
        return () => ctrl.abort();
    },

    exec: (
        serviceId: string,
        container: string,
        command: string,
        onLine: (line: string) => void,
        onDone: () => void,
        onError?: (msg: string) => void
    ): (() => void) => {
        const ctrl = new AbortController();
        streamEvents(`/api/v1/services/${serviceId}/exec`, (type, data) => {
            if (type === "done") ctrl.abort();
            else if (type === "error") { onError?.(data || "Command failed"); ctrl.abort(); }
            else if (data) onLine(data);
        }, ctrl.signal, {
            method: "POST",
            body: JSON.stringify({ container, command }),
            // The host's shell needs the password confirmed (D-53).
            headers: container === "@server" ? reauthHeader() : undefined,
        })
            .then(onDone, (err: Error) => {
                if (err.name !== "AbortError") onError?.(err.message ?? "Command failed");
                onDone();
            });
        return () => ctrl.abort();
    },

    getStats: (serviceId: string, container?: string) =>
        fetchAPI<{
            cpu_percent: number;
            memory_mb: number;
            memory_limit_mb: number;
            /** When the container last started: a deploy or a restart. */
            started_at?: string;
            /** Times Docker restarted it after it exited. */
            restart_count: number;
        }>(
            `/api/v1/services/${serviceId}/stats${container ? `?container=${encodeURIComponent(container)}` : ""}`
        ),

    /** One point a minute, a day for 7d; composeService "" sums a stack's compose services. */
    getMetrics: (serviceId: string, range: "1h" | "24h" | "7d" = "1h", composeService = "") =>
        fetchAPI<Array<{ timestamp: string; cpu_percent: number; memory_mb: number; throttled_pct: number; largest_container?: boolean }>>(
            `/api/v1/services/${serviceId}/metrics?range=${range}${
                composeService ? `&compose_service=${encodeURIComponent(composeService)}` : ""
            }`
        ),

    listContainers: (serviceId: string) =>
        fetchAPI<string[]>(`/api/v1/services/${serviceId}/containers`),

    /** Asks the running container for a health-check path once. */
    testHealthCheck: (serviceId: string, body: { path: string; port: number; timeout: number }) =>
        fetchAPI<HealthTestResult>(`/api/v1/services/${serviceId}/health-check/test`, {
            method: "POST",
            body: JSON.stringify(body),
        }),

    // ── Scoped shared services ────────────────────────────────────────────────

    listProjectScoped: (projectId: string) =>
        fetchAPI<Service[]>(`/api/v1/projects/${projectId}/shared-services`),

    createProjectScoped: (projectId: string, input: CreateServiceInput) =>
        fetchAPI<CreatedService>(`/api/v1/projects/${projectId}/shared-services`, {
            method: "POST",
            body: JSON.stringify(input),
        }),

    /** Every service of the active workspace, for the sidebar and ⌘K: one request. */
    listForWorkspace: () =>
        fetchAPI<WorkspaceService[]>(`/api/v1/workspace/services`),

    listOrgScoped: () =>
        fetchAPI<Service[]>(`/api/v1/shared-services`),

    createOrgScoped: (input: CreateServiceInput) =>
        fetchAPI<CreatedService>(`/api/v1/shared-services`, {
            method: "POST",
            body: JSON.stringify(input),
        }),

    /** The caller's copy of a catalog app's admin password, without its
     *  value (D-160): kept for its creator until dismissed or 7 days. */
    adminPassword: (id: string) =>
        fetchAPI<AdminPasswordPending>(`/api/v1/services/${id}/admin-password`),

    /** Its value: needs the password confirmed (useReauth first). */
    adminPasswordValue: (id: string) =>
        fetchAPI<AdminPasswordValue>(`/api/v1/services/${id}/admin-password/value`, { headers: reauthHeader() }),

    /** Reset (D-160): needs the password confirmed (useReauth first). */
    resetAdminPassword: (id: string) =>
        fetchAPI<AdminPasswordValue>(`/api/v1/services/${id}/admin-password`, { method: "POST", headers: reauthHeader() }),

    /** Deletes the caller's copy. */
    dismissAdminPassword: (id: string) =>
        fetchAPI<void>(`/api/v1/services/${id}/admin-password`, { method: "DELETE" }),

    exportCompose: async (projectId: string): Promise<void> => {
        const text = await fetchText(`/api/v1/projects/${projectId}/compose-export`);
        const blob = new Blob([text], { type: "application/x-yaml" });
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = "docker-compose.yml";
        a.click();
        // Delay revoke so the browser has time to initiate the download.
        setTimeout(() => URL.revokeObjectURL(url), 1000);
    },
};
