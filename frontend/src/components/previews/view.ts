import type { Preview, PreviewStatus } from "@/lib/api/preview.service";

/** "a, b and c". */
export function joinNames(xs: string[]): string {
  const ys = xs.filter(Boolean);
  return ys.length <= 1 ? ys.join("") : `${ys.slice(0, -1).join(", ")} and ${ys[ys.length - 1]}`;
}

/** What a preview of env is made of, under the settings’ Copies select. */
export function copiesLine(env: string, rebuilt: string[], reached: string[]): string {
  if (rebuilt.length === 0) return `No service of ${env} builds from this project’s repositories: a preview would have nothing to build.`;
  const build = `A preview builds ${joinNames(rebuilt)} from the pull request (${rebuilt.length === 1 ? "it builds" : "they build"} from this repository)`;
  const reach = reached.length > 0 ? `, and reaches ${env}’s ${joinNames(reached)} by name, with ${env}’s variables and secrets.` : `, with ${env}’s variables and secrets.`;
  return `${build}${reach} It writes to ${env}’s database, and doesn’t run ${env}’s migrate or pre-deploy commands.`;
}

/** An address a preview's service would get, as the settings show it. */
export function addressExample(domain: string, service: string, project: string, ip: string | undefined): string {
  if (domain) return `pr-42-${service}.${domain}`;
  return `${service}-pr42-${project.toLowerCase().replace(/[^a-z0-9]+/g, "-")}.${(ip ?? "203.0.113.10").replaceAll(".", "-")}.sslip.io`;
}

/** The domain Let's Encrypt counts certificates under, roughly: its last
 *  two labels (the backend uses the Public Suffix List). */
export function registeredDomain(domain: string): string {
  return domain.split(".").slice(-2).join(".");
}

/** A preview as the turn-off dialog lists it. */
export function previewRowText(p: Preview): string {
  const name = p.environment?.name ?? `pr-${p.pull_number}`;
  const services = p.services.map((s) => s.name).join(", ");
  return [name, services, STATUS[p.status].label].filter(Boolean).join(" · ");
}

export type Tone = "running" | "building" | "failed" | "pending" | "muted";

/** Each status's words and colour. */
export const STATUS: Record<PreviewStatus, { label: string; tone: Tone }> = {
  needs_approval: { label: "Needs approval", tone: "pending" },
  waiting_room: { label: "Waiting for room", tone: "muted" },
  blocked: { label: "Can’t start", tone: "muted" },
  starting: { label: "Starting", tone: "building" },
  updating: { label: "Updating", tone: "building" },
  running: { label: "Running", tone: "running" },
  failed: { label: "Failed", tone: "failed" },
  stopped: { label: "Stopped", tone: "muted" },
  removing: { label: "Removing", tone: "muted" },
};

/** The status as the project's table says it: Stopped for room when a
 *  deploy needed the memory. */
export function statusLabel(p: Preview): string {
  if (p.status === "stopped" && p.stopped_reason === "room") return "Stopped for room";
  return STATUS[p.status].label;
}

/** The line under a status in the project's table; max is how many
 *  previews the project runs at once. */
export function statusWhy(p: Preview, max = 5): string {
  switch (p.status) {
    case "needs_approval":
      if (p.opened_with_key?.agent) return `opened by ${p.opened_with_key.agent}, an agent`;
      return p.opened_with_key ? "opened with an API key" : "pushed to with an API key";
    case "waiting_room":
      return `${max} previews at once`;
    case "starting":
    case "updating": {
      const building = p.services.filter((s) => s.status === "starting").map((s) => s.name);
      return building.length ? `building ${joinNames(building)}` : "";
    }
    case "failed": {
      const f = p.services.find((s) => s.status === "failed");
      return f ? `${f.name}’s deploy failed` : "";
    }
    case "stopped":
      return p.stopped_reason === "room" ? (p.note ?? "") : "stopped by hand";
    case "blocked":
      return "can’t start";
  }
  return "";
}

/** The first address of a preview, and how many more. */
export function firstAddress(p: Preview): { url: string; more: number } | null {
  const all = p.services.flatMap((s) => s.addresses);
  return all.length ? { url: all[0], more: all.length - 1 } : null;
}

export const TONE_TEXT: Record<Tone, string> = {
  running: "text-status-running-fg",
  building: "text-status-building-fg",
  failed: "text-status-failed-fg",
  pending: "text-status-pending-fg",
  muted: "text-muted",
};

export const TONE_CHIP: Record<Tone, string> = {
  running: "bg-status-running-soft text-status-running-fg",
  building: "bg-status-building-soft text-status-building-fg",
  failed: "bg-status-failed-soft text-status-failed-fg",
  pending: "bg-status-pending-soft text-status-pending-fg",
  muted: "bg-raised text-muted",
};
