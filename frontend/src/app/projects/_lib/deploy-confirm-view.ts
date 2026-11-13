// What a deploy into a protected environment confirms (D-143, board
// ReworkDeployConfirm): the commits it moves, what else goes live with it,
// and who else is deploying there.
import type { CheckSummary, GitCommit } from "@/lib/api/code.service";
import type { Deployment } from "@/lib/api/deployment.service";
import type { Domain } from "@/lib/api/domain.service";
import type { ProjectOverview } from "@/lib/api/environment.service";
import type { Service } from "@/lib/api/service.service";
import { ago } from "@/lib/time";
import { triggerWho, whileDeploying } from "../[id]/services/[serviceId]/_components/deployment-view";
import type { Pending } from "../[id]/services/[serviceId]/_components/variables-view";

/** Deploy builds or pulls what's new; Roll back goes to an older deployment;
 *  Redeploy runs the live one again with today's settings. */
export type ConfirmMode = "deploy" | "rollback" | "redeploy";

export interface CommitRow {
  sha: string;
  subject: string;
  author: string;
  /** Its own deploy failed in this environment: that deployment's error, word for word. */
  failed?: { text: string; deploymentId: string };
}

/** The commits a deploy takes live (or a roll back takes away), newest
 *  first. compare lists them oldest first. A commit whose own deployment
 *  failed here says so, with that deployment's error. */
export function commitRows(commits: GitCommit[], deployments: Pick<Deployment, "id" | "commit_sha" | "status" | "error_msg" | "created_at">[], now: number): CommitRow[] {
  return [...commits].reverse().map((c) => {
    const d = deployments.find((x) => x.commit_sha === c.sha && x.status === "failed");
    return {
      sha: c.sha.slice(0, 7),
      subject: c.subject,
      author: c.author_name,
      ...(d ? { failed: { text: `Its own deploy failed here ${ago(d.created_at, now)}.${d.error_msg ? ` ${d.error_msg}` : ""}`, deploymentId: d.id } } : {}),
    };
  });
}

/** "3 commits go live", "1 commit leaves"; "250+" when compare stopped. */
export function commitsLabel(n: number, truncated: boolean, mode: ConfirmMode): string {
  const count = `${n}${truncated ? "+" : ""} ${n === 1 && !truncated ? "commit" : "commits"}`;
  if (mode === "rollback") return `${count} ${n === 1 && !truncated ? "leaves" : "leave"}`;
  return `${count} go${n === 1 && !truncated ? "es" : ""} live`;
}

export interface AlsoRow {
  key: string;
  text: string;
  by?: string;
  at: string;
}

/** Also goes live: the variables and secrets saved since the live deploy
 *  (step 7's pendingChanges), then the domains added since and not yet
 *  routed. With nothing live every domain not yet routed goes live. */
export function alsoGoesLive(pending: Pending[], domains: Pick<Domain, "hostname" | "routed_at" | "created_at" | "created_by">[], since?: string | null): AlsoRow[] {
  const t = since ? new Date(since).getTime() : -Infinity;
  const added = domains
    .filter((d) => !d.routed_at && new Date(d.created_at).getTime() > t)
    .map((d) => ({ key: d.hostname, text: "routed from this deploy", by: d.created_by || undefined, at: d.created_at }));
  return [...pending, ...added];
}

/** "web is deploying in production too (alex, 2m ago). The two don’t wait
 *  for each other." From the project's recent deployments; "" when none. */
export function concurrentLine(recent: ProjectOverview["recent_deployments"], serviceId: string, envName: string, now: number): string {
  const busy = recent.filter((d) => (d.status === "pending" || d.status === "building") && d.service_id !== serviceId && d.environment_name === envName);
  if (busy.length === 0) return "";
  if (busy.length > 1) {
    const names = new Intl.ListFormat("en", { type: "conjunction" }).format([...new Set(busy.map((d) => d.service_name))]);
    return `${names} are deploying in ${envName} too. They don’t wait for each other.`;
  }
  const d = busy[0];
  const who = triggerWho(d);
  return `${d.service_name} is deploying in ${envName} too (${who ? `${who}, ` : ""}${ago(d.created_at, now)}). The two don’t wait for each other.`;
}

/** What users see while it deploys, by strategy (D-42) and health check.
 *  back: say a roll back is one step (off where the next line says so). */
export function strategyLine(
  service: Pick<Service, "effective_deploy_strategy" | "health_check_retries" | "health_check_path">,
  live: string,
  next: string,
  mode: ConfirmMode,
  back = true,
): string {
  if (!live) return `Nothing is live yet: if ${next} fails its health check, nothing changes for users.`;
  // A redeploy, or a deploy pulling the tag that is live, starts the same version again.
  const same = mode === "redeploy" || live === next;
  const [from, to] = same ? [`The running ${live}`, "its new container"] : [live, next];
  if (service.effective_deploy_strategy === "recreate") {
    return `Stop then start: ${whileDeploying(service, from, to)} Users get a few seconds of downtime.`;
  }
  const check = (service.health_check_retries ?? 0) > 0 && service.health_check_path;
  const serving = check ? `${from} keeps serving until ${to} passes GET ${service.health_check_path}.` : whileDeploying(service, from, to);
  const tail = same || !back ? "" : `; afterwards you can roll back to ${live} in one step`;
  return `Rolling: ${serving} If it doesn’t, nothing changes for users${tail}.`;
}

/** A commit's checks as the Deploys row says them. */
export function checksText(c: CheckSummary | null | undefined): { text: string; tone: "ok" | "bad" | "pending" } | null {
  if (!c) return null;
  if (c.state === "success") return { text: "✓ checks passed", tone: "ok" };
  if (c.state === "failure") return { text: `✗ ${c.failed} ${c.failed === 1 ? "check" : "checks"} failed`, tone: "bad" };
  return { text: "checks running", tone: "pending" };
}
