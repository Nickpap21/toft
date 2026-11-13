import type { Deployment } from "@/lib/api/deployment.service";
import type { ServiceLatency } from "@/lib/api/resources.service";
import type { Service } from "@/lib/api/service.service";
import type { StatusTone } from "@/lib/status";
import { addonName, imageRef } from "../../../../_lib/projects-view";

// The Service page (board: private/design/canvas/Service.dc.html). The pure
// parts: the page renders what these return.

/** A deployment as its row reads: a record stays "running" once it went
 *  live, so only the service's active deployment is Live, and the earlier
 *  ones were Replaced. When the service has no active deployment (a failed
 *  deploy clears it, though the previous container keeps serving), liveId
 *  is the last that went live, shown as Last live. */
export function deploymentState(d: Pick<Deployment, "id" | "status">, liveId?: string, liveKnown = true): { label: string; tone: StatusTone } {
  switch (d.status) {
    case "running":
      if (d.id !== liveId) return { label: "Replaced", tone: "neutral" };
      return liveKnown ? { label: "Live", tone: "running" } : { label: "Last live", tone: "neutral" };
    case "failed":
      return { label: "Failed", tone: "failed" };
    case "building":
      return { label: "Building", tone: "building" };
    case "stopped":
      return { label: "Stopped", tone: "neutral" };
    default:
      return { label: "Queued", tone: "pending" };
  }
}

export const inProgress = (d: Pick<Deployment, "status">) => d.status === "building" || d.status === "pending";

/** What a deployment ran: its commit, else its image's tag (without a
 *  digest: the full reference is the hover's, deploymentRefTitle). */
export function deploymentRef(d: Pick<Deployment, "commit_sha" | "image_tag">): string {
  if (d.commit_sha) return d.commit_sha.slice(0, 7);
  if (!d.image_tag) return "";
  const ref = imageRef(d.image_tag);
  const tag = ref.slice(ref.lastIndexOf("/") + 1);
  return tag.length > 24 ? `${tag.slice(0, 24)}…` : tag;
}

/** The address anyone can open an admin tool at, with neither basic auth
 *  nor an IP allowlist; "" when it has none or is protected. The Inbox's
 *  exposed_tool says the same (the API's kind decides what a tool is). */
export function exposedAt(
  s: Pick<Service, "kind" | "source_type" | "basic_auth_user" | "ip_allowlist">,
  hostnames: string[],
): string {
  if (s.kind !== "tool" || s.source_type !== "docker-image") return "";
  if (s.basic_auth_user || (s.ip_allowlist ?? []).length > 0) return "";
  return hostnames[0] ?? "";
}

/** What the header's Deploy deploys, as its label names it: a git
 *  service's branch, an image service's saved image, a stack's own
 *  IMAGE_TAG (imageTag; both follow what went live, so never a version
 *  whose deploy failed); "" for a stack without its own. */
export function deployTarget(s: Pick<Service, "source_type" | "git_branch" | "docker_image">, imageTag = ""): string {
  if (s.source_type === "git") return s.git_branch ?? "";
  if (s.source_type === "docker-image") return deploymentRef({ commit_sha: "", image_tag: s.docker_image ?? "" });
  return deploymentRef({ commit_sha: "", image_tag: imageTag });
}

/** The hover of deploymentRef: the full commit or image reference. */
export const deploymentRefTitle = (d: Pick<Deployment, "commit_sha" | "image_tag">) =>
  d.commit_sha || d.image_tag || undefined;

/** Start to finish, in seconds; null until it finished. */
export function deploymentSeconds(d: Pick<Deployment, "started_at" | "finished_at">): number | null {
  if (!d.started_at || !d.finished_at) return null;
  return (new Date(d.finished_at).getTime() - new Date(d.started_at).getTime()) / 1000;
}

/** Whether rolling back to d would run something other than live: another
 *  image or commit. Rolling back to the same one is a redeploy. */
export function differsFrom(d: Pick<Deployment, "image_tag" | "commit_sha">, live: Pick<Deployment, "image_tag" | "commit_sha"> | null): boolean {
  return !live || d.image_tag !== live.image_tag || (d.commit_sha ?? "") !== (live.commit_sha ?? "");
}

/** The deployment a rollback goes back to: the latest one before the live
 *  one that went live, has an image to run again, and runs something else. */
export function rollbackTarget(deployments: Deployment[], activeId?: string): Deployment | null {
  const index = deployments.findIndex((d) => d.id === activeId);
  const live = index >= 0 ? deployments[index] : null;
  return deployments.slice(index + 1).find((d) => d.status === "running" && !!d.image_tag && !d.image_removed && differsFrom(d, live)) ?? null;
}

/** A runtime log line's level, guessed from its words: logs carry none. */
export function logLevel(line: string): "error" | "warn" | "" {
  if (/\b(error|fatal|panic|exception|critical)\b/i.test(line)) return "error";
  if (/\b(warn|warning)\b/i.test(line)) return "warn";
  return "";
}

