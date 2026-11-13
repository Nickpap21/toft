"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { CapacityDialog, capacityWarningFrom, type CapacityWarning } from "@/components/ui/capacity-dialog";
import { deploymentService } from "@/lib/api/deployment.service";
import type { WorkspaceEnvironment } from "@/lib/api/environment.service";
import type { WorkspaceService } from "@/lib/api/service.service";
import { serviceQuery, workspaceEnvironmentsQuery } from "@/lib/queries";
import { possessive } from "@/lib/utils";
import { DeployConfirm } from "@/app/projects/_lib/deploy-confirm";

/** A deploy started on the Domains page: in a protected environment it
 *  confirms first (D-55, D-143), elsewhere it starts at once. Protection is
 *  read before deciding, so nothing deploys while it's unknown. Render
 *  `dialogs` beside what calls `start`. */
export function useDomainsDeploy() {
  const queryClient = useQueryClient();
  const [ask, setAsk] = useState<{ service: WorkspaceService; env: WorkspaceEnvironment } | null>(null);
  const [capacity, setCapacity] = useState<CapacityWarning | null>(null);
  const { data: full } = useQuery({ ...serviceQuery(ask?.service.id ?? ""), enabled: !!ask });
  const deploy = useMutation({
    mutationFn: (v: { service: WorkspaceService; force?: boolean; commitSha?: string }) => deploymentService.trigger(v.service.id, { commitSha: v.commitSha }, v.force),
    onSuccess: (_, v) => {
      setAsk(null);
      toast.success(`Deploying ${v.service.name}`);
      queryClient.invalidateQueries({ queryKey: ["workspace-services"] });
    },
    onError: (err: unknown, v) => {
      const warning = capacityWarningFrom(err);
      if (warning) setCapacity(warning);
      else toast.error(err instanceof Error && err.message ? `${v.service.name}: ${err.message}` : `Failed to deploy ${v.service.name}`);
    },
  });

  async function start(service: WorkspaceService) {
    let env: WorkspaceEnvironment | undefined;
    if (service.environment_id) {
      try {
        env = (await queryClient.fetchQuery(workspaceEnvironmentsQuery)).find((e) => e.id === service.environment_id);
      } catch {
        toast.error(`Couldn’t read ${possessive(service.name)} environment to deploy it`);
        return;
      }
    }
    if (env?.protected) setAsk({ service, env });
    else deploy.mutate({ service });
  }

  const dialogs = (
    <>
      {ask && full && (
        <DeployConfirm
          open
          service={full}
          env={ask.env}
          pending={deploy.isPending}
          onCancel={() => setAsk(null)}
          onConfirm={(commitSha) => deploy.mutate({ service: ask.service, commitSha })}
        />
      )}
      <CapacityDialog
        warning={capacity}
        serviceName={deploy.variables?.service.name}
        onCancel={() => setCapacity(null)}
        onDeployAnyway={() => {
          setCapacity(null);
          if (deploy.variables) deploy.mutate({ ...deploy.variables, force: true });
        }}
      />
    </>
  );
  return { start, pending: deploy.isPending, dialogs };
}

/** A needs row's Deploy <service>: its next deploy routes the domain. */
export function DeployButton({ service, className }: { service: WorkspaceService; className: string }) {
  const { start, pending, dialogs } = useDomainsDeploy();
  return (
    <>
      <button type="button" className={className} disabled={pending} onClick={() => void start(service)}>
        {pending ? "Deploying…" : `Deploy ${service.name}`}
      </button>
      {dialogs}
    </>
  );
}
