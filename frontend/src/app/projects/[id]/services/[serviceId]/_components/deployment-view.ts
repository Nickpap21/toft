import type { Deployment, LogEntry } from "@/lib/api/deployment.service";
import type { Service } from "@/lib/api/service.service";
import { lineStage, type DeployStage, type StageDef } from "@/components/ui/deploy-stage-tracker";
import type { StatusTone } from "@/lib/status";
import { formatDuration } from "@/lib/time";
import { deploymentState, differsFrom, logLevel } from "./service-view";

// The Deployments tab and a deployment's page (boards: Deployments,
// Deployment; D-44). The pure parts: the pages render what these return.

/** A small outlined button, as in the boards' table rows. */
export const rowButton =
  "touch-target inline-flex h-[30px] items-center rounded-md border border-strong bg-raised px-2.5 text-xs font-medium text-fg hover:border-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus";

// ─── Times ────────────────────────────────────────────────────────────────────

const pad = (n: number) => String(n).padStart(2, "0");

/** "2026-10-12 16:41", local time; with seconds, "2026-10-12 16:41:07". */
export function isoTime(iso: string, seconds = false): string {
  const d = new Date(iso);
  const day = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  return `${day} ${clock(iso, seconds)}`;
}

/** "16:42:05", or "16:42" without seconds. */
export function clock(iso: string, seconds = true): string {
  const d = new Date(iso);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}${seconds ? `:${pad(d.getSeconds())}` : ""}`;
}

/** The boards' "when": relative under a day ("3h ago"), ISO after. */
export function when(iso: string, now: number): { text: string; relative: boolean } {
  const s = Math.max(0, Math.floor((now - new Date(iso).getTime()) / 1000));
  if (s < 60) return { text: "just now", relative: true };
  if (s < 3600) return { text: `${Math.floor(s / 60)}m ago`, relative: true };
  if (s < 86400) return { text: `${Math.floor(s / 3600)}h ago`, relative: true };
  return { text: isoTime(iso), relative: false };
}

const seconds = (from: string, to: string) => (new Date(to).getTime() - new Date(from).getTime()) / 1000;

// ─── The list: filters and counts ─────────────────────────────────────────────

export type StatusFilter = "all" | "live" | "failed" | "refused";

/** The board's chips (ReworkDeploys). */
export const STATUS_FILTERS: { key: StatusFilter; label: string }[] = [
  { key: "all", label: "All" },
  { key: "live", label: "Live" },
  { key: "failed", label: "Failed" },
  { key: "refused", label: "Refused" },
];

/** "Push" of "Push · alex"; "" when nothing was recorded. */
export const triggerKind = (d: Pick<Deployment, "triggered_by">) => d.triggered_by?.split(" · ")[0] ?? "";

/** "alex" of "Push · alex", "Claude Code for alex" of an agent's
 *  "Rollback · Agent · Claude Code for alex" (D-100), "Toft" of Toft's own
 *  "Toft · certificate issued: switch to HTTPS" (D-159); "" when none. */
export function triggerWho(d: Pick<Deployment, "triggered_by">): string {
  const parts = (d.triggered_by ?? "").split(" · ");
  if (parts[0] === "Toft") return "Toft";
  return parts.length > 1 ? parts[parts.length - 1] : "";
}

/** deploymentState, with a deploy the policy refused before it started
 *  as Refused (amber): a Deployment and the project Overview's row both
 *  say so (started_at). */
export function rowState(d: Started, liveId?: string, liveKnown = true): { label: string; tone: StatusTone } {
  return wasRefused(d) ? { label: "Refused", tone: "pending" } : deploymentState(d, liveId, liveKnown);
}

/** Which chip a deployment falls under; Replaced, Building and the rest
 *  only under All. */
export function statusKey(d: Deployment, liveId?: string, liveKnown = true): StatusFilter | "" {
  switch (rowState(d, liveId, liveKnown).label) {
    case "Live": return "live";
    case "Failed": return "failed";
    case "Refused": return "refused";
    default: return "";
  }
}

/** The deployments a chip keeps, and each chip's count. */
export function filterDeployments(
  list: Deployment[],
  status: StatusFilter,
  liveId?: string,
  liveKnown = true,
): { rows: Deployment[]; counts: Record<StatusFilter, number> } {
  const counts: Record<StatusFilter, number> = { all: list.length, live: 0, failed: 0, refused: 0 };
  const rows: Deployment[] = [];
  for (const d of list) {
    const key = statusKey(d, liveId, liveKnown);
    if (key) counts[key]++;
    if (status === "all" || key === status) rows.push(d);
  }
  return { rows, counts };
}

/** "No refused deploys." */
export const noMatchText = (status: StatusFilter) => `No ${status === "all" ? "" : `${status} `}deploys.`;

const VERB: Record<string, string> = { Push: "push", Merge: "merge", Rollback: "rollback", Redeploy: "redeploy", Toft: "redeploy", API: "API deploy" };

/** "merge by alex"; "" when nothing was recorded. */
function byWho(d: Pick<Deployment, "triggered_by">): string {
  const who = triggerWho(d);
  return who ? `${VERB[triggerKind(d)] ?? "deploy"} by ${who}` : "";
}

/** A row's second line: who started it and how it went ("merge by alex ·
 *  1m 51s", "push by alex · refused by the deploy policy"). */
export function deployLine(d: Deployment, now: number): string {
  const head = byWho(d);
  const took = d.started_at ? seconds(d.started_at, d.finished_at ?? new Date(now).toISOString()) : null;
  const how =
    wasRefused(d) ? "refused by the deploy policy"
    : d.status === "failed" ? (took !== null ? `failed after ${formatDuration(took)}` : "failed")
    : d.status === "building" || d.status === "pending" ? (took !== null ? `${formatDuration(took)} so far` : "queued")
    : took !== null ? formatDuration(took) : "";
  return [head, how].filter(Boolean).join(" · ");
}

/** Details' Trigger: "merge of !45", "push by sam". */
export function triggerText(d: Deployment): string {
  if (triggerKind(d) === "Merge" && d.pull_request) return `merge of !${d.pull_request.number}`;
  return byWho(d) || "—";
}

type HowService = Pick<Service, "name" | "source_type" | "git_branch" | "deploy_on_push" | "effective_deploy_strategy" | "health_check_path" | "health_check_retries" | "rollback_images_kept">;

/** The board's "How api deploys": on push, the switch, roll back, and
 *  whether its environment asks first. */
export function howDeploys(s: HowService, env: { name: string; protected?: boolean } | undefined): [string, string][] {
  // `…` marks a branch or path, set in mono.
  const rows: [string, string][] = [];
  if (s.source_type === "git" && s.git_branch) {
    rows.push(["On push", s.deploy_on_push ? `A push to \`${s.git_branch}\` deploys` : `A push to \`${s.git_branch}\` doesn’t deploy; Deploy does`]);
  }
  rows.push([
    "Switch",
    s.effective_deploy_strategy === "recreate" ? "Recreate: the old container stops, then the new one starts"
    : (s.health_check_retries ?? 0) > 0 && s.health_check_path ? `Rolling: the new container passes \`GET ${s.health_check_path}\` before the old one stops`
    : "Rolling: the new container starts before the old one stops",
  ]);
  const kept = s.rollback_images_kept ?? 0;
  if (kept > 0) rows.push(["Roll back", `To ${kept === 1 ? "the last image" : `any of the last ${kept} images`}, with today’s variables`]);
  if (env) rows.push(["Protected", env.protected ? `${env.name} asks before every deploy` : `No: ${env.name} deploys at once`]);
  return rows;
}

