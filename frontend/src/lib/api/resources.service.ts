import type { components } from "./schema";
import { fetchAPI, fetchVersioned } from "./client";
import { formatBytes } from "./sharedvolume.service";

// Memory and CPU limits, usage and suggestions (D-41).

export interface ResourceSuggestion {
    direction: "up" | "down" | "set";
    /** "768m" for memory, "1.5" (cores) for CPU. */
    value: string;
    reason: string;
    days: number;
}

export interface ResourceRow {
    /** "" for a single container. */
    compose_service: string;
    kind: "database" | "cache" | "tool" | "app";
    memory: {
        /** The limit in force, "512m" or "1g"; "" is none. */
        limit: string;
        source: "panel" | "compose_file" | "none";
        /** The compose file's limit, even when the panel's overrides it. */
        file_limit: string;
        peak_14d_mb: number | null;
        /** Days measured of the last 14 (UTC). */
        days: number;
        oom_kills_7d: number;
        suggestion: ResourceSuggestion | null;
    };
    cpu: {
        /** Cores, "1.5"; "" is none. */
        limit: string;
        p95_cores: number | null;
        throttled_max_pct: number | null;
        /** Yesterday's (UTC) average. */
        wait_avg_pct: number | null;
        suggestion: ResourceSuggestion | null;
    };
    /** Restarts over the last 24 hours, counted from the minute samples;
     *  null where nothing counts them yet (D-156). */
    restarts_24h: number | null;
    /** Where the counted samples start, while they cover less than 24 hours. */
    restarts_since: string | null;
}

/** HTTP request latency over the last 24 hours, from the server's Traefik (D-156). */
export type ServiceLatency = components["schemas"]["ServiceLatency"];

export interface ServiceResources {
    grace_until: string | null;
    rows: ResourceRow[];
    /** null: no minute recorded (no domain, or routed in the last two minutes). */
    latency_24h: ServiceLatency | null;
}

export interface ComposeLimit {
    memory_mb: number;
    cpu_cores: number | null;
}

export interface ServerResources {
    /** Only the rows with a memory suggestion. */
    services: { service_id: string; service_name: string; compose_service: string; suggestion: ResourceSuggestion }[];
    frees_mb: number;
    /** Yesterday's (UTC) CPU waiting; day is "2026-10-18". */
    cpu_wait: { day: string; avg_pct: number } | null;
}

export const resourcesService = {
    /** The memory limit a new service running this gets (D-41). */
    defaultLimit: (addonSlug: string, image: string) =>
        fetchAPI<{ kind: ResourceRow["kind"]; memory_mb: number }>(
            `/api/v1/resources/default-limit?addon_slug=${encodeURIComponent(addonSlug)}&image=${encodeURIComponent(image)}`,
        ),

    /** version is the panel limits': what setComposeLimits must send back. */
    service: async (serviceId: string): Promise<ServiceResources & { version: string }> => {
        const { data, version } = await fetchVersioned<ServiceResources>(`/api/v1/services/${serviceId}/resources`);
        return { ...data, version };
    },

    /** Replaces the stack's panel limits; one left out goes back to the compose
     *  file's. version is the one they were read with: the API refuses a stale
     *  one (changedSinceRead). */
    setComposeLimits: async (serviceId: string, version: string, limits: Record<string, ComposeLimit>): Promise<ServiceResources & { version: string }> => {
        const body: components["schemas"]["ComposeLimitsInput"] = { version, limits };
        const saved = await fetchVersioned<ServiceResources>(`/api/v1/services/${serviceId}/compose-limits`, {
            method: "PUT",
            body: JSON.stringify(body),
        });
        return { ...saved.data, version: saved.version };
    },

    server: (serverId: string) => fetchAPI<ServerResources>(`/api/v1/servers/${serverId}/resources`),
};

/** MiB of a limit, read as the backend reads it (resources.ParseMB):
 *  "512m", "1G", "1.5g", "2gb", "524288k", plain bytes; 0 for "" or
 *  anything else. */
export function limitMB(limit: string): number {
    const m = /^(\d+(?:\.\d+)?)(?:([gmk])b?)?$/.exec(limit.trim().toLowerCase());
    if (!m) return 0;
    const mib = m[2] === "g" ? 1024 : m[2] === "m" ? 1 : m[2] === "k" ? 1 / 1024 : 1 / 1024 ** 2;
    return Math.ceil(parseFloat(m[1]) * mib);
}

/** "704 MiB" under 1 GiB, "2.1 GiB" from there. */
export function formatMiB(mb: number): string {
    return mb >= 1024 ? formatBytes(mb * 1024 ** 2) : `${mb} MiB`;
}

/** A UTC day as "19 Oct": the backend's days are UTC. */
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** "19 Oct", in UTC, as the backend writes dates (Go's "2 Jan"); the browser's
 *  locale data would give "19 Sept" and may differ between browsers. */
export function formatDay(iso: string): string {
    const d = new Date(iso);
    return `${d.getUTCDate()} ${MONTHS[d.getUTCMonth()]}`;
}

/** "↓ 320m", "↑ 768m", "Set 320m". */
export function suggestionLabel(s: ResourceSuggestion): string {
    return `${s.direction === "down" ? "↓" : s.direction === "up" ? "↑" : "Set"} ${s.value}`;
}
