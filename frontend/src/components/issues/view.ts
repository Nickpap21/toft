import type { IssueCounts, IssueFilters, IssueLink, IssuePriority, IssueQuery, IssueStatus, IssueTimelineItem } from "@/lib/api/issue.service";

// Issues' rules that decide what a row, a tab or the panel shows (ReworkIssues).

/** The part of a branch that names the key, with or without its zeros
 *  (D-83): "sto-97" for STO-097 on chore/sto-97-restore-drill. A branch that
 *  doesn't name it (a renamed key) stays whole. */
export function shortBranch(branch: string, key: string): string {
  const k = /^(.+)-(\d+)$/.exec(key);
  if (!k) return branch;
  for (const m of branch.matchAll(/(?<![a-z0-9])([a-z][a-z0-9]*)-(\d+)(?![a-z0-9])/gi)) {
    if (m[1].toLowerCase() === k[1].toLowerCase() && Number(m[2]) === Number(k[2])) return m[0];
  }
  return branch;
}

export const sum = (c: IssueCounts | undefined, only?: IssueStatus[]) =>
  c ? (Object.entries(c) as [IssueStatus, number][]).filter(([s]) => !only || only.includes(s)).reduce((n, [, v]) => n + v, 0) : undefined;

const list = (v: string | null) => (v ? v.split(",").filter(Boolean) : []);

/** The URL's filters over the view's. With no view, the URL's alone: what
 *  All issues and Active count on any tab (a cycle chip and a search reach
 *  them, the tab's own filters don't). */
export function filtersFrom(sp: Pick<URLSearchParams, "get" | "has">, view: IssueFilters): IssueFilters {
  return {
    status: sp.has("status") ? (list(sp.get("status")) as IssueStatus[]) : view.status,
    priority: sp.has("priority") ? (list(sp.get("priority")) as IssuePriority[]) : view.priority,
    assignee: sp.get("assignee") ?? view.assignee,
    label: sp.has("label") ? list(sp.get("label")) : view.label,
    type: sp.has("type") ? list(sp.get("type")) : view.type,
    parent: sp.get("parent") ?? view.parent,
    cycle: sp.get("cycle") ?? view.cycle,
    q: sp.get("q") ?? view.q,
    order: (sp.get("order") as IssueFilters["order"]) ?? view.order,
    group: (sp.get("group") as IssueFilters["group"]) ?? view.group,
  };
}

/** What a count reads of some filters: every status, the list's other filters. */
export function countQuery(f: IssueFilters): IssueQuery {
  const q: IssueQuery = { q: f.q, priority: f.priority, assignee: f.assignee, label: f.label, type: f.type, parent: f.parent, cycle: f.cycle };
  return Object.fromEntries(Object.entries(q).filter(([, v]) => v !== undefined));
}

/** "Show N more": what the status's count says isn't loaded yet. */
export const moreCount = (count: number | undefined, loaded: number) => Math.max((count ?? 0) - loaded, 0);

/** A merged row's end in the panel's History: the deploys that pull request
 *  started. By number and repository: a number alone can claim another
 *  repository's deploy. */
export function mergedClause(pull: IssueTimelineItem["pull_request"], links: IssueLink[], project: string): string {
  if (!pull) return "";
  const link = links.find((l) => l.kind === "pull_request" && l.number === pull.number && l.repository === pull.repository);
  const where = (link?.deploys ?? []).filter((d) => d.state !== "waiting").map((d) => `${project} / ${d.service}`);
  return where.length ? `; deploy of ${where.join(" and ")} started` : "";
}

/** The panel's Linked work: a branch goes once its pull request is linked,
 *  which stands for it; a branch with no pull request yet stays. */
export function panelLinks(links: IssueLink[]): IssueLink[] {
  return links.filter((l) => l.kind !== "branch" || !links.some((p) => p.kind === "pull_request" && p.repository === l.repository && p.head_branch === l.branch));
}
