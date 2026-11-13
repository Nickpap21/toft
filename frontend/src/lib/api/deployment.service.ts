import type { IssueRef } from "./issue.service";
import { queryOptions } from "@tanstack/react-query";
import { ApiError, fetchAPI, streamEvents } from "./client";

// Monotonically-increasing counter for synthetic log entry IDs when JSON parse fails.
// Using Date.now() risks duplicates under rapid-fire errors; a counter does not.
let _logFallbackId = 0;

export type DeploymentStatus =
    | "pending"
    | "building"
    | "running"
    | "failed"
    | "stopped";

export interface Deployment {
    id: string;
    service_id: string;
    status: DeploymentStatus;
    image_tag: string;
    /** Its image was removed to free disk space (D-135): it can't be rolled back to. */
    image_removed?: boolean;
    commit_sha: string;
    /** A push's head commit subject. */
    commit_message?: string;
    /** How and by whom: "Manual · Alex", "API · Alex", "Rollback · Alex", "Redeploy · Alex" (a rollback to the live one), "Push · alex", "Merge · alex"; an agent's "Agent · Claude Code for Alex", "Rollback · Agent · Claude Code for Alex" (D-100). */
    triggered_by?: string;
    /** The commit a compose service's repository was checked out at. */
    compose_commit_sha?: string;
    /** The pull request whose merge started it (D-77). */
    /** The pull request whose merge started it (D-77), with the issues it ships (D-81). */
    pull_request?: { number: number; repository_id: string; issues?: IssueRef[] } | null;
    error_msg: string;
    created_at: string;
    started_at?: string;
    finished_at?: string;
}

export interface LogEntry {
    id: number;
    deployment_id: string;
    level: string;
    message: string;
    created_at: string;
}

export const deploymentService = {
    listForService: (serviceId: string, limit = 25, offset = 0) =>
        fetchAPI<Deployment[]>(
            `/api/v1/services/${serviceId}/deployments?limit=${limit}&offset=${offset}`
        ),

    /** Every deployment the panel keeps of a service, newest first. */
    listAll: async (serviceId: string) => {
        // ponytail: the whole history in pages of 100, since the hourly GC keeps
        // 200 per service (D-44); filter in the API if retention grows.
        const all: Deployment[] = [];
        for (;;) {
            const page = await deploymentService.listForService(serviceId, 100, all.length);
            all.push(...page);
            if (page.length < 100) return all;
        }
    },

    get: (serviceId: string, deploymentId: string) =>
        fetchAPI<Deployment>(
            `/api/v1/services/${serviceId}/deployments/${deploymentId}`
        ),

    trigger: (
        serviceId: string,
        opts?: {
            commitSha?: string;
            imageTag?: string;
            gitRef?: string;
            gitRefType?: "branch" | "tag";
            /** Moves the service to this server ("" the panel host). */
            serverId?: string;
        },
        force = false
    ) =>
        fetchAPI<Deployment>(
            `/api/v1/services/${serviceId}/deployments${force ? '?force=true' : ''}`,
            {
                method: "POST",
                body: JSON.stringify({
                    commit_sha: opts?.commitSha ?? "",
                    image_tag: opts?.imageTag ?? "",
                    git_ref: opts?.gitRef ?? "",
                    git_ref_type: opts?.gitRefType ?? "",
                    ...(opts?.serverId !== undefined ? { server_id: opts.serverId } : {}),
                }),
            }
        ),

    rollback: (serviceId: string, deploymentId: string) =>
        fetchAPI<Deployment>(`/api/v1/services/${serviceId}/deployments/${deploymentId}/rollback`, {
            method: "POST",
        }),

    getLogs: (serviceId: string, deploymentId: string, search?: string) =>
        fetchAPI<LogEntry[]>(
            `/api/v1/services/${serviceId}/deployments/${deploymentId}/logs${search ? `?search=${encodeURIComponent(search)}` : ""}`
        ),

    /**
     * Opens a Server-Sent Events stream for live log tailing.
     * Returns a cleanup function that closes the stream.
     * onDone is called with the final deployment + service status when the stream ends.
     * onError is called with an error message if the stream fails before done.
     */
    streamLogs: (
        serviceId: string,
        deploymentId: string,
        onLine: (entry: LogEntry) => void,
        onDone: (result?: { status: string; runtime_status: string }) => void,
        onError?: (msg: string) => void
    ): (() => void) => {
        const ctrl = new AbortController();
        // Ending without "done", or failing, is an error; an abort (cleanup,
        // or after "done") is not.
        const fail = (err?: unknown) => {
            if (ctrl.signal.aborted) return;
            onError?.(err instanceof ApiError ? err.message : "Log stream disconnected unexpectedly.");
            onDone();
        };

        streamEvents(
            `/api/v1/services/${serviceId}/deployments/${deploymentId}/logs/stream`,
            (type, data) => {
                if (type === "done") {
                    ctrl.abort();
                    let result: { status: string; runtime_status: string } | undefined;
                    try {
                        result = JSON.parse(data);
                    } catch {
                        // No final status: the caller refetches.
                    }
                    onDone(result);
                    return;
                }
                if (type !== "message") return;
                try {
                    onLine(JSON.parse(data) as LogEntry);
                } catch {
                    // Unparseable line — treat as a raw message
                    onLine({
                        id: --_logFallbackId,
                        deployment_id: deploymentId,
                        level: "info",
                        message: data,
                        created_at: new Date().toISOString(),
                    });
                }
            },
            ctrl.signal
        ).then(() => fail(), fail);

        return () => ctrl.abort();
    },
};

/** A service's whole deployment history: the Deployments tab, its count in
 *  the tab bar, Overview's recent ones and a deployment's page share it. */
export const allDeploymentsQuery = (serviceId: string) =>
    queryOptions({
        queryKey: ["deployments", serviceId, "all"],
        queryFn: () => deploymentService.listAll(serviceId),
    });
