import { fetchAPI } from "./client";

export interface ObservabilityConfig {
    id: string;
    org_id: string;
    project_id?: string;
    loki_service_id?: string;
    grafana_service_id?: string;
    prometheus_service_id?: string;
    loki_url?: string;
    grafana_url?: string;
    prometheus_url?: string;
    created_at: string;
    updated_at: string;
}

/** Where one container's output goes: its Docker log driver and, for
 *  Loki's, the push address. */
export interface ContainerLog {
    name: string;
    driver: string;
    loki_url?: string;
}

/** The workspace's containers on one server and where they log
 *  (GET /observability/logs). error says why it couldn't be read. */
export interface ServerLogs {
    /** "" is the panel host. */
    server_id: string;
    error?: string;
    services: { service_id: string; containers: ContainerLog[] }[];
}

/** A project's own Loki and Grafana ("" when it uses the workspace's). */
export interface ProjectObservability {
    project_id: string;
    name: string;
    loki_url: string;
    grafana_url: string;
}

export const observabilityService = {
    /** Reads every server: where each service's containers send their output. */
    logs: () => fetchAPI<ServerLogs[]>(`/api/v1/observability/logs`),

    projects: () => fetchAPI<ProjectObservability[]>(`/api/v1/observability/projects`),

    getForOrg: () =>
        fetchAPI<ObservabilityConfig | Record<string, never>>(`/api/v1/observability`),

    getForProject: (projectId: string) =>
        fetchAPI<ObservabilityConfig | Record<string, never>>(`/api/v1/projects/${projectId}/observability`),

    /** Forgets the workspace's Loki, Grafana or Prometheus; the service keeps running (D-162). */
    unregister: (kind: "loki" | "grafana" | "prometheus") =>
        fetchAPI<void>(`/api/v1/observability/${kind}`, { method: "DELETE" }),

    registerLoki: (serviceId: string, lokiUrl?: string) =>
        fetchAPI<ObservabilityConfig>(`/api/v1/observability/loki`, {
            method: "PUT",
            body: JSON.stringify({ service_id: serviceId, loki_url: lokiUrl }),
        }),

    registerGrafana: (serviceId: string, grafanaUrl?: string) =>
        fetchAPI<ObservabilityConfig>(`/api/v1/observability/grafana`, {
            method: "PUT",
            body: JSON.stringify({ service_id: serviceId, grafana_url: grafanaUrl }),
        }),

    registerPrometheus: (serviceId: string) =>
        fetchAPI<ObservabilityConfig>(`/api/v1/observability/prometheus`, {
            method: "PUT",
            body: JSON.stringify({ service_id: serviceId }),
        }),

    registerProjectLoki: (projectId: string, serviceId: string, lokiUrl?: string) =>
        fetchAPI<ObservabilityConfig>(`/api/v1/projects/${projectId}/observability/loki`, {
            method: "PUT",
            body: JSON.stringify({ service_id: serviceId, loki_url: lokiUrl }),
        }),

    registerProjectGrafana: (projectId: string, serviceId: string, grafanaUrl?: string) =>
        fetchAPI<ObservabilityConfig>(`/api/v1/projects/${projectId}/observability/grafana`, {
            method: "PUT",
            body: JSON.stringify({ service_id: serviceId, grafana_url: grafanaUrl }),
        }),
};
