"use client";

import { useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiError, changedSinceRead, type Versioned } from "@/lib/api/client";
import { sharedVolumeService, type SharedVolumeMount } from "@/lib/api/sharedvolume.service";
import { Button } from "@/components/ui/button";
import { ReadFailed } from "@/components/shell/unreachable";
import { focusAfterReread } from "@/lib/focus-after-reread";

interface SharedMountsProps {
    serviceId: string;
    /** "" is the control-plane server; only volumes there can be mounted. */
    serverId: string;
    canEdit: boolean;
}

/** After a Save refused because the list changed: the fresh read with what
 *  was changed here on top, by container path. A mount removed here stays
 *  removed, one changed or added here stays so, the rest is the fresh read. */
function rebaseMounts(typed: SharedVolumeMount[], read: SharedVolumeMount[], fresh: SharedVolumeMount[]): SharedVolumeMount[] {
    const unchanged = (t: SharedVolumeMount, r?: SharedVolumeMount) =>
        !!r && t.shared_volume_id === r.shared_volume_id && t.read_only === r.read_only;
    const out = fresh
        .filter((f) => !read.some((r) => r.target === f.target) || typed.some((t) => t.target === f.target))
        .map((f) => {
            const t = typed.find((x) => x.target === f.target);
            return t && !unchanged(t, read.find((r) => r.target === f.target)) ? t : f;
        });
    return [...out, ...typed.filter((t) => !out.some((o) => o.target === t.target) && !unchanged(t, read.find((r) => r.target === t.target)))];
}

/**
 * The service's mounts of the workspace's shared volumes (D-38), under
 * Settings → Volumes. Saved on their own (PUT /services/:id/shared-volumes),
 * and used from the next deploy.
 */
