// Workspace services' rules (board: ReworkSharedServices, D-147), kept apart
// from the page so they are tested.

import type { WorkspaceEnvironment } from "@/lib/api/environment.service";
import type { Server } from "@/lib/api/server.service";
import type { WorkspaceService } from "@/lib/api/service.service";
import { isServing, statusLabel } from "@/lib/status";
import { possessive } from "@/lib/utils";
import { inProse, onServer, reachFrom, serverName } from "../projects/_lib/projects-view";

type ServerRef = Pick<Server, "id" | "name">;
type ServiceRef = Pick<WorkspaceService, "id" | "name" | "scope" | "project_id" | "environment_id" | "server_id">;
type ProjectRef = { id: string; name: string };

const and = (items: string[]) => new Intl.ListFormat("en").format(items);

/** The header's server part: "all on fsn1-prod-01", "on 2 servers". */
export function serverPart(services: { server_id?: string }[], servers: ServerRef[] | undefined): string {
  const ids = new Set(services.map((s) => s.server_id ?? ""));
  if (ids.size > 1) return `on ${ids.size} servers`;
  const [id] = ids;
  if (id === undefined) return "";
  const name = inProse(serverName(id, servers));
  return name ? `all on ${name}` : "";
}

/** "for 21 days" since the live deployment went live; "" when none is, or it
 *  isn't serving now (a stopped service hasn't been running for 21 days). */
export function liveFor(status: string, liveSince: string | null | undefined, now: number): string {
  if (!liveSince || !isServing(status)) return "";
  const minutes = Math.floor((now - new Date(liveSince).getTime()) / 60_000);
  const [n, unit] = minutes >= 1440 ? [Math.floor(minutes / 1440), "day"] : minutes >= 60 ? [Math.floor(minutes / 60), "hour"] : [Math.max(minutes, 1), "minute"];
  return `for ${n} ${unit}${n === 1 ? "" : "s"}`;
}

/** The Services card's summary: "4 running", "3 running · 1 failed". */
export function statusLine(services: { runtime_status: string }[]): string {
  const counts = new Map<string, number>();
  for (const s of services) {
    const label = statusLabel(s.runtime_status).toLowerCase();
    counts.set(label, (counts.get(label) ?? 0) + 1);
  }
  return [...counts]
    .sort(([a], [b]) => Number(b === "running") - Number(a === "running"))
    .map(([label, n]) => `${n} ${label}`)
    .join(" · ");
}

/** Reached from's two lines: every project by the address it has, else the
 *  environments on its server by alias, if it has one (Reached as's test);
 *  null when no project runs on its server. */
export function reachedFrom(
  service: { id: string; server_id?: string; connect_to?: { port: number } },
  services: ServiceRef[],
  environments: WorkspaceEnvironment[],
  projects: ProjectRef[],
  domains: { service_id: string }[],
): { by: string; via: string } | null {
  const serverId = service.server_id ?? "";
  const elsewhere = services.some((s) => s.scope === "environment" && (s.server_id || "") !== serverId);
  const alias = !!service.connect_to?.port;
  if ((elsewhere || !alias) && domains.some((d) => d.service_id === service.id)) return { by: "Every project", via: "by its address" };
  if (!alias) return { by: "No other service", via: "it has no alias or address" };
  const by = reachFrom(serverId, services, environments, projects);
  return by ? { by, via: "by alias" } : null;
}

/** Who runs on a server, as the Logs table's second line: "storefront’s
 *  production, the workspace’s 4". */
export function runsOn(serverId: string, services: ServiceRef[], environments: WorkspaceEnvironment[], projects: ProjectRef[]): string {
  const parts = onServer(serverId, services, environments, projects).map((t) => (t.all ? t.name : `${possessive(t.name)} ${t.here.map((e) => e.name).join(", ")}`));
  const workspace = services.filter((s) => s.scope === "org" && (s.server_id || "") === serverId).length;
  if (workspace) parts.push(`the workspace’s ${workspace}`);
  return parts.join(", ");
}

/**
 * Who reaches the workspace services, one row per server that runs a
 * project's services (D-31): by alias on a server that runs workspace
 * services; elsewhere only those with an address.
 */
export function whoReaches(
  servers: ServerRef[] | undefined,
  services: ServiceRef[],
  environments: WorkspaceEnvironment[],
  projects: ProjectRef[],
  domains: { service_id: string }[],
): { id: string; server: string; text: string }[] {
  const workspace = services.filter((s) => s.scope === "org");
  const addressed = workspace.filter((s) => domains.some((d) => d.service_id === s.id)).map((s) => s.name);
  const ids = [...new Set(services.filter((s) => s.scope === "environment").map((s) => s.server_id || ""))];
  return ids
    .map((id) => {
      const who = onServer(id, services, environments, projects)
        .map((t) => {
          if (t.all) return t.name;
          const named = t.here.filter((e) => !e.preview).map((e) => e.name);
          return `${possessive(t.name)} ${and(t.here.some((e) => e.preview) ? [...named, "previews"] : named)}`;
        })
        .join(", ");
      const text = workspace.some((s) => (s.server_id || "") === id)
        ? `${who}, by alias`
        : addressed.length > 0
          ? `${who}: only ${and(addressed)}, at ${addressed.length === 1 ? "its address" : "their addresses"}`
          : `${who}: none, no workspace service has an address`;
      return { id, server: serverName(id, servers), text };
    })
    .sort((a, b) => a.server.localeCompare(b.server));
}