// ─── A deployment: its log, by step ───────────────────────────────────────────

export type Level = "info" | "warn" | "error";

/** A deploy log line's level. The pipeline stores every line as info, so ✗
 *  and ⚠ (its own failure and warning marks) and the words decide; ⇄ marks
 *  the panel's own line beside an agent's rollout (D-111). */
export function entryLevel(e: Pick<LogEntry, "level" | "message">): Level {
  const text = e.message.trimStart().replace(/^⇄ /, "");
  if (e.level === "error" || text.startsWith("✗") || logLevel(text) === "error") return "error";
  if (text.startsWith("⚠") || logLevel(text) === "warn" || /\bdenied\b/i.test(text)) return "warn";
  return "info";
}

/** A line as it reads: carriage-return progress ("Counting objects: 1%\r…")
 *  shows only its last value. */
export const lineText = (message: string) => message.split("\r").filter((s) => s.trim() !== "").pop() ?? "";

export interface LogGroup {
  key: DeployStage;
  label: string;
  lines: LogEntry[];
}

/** The log split into the pipeline's steps by their markers, in order: every
 *  step, with the lines it wrote (none for a step not reached). A marker of
 *  an earlier step, or of one not in stages, stays in the current step. */
export function groupLog(entries: LogEntry[], stages: StageDef[]): LogGroup[] {
  const groups = stages.map((s) => ({ key: s.key, label: s.label, lines: [] as LogEntry[] }));
  let at = 0;
  for (const e of entries) {
    const i = stages.findIndex((s) => s.key === lineStage(e.message));
    if (i > at) at = i;
    groups[at].lines.push(e);
  }
  return groups;
}

export type StepState = "done" | "failed" | "running" | "todo";

export interface Step {
  key: DeployStage;
  label: string;
  state: StepState;
  /** "3s", "not reached", "waiting". */
  time: string;
  lines: LogEntry[];
}

/** A deployment that failed before it started: refused by the deploy policy
 *  on a push, before anything ran. */
export const wasRefused = (d: Started) => d.status === "failed" && !d.started_at;

/** A Deployment leaves started_at out of a deploy that never started; the
 *  project Overview's RecentDeployment sends it as null. */
type Started = Pick<Deployment, "id" | "status"> & { started_at?: string | null };

/** Each step of a deployment, with its state and how long it took: from its
 *  first line to the next step's first line (or the end). The deployment's
 *  own record decides done, failed or running; the log, how far it got. */
