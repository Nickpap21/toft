import { projectRows, type ProjectRow } from "@/components/home/home";
import type { ProjectDeploy } from "@/lib/api/dashboard.service";
import type { WorkspaceEnvironment } from "@/lib/api/environment.service";
import type { Server } from "@/lib/api/server.service";
import type { Service, WorkspaceService } from "@/lib/api/service.service";
import type { ProjectHealth } from "@/components/shell/health";
import type { StatusTone } from "@/lib/status";

/** "just now", "4m ago", "3h ago" within a day, else "2026-10-12 16:41"
 *  (D-46's rule, in the boards' compact form). */
export function ago(iso: string, now: number): string {
  const s = Math.max(0, (now - new Date(iso).getTime()) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** A server by name; "" is the panel host. */
export function serverName(
  id: string | undefined,
  servers: Pick<Server, "id" | "name">[] | undefined,
): string {
  if (!id) return "panel host";
  return servers?.find((s) => s.id === id)?.name ?? "";
}

export interface ProjectsRow extends ProjectRow {
  description: string;
  /** Its environments but previews, oldest (production) first. */
  environments: WorkspaceEnvironment[];
  /** Its pull requests' preview environments: "!12" for pr-12, "docs!3"
   *  for docs-pr-3 (another repository's, preview.EnvironmentName). */
  previews: string[];
  /** Where it runs: "fsn1-prod-01 · staging and previews on hel1-prod-03",
   *  production's server first; "no services". */
  where: string;
  services: number;
  /** The Production column: its first environment's own services. */
  production: Pick<ProjectRow, "bar" | "servicesText" | "servicesTone">;
}

const NEED: ProjectHealth[] = ["failed", "building", "unhealthy", "running", "idle"];

/** The Projects table (board: ReworkProjects): Home's project row, plus
 *  each project's environments, where they run and production's services,
 *  what needs the person first (failing, deploying, unhealthy, running,
 *  idle), then by name. */
export function projectsTable(
  projects: { id: string; name: string; description: string; doc_pages?: number }[],
  services: WorkspaceService[],
  environments: WorkspaceEnvironment[],
  deploys: ProjectDeploy[],
  servers: Pick<Server, "id" | "name">[] | undefined,
): ProjectsRow[] {
  const rows = projectRows(projects, services, deploys);
  return rows
    .map((row, i) => {
      const own = services.filter((s) => s.project_id === row.id);
      const envs = environments.filter((e) => e.project_id === row.id);
      const named = envs.filter((e) => !e.preview);
      const prodId = named[0]?.id;
      const [prod] = projectRows([projects[i]], own.filter((s) => s.environment_id === prodId), []);
      // Each environment's server, by most services; previews as one.
      const serverOf = (ids: Set<string>) => {
        const counts = new Map<string, number>();
        for (const s of own) {
          if (!ids.has(s.environment_id)) continue;
          const name = serverName(s.server_id, servers);
          if (name) counts.set(name, (counts.get(name) ?? 0) + 1);
        }
        return [...counts.entries()].sort((a, b) => b[1] - a[1])[0]?.[0] ?? "";
      };
      const home = prodId ? serverOf(new Set([prodId])) : "";
      const elsewhere = new Map<string, string[]>();
      const place = (label: string, ids: string[]) => {
        const at = serverOf(new Set(ids));
        if (at && at !== home) elsewhere.set(at, [...(elsewhere.get(at) ?? []), label]);
      };
      for (const e of named.slice(1)) place(e.name, [e.id]);
      if (envs.some((e) => e.preview)) place("previews", envs.filter((e) => e.preview).map((e) => e.id));
      const where = [home, ...[...elsewhere].map(([at, labels]) => `${and(labels)} on ${at}`)].filter(Boolean).join(" · ");
      const prodTotal = prod.bar.reduce((n, b) => n + b.n, 0);
      return {
        ...row,
        description: projects[i].description,
        environments: named,
        previews: envs
          .filter((e) => e.preview)
          .map((e) => e.name.replace(/^(?:(.+)-)?pr-(\d+)$/, (_, repo, n) => `${repo ?? ""}!${n}`)),
        where: own.length ? where : "no services",
        services: own.length,
        production: prodTotal
          ? prod
          : {
              bar: [],
              servicesText: own.length ? `Nothing in ${named[0]?.name ?? "production"}` : projects[i].doc_pages ? "Docs only" : "No services",
              servicesTone: "neutral" as StatusTone,
            },
      };
    })
    .sort((a, b) => NEED.indexOf(a.health) - NEED.indexOf(b.health) || a.name.localeCompare(b.name));
}

const and = (list: string[]) => new Intl.ListFormat("en", { type: "conjunction" }).format(list);

/** Catalog names that capitalising the slug gets wrong. */
const ADDON_NAMES: Record<string, string> = {
  mysql: "MySQL", mongodb: "MongoDB", pgadmin: "pgAdmin", phpmyadmin: "phpMyAdmin", mailhog: "MailHog",
  minio: "MinIO", redisinsight: "RedisInsight", rabbitmq: "RabbitMQ", "uptime-kuma": "Uptime Kuma", n8n: "n8n",
};

/** A catalog entry's name by its slug: "Postgres", "Uptime Kuma", "n8n". */
export function addonName(slug: string): string {
  return ADDON_NAMES[slug] ?? slug.charAt(0).toUpperCase() + slug.slice(1);
}

/** "Database", "Tool", "Web", "Worker", "Uptime Kuma", "Compose": what a
 *  service is, as the boards list it under its name. A database or cache
 *  and an admin tool say so whether from the catalog or typed in by image. */
export function kindOf(
  s: Pick<Service, "addon_slug" | "source_type" | "type" | "kind">,
): string {
  if (s.kind === "database" || s.kind === "cache") return "Database";
  if (s.kind === "tool") return "Tool";
  if (s.addon_slug) return addonName(s.addon_slug);
  if (s.source_type === "docker-compose") return "Compose";
  return s.type === "worker" ? "Worker" : "Web";
}

/** An image as people read it: "mysql:8.0" for "mysql:8.0@sha256:…", the
 *  digest left for a hover; "mysql@sha256:1a2b3c4" when it has no tag. */
export function imageRef(ref: string): string {
  const at = ref.indexOf("@");
  if (at < 0) return ref;
  const name = ref.slice(0, at);
  if (name.slice(name.lastIndexOf("/") + 1).includes(":")) return name;
  return `${name}@${ref.slice(at + 1, at + 15)}`;
}

/** Where a service's code comes from, by its source: "main · services/web",
 *  "postgres:16.4", "compose". */
export function sourceOf(
  s: Pick<
    Service,
    | "source_type"
    | "git_branch"
    | "base_directory"
    | "docker_image"
    | "compose_repo_url"
    | "compose_file_path"
  >,
): string {
  if (s.source_type === "docker-image") return imageRef(s.docker_image ?? "");
  if (s.source_type === "docker-compose") {
    return s.compose_repo_url
      ? [s.git_branch, s.compose_file_path].filter(Boolean).join(" · ")
      : "compose";
  }
  const dir = (s.base_directory ?? "").replace(/^\/+|\/+$/g, "");
  return [s.git_branch || "main", dir].filter(Boolean).join(" · ");
}

/** Each project's environments with services on serverId, by project name;
 *  all says they are every one of its environments. */
export function onServer(
  serverId: string,
  services: Pick<
    WorkspaceService,
    "scope" | "project_id" | "environment_id" | "server_id"
  >[],
  environments: WorkspaceEnvironment[],
  projects: { id: string; name: string }[],
): { name: string; here: WorkspaceEnvironment[]; all: boolean }[] {
  return [...projects]
    .sort((a, b) => a.name.localeCompare(b.name))
    .flatMap((p) => {
      const envs = environments.filter((e) => e.project_id === p.id);
      const here = envs.filter((e) =>
        services.some(
          (s) =>
            s.scope === "environment" &&
            s.environment_id === e.id &&
            (s.server_id || "") === serverId,
        ),
      );
      return here.length > 0
        ? [{ name: p.name, here, all: here.length === envs.length }]
        : [];
    });
}

/** Which projects' environments reach a workspace service on serverId by
 *  alias: those with services on the same server (D-31). "storefront
 *  production, staging; billing-api all environments", or "" for none. */
export function reachFrom(
  serverId: string,
  services: Pick<
    WorkspaceService,
    "scope" | "project_id" | "environment_id" | "server_id"
  >[],
  environments: WorkspaceEnvironment[],
  projects: { id: string; name: string }[],
): string {
  return onServer(serverId, services, environments, projects)
    .map((t) =>
      `${t.name} ${t.all && t.here.length > 1 ? "all environments" : t.here.map((e) => e.name).join(", ")}`,
    )
    .join("; ");
}

/** A server's name in a sentence: "the panel host", else its name. */
export const inProse = (name: string) =>
  name === "panel host" ? "the panel host" : name;


const DEPLOYING_STATES = new Set(["deploying", "building", "pending", "queued"]);
const FAILING_STATES = new Set(["error", "unhealthy"]);

/** An environment's state in a word, worst first: "1 deploying",
 *  "2 failing", Running, Idle (nothing deployed yet), Stopped. */
export function envState(svcs: Pick<Service, "runtime_status">[]): { tone: StatusTone; label: string } {
  if (svcs.length === 0) return { tone: "neutral", label: "No services" };
  const deploying = svcs.filter((s) => DEPLOYING_STATES.has(s.runtime_status)).length;
  if (deploying) return { tone: "building", label: `${deploying} deploying` };
  const failing = svcs.filter((s) => FAILING_STATES.has(s.runtime_status)).length;
  if (failing) return { tone: "failed", label: `${failing} failing` };
  if (svcs.some((s) => s.runtime_status === "running")) return { tone: "running", label: "Running" };
  if (svcs.every((s) => s.runtime_status === "idle")) return { tone: "neutral", label: "Idle" };
  return { tone: "neutral", label: "Stopped" };
}

/** The Services tab's status toggle, shown over more than 8 services:
 *  Failing is error or unhealthy, Stopped is anything not running and not
 *  on its way (stopped, or never deployed). */
export type StatusFilter = "" | "failing" | "stopped";
const STOPPED_STATES = new Set(["stopped", "idle"]);
export function inStatus(s: Pick<Service, "runtime_status">, f: StatusFilter): boolean {
  if (f === "failing") return FAILING_STATES.has(s.runtime_status);
  if (f === "stopped") return STOPPED_STATES.has(s.runtime_status);
  return true;
}

/** The Services tab's filter box: a service's name, image or source, or
 *  one of its domains, ignoring case. */
export function matchesText(
  s: Pick<Service, "name" | "docker_image"> & Parameters<typeof sourceOf>[0],
  domains: string[],
  q: string,
): boolean {
  const t = q.trim().toLowerCase();
  if (!t) return true;
  return [s.name, s.docker_image ?? "", sourceOf(s), ...domains].some((x) => x.toLowerCase().includes(t));
}
