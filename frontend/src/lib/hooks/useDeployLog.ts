"use client";

import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { deploymentService } from "@/lib/api/deployment.service";

/** A deploy's log lines so far (the last 50), followed live while it runs.
 *  When it ends, what shows it (Home, the service's deployments, status,
 *  usage and containers) is refetched. Empty ids follow nothing. */
export function useDeployLog(serviceId: string, deploymentId: string): string[] {
  const queryClient = useQueryClient();
  const [log, setLog] = useState<{ id: string; lines: string[] }>({ id: "", lines: [] });
  useEffect(() => {
    if (!deploymentId) return;
    return deploymentService.streamLogs(
      serviceId,
      deploymentId,
      (entry) => setLog((prev) => ({
        id: deploymentId,
        lines: [...(prev.id === deploymentId ? prev.lines : []), entry.message].slice(-50),
      })),
      () => {
        void queryClient.invalidateQueries({ queryKey: ["dashboard"] });
        void queryClient.invalidateQueries({ queryKey: ["deployments", serviceId] });
        void queryClient.invalidateQueries({ queryKey: ["service", serviceId] });
        // The new container: its usage, and the containers to pick from.
        void queryClient.invalidateQueries({ queryKey: ["stats", serviceId] });
        void queryClient.invalidateQueries({ queryKey: ["containers", serviceId] });
      },
    );
  }, [serviceId, deploymentId, queryClient]);
  return log.id === deploymentId ? log.lines : [];
}
