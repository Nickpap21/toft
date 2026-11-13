import type { components } from "./schema";
import { queryOptions } from "@tanstack/react-query";
import { fetchAPI } from "./client";

// GET /api/v1/dashboard: Home's workspace-wide data (internal/dashboard).

export interface FailedDeploy {
    id: string;
    service_id: string;
    service_name: string;
    project_id: string;
    project_name: string;
    environment_name: string;
    server_name: string;
    error_summary: string;
    commit_sha: string;
    failed_at: string;
}

/** subject is a service, server or shared volume ID, or "control-plane". */
export interface OpenAlert {
    id: string;
    kind: string;
    subject: string;
    subject_name: string;
    detail: string;
    project_id: string;
    project_name: string;
    environment_name: string;
    fired_at: string;
}

export interface LiveDeploy {
    id: string;
    status: string;
    service_id: string;
    service_name: string;
    source_type: string;
    has_compose_repo: boolean;
    project_id: string;
    project_name: string;
    environment_name: string;
    server_name: string;
    commit_sha: string;
    created_at: string;
}

export interface ProjectDeploy {
    project_id: string;
    status: string;
    service_name: string;
    source_type: string;
    git_branch: string;
    docker_image: string;
    created_at: string;
}

export interface RecentDeployment {
    id: string;
    status: string;
    service_id: string;
    service_name: string;
    project_id: string;
    project_name: string;
    environment_name: string;
    created_at: string;
    finished_at: string | null;
}

export type DashboardData = components["schemas"]["DashboardData"];

/** Refreshed every 10 s, and on every deploy event (useEventStream). */
export const dashboardQuery = queryOptions({
    queryKey: ["dashboard"],
    queryFn: () => fetchAPI<DashboardData>("/api/v1/dashboard"),
    refetchInterval: 10_000,
});
