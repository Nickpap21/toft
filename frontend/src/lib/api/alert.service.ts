import { fetchAPI } from "./client";

/** A workspace's alert webhook. signing_secret is present only in the
 *  response that creates it or rotates it. */
export interface AlertChannel {
    webhook_url: string;
    enabled: boolean;
    updated_at: string;
    signing_secret?: string;
    /** The last message posted (an alert or Send test) and the receiver's
     *  answer: its HTTP status, null when it gave none, and the error. */
    last_delivery_at: string | null;
    last_delivery_status: number | null;
    last_delivery_error: string;
}

export type AlertKind =
    | "deploy_failed"
    | "oom_killed"
    | "service_down"
    | "service_restarting"
    | "service_unhealthy"
    | "container_exited"
    | "server_unreachable"
    | "disk_full"
    | "volume_full"
    | "backup_failed"
    // The panel's own (D-68), about "control-plane", for operators.
    | "panel_container"
    | "panel_certificate"
    | "panel_upgrade_failed"
    // A repository's import or mirror failing (D-79), about the mirror.
    | "mirror_failing"
    // A server's agent away, or frozen after a restore (D-106, D-113).
    | "agent_offline"
    | "agent_frozen"
    // A host's updates (D-133), about a server or "control-plane".
    | "security_updates"
    | "reboot_required"
    | "docker_advisory";

/** An alert open now or fired in the last 30 days (GET /alerts). */
export interface RecentAlert {
    id: string;
    kind: AlertKind;
    /** A service, server or shared volume ID, or "control-plane". */
    subject: string;
    detail: string;
    fired_at: string;
    resolved_at: string | null;
    /** A failed deploy or a memory kill: one message, nothing to resolve. */
    one_off: boolean;
    subject_name: string;
    project_id?: string;
    project_name: string;
    environment_name: string;
    server_name: string;
}

export const alertService = {
    /** Null when the workspace has no webhook. */
    get: () => fetchAPI<AlertChannel | null>("/api/v1/alerts/channel"),

    put: (webhookUrl: string, enabled: boolean) =>
        fetchAPI<AlertChannel>("/api/v1/alerts/channel", {
            method: "PUT",
            body: JSON.stringify({ webhook_url: webhookUrl, enabled }),
        }),

    rotateSecret: () =>
        fetchAPI<AlertChannel>("/api/v1/alerts/channel/rotate-secret", { method: "POST" }),

    /** Sends a test message now; 502 says why it wasn't delivered. */
    test: () => fetchAPI<void>("/api/v1/alerts/channel/test", { method: "POST" }),

    delete: () => fetchAPI<void>("/api/v1/alerts/channel", { method: "DELETE" }),

    recent: () => fetchAPI<RecentAlert[]>("/api/v1/alerts"),
};
