"use client";

import type { RefObject } from "react";
import { ApiError } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
} from "@/components/ui/dialog";

/** The 409 a deploy gets when it needs more CPU or memory than the workspace has left on the server. */
export interface CapacityWarning {
    code: string;
    can_bypass: boolean;
    effective_cpu_cores: number;
    effective_memory_mb: number;
    allocated_cpu_cores: number;
    allocated_memory_mb: number;
    requested_cpu_cores: number;
    requested_memory_mb: number;
    /** The panel's own server: every workspace's room, counted in memory only, an instance operator's to pass. */
    panel_host?: boolean;
}

/** The capacity refusal in a deploy's error, or null: it arrives as { data: CapacityWarning } with a 409. */
export function capacityWarningFrom(err: unknown): CapacityWarning | null {
    if (!(err instanceof ApiError) || err.status !== 409) return null;
    const v = (err.body as { data?: unknown } | null)?.data;
    if (typeof v !== "object" || v === null) return null;
    const o = v as Record<string, unknown>;
    const numbers = ["effective_cpu_cores", "effective_memory_mb", "allocated_cpu_cores", "allocated_memory_mb", "requested_cpu_cores", "requested_memory_mb"];
    return typeof o.code === "string" && typeof o.can_bypass === "boolean" && numbers.every((k) => typeof o[k] === "number")
        ? (v as CapacityWarning)
        : null;
}

/** MiB under 1 GiB, else GiB with one decimal. */
export const formatMemory = (mb: number) => (mb >= 1024 ? `${(mb / 1024).toFixed(1)} GiB` : `${Math.round(mb)} MiB`);

const cores = (n: number) => `${Number(n.toFixed(2))} ${n === 1 ? "core" : "cores"}`;

interface CapacityDialogProps {
    /** The refusal; the dialog is open while it is set. */
    warning: CapacityWarning | null;
    /** The service being deployed, named in the title when the page shows several. */
    serviceName?: string;
    onCancel: () => void;
    /** Deploys again past the check; only offered when the role allows it. */
    onDeployAnyway: () => void;
    /** New service's Create and deploy: the service doesn't exist yet, so nothing was created. */
    create?: boolean;
    /** The button that asked: focus returns there at close (DialogContent's returnFocus). */
    returnFocus?: RefObject<HTMLElement | null>;
}

/**
 * A deploy refused because the workspace's share of its server (or the whole
 * server) has too little CPU or memory left. Shows what is left against what
 * the deploy asks for; a role that may bypass the check can deploy anyway.
 */
export function CapacityDialog({ warning, serviceName, onCancel, onDeployAnyway, create = false, returnFocus }: CapacityDialogProps) {
    const w = warning;
    const memLeft = w ? Math.max(w.effective_memory_mb - w.allocated_memory_mb, 0) : 0;
    const cpuLeft = w ? Math.max(w.effective_cpu_cores - w.allocated_cpu_cores, 0) : 0;
    const memShort = !!w && w.allocated_memory_mb + w.requested_memory_mb > w.effective_memory_mb;
    const cpuShort = !!w && w.allocated_cpu_cores + w.requested_cpu_cores > w.effective_cpu_cores;

    return (
        <Dialog open={!!w} onOpenChange={(open) => !open && onCancel()}>
            <DialogContent returnFocus={returnFocus}>
                <DialogHeader>
                    <DialogTitle>{serviceName ? `Not enough room to deploy ${serviceName}` : "Not enough room on the server"}</DialogTitle>
                    <DialogDescription>
                        This {create ? "service" : "deploy"} needs more {memShort && cpuShort ? "memory and CPU" : cpuShort ? "CPU" : "memory"} than{" "}
                        {w?.panel_host ? "the panel’s server has left for its apps" : "this workspace has left on its server"}. Nothing was{" "}
                        {create ? "created" : "deployed"}.
                    </DialogDescription>
                </DialogHeader>
                {w && (
                    <div className="space-y-3 py-1 text-sm">
                        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 rounded-md bg-raised p-3">
                            <dt className="text-muted">Memory</dt>
                            <dd className={memShort ? "text-status-failed-fg" : undefined}>
                                needs {formatMemory(w.requested_memory_mb)}, {formatMemory(memLeft)} left of {formatMemory(w.effective_memory_mb)}
                            </dd>
                            {!w.panel_host && (
                                <>
                                    <dt className="text-muted">CPU</dt>
                                    <dd className={cpuShort ? "text-status-failed-fg" : undefined}>
                                        needs {cores(w.requested_cpu_cores)}, {cores(cpuLeft)} left of {cores(w.effective_cpu_cores)}
                                    </dd>
                                </>
                            )}
                        </dl>
                        <p className="text-muted">
                            {create
                                ? w.can_bypass
                                    ? "Lower its memory above or pick another server, or create and deploy it anyway: the server may then run short for every service on it."
                                    : `Lower its memory above or pick another server, or ask ${w.panel_host ? "an instance operator" : "a workspace owner"} to create it.`
                                : w.can_bypass
                                  ? "Lower the service's limits in its settings, or deploy anyway: the server may then run short for every service on it."
                                  : `Lower the service's limits in its settings, or ask ${w.panel_host ? "an instance operator" : "a workspace owner"} to deploy it.`}
                        </p>
                    </div>
                )}
                <DialogFooter>
                    <Button variant="outline" onClick={onCancel}>
                        Cancel
                    </Button>
                    {w?.can_bypass && (
                        <Button variant="destructive" onClick={onDeployAnyway}>
                            {create ? "Create and deploy anyway" : "Deploy anyway"}
                        </Button>
                    )}
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
}