/** "founders/storefront", from a repository URL. */
export function repoPath(url: string): string {
  return url.replace(/^(https?:\/\/|ssh:\/\/|git@)[^/:]+[/:]/, "").replace(/\.git$/, "");
}

/** Where the service is deployed from, as the board's Source reads.
 *  repoName is the project's repository it builds from, by name
 *  ("founders/storefront", D-77), when it builds from one. */
export function sourceText(
  s: Pick<Service, "source_type" | "git_repo_url" | "git_branch" | "base_directory" | "docker_image" | "compose_repo_url" | "compose_file_path"> & Partial<Pick<Service, "repository_id">>,
  repoName = "",
): string {
  const repo = s.repository_id ? repoName || "the project’s repository" : "";
  switch (s.source_type) {
    case "git":
      return [repo || repoPath(s.git_repo_url ?? ""), s.git_branch, s.base_directory].filter(Boolean).join(" · ");
    case "docker-image":
      return s.docker_image ?? "";
    default:
      return repo || s.compose_repo_url
        ? [repo || repoPath(s.compose_repo_url ?? ""), s.git_branch, s.compose_file_path].filter(Boolean).join(" · ")
        : "compose file in the panel";
  }
}

/** The board's "Services / Go · Dockerfile" line: how the service is built. */
export function kindText(s: Pick<Service, "source_type" | "build_pack" | "addon_slug">): string {
  if (s.addon_slug) return addonName(s.addon_slug);
  switch (s.source_type) {
    case "git":
      return s.build_pack === "dockerfile" ? "Git · Dockerfile" : s.build_pack === "nixpacks" ? "Git · Nixpacks" : "Git";
    case "docker-image":
      return "Image";
    default:
      return "Compose";
  }
}

/** A memory limit as the boards write it: "512m" → "512 MiB", "1536m" →
 *  "1.5 GiB", "1g" → "1 GiB"; "" for none. Docker notation stays in inputs. */
export function memoryText(limit?: string): string {
  const m = /^(\d+(?:\.\d+)?)([mg])$/i.exec((limit ?? "").trim());
  if (!m) return limit ?? "";
  const mb = parseFloat(m[1]) * (m[2].toLowerCase() === "g" ? 1024 : 1);
  return mb >= 1024 ? `${+(mb / 1024).toFixed(1)} GiB` : `${+mb.toFixed(0)} MiB`;
}

/** A service's limits as the header reads them: "1 vCPU · 512 MiB"; "" for none. */
export function limitsText(cpu?: string, memory?: string): string {
  return [cpu ? `${cpu} vCPU` : "", memoryText(memory)].filter(Boolean).join(" · ");
}

const msText = (ms: number) =>
  ms >= 1000 ? `${+(ms / 1000).toFixed(1)} s` : ms < 10 ? `${+ms.toFixed(1)} ms` : `${Math.round(ms)} ms`;

const unmeasuredText: Record<NonNullable<ServiceLatency["unmeasured"]>, string> = {
  adopted: "not measured: Toft didn't start this server's Traefik",
  agent: "not measured yet: this server's agent upgrades when it connects",
  traefik: "not measured yet: this server's Traefik restarts to measure it",
};

/** The Usage facts' p95 latency (D-156): "≈72 ms" interpolated within its
 *  bucket, the bucket's bounds in the title; the count alone under 20
 *  requests; why, when the server's Traefik isn't measured. */
export function latencyView(l: ServiceLatency): { value: string; note: string; title?: string } {
  if (l.unmeasured) return { value: "", note: unmeasuredText[l.unmeasured] };
  const count = `${l.requests.toLocaleString("en-US")} ${l.requests === 1 ? "request" : "requests"}`;
  const window = l.since
    ? `since ${new Date(l.since).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}`
    : "in the last 24 hours";
  if (l.requests === 0) return { value: "", note: "no requests", title: `No requests ${window}` };
  if (l.p95_ms === null || !l.p95_bucket) return { value: "", note: count, title: `${count} ${window}: too few for a p95` };
  const b = l.p95_bucket;
  const bounds = b.high_ms === null
    ? `Above ${msText(b.low_ms)}, Traefik's top bucket`
    : `Between ${msText(b.low_ms)} and ${msText(b.high_ms)}, from Traefik's buckets`;
  return {
    value: b.high_ms === null ? `over ${msText(b.low_ms)}` : `≈${msText(l.p95_ms)}`,
    note: count,
    title: `${bounds}; ${count} ${window}`,
  };
}

/** "3h 12m" (the board's Uptime): minutes under an hour, hours and minutes
 *  under a day, then days and hours. */
export function uptimeText(ms: number): string {
  const m = Math.max(0, Math.floor(ms / 60_000));
  if (m < 1) return "under a minute";
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m`;
  return `${Math.floor(h / 24)}d ${h % 24}h`;
}
