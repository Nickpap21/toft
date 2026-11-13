// Pure helpers for Settings → Agents (board: Agents, D-100).
import type { AgentConnection, AgentPolicyCell, AgentWaiting } from "@/lib/api/agents.service";
import type { AuditEntry } from "@/lib/api/org.service";
import { agentRule } from "@/lib/audit";

export type Decision = "allow" | "ask" | "never";

const RANK: Record<Decision, number> = { never: 0, ask: 1, allow: 2 };

/** Loosening (never < ask < allow) needs the owner's password. */
export const loosens = (from: Decision, to: Decision) => RANK[to] > RANK[from];

export const DECISION_LABEL: Record<Decision, string> = {
  allow: "Allow",
  ask: "Ask a person",
  never: "Never",
};

// What each tool asks to do, in a waiting request's title (D-102).
const WANTS: Record<string, string> = {
  deploy: "deploy",
  rollback: "roll back",
  create_preview: "start a preview of",
  create_branch: "make a branch in",
  open_pr: "open a pull request in",
  create_issue: "open an issue in",
  comment: "comment in",
  set_env_var: "change a variable of",
  set_secret: "set a secret of",
  run_runbook_step: "run a runbook step in",
};

/** "Claude Code wants to roll back billing-api / worker in production". */
export function waitingTitle(w: Pick<AgentWaiting, "agent" | "tool" | "target">): string {
  return `${w.agent} wants to ${WANTS[w.tool] ?? w.tool} ${w.target}`;
}

export type Action = AgentPolicyCell["action"];
export type EnvClass = AgentPolicyCell["env_class"];

/** The policy table's rows (board Agents, D-102): code and issues touch no
 *  environment, so they have one cell, env class "any". */
export const POLICY_ROWS: { action: Action; label: string; short: string; tools: string; note?: string; hint?: string }[] = [
  { action: "deploy", label: "Deploy or roll back", short: "Deploy or roll back", tools: "deploy rollback create_preview", note: "Databases and caches count as Protected, wherever they run" },
  {
    action: "code", label: "Open branches and pull requests", short: "Branches and pull requests", tools: "create_branch open_pr",
    hint: "Anywhere in the project: a branch or pull request touches no environment. Pushes go only to branches the agent created; its pull requests’ previews wait for approval of each commit and run Sandboxed.",
  },
  { action: "issues", label: "Open issues and comment", short: "Issues and comments", tools: "create_issue comment", hint: "Anywhere in the project: at most 20 issues and 60 comments an hour per agent" },
  { action: "variables", label: "Change variables", short: "Change variables", tools: "set_env_var" },
  { action: "secrets", label: "Set a secret (agents never read one back)", short: "Set a secret", tools: "set_secret" },
  { action: "runbooks", label: "Run runbook steps in a service’s containers", short: "Runbook steps", tools: "run_runbook_step" },
];

export const ENV_CLASSES: { cls: EnvClass; name: string }[] = [
  { cls: "preview", name: "Previews" },
  { cls: "other", name: "Other environments" },
  { cls: "protected", name: "Protected" },
];

/** A row's classes: "any" alone for code and issues. */
export const classesOf = (action: Action): EnvClass[] =>
  action === "code" || action === "issues" ? ["any"] : ["preview", "other", "protected"];

type Cell = Pick<AgentPolicyCell, "action" | "env_class" | "decision">;

export const cellOf = (cells: Cell[], action: Action, cls: EnvClass): Decision | undefined =>
  cells.find((c) => c.action === action && c.env_class === cls)?.decision;

/** A project's own cells against the workspace's, in the table's order:
 *  "Deploy or roll back · Other environments", its decision and the workspace's. */
export function projectDiffs(own: Cell[], ws: Cell[]): { where: string; decision: Decision; ws: Decision }[] {
  return POLICY_ROWS.flatMap((r) =>
    classesOf(r.action).flatMap((cls) => {
      const d = cellOf(own, r.action, cls);
      if (!d) return [];
      const name = ENV_CLASSES.find((e) => e.cls === cls)?.name;
      return [{ where: name ? `${r.short} · ${name}` : r.short, decision: d, ws: cellOf(ws, r.action, cls) ?? d }];
    }),
  );
}

