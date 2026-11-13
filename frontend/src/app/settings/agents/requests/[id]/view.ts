import type { AgentApproval } from "@/lib/api/agents.service";
import type { StatusTone } from "@/lib/status";
import { formatDateTime } from "@/lib/utils";

// An agent's request waiting for a person (board AgentApproval, D-100): the
// pure parts the page renders.

/** "14:36" in the viewer's time zone. */
export function clock(iso: string): string {
  return new Date(iso).toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" });
}

/** The clock on the same day, else the date too. */
export const stamp = (iso: string, now: number) =>
  new Date(iso).toDateString() === new Date(now).toDateString() ? clock(iso) : formatDateTime(iso);

type Req = Pick<AgentApproval, "agent" | "tool" | "target" | "request">;

const body = (a: Pick<AgentApproval, "request">) => a.request.body ?? {};
const field = (a: Pick<AgentApproval, "request">, key: string) => {
  const v = body(a)[key];
  return typeof v === "string" ? v : typeof v === "number" ? String(v) : "";
};

/** The secret's or variable's key: set_secret's PUT names it in the path. */
export function keyOf(a: Pick<AgentApproval, "request">): string {
  const m = a.request.path.match(/\/secrets\/([^/]+)$/);
  return m ? decodeURIComponent(m[1]) : field(a, "key");
}

/** run_runbook_step's page, step (from 0) and pinned version. */
export function stepOf(a: Pick<AgentApproval, "request">): { project: string; page: string; step: number; version: number } | null {
  const m = a.request.path.match(/\/projects\/([^/]+)\/docs\/([^/]+)\/steps\/(\d+)\/run$/);
  return m ? { project: m[1], page: m[2], step: Number(m[3]), version: Number(body(a).version) || 0 } : null;
}

