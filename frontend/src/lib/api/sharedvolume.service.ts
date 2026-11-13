import type { components } from "./schema";
import { fetchAPI, fetchVersioned } from "./client";

// Shared volumes (D-38): storage the platform creates and names, owned by
// the workspace, which its services on the same server mount.

export type SharedVolumeMountedBy = components["schemas"]["MountedBy"];

export type SharedVolume = components["schemas"]["Volume"];

export type SharedVolumeMount = components["schemas"]["Mount"];

/** 10 GiB, the default limit (founders, 2026-10-12). */
export const DEFAULT_SIZE_LIMIT_BYTES = 10 * 1024 ** 3;

export const sharedVolumeService = {
    list: () => fetchAPI<SharedVolume[]>("/api/v1/shared-volumes"),

    create: (input: { name: string; server_id: string; size_limit_bytes: number }) =>
        fetchAPI<SharedVolume>("/api/v1/shared-volumes", { method: "POST", body: JSON.stringify(input) }),

    updateLimit: (id: string, sizeLimitBytes: number) =>
        fetchAPI<SharedVolume>(`/api/v1/shared-volumes/${id}`, {
            method: "PATCH",
            body: JSON.stringify({ size_limit_bytes: sizeLimitBytes }),
        }),

    delete: (id: string) => fetchAPI<void>(`/api/v1/shared-volumes/${id}`, { method: "DELETE" }),

    /** A service's mounts and the version of this read. */
    serviceMounts: (serviceId: string) => fetchVersioned<SharedVolumeMount[]>(`/api/v1/services/${serviceId}/shared-volumes`),

    /** Replaces a service's mounts; version is the one they were read with:
     *  the API refuses a stale one (changedSinceRead). */
    setServiceMounts: (serviceId: string, version: string, mounts: SharedVolumeMount[]) => {
        const body: components["schemas"]["MountsInput"] = {
            version,
            mounts: mounts.map(({ shared_volume_id, target, read_only }) => ({ shared_volume_id, target, read_only })),
        };
        return fetchVersioned<SharedVolumeMount[]>(`/api/v1/services/${serviceId}/shared-volumes`, {
            method: "PUT",
            body: JSON.stringify(body),
        });
    },
};

/** Binary units, as the alerts say them: "3.1 GiB". */
export function formatBytes(n: number): string {
    if (n < 1024) return `${n} B`;
    const units = ["KiB", "MiB", "GiB", "TiB", "PiB"];
    let v = n / 1024;
    let i = 0;
    while (v >= 1024 && i < units.length - 1) {
        v /= 1024;
        i++;
    }
    return `${v.toFixed(1)} ${units[i]}`;
}