export function steps(d: Deployment, groups: LogGroup[], now: number): Step[] {
  const reached = groups.reduce((at, g, i) => (g.lines.length > 0 ? i : at), -1);
  const refused = wasRefused(d);
  const end = d.finished_at ?? new Date(now).toISOString();
  return groups.map((g, i) => {
    const next = groups.slice(i + 1).find((n) => n.lines.length > 0);
    const took = g.lines.length > 0 ? formatDuration(seconds(g.lines[0].created_at, next ? next.lines[0].created_at : end)) : "";
    let state: StepState;
    if (d.status === "running") state = "done";
    else if (refused) state = "todo";
    else if (d.status === "failed") state = i < reached ? "done" : i === reached ? "failed" : "todo";
    else state = i < reached ? "done" : i === reached ? "running" : "todo";
    const time =
      state === "todo" ? (refused ? "not run" : d.status === "failed" ? (g.key === "done" ? "not switched" : "not reached") : "waiting")
      : took;
    return { key: g.key, label: g.label, state, time, lines: g.lines };
  });
}

// ─── A deployment: what the page says about it ────────────────────────────────

/** "pushed by sam": who started it, as a verb. */
export function startedBy(d: Pick<Deployment, "triggered_by">): string {
  const kind = triggerKind(d);
  const who = triggerWho(d);
  if (!who) return "";
  switch (kind) {
    case "Push": return `pushed by ${who}`;
    case "Merge": return `merged by ${who}`;
    case "Rollback": return `rolled back by ${who}`;
    case "Redeploy":
    case "Toft": return `redeployed by ${who}`;
    case "API": return `deployed through the API by ${who}`;
    default: return `deployed by ${who}`;
  }
}

/** The deployment a rollback to d would run, when there is one: d went live,
 *  was replaced, ran an image other than what is live now, and its image is
 *  still on the server. */
export function canRollBack(d: Deployment, liveId: string | undefined, live: Deployment | null, liveKnown = true): boolean {
  return !d.image_removed && rollbackCandidate(d, liveId, live, liveKnown);
}

/** A deployment a rollback would go back to, but a disk clean removed its
 *  image (D-135): its Roll back shows greyed, saying why. */
export function rollbackRemoved(d: Deployment, liveId: string | undefined, live: Deployment | null, liveKnown = true): boolean {
  return !!d.image_removed && rollbackCandidate(d, liveId, live, liveKnown);
}

function rollbackCandidate(d: Deployment, liveId: string | undefined, live: Deployment | null, liveKnown: boolean): boolean {
  return deploymentState(d, liveId, liveKnown).label === "Replaced" && !!d.image_tag && differsFrom(d, live);
}

/** Why a removed image's deployment can't be rolled back to. */
export const IMAGE_REMOVED_NOTE =
  "Its image was removed to free disk space, so it can’t be rolled back to. A new deploy builds its branch again; Settings → Resources sets how many deploys keep their image.";

/** The same, in a row of deployments. */
export const IMAGE_REMOVED_SHORT = "Its image was removed to free disk space, so it can’t be rolled back to.";

/** What "Deploy <ref> again" would deploy, only where the deploy runs that
 *  same version: a compose checkout at its recorded commit, or the image a
 *  service still runs. A git build always takes its branch's tip (D-44), so
 *  it is null there, as when nothing was recorded. */
export function deployAgain(
  d: Deployment,
  service: Pick<Service, "source_type" | "docker_image">,
): { ref: string; opts: { commitSha?: string } } | null {
  if (service.source_type === "docker-compose" && d.compose_commit_sha) {
    return { ref: d.compose_commit_sha.slice(0, 7), opts: { commitSha: d.compose_commit_sha } };
  }
  if (service.source_type === "docker-image" && d.image_tag && d.image_tag === service.docker_image) {
    const tag = d.image_tag.slice(d.image_tag.lastIndexOf("/") + 1);
    return { ref: tag, opts: {} };
  }
  return null;
}

/** "stopped at the health check": where a failed deployment stopped. */
export const stoppedAt = (s: Pick<Step, "label">) => `stopped at the ${s.label.toLowerCase()}`;

/** What the log says became of the previous version after a failure, when
 *  it says: the pipeline writes it as it puts the previous container back. */
export function previousKept(entries: Pick<LogEntry, "message">[]): boolean {
  return entries.some((e) => /^↩ (The previous container keeps serving|Started the previous container again)/.test(e.message.trimStart()));
}

/** What the live version does while a deploy runs, by deploy strategy
 *  (D-42) and health check. */
export function whileDeploying(service: Pick<Service, "effective_deploy_strategy" | "health_check_retries">, liveRef: string, ref: string): string {
  if (service.effective_deploy_strategy === "recreate") {
    return `${liveRef} stops just before ${ref} starts, and starts again if ${ref} fails.`;
  }
  return (service.health_check_retries ?? 0) > 0
    ? `${liveRef} keeps serving until ${ref} passes its health check.`
    : `${liveRef} keeps serving until ${ref}'s container starts.`;
}