/** The pull request or issue a comment or a preview names. */
const pullOf = (a: Pick<AgentApproval, "request">) => a.request.path.match(/\/pulls\/(\d+)\//)?.[1] ?? "";
const issueOf = (a: Pick<AgentApproval, "request">) => a.request.path.match(/\/issues\/(\d+)\//)?.[1] ?? "";

/** The tool as a noun: "deploy", "rollback", "secret"… */
const NOUN: Record<string, string> = {
  deploy: "deploy", rollback: "rollback", set_secret: "secret", set_env_var: "variable", run_runbook_step: "step",
  create_branch: "branch", open_pr: "pull request", create_issue: "issue", comment: "comment", create_preview: "preview",
};
const noun = (tool: string) => NOUN[tool] ?? "request";

/** What a run that went through did, as a sentence's start. */
const DONE: Record<string, string> = {
  deploy: "The deploy started", rollback: "The rollback started", set_secret: "The secret was set", set_env_var: "The variable was set",
  run_runbook_step: "The step ran", create_branch: "The branch was made", open_pr: "The pull request was opened",
  create_issue: "The issue was opened", comment: "The comment was posted", create_preview: "The preview started",
};

/** The approve button: "Approve and deploy", "Approve and set the secret". */
export function approveLabel(a: Req): string {
  switch (a.tool) {
    case "rollback": return "Approve and roll back";
    case "set_secret": return "Approve and set the secret";
    case "set_env_var": return "Approve and set the variable";
    case "run_runbook_step": return `Approve and run step ${(stepOf(a)?.step ?? 0) + 1}`;
    case "create_branch": return "Approve and make the branch";
    case "open_pr": return "Approve and open it";
    case "create_issue": return "Approve and open it";
    case "comment": return "Approve and post it";
    case "create_preview": return "Approve and start it";
    default: return "Approve and deploy";
  }
}

/** The line under the buttons. */
export function approveHint(a: Req & Pick<AgentApproval, "user_name" | "rollback" | "plan_steps">, canDecide = true): string {
  const as = `as ${a.agent} for ${a.user_name}${canDecide ? ", approved by you" : ""}`;
  if (a.tool === "deploy" && a.rollback) {
    const to = a.rollback.to_commit ? a.rollback.to_commit.slice(0, 7) : "what is live now";
    const marks = a.plan_steps.length > 0 ? "; the plan marks which" : "";
    return `Approving runs this deploy once, ${as}, and lets it roll back to ${to} without asking again, so an incident doesn’t wait for a person. Any other step it asks for waits again${marks}.`;
  }
  switch (a.tool) {
    case "set_secret": return `Approving sets this secret once, ${as}. Agents can never read it back.`;
    case "run_runbook_step": return `Approving runs this step once, ${as}. The audit log records it as doc.step_run.`;
    case "deploy":
    case "rollback": return `Approving runs this ${noun(a.tool)} once, ${as}. Anything else it asks waits again.`;
    default: return `Approving does this once, ${as}. Anything else it asks waits again.`;
  }
}

/** "Claude Code wants to set the secret STRIPE_WEBHOOK_SECRET on billing-api
 *  / api in staging"; pageTitle names a runbook step's page once it loaded. */
export function title(a: Req, pageTitle = ""): string {
  const t = a.target;
  const quoted = (k: string) => (field(a, k) ? ` “${field(a, k)}”` : "");
  let what: string;
  switch (a.tool) {
    case "rollback": what = `roll back ${t}`; break;
    case "set_secret": what = `set the secret ${keyOf(a)} on ${t}`; break;
    case "set_env_var": what = `set the variable ${keyOf(a)} on ${t}`; break;
    case "run_runbook_step": {
      const st = stepOf(a);
      what = `run step ${(st?.step ?? 0) + 1} of ${pageTitle ? `“${pageTitle}”` : "a runbook"} in ${t}`;
      break;
    }
    case "create_branch": what = `make the branch ${field(a, "name")} in ${t}`; break;
    case "open_pr": what = `open the pull request${quoted("title")} in ${t}`; break;
    case "create_issue": what = `open the issue${quoted("title")} in ${t}`; break;
    case "comment": what = `comment on ${pullOf(a) ? `!${pullOf(a)}` : `issue ${issueOf(a)}`} in ${t}`; break;
    case "create_preview": what = `start the preview of !${pullOf(a)} in ${t}`; break;
    default: what = `deploy ${t}`;
  }
  return `${a.agent} wants to ${what}`;
}

/** What the deployment an approved deploy or rollback started came to. */
export type Run = { status: string; error_msg: string };

/** The chip and the line beside it: when it expires while it waits, who
 *  decided and when once someone did, and what the run came to. me is the
 *  viewer's name: "by you". */
export function chip(
  a: Pick<AgentApproval, "state" | "expires_at"> & Partial<Pick<AgentApproval, "result" | "decided_by" | "decided_at">>,
  now: number,
  me = "",
  run?: Run,
): { label: string; tone: StatusTone; hint: string } {
  const at = a.decided_at ? stamp(a.decided_at, now) : "";
  const by = `by ${a.decided_by && a.decided_by === me ? "you" : a.decided_by} at ${at}`;
  switch (a.state) {
    case "approved":
      if (a.result === null) return { label: "Running", tone: "building", hint: `approved ${by}` };
      if (a.result && a.result.status >= 300) return { label: "Approved · didn’t start", tone: "failed", hint: by };
      // A step that ran and failed did start, as did a deploy that failed later.
      if (run?.status === "failed" || (a.result && stepOutput({ result: a.result })?.error)) return { label: "Approved · failed", tone: "failed", hint: by };
      return { label: "Approved", tone: "running", hint: by };
    case "denied":
      return { label: "Denied", tone: "failed", hint: by };
    case "cancelled":
      return { label: "Turned down", tone: "neutral", hint: at ? `at ${at}` : "" };
    case "expired":
      return { label: "Expired", tone: "neutral", hint: `at ${stamp(a.expires_at, now)}: nothing ran` };
    default:
      return { label: "Waiting for a person", tone: "pending", hint: `expires ${clock(a.expires_at)}, ${expiresIn(a.expires_at, now)}` };
  }
}

/** A target's parts: "storefront / api in production" is the project
 *  storefront, api, in production. An environment's target has no " in ",
 *  a project's no " / ". */
export function targetParts(target: string): { project: string; what: string; env: string } {
  const at = target.lastIndexOf(" in ");
  const head = at > 0 ? target.slice(0, at) : target;
  const slash = head.indexOf(" / ");
  return { project: slash >= 0 ? head.slice(0, slash) : "", what: slash >= 0 ? head.slice(slash + 3) : head, env: at > 0 ? target.slice(at + 4) : "" };
}

/** The page's heading without the project (the meta line names it), up to
 *  the environment, which the page draws as its chip: "Claude Code wants to
 *  deploy api to [production]". */
export function heading(a: Req, pageTitle = ""): { text: string; env: string } {
  const { what, env } = targetParts(a.target);
  const text = title({ ...a, target: what }, pageTitle);
  return env ? { text: `${text} ${a.tool === "deploy" ? "to" : "in"}`, env } : { text, env };
}

/** What a deploy request names, as step 8's block takes it (D-143); none
 *  at all deploys what the service is set to. */
export function requestVersion(a: Pick<AgentApproval, "request">): { commitSha?: string; gitRef?: string; imageTag?: string } | undefined {
  const v = { commitSha: field(a, "commit_sha"), gitRef: field(a, "git_ref"), imageTag: field(a, "image_tag") };
  const out = Object.fromEntries(Object.entries(v).filter(([, x]) => x));
  return Object.keys(out).length > 0 ? out : undefined;
}

/** The deployment a rollback request goes back to (its path names it). */
export const rollbackTo = (a: Pick<AgentApproval, "request">) => a.request.path.match(/\/deployments\/([^/]+)\/rollback$/)?.[1] ?? "";

/** "in 14 min"; never "in 0 min" while it still waits. */
export function expiresIn(iso: string, now: number): string {
  return `in ${Math.max(1, Math.ceil((new Date(iso).getTime() - now) / 60_000))} min`;
}

/** Past a few lines' worth at phone width, so the page folds it behind
 *  Show all. ponytail: counts characters, not rendered lines; measure the
 *  element if Show all ever offers nothing more on a wider phone. */
export const isLong = (text: string) => text.length > 180 || text.split("\n").length > 4;

/** The plan's steps when it is a numbered list ("1. …"), else null. */
export function planSteps(plan: string): string[] | null {
  const lines = plan.split("\n").map((l) => l.trim()).filter(Boolean);
  if (lines.length === 0 || !lines.every((l) => /^\d+[.)]\s/.test(l))) return null;
  return lines.map((l) => l.replace(/^\d+[.)]\s+/, ""));
}

