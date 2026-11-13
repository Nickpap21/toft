import type { Service } from "@/lib/api/service.service";
import type { StatusTone } from "@/lib/status";

export type ProjectHealth = "failed" | "building" | "unhealthy" | "running" | "idle";

export interface HealthSummary {
  health: ProjectHealth;
  running: number;
  deploying: number;
  unhealthy: number;
  failing: number;
}

/** One signal per project: anything failing wins, then anything deploying,
 *  then anything unhealthy, then anything running; a project with nothing
 *  running is idle. */
export function projectHealth(services: Pick<Service, "runtime_status">[] | undefined): HealthSummary {
  const list = services ?? [];
  const count = (s: string) => list.filter((x) => x.runtime_status === s).length;
  const failing = count("error");
  const deploying = count("deploying");
  const unhealthy = count("unhealthy");
  const running = count("running");
  const health: ProjectHealth = failing ? "failed" : deploying ? "building" : unhealthy ? "unhealthy" : running ? "running" : "idle";
  return { health, running, deploying, unhealthy, failing };
}

/** "N running" beside an environment, everywhere it is said: the
 *  environment's own services that run. Project services serve every
 *  environment and are counted on their own card, not in each environment. */
export function runningIn(
  services: Pick<Service, "runtime_status" | "environment_id">[] | undefined,
  envId: string | "all",
): number {
  return (services ?? []).filter(
    (s) => s.runtime_status === "running" && (envId === "all" || s.environment_id === envId),
  ).length;
}

/** Words for screen readers and tooltips, so colour is never the only signal. */
export function healthLabel(summary: HealthSummary): string {
  switch (summary.health) {
    case "failed":
      return summary.failing === 1 ? "1 service failing" : `${summary.failing} services failing`;
    case "building":
      return "deploying";
    case "unhealthy":
      return summary.unhealthy === 1 ? "1 service unhealthy" : `${summary.unhealthy} services unhealthy`;
    case "running":
      return "running";
    default:
      return "nothing running";
  }
}

export const HEALTH_TONE: Record<ProjectHealth, StatusTone> = {
  failed: "failed",
  building: "building",
  unhealthy: "pending",
  running: "running",
  idle: "neutral",
};
