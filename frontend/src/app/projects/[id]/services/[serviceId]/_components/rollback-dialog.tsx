"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { RotateCcw } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { deploymentService, type Deployment } from "@/lib/api/deployment.service";
import type { Service } from "@/lib/api/service.service";
import { environmentsQuery } from "@/lib/queries";
import { DeployConfirm } from "@/app/projects/_lib/deploy-confirm";
import { deploymentRef } from "./service-view";
import { isoTime, whileDeploying } from "./deployment-view";

interface RollbackDialogProps {
  service: Service;
  /** The deployment to go back to; null closes the dialog. */
  target: Deployment | null;
  /** What runs now, which the rollback replaces. */
  live: Deployment | null;
  onClose: () => void;
}

/** Confirms a rollback (board: Deployments, view rollback), and starts it;
 *  in a protected environment the protected confirm does (D-143). */
export function RollbackDialog({ service, target, live, onClose }: RollbackDialogProps) {
  const queryClient = useQueryClient();
  const rollback = useMutation({
    mutationFn: (deploymentId: string) => deploymentService.rollback(service.id, deploymentId),
    onSuccess: () => {
      toast.success("Rollback triggered");
      onClose();
      queryClient.invalidateQueries({ queryKey: ["deployments", service.id] });
      queryClient.invalidateQueries({ queryKey: ["service", service.id] });
    },
    onError: (err: unknown) => toast.error(err instanceof Error && err.message ? err.message : "Failed to trigger rollback"),
  });
  const inEnv = !!service.project_id && !!service.environment_id;
  const { data: envs, isPending: envsPending } = useQuery({ ...environmentsQuery(service.project_id), enabled: inEnv });
  // Until protection is known, neither dialog: the plain one could roll back unconfirmed.
  if (inEnv && envsPending) return null;
  const env = envs?.find((e) => e.id === service.environment_id);
  if (env?.protected) {
    return (
      <DeployConfirm
        open={target !== null}
        service={service}
        env={env}
        mode="rollback"
        target={target}
        pending={rollback.isPending}
        onCancel={onClose}
        onConfirm={() => target && rollback.mutate(target.id)}
      />
    );
  }
  const ref = target ? deploymentRef(target) || target.id.slice(0, 8) : "";
  const liveRef = live ? deploymentRef(live) || live.id.slice(0, 8) : "";
  const row = "flex gap-3 px-3 py-[9px]";
  const dt = "w-[72px] flex-none text-muted";

  return (
    <Dialog open={target !== null} onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="max-w-[520px] rounded-xl p-0">
        <DialogHeader className="gap-1.5 px-5 pt-[18px] text-left">
          <DialogTitle className="text-[17px] font-semibold leading-6">Roll back {service.name} to {ref}?</DialogTitle>
          <DialogDescription className="text-[13px] leading-[19px] text-muted">
            This starts a new deployment from the image this one {service.source_type === "git" ? "built" : "ran"} on{" "}
            <span className="font-mono text-xs text-fg">{target ? isoTime(target.created_at) : ""}</span>. It runs with{" "}
            {service.name}&apos;s current variables, secrets and domains, not the ones it had then.
          </DialogDescription>
        </DialogHeader>
        {target && (
          <dl className="mx-5 mt-4 flex flex-col rounded-lg border border-subtle text-[13px]">
            {(target.commit_sha || target.commit_message) && (
              <div className={row}>
                <dt className={dt}>Commit</dt>
                <dd className="min-w-0">
                  {target.commit_sha && <span className="font-mono text-xs text-fg">{target.commit_sha.slice(0, 7)}</span>}{" "}
                  <span className="text-fg">{target.commit_message}</span>
                </dd>
              </div>
            )}
            <div className={`${row} border-t border-subtle first:border-t-0`}>
              <dt className={dt}>Image</dt>
              <dd className="min-w-0 break-all text-fg">
                {service.source_type === "git"
                  ? <>built by deployment <span className="font-mono text-xs">{target.id.slice(0, 8)}</span></>
                  : <span className="font-mono text-xs">{target.image_tag}</span>}
              </dd>
            </div>
            {live && (
              <div className={`${row} border-t border-subtle`}>
                <dt className={dt}>Replaces</dt>
                <dd className="min-w-0 text-fg">
                  <span className="font-mono text-xs">{liveRef}</span>, live now. {whileDeploying(service, "It", ref)}
                </dd>
              </div>
            )}
          </dl>
        )}
        <DialogFooter className="gap-2 px-5 pb-5 pt-[18px]">
          <Button variant="outline" onClick={onClose}>Cancel</Button>
          <Button onClick={() => target && rollback.mutate(target.id)} disabled={rollback.isPending} className="gap-1.5">
            <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
            {rollback.isPending ? "Rolling back…" : `Roll back to ${ref}`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