export interface Fact {
  k: string;
  v: string;
  more: string;
  mono: boolean;
  href?: string;
  /** The value in the pending colour: "Hidden". */
  warn?: boolean;
}

/** How the request page names the policy's class of a target. */
const CLASS: Record<string, string> = { preview: "preview", other: "other environment", protected: "protected" };

/** What Toft runs on Approve, from what the request carries: nothing it
 *  can't know (the live commit, the server) is guessed. secret is the
 *  secret set_secret replaces, when one exists (null: a new one); live is
 *  what the page read beside it: the container a runbook step runs in (the
 *  service's first) and its server. The class is the policy's (env_class):
 *  a database or cache counts as protected. */
export function facts(
  a: AgentApproval,
  deploymentHref: (id: string) => string,
  secret?: { updated_at: string; updated_by?: string } | null,
  live: { container?: string; server?: string } = {},
  now = Date.now(),
): Fact[] {
  const at = a.target.lastIndexOf(" in ");
  const k = a.service_id ? "Service" : a.environment_id ? "Environment" : "Project";
  const cls = CLASS[a.env_class ?? ""] ?? "";
  const more = at > 0 ? `${a.target.slice(at + 1)}${cls ? ` (${cls})` : ""}` : "";
  const where: Fact = at > 0 ? { k, v: a.target.slice(0, at), more, mono: false } : { k, v: a.target, more, mono: false };
  const out: Fact[] = [where];
  const str = (key: string) => field(a, key);
  switch (a.tool) {
    case "rollback": {
      const id = rollbackTo(a);
      if (id) out.push({ k: "Rolls back to", v: id.slice(0, 8), more: "", mono: true, href: deploymentHref(id) });
      break;
    }
    case "deploy": {
      const sha = str("commit_sha");
      const ref = str("git_ref");
      const tag = str("image_tag");
      if (sha) out.push({ k: "Deploys", v: sha.slice(0, 7), more: ref ? `from ${ref}` : "", mono: true });
      else if (ref) out.push({ k: "Deploys", v: ref, more: "at its latest commit when it runs", mono: true });
      else if (tag) out.push({ k: "Deploys", v: tag, more: "", mono: true });
      else out.push({ k: "Deploys", v: "What the service is set to deploy", more: "", mono: false });
      break;
    }
    case "set_secret": {
      const by = secret?.updated_by ? `${secret.updated_by} set` : "set";
      const when = secret ? new Date(secret.updated_at).toLocaleDateString("en-GB", { day: "numeric", month: "short" }) : "";
      out.push({ k: "Secret", v: keyOf(a), more: secret ? `replaces the value ${by} on ${when}` : secret === null ? "a new secret" : "", mono: true });
      out.push({ k: "Value", v: "Hidden", more: "from everyone, you included. The agent sent it; Toft can only set it.", mono: false, warn: true });
      out.push({ k: "Takes effect", v: "At the next deploy", more: "nothing redeploys on Approve", mono: false });
      break;
    }
    case "set_env_var":
      out.push({ k: "Variable", v: keyOf(a), more: "", mono: true });
      out.push(
        body(a).masked === true
          ? { k: "Value", v: "Masked", more: "it is set masked, so it isn’t shown here", mono: false, warn: true }
          : { k: "Value", v: str("value"), more: "", mono: true },
      );
      out.push({ k: "Takes effect", v: "At the next deploy", more: "nothing redeploys on Approve", mono: false });
      break;
    case "run_runbook_step":
      out[0] = { ...where, k: "Runs in", more: `${where.more}: its first container, as the image’s user` };
      if (live.container) out.push({ k: "Container", v: live.container, more: live.server ? `on ${live.server}` : "", mono: true });
      out.push({ k: "Never on", v: "the server’s host", more: "host steps are for instance operators, with their password", mono: false });
      out.push({ k: "Output", v: "Back to the agent", more: "and kept with this request", mono: false });
      break;
    case "create_branch":
      out.push({ k: "Branch", v: str("name"), more: `from ${str("from") || "the default branch"}`, mono: true });
      out.push({ k: "Pushes", v: "This agent only", more: "to this branch alone, with a password that lasts 24 hours", mono: false });
      break;
    case "open_pr":
      out.push({ k: "Pull request", v: str("title"), more: "", mono: false });
      out.push({ k: "Merges", v: str("head"), more: `into ${str("base") || "the default branch"}, once a person merges it`, mono: true });
      out.push({ k: "Preview", v: "Waits for a person", more: "to approve each commit, and runs Sandboxed", mono: false });
      break;
    case "create_issue":
      out.push({ k: "Issue", v: str("title"), more: str("priority") ? `priority ${str("priority")}` : "", mono: false });
      break;
    case "comment":
      out.push({ k: "On", v: pullOf(a) ? `!${pullOf(a)}` : `issue ${issueOf(a)}`, more: "", mono: false });
      if (str("body")) out.push({ k: "Comment", v: str("body").length > 280 ? `${str("body").slice(0, 280)}…` : str("body"), more: "", mono: false });
      break;
    case "create_preview":
      out.push({ k: "Preview of", v: `!${pullOf(a)}`, more: "with the pull request’s own trust: an agent’s or a key’s waits for approval and runs Sandboxed", mono: false });
      break;
  }
  const also = alsoAllows(a, now);
  if (also) out.push({ k: "Also allows", v: also.v, more: also.more, mono: false });
  return out;
}

