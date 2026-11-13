import type { components } from "@/lib/api/schema";
import type { CheckState, CommitCheck, PullRequestDetail } from "@/lib/api/code.service";
import type { Preview } from "@/lib/api/preview.service";
import { checkTitle } from "@/components/code/checks";
import { shortSha } from "@/components/code/repo";
import { timeAgo } from "@/lib/time";

type Person = components["schemas"]["Person"];

// A pull request (board ReworkPull, 7g step 14): the pure parts the page renders.

export type Tab = "conversation" | "commits" | "checks" | "files";

/** The tab in ?tab=; the old "changes" reads as Files. */
export function tabOf(q: string | null): Tab {
  if (q === "files" || q === "changes") return "files";
  return q === "commits" || q === "checks" ? q : "conversation";
}

/** "Claude Code, for sam" for an agent's work (D-100), else the person's name. */
export function byline(p: Person | null | undefined, fallback: string): string {
  if (!p) return fallback;
  return p.agent ? `${p.agent}, for ${p.name}` : p.name;
}

/** The header's "Claude Code, for sam, wants to merge". */
export function headerLead(p: Pick<PullRequestDetail, "state" | "author"> & { merged_by?: Person | null }): string {
  if (p.state === "merged") return `${byline(p.merged_by, "A deleted account")} merged`;
  const who = byline(p.author, "A deleted account");
  return `${p.author?.agent ? `${who},` : who} ${p.state === "open" ? "wants to merge" : "proposed"}`;
}

/** The Merge card's first line: who may merge (D-102; reviews are a Follow-up). */
export function whoMerges(p: Pick<PullRequestDetail, "author">): string {
  return p.author?.agent ? "An owner or developer merges it. An agent’s pull request is never merged on its own say." : "An owner or developer merges it.";
}

/** Toft's preview checks, toft/preview/<service>. */
export const isPreviewCheck = (name: string) => name.startsWith("toft/preview/");

/** The head commit's checks as the Merge card's line: what failed, else
 *  what runs, else that they passed. */
export function checksLine(checks: Pick<CommitCheck, "name" | "state">[], sha: string): { state: CheckState; text: string } | null {
  if (checks.length === 0) return null;
  const at = ` on ${shortSha(sha)}`;
  const name = (c: Pick<CommitCheck, "name">) => checkTitle(c.name).title;
  const failed = checks.filter((c) => c.state === "failure" || c.state === "cancelled");
  if (failed.length) return { state: "failure", text: `${failed.length === 1 ? `${name(failed[0])} failed` : `${failed.length} checks failed`}${at}` };
  const running = checks.filter((c) => c.state === "pending" || c.state === "running");
  if (running.length) return { state: "running", text: `${running.length === 1 ? `${name(running[0])} is running` : `${running.length} checks are running`}${at}` };
  return { state: "success", text: `${checks.length === 1 ? `${name(checks[0])} passed` : `All ${checks.length} checks passed`}${at}` };
}

/** The Checks card's count, "1 passed · 2 waiting": while the preview's
 *  commit waits for approval, its pending checks wait rather than run. */
export function checksSummary(checks: Pick<CommitCheck, "name" | "state">[], previewWaits: boolean): string {
  const n = (f: (c: Pick<CommitCheck, "name" | "state">) => boolean) => checks.filter(f).length;
  const waiting = n((c) => previewWaits && c.state === "pending" && isPreviewCheck(c.name));
  const running = n((c) => c.state === "pending" || c.state === "running") - waiting;
  const failing = n((c) => c.state === "failure" || c.state === "cancelled");
  const passed = n((c) => c.state === "success" || c.state === "skipped");
  if (passed === checks.length) return `All ${passed} passed`;
  return [failing && `${failing} failing`, passed && `${passed} passed`, running && `${running} running`, waiting && `${waiting} waiting`].filter(Boolean).join(" · ");
}

/** An untrusted preview has a commit to approve (D-97), and the viewer may
 *  approve it: the page leads with it. A blocked preview can't start, so
 *  approving does nothing yet. */
export function commitWaits(pv: Pick<Preview, "status" | "untrusted" | "needs_approval"> | null | undefined, canWrite: boolean): boolean {
  if (!pv || !canWrite || pv.status === "blocked") return false;
  return pv.status === "needs_approval" || (pv.untrusted && pv.needs_approval);
}

/** About → Agent's tail: the agent's branch password (D-102). */
export function passwordLine(expiresAt: string, now: number): string {
  const at = Date.parse(expiresAt);
  if (at <= now) return "its branch password expired; it asks again to push";
  const left = timeAgo(new Date(now).toISOString(), at);
  return `its branch password expires in ${left === "now" ? "under a minute" : left}`;
}