/** Whether setting a project's cell ("" drops its rule) loosens what
 *  applies to the project: then the API asks for the password. */
export const projectLoosens = (own: Decision | undefined, ws: Decision, to: Decision | "") =>
  loosens(own ?? ws, to || ws);

/** Whether dropping every rule of a project loosens one of its cells. */
export const resetLoosens = (own: Cell[], ws: Cell[]) =>
  own.some((c) => loosens(c.decision, cellOf(ws, c.action, c.env_class) ?? c.decision));

/** Whole minutes left, rounded up, never below 0. */
export function minutesLeft(iso: string, now: number): number {
  return Math.max(0, Math.ceil((new Date(iso).getTime() - now) / 60_000));
}

/** "OAuth · claude.ai" for a client known by its metadata URL, else the
 *  client registered itself. */
export function clientLabel(c: Pick<AgentConnection, "client_kind" | "client">): string {
  if (c.client_kind === "cimd") {
    try {
      return `OAuth · ${new URL(c.client).host}`;
    } catch {
      return "OAuth · registered client";
    }
  }
  return "OAuth · registered client";
}

export function reaches(c: Pick<AgentConnection, "all_projects" | "projects">): string {
  return c.all_projects ? "Every project" : c.projects.join(", ");
}

export const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** Expiry within a week shows amber: the person consents again soon. */
export const expiresSoon = (iso: string, now: number) =>
  new Date(iso).getTime() - now < 7 * 86_400_000;

export type Tone = "ok" | "wait" | "no";

/** An agent.call's outcome as the board's chip. */
export function outcomeChip(outcome: unknown): { label: string; tone: Tone } {
  switch (outcome) {
    case "allowed":
      return { label: "Allowed", tone: "ok" };
    case "approved":
      return { label: "Approved", tone: "ok" };
    case "asked":
      return { label: "Asked a person", tone: "wait" };
    case "denied":
      return { label: "Denied", tone: "no" };
    case "expired":
      return { label: "Expired", tone: "no" };
    case "refused":
      return { label: "Refused", tone: "no" };
    case "failed":
      return { label: "Failed", tone: "no" };
    default:
      return { label: typeof outcome === "string" && outcome ? capitalize(outcome) : "Unknown", tone: "no" };
  }
}

/** "storefront / api · staging · <detail> · Founders’ rule": what a call
 *  touched, and which rule decided it (D-101). */
export function callTarget(e: AuditEntry, workspace: string): string {
  const d = (e.detail ?? {}) as Record<string, unknown>;
  // The API records a code ("agents_paused"); show it as words.
  const detail = typeof d.detail === "string" ? d.detail.replaceAll("_", " ") : "";
  return [e.resource, e.target?.environment, detail, agentRule(e, workspace)].filter(Boolean).join(" · ");
}

/** The policy table's columns with their hints: the workspace's
 *  environments of each class by name, a few of them (board Agents). */
export function envColumns(envs: { name: string; preview?: unknown; protected: boolean }[]): { cls: EnvClass; name: string; hint: string }[] {
  const names = (f: (e: (typeof envs)[number]) => boolean, none: string) => {
    const all = [...new Set(envs.filter(f).map((e) => e.name))];
    return all.length === 0 ? none : all.length > 3 ? `${all.slice(0, 3).join(", ")}, …` : all.join(", ");
  };
  const hint: Record<string, string> = {
    preview: names((e) => !!e.preview, "pull requests’ previews"),
    other: names((e) => !e.preview && !e.protected, "none yet"),
    protected: names((e) => !e.preview && e.protected, "none yet"),
  };
  return ENV_CLASSES.map((e) => ({ ...e, hint: hint[e.cls] }));
}