/** The rollback a deploy's approval also allows (D-146): back to what was
 *  live, once, for 30 minutes after the deploy goes live. next names what
 *  it deploys, once the page knows ("a81c0de"). */
export function alsoAllows(a: Pick<AgentApproval, "rollback" | "state" | "agent">, now = Date.now(), next = "this deploy"): { v: string; more: string } | null {
  const rb = a.rollback;
  if (!rb) return null;
  const to = `rolling back to ${rb.to_commit ? rb.to_commit.slice(0, 7) : "the deployment live now"}`;
  const v = to.charAt(0).toUpperCase() + to.slice(1);
  if (rb.used_at) return { v: `Rolled back to ${rb.to_commit ? rb.to_commit.slice(0, 7) : "it"} at ${stamp(rb.used_at, now)}`, more: "without asking again" };
  if (!rb.until) return { v: `${v} for 30 minutes after ${next} goes live`, more: `once, by ${a.agent}, while ${next} is what’s live` };
  if (rb.active) return { v: `${v} until ${clock(rb.until)}`, more: `once, by ${a.agent}, while ${next} is what’s live` };
  if (Date.parse(rb.until) <= now) return { v: `${v} until ${stamp(rb.until, now)}: over`, more: "a rollback asks again" };
  return { v: `${v}: no longer`, more: "this deploy isn’t what’s live, or the approver’s role changed; a rollback asks again" };
}

/** Toft's mark on a step of the agent's plan (D-146). */
const MARK: Record<string, { label: string; ask: boolean }> = {
  this_request: { label: "this request", ask: false },
  allowed_by_this_approval: { label: "allowed by this approval", ask: false },
  will_ask_again: { label: "will ask again", ask: true },
  refused: { label: "Toft would refuse", ask: false },
};

/** A step's mark as the page shows it: the covered rollback names its end
 *  once the deploy is live. */