export function SharedMounts({ serviceId, serverId, canEdit }: SharedMountsProps) {
    const queryClient = useQueryClient();
    const key = ["shared-mounts", serviceId];
    const { data: saved, error: savedError, refetch: refetchSaved } = useQuery({ queryKey: key, queryFn: () => sharedVolumeService.serviceMounts(serviceId) });
    const { data: volumes, error: volumesError, refetch: refetchVolumes } = useQuery({ queryKey: ["shared-volumes"], queryFn: () => sharedVolumeService.list() });
    const onServer = (volumes ?? []).filter((v) => v.server_id === serverId);
    // A failed or pending read is never an empty list, and never something to
    // save from.
    const ready = !!saved && !!volumes;
    const failed = (!saved && savedError) || (!volumes && volumesError) || null;

    // Edit a copy, with the read it started from: Save sends that read's
    // version, so a change someone made since is refused, not undone.
    const [draft, setDraft] = useState<{ rows: SharedVolumeMount[]; read: Versioned<SharedVolumeMount[]> } | null>(null);
    const current = draft?.rows ?? saved?.data ?? [];
    const dirty = draft !== null;
    const setRows = (rows: SharedVolumeMount[]) => setDraft((d) => ({ rows, read: d?.read ?? saved! }));

    // The Save refused because the mounts changed since they were read.
    const [conflict, setConflict] = useState(false);
    const readAgain = async () => {
        const fresh = await refetchSaved();
        if (!fresh.data || fresh.isError) {
            toast.error("Couldn’t read the shared volumes. Try again.");
            return;
        }
        setDraft((d) => d && { rows: rebaseMounts(d.rows, d.read.data, fresh.data.data), read: fresh.data });
        setConflict(false);
        focusAfterReread("sm-save", "sm-h");
    };

    const save = useMutation({
        mutationFn: () => sharedVolumeService.setServiceMounts(serviceId, draft!.read.version, current),
        onSuccess: (mounts) => {
            queryClient.setQueryData(key, mounts);
            queryClient.invalidateQueries({ queryKey: ["shared-volumes"] });
            setDraft(null);
            setConflict(false);
            toast.success("Shared volumes saved: they are mounted from the next deploy");
        },
        onError: (err) => {
            if (changedSinceRead(err)) {
                setConflict(true);
                return;
            }
            toast.error(err instanceof ApiError ? err.message : "Failed to save the shared volumes");
        },
    });

    const update = (i: number, patch: Partial<SharedVolumeMount>) =>
        setRows(current.map((m, j) => (j === i ? { ...m, ...patch } : m)));

    return (
        <div className="space-y-2 pt-2">
            <p id="sm-h" tabIndex={-1} className="text-[13px] font-medium text-fg">Shared volumes</p>
            <p className="text-xs leading-[17px] text-muted">
                Storage the workspace shares between services on this server, such as uploaded images. Managed under{" "}
                <Link href="/settings/storage" className="inline-link">
                    Settings → Storage
                </Link>
                ; no operator exception is needed.
            </p>
            {failed && (
                <ReadFailed
                    heading="h2"
                    error={failed}
                    onRetry={() => {
                        if (!saved) void refetchSaved();
                        if (!volumes) void refetchVolumes();
                    }}
                />
            )}
            {!failed && !ready && <div className="h-8 rounded-md bg-raised motion-safe:animate-pulse" role="status" aria-label="Loading the shared volumes" />}
            {ready && current.length > 0 && (
                <ul className="space-y-2">
                    {current.map((m, i) => (
                        <li key={i} className="flex flex-wrap items-center gap-2">
                            <select
                                aria-label="Shared volume"
                                value={m.shared_volume_id}
                                disabled={!canEdit}
                                onChange={(e) => update(i, { shared_volume_id: e.target.value })}
                                className="h-8 rounded-md border border-strong/70 bg-ground text-xs text-fg disabled:opacity-60 px-1.5"
                            >
                                <option value="">Choose a volume…</option>
                                {onServer.map((v) => (
                                    <option key={v.id} value={v.id}>
                                        {v.name}
                                    </option>
                                ))}
                            </select>
                            <input
                                aria-label="Container path"
                                value={m.target}
                                disabled={!canEdit}
                                onChange={(e) => update(i, { target: e.target.value })}
                                placeholder="/app/images"
                                className="h-8 rounded-md border border-strong/70 bg-ground text-xs text-fg disabled:opacity-60 min-w-40 flex-1 px-2 font-mono placeholder:text-muted"
                            />
                            <label className="flex items-center gap-1 text-xs text-muted">
                                <input
                                    type="checkbox"
                                    checked={m.read_only}
                                    disabled={!canEdit}
                                    onChange={(e) => update(i, { read_only: e.target.checked })}
                                />
                                read-only
                            </label>
                            {canEdit && (
                                <button
                                    type="button"
                                    title="Remove"
                                    aria-label="Remove this shared volume"
                                    className="h-6 w-6 rounded text-muted hover:text-status-failed-fg"
                                    onClick={() => setRows(current.filter((_, j) => j !== i))}
                                >
                                    ×
                                </button>
                            )}
                        </li>
                    ))}
                </ul>
            )}
            {conflict && (
                <div role="alert" className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 rounded-md bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
                    <p className="min-w-0 flex-[1_1_320px]">
                        These shared volumes changed since you opened this page, so nothing was saved. What you typed is still here.
                    </p>
                    <Button type="button" variant="outline" size="sm" onClick={() => void readAgain()}>
                        Read them again
                    </Button>
                </div>
            )}
            {canEdit && ready && (
                <div className="flex items-center gap-3">
                    {onServer.length > 0 ? (
                        <button
                            type="button"
                            className="text-[13px] text-link hover:underline"
                            onClick={() => setRows([...current, { shared_volume_id: "", target: "", read_only: false }])}
                        >
                            + Mount a shared volume
                        </button>
                    ) : (
                        <p className="text-xs text-muted">The workspace has no shared volume on this server.</p>
                    )}
                    {dirty && (
                        <Button
                            id="sm-save"
                            size="sm"
                            disabled={save.isPending || conflict || current.some((m) => !m.shared_volume_id || !m.target.trim())}
                            onClick={() => save.mutate()}
                        >
                            {save.isPending ? "Saving…" : "Save shared volumes"}
                        </Button>
                    )}
                </div>
            )}
        </div>
    );
}
