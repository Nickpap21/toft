"use client";

import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { deploymentService, type Deployment } from "@/lib/api/deployment.service";
import type { Environment } from "@/lib/api/environment.service";
import type { Service } from "@/lib/api/service.service";
import { environmentsQuery, serviceQuery } from "@/lib/queries";
import { DeployConfirm } from "./deploy-confirm";

/** Redeploy: the live deployment's image again with today's variables and
 *  settings, no build (the rollback endpoint on the live deployment,
 *  recorded "Redeploy by …"). In a protected environment it confirms first
 *  (D-55); elsewhere it starts at once, as a row's Deploy does. Render
 *  `dialog` once beside the rows. */
export function useRedeploy() {
  const queryClient = useQueryClient();
  const [ask, setAsk] = useState<{ service: Service; env: Environment; target: Deployment } | null>(null);
  const run = useMutation({
    mutationFn: (v: { serviceId: string; deploymentId: string; name: string }) => deploymentService.rollback(v.serviceId, v.deploymentId),
    onSuccess: (_, v) => {
      setAsk(null);
      toast.success(`Redeploying ${v.name}`);
      queryClient.invalidateQueries({ queryKey: ["deployments", v.serviceId] });
      queryClient.invalidateQueries({ queryKey: ["service", v.serviceId] });
      queryClient.invalidateQueries({ queryKey: ["project-overview"] });
    },
    onError: (err: unknown, v) => toast.error(err instanceof Error && err.message ? `${v.name}: ${err.message}` : `Failed to redeploy ${v.name}`),
  });

  async function redeploy(serviceId: string, deploymentId: string) {
    try {
      const service = await queryClient.fetchQuery(serviceQuery(serviceId));
      const envs = service.environment_id ? await queryClient.fetchQuery(environmentsQuery(service.project_id)) : [];
      const env = envs.find((e) => e.id === service.environment_id);
      if (!env?.protected) {
        run.mutate({ serviceId, deploymentId, name: service.name });
        return;
      }
      setAsk({ service, env, target: await deploymentService.get(serviceId, deploymentId) });
    } catch (err) {
      toast.error(err instanceof Error && err.message ? err.message : "Couldn’t read the service to redeploy it");
    }
  }

  const dialog = ask && (
    <DeployConfirm
      open
      service={ask.service}
      env={ask.env}
      mode="redeploy"
      target={ask.target}
      pending={run.isPending}
      onCancel={() => setAsk(null)}
      onConfirm={() => run.mutate({ serviceId: ask.service.id, deploymentId: ask.target.id, name: ask.service.name })}
    />
  );
  return { redeploy, dialog, pending: run.isPending };
}