export function markOf(mark: string | null | undefined, a: Pick<AgentApproval, "rollback">): { label: string; ask: boolean } | undefined {
  const m = mark ? MARK[mark] : undefined;
  const until = a.rollback?.active ? a.rollback.until : null;
  return m && mark === "allowed_by_this_approval" && until ? { ...m, label: `allowed until ${clock(until)}` } : m;
}

/** A runbook step's command: the page's toft-run fences, in order, as the
 *  API numbers them (internal/doc parseBlock: a service ID and a command). */
export function runSteps(markdown: string): { command: string; host: boolean }[] {
  // ponytail: fences with ``` only, as the editor writes them; a ~~~ fence or
  // one nested in a longer fence would be miscounted.
  const re = /^```toft-run[ \t]+([0-9a-f-]{36})([ \t]+host)?[ \t]*\n([\s\S]*?)^```[ \t]*$/gm;
  const out: { command: string; host: boolean }[] = [];
  for (const m of markdown.matchAll(re)) if (m[3].trim()) out.push({ command: m[3].replace(/\n$/, ""), host: !!m[2] });
  return out;
}

/** Lines a runbook step printed, from its stored result. */
export function stepOutput(a: Pick<AgentApproval, "result">): { lines: string[]; error: string } | null {
  const data = a.result?.body?.data as { lines?: unknown; error?: unknown } | undefined;
  if (!data || !Array.isArray(data.lines)) return null;
  return { lines: data.lines.filter((l): l is string => typeof l === "string").slice(-50), error: typeof data.error === "string" ? data.error : "" };
}

/** The ID of the deployment an approved request started, when it did. */
export function startedDeployment(a: Pick<AgentApproval, "result">): string {
  const data = a.result?.body?.data as { id?: unknown } | undefined;
  return a.result && a.result.status < 300 && typeof data?.id === "string" ? data.id : "";
}

/** " a81c0de went live at 14:20. Claude Code may roll back to 9b7d3f0
 *  until 14:50 without asking." (D-146), or "" before it went live. */
function wentLive(a: AgentApproval, now: number): string {
  const rb = a.rollback;
  if (!rb?.live_at) return "";
  let s = ` ${rb.commit ? rb.commit.slice(0, 7) : "It"} went live at ${stamp(rb.live_at, now)}.`;
  const to = rb.to_commit ? rb.to_commit.slice(0, 7) : "the deployment before it";
  if (rb.used_at) s += ` ${a.agent} rolled back to ${to} at ${stamp(rb.used_at, now)} without asking.`;
  else if (rb.active && rb.until) s += ` ${a.agent} may roll back to ${to} until ${clock(rb.until)} without asking.`;
  return s;
}

/** A decided request in words, for the card at the end of the page. me
 *  is the viewer's name: "You approved". */
export function decision(a: AgentApproval, now = Date.now(), me = "", run?: Run): string | null {
  const n = noun(a.tool);
  const who = a.decided_by && a.decided_by === me ? "You" : a.decided_by;
  const at = a.decided_at ? stamp(a.decided_at, now) : "";
  switch (a.state) {
    case "approved": {
      const r = a.result;
      const out = stepOutput(a);
      if (!r) return `${who} approved at ${at}. Running as ${a.agent} for ${a.user_name}…`;
      if (r.status < 300 && !out?.error) {
        if (run?.status === "failed") return `${who} approved at ${at}, but the ${n} failed${run.error_msg ? `: ${run.error_msg}` : ""}.`;
        const live = wentLive(a, now);
        return live ? `${who} approved at ${at}.${live}` : `${who} approved at ${at}. ${DONE[a.tool] ?? "It ran"} as ${a.agent} for ${a.user_name}.`;
      }
      const err = out?.error || (typeof r.body?.error === "string" ? r.body.error : `the panel answered ${r.status}`);
      const ran = a.tool === "deploy" || a.tool === "rollback" ? "start" : "go through";
      return `${who} approved at ${at}, but the ${n} didn’t ${ran}: ${err}.`;
    }
    case "denied":
      return `${who} denied at ${at}, so nothing ran. ${a.agent} was told; it may ask again with another reason.`;
    case "cancelled":
      return `Turned down${at ? ` at ${at}` : ""}, so nothing ran. Pausing agents or revoking the connection turns down its waiting requests.`;
    case "expired": {
      const mins = Math.round((Date.parse(a.expires_at) - Date.parse(a.created_at)) / 60_000);
      return `Nobody decided within ${mins} minutes, so nothing ran. ${a.agent} was told; it can ask again.`;
    }
    default:
      return null;
  }
}
