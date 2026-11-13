"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, Copy, CopyPlus, ExternalLink, MoreHorizontal, Pencil, Trash2, X } from "lucide-react";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ApiError, changedSinceRead } from "@/lib/api/client";
import { LoadFailed, Read, readFailed } from "@/components/shell/unreachable";
import { issueService, issueTypesQuery, type IssueDetail, type IssuePriority, type IssueRead, type IssueStatus, type IssueTimelineItem } from "@/lib/api/issue.service";
import { orgService } from "@/lib/api/org.service";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import { refreshCounts } from "@/lib/queries";
import { cn } from "@/lib/utils";
import { MarkdownField } from "@/components/code/pulls";
import { MentionTextarea } from "@/components/code/mention-textarea";
import { compactAgo, useRepos } from "@/components/code/repo";
import { CLOSED, Glyph, ICON, issueHref, PRIORITY, PRIORITY_ORDER, PriorityBars, STATUS, STATUS_ORDER, StatusChip, StatusIcon } from "./meta";
import { BlockedChip, ChildrenSection, CloneDialog, IssuePicker, RELATION_LABEL, RelationsSection, TypeIcon } from "./deep";
import { NewIssueDialog } from "./new-issue";
import { CarriedChip, CycleItems, CycleText, DueItems, dueOf, EstimateItems, cycleName, fmtDay, pointsText, projectCyclesQuery } from "./cycles";
import { personName, Who } from "@/components/agent-person";
import { mergedClause, panelLinks } from "./view";

// One issue (IssueList's panel, and the issue page from its ↗; D-81): its
// properties, description, what moves it next, linked work, history and
// comments.

export function useCanWriteIssues(): boolean {
  const role = useOrgRole();
  return role === "owner" || role === "developer";
}

/** Invalidates what an issue's change shows elsewhere. */
export function useIssueRefresh(projectId: string) {
  const qc = useQueryClient();
  return (n?: number) => {
    qc.invalidateQueries({ queryKey: ["issues", projectId] });
    qc.invalidateQueries({ queryKey: ["issue-counts", projectId] });
    qc.invalidateQueries({ queryKey: ["issue-labels", projectId] });
    refreshCounts(qc);
    if (n) {
      qc.invalidateQueries({ queryKey: ["issue", projectId, n] });
      qc.invalidateQueries({ queryKey: ["issue-timeline", projectId, n] });
    }
  };
}

/** Markdown the server rendered (D-23), with the project's issue keys and,
 *  in a project with one repository, its pull requests as links. */
export function IssueMarkdown({ html, projectId, className }: { html: string; projectId: string; className?: string }) {
  const el = useRef<HTMLDivElement>(null);
  const router = useRouter();
  const { repos } = useRepos(projectId);
  const only = repos?.length === 1 ? repos[0] : undefined;
  useEffect(() => {
    const root = el.current;
    if (!root) return;
    root.querySelectorAll<HTMLAnchorElement>("a[data-toft-issue]").forEach((a) => {
      a.href = issueHref(projectId, Number(a.dataset.toftIssue));
    });
    root.querySelectorAll<HTMLAnchorElement>("a[data-toft-pull]").forEach((a) => {
      if (only) a.href = `/projects/${projectId}/code/${encodeURIComponent(only.name)}/pulls/${a.dataset.toftPull}`;
      else a.replaceWith(document.createTextNode(a.textContent ?? ""));
    });
  }, [html, projectId, only]);
  return (
    <div
      ref={el}
      className={cn("toft-md", className)}
      onClick={(e) => {
        const a = (e.target as HTMLElement).closest("a[data-toft-issue], a[data-toft-pull]");
        if (a instanceof HTMLAnchorElement && a.getAttribute("href") && !e.metaKey && !e.ctrlKey && !e.shiftKey && e.button === 0) {
          e.preventDefault();
          router.push(a.pathname);
        }
      }}
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof ApiError ? err.message : fallback;
}

/** "Moves to … when …": what moves it next (D-06, D-81). */
export function nextMove(issue: IssueDetail, project: string): string {
  const branch = issue.links.find((l) => l.kind === "branch");
  const merged = issue.links.filter((l) => l.kind === "pull_request" && l.state === "merged");
  if (issue.type.kind === "container" && !CLOSED.includes(issue.status)) {
    return issue.children.length
      ? `Ships by itself once every issue in this ${issue.type.name.toLowerCase()} is shipped or cancelled (${issue.progress?.done ?? 0} of ${issue.children.length} so far).`
      : `Ships by itself once the issues you add to it are shipped or cancelled.`;
  }
  switch (issue.status) {
    case "backlog":
    case "todo":
      return `Moves to In progress when someone pushes a branch that names ${issue.key}.`;
    case "in_progress":
      return branch ? "Moves to In review when a pull request for this branch opens." : `Moves to In review when a pull request that names ${issue.key} opens.`;
    case "in_review": {
      const waiting = merged.flatMap((l) => (l.deploys ?? []).filter((d) => d.state !== "live").map((d) => ({ d, sha: l.merge_sha ?? "" })));
      if (waiting.length > 0) {
        const where = waiting.map(({ d }) => `${project} / ${d.service}`).join(" and ");
        return `Moves to Shipped when ${waiting[0].sha.slice(0, 7)} is live on ${where}.`;
      }
      return "Moves to Shipped when the merge deploy is live.";
    }
  }
  return "";
}

const LINK_STATE: Record<string, { icon: string; tint: string }> = {
  open: { icon: ICON.pr, tint: "text-status-running" },
  merged: { icon: ICON.merge, tint: "text-link" },
  closed: { icon: ICON.pr, tint: "text-status-failed" },
};
const DEPLOY_STATE: Record<string, { icon: string; tint: string; label: string }> = {
  live: { icon: ICON.check, tint: "text-status-running", label: "live" },
  building: { icon: ICON.layers, tint: "text-status-building", label: "building" },
  failed: { icon: ICON.x, tint: "text-status-failed", label: "failed" },
  waiting: { icon: ICON.clock, tint: "text-muted", label: "waiting" },
};

function LinkedWork({ issue, projectId, project, page }: { issue: IssueDetail; projectId: string; project: string; page?: boolean }) {
  const rows: { key: string; href: string; icon: string; tint: string; text: string; meta: string; metaTint: string }[] = [];
  const mergedHeads = issue.links.filter((l) => l.kind === "pull_request" && l.state === "merged").map((l) => l.head_branch);
  for (const l of page ? issue.links : panelLinks(issue.links)) {
    const code = `/projects/${projectId}/code/${encodeURIComponent(l.repository)}`;
    if (l.kind === "branch") {
      const gone = l.exists === false;
      rows.push({
        key: `b-${l.repository}-${l.branch}`,
        href: gone ? `${code}/pulls` : `${code}?ref=${encodeURIComponent(l.branch ?? "")}`,
        icon: ICON.branch,
        tint: "text-muted",
        text: l.branch ?? "",
        meta: gone ? (mergedHeads.includes(l.branch) ? "deleted after merge" : "deleted") : "",
        metaTint: "text-muted",
      });
      continue;
    }
    const s = LINK_STATE[l.state ?? "open"];
    rows.push({ key: `p-${l.repository}-${l.number}`, href: `${code}/pulls/${l.number}`, icon: s.icon, tint: s.tint, text: `!${l.number} ${l.title ?? ""}`, meta: l.state ?? "", metaTint: s.tint });
    for (const d of l.deploys ?? []) {
      const ds = DEPLOY_STATE[d.state];
      rows.push({
        key: `d-${l.number}-${d.service_id}`,
        href: `/projects/${projectId}/services/${d.service_id}${d.deployment_id ? `/deployments?deployment=${d.deployment_id}` : ""}`,
        icon: ds.icon,
        tint: ds.tint,
        text: `${project} / ${d.service}${d.sha ? ` · ${d.sha.slice(0, 7)}` : ""}`,
        meta: ds.label,
        metaTint: ds.tint,
      });
    }
  }
  if (rows.length === 0) return null;
  return (
    <div className="flex flex-col gap-2">
      <h3 className="text-xs font-medium leading-4 text-muted">Linked work</h3>
      {rows.map((r) => (
        <Link key={r.key} href={r.href} className="touch-target flex min-w-0 items-start gap-2 text-[13px] text-fg hover:underline pointer-coarse:items-center">
          <Glyph d={r.icon} className={cn("mt-[3px]", r.tint)} />
          <span className="min-w-0 flex-[0_1_auto] font-mono text-xs leading-5 [overflow-wrap:anywhere]">{r.text}</span>
          {r.meta && <span className={cn("ml-auto flex-none text-xs leading-5", r.metaTint)}>{r.meta}</span>}
        </Link>
      ))}
    </div>
  );
}

const relLabel = (k?: string) => (k && k in RELATION_LABEL ? RELATION_LABEL[k as keyof typeof RELATION_LABEL].toLowerCase() : k ?? "");

/** One history row's words: who, what, and whether an event did it. */
export function describe(it: IssueTimelineItem, me: string | undefined): { who: string; text: string; icon: string; tint: string; status?: IssueStatus } {
  const actor = personName(it.actor, "Someone");
  const pull = it.pull_request ? `!${it.pull_request.number}` : "";
  switch (it.kind) {
    case "opened":
      return { who: actor, text: "opened the issue", icon: ICON.dot, tint: "text-muted" };
    case "assigned":
      if (!it.to) return { who: actor, text: "removed the assignee", icon: ICON.user, tint: "text-muted" };
      if (it.to === it.actor?.id) return { who: actor, text: "assigned it to themselves", icon: ICON.user, tint: "text-muted" };
      return { who: actor, text: `assigned it to ${it.to === me ? "you" : (it.assignee?.name ?? "someone")}`, icon: ICON.user, tint: "text-muted" };
    case "priority":
      return { who: actor, text: `set the priority to ${PRIORITY[(it.to ?? "none") as IssuePriority].label.toLowerCase()}`, icon: ICON.bolt, tint: "text-muted" };
    case "labels":
      return { who: actor, text: it.to ? `set the labels to ${it.to}` : "removed the labels", icon: ICON.dot, tint: "text-muted" };
    case "title":
      return { who: actor, text: `renamed it from “${it.from}”`, icon: ICON.pencil, tint: "text-muted" };
    case "linked":
      return it.branch
        ? { who: actor, text: `pushed ${it.branch}`, icon: ICON.branch, tint: "text-muted" }
        : { who: actor, text: `opened ${pull}`, icon: ICON.pr, tint: "text-link" };
    case "merged":
      return { who: actor, text: `merged ${pull}`, icon: ICON.merge, tint: "text-link" };
    case "type":
      return { who: actor, text: `changed the type from ${it.from} to ${it.to}`, icon: ICON.dot, tint: "text-muted" };
    case "parent":
      return { who: actor, text: it.to ? `set the parent to ${it.to}` : `removed it from ${it.from}`, icon: ICON.dot, tint: "text-muted" };
    case "related":
      return { who: actor, text: `linked it: ${relLabel(it.from)} ${it.to}`, icon: ICON.pr, tint: "text-muted" };
    case "unrelated":
      return { who: actor, text: `removed the link: ${relLabel(it.from)} ${it.to}`, icon: ICON.x, tint: "text-muted" };
    case "unblocked":
      return { who: "Unblocked:", text: `${it.to} ${it.from === "shipped" ? "shipped" : "was cancelled"}`, icon: ICON.check, tint: "text-status-running" };
    case "deploy_failed":
      return { who: "", text: `A deploy failed again${it.to ? ` at ${it.to}` : ""}`, icon: ICON.dot, tint: "text-status-failed-fg" };
    case "cloned":
      return { who: actor, text: it.from === "clones" ? `cloned it from ${it.to}` : `cloned it as ${it.to}`, icon: ICON.dot, tint: "text-muted" };
    case "cycle":
      if (it.automatic) return { who: `Cycle ${it.to}:`, text: `unfinished when Cycle ${it.from} ended`, icon: ICON.dot, tint: "text-status-building" };
      if (!it.to) return { who: actor, text: `took it out of Cycle ${it.from}`, icon: ICON.dot, tint: "text-muted" };
      return { who: actor, text: it.from ? `moved it from Cycle ${it.from} to Cycle ${it.to}` : `planned it in Cycle ${it.to}`, icon: ICON.dot, tint: "text-muted" };
    case "start":
      return { who: actor, text: it.to ? `set the start to ${fmtDay(it.to)}` : "removed the start date", icon: ICON.dot, tint: "text-muted" };
    case "due":
      return { who: actor, text: it.to ? `set the due date to ${fmtDay(it.to)}` : "removed the due date", icon: ICON.dot, tint: "text-muted" };
    case "estimate":
      return { who: actor, text: it.to ? `estimated it at ${it.to === "1" ? "1 point" : `${it.to} points`}` : "removed the estimate", icon: ICON.dot, tint: "text-muted" };
    case "status": {
      const to = (it.to ?? "todo") as IssueStatus;
      if (!it.automatic) return { who: actor, text: `moved it to ${STATUS[to].name}`, icon: "", tint: "", status: to };
      const name = `${STATUS[to].name}:`;
      if (to === "in_progress" && it.branch) return { who: name, text: `${actor} pushed ${it.branch}`, icon: ICON.branch, tint: STATUS[to].tint };
      if (to === "in_progress") return { who: name, text: `${actor} closed ${pull} without merging`, icon: ICON.pr, tint: STATUS[to].tint };
      if (to === "in_review") return { who: name, text: `${actor} opened ${pull}`, icon: ICON.pr, tint: STATUS[to].tint };
      if (to === "shipped" && it.deploy) return { who: name, text: `${it.deploy.sha.slice(0, 7)} went live on ${it.deploy.service}`, icon: ICON.check, tint: "text-status-running" };
      if (to === "shipped" && !it.pull_request) return { who: name, text: "every issue in it is shipped or cancelled", icon: ICON.check, tint: "text-status-running" };
      return { who: name, text: `${actor} merged ${pull}, and nothing deploys from its branch`, icon: ICON.check, tint: "text-status-running" };
    }
  }
  return { who: actor, text: "commented", icon: ICON.comment, tint: "text-muted" };
}

function Auto() {
  return (
    <span className="ml-0.5 inline-flex items-center gap-[3px] whitespace-nowrap rounded border border-strong px-[5px] text-[11px] leading-4 text-fg/70">
      <Glyph d={ICON.bolt} size={9} />
      automatic
    </span>
  );
}

function History({ issue, projectId, project, now, page }: { issue: IssueDetail; projectId: string; project: string; now: number; page?: boolean }) {
  const { user } = useAuth();
  const svc = issueService(projectId);
  const refresh = useIssueRefresh(projectId);
  const owner = useOrgRole() === "owner";
  const timeline = useQuery({ queryKey: ["issue-timeline", projectId, issue.number], queryFn: () => svc.timeline(issue.number) });
  const [editing, setEditing] = useState<{ id: string; body: string } | null>(null);
  const edit = useMutation({
    mutationFn: (e: { id: string; body: string }) => svc.editComment(issue.number, e.id, e.body),
    onSuccess: () => {
      setEditing(null);
      refresh(issue.number);
    },
    onError: (err) => toast.error(errorMessage(err, "Couldn't save the comment")),
  });
  const [deleting, setDeleting] = useState<string | null>(null);
  const remove = useMutation({
    mutationFn: (id: string) => svc.deleteComment(issue.number, id),
    onSuccess: () => {
      setDeleting(null);
      refresh(issue.number);
    },
    onError: (err) => toast.error(errorMessage(err, "Couldn't delete the comment")),
  });
  const next = page && issue.status === "in_review" && issue.links.some((l) => l.kind === "pull_request" && l.state === "merged");
  return (
    <div className="flex flex-col gap-2">
      <h3 className="text-xs font-medium leading-4 text-muted">History</h3>
      <Read query={timeline} what="the history">
        {(read) => (
          <ol className="flex flex-col gap-2.5">
            {read.data.map((it) => {
              if (it.type === "comment") {
                const mine = it.actor?.id === user?.id;
                return (
                  <li key={it.id} className="flex flex-col gap-1.5 text-[13px] leading-[19px]">
                    <div className="flex items-start gap-2.5">
                      {page && <Glyph d={ICON.comment} className="mt-[3px] text-muted" />}
                      <span className="min-w-0 flex-1 text-fg/70">
                        <Who person={it.actor} fallback="Someone" className="font-medium text-fg" /> commented{it.edited_at ? " (edited)" : ""}
                      </span>
                      {mine && (
                        <button type="button" aria-label="Edit your comment" onClick={() => setEditing({ id: it.id, body: it.body ?? "" })} className="touch-target inline-flex items-center justify-center text-muted hover:text-fg pointer-coarse:min-w-11">
                          <Pencil className="h-3 w-3" aria-hidden="true" />
                        </button>
                      )}
                      {(mine || owner) && (
                        <button type="button" aria-label="Delete the comment…" onClick={() => setDeleting(it.id)} className="touch-target inline-flex items-center justify-center text-muted hover:text-status-failed-fg pointer-coarse:min-w-11">
                          <Trash2 className="h-3 w-3" aria-hidden="true" />
                        </button>
                      )}
                      <span className="flex-none font-mono text-xs text-muted">{compactAgo(it.created_at, now)}</span>
                    </div>
                    {editing?.id === it.id ? (
                      <div className={cn("flex flex-col gap-2", page && "ml-6")}>
                        <MarkdownField id={`edit-${it.id}`} label="Edit comment" value={editing.body} onChange={(body) => setEditing({ id: it.id, body })} rows={3} projectId={projectId} onSubmit={() => edit.mutate(editing)} />
                        <div className="flex justify-end gap-2">
                          <button type="button" onClick={() => setEditing(null)} className="touch-target h-8 rounded-lg px-3 text-[13px] text-fg/70 hover:text-fg">
                            Cancel
                          </button>
                          <button type="button" disabled={edit.isPending || !editing.body.trim()} onClick={() => edit.mutate(editing)} className="touch-target h-8 rounded-lg border border-strong bg-raised px-3 text-[13px] font-medium text-fg disabled:opacity-50">
                            Save
                          </button>
                        </div>
                      </div>
                    ) : (
                      <IssueMarkdown html={it.html ?? ""} projectId={projectId} className={cn("rounded-lg border border-subtle bg-ground px-3 py-2 text-[13px] leading-5", page && "ml-6")} />
                    )}
                  </li>
                );
              }
              const d = describe(it, user?.id);
              return (
                <li key={it.id} className="flex items-start gap-2.5 text-[13px] leading-[19px]">
                  {page && (d.status ? <StatusIcon status={d.status} size={14} className="mt-[3px]" /> : <Glyph d={d.icon} className={cn("mt-[3px]", d.tint)} />)}
                  <span className={cn("min-w-0 flex-1", page ? "text-fg/70" : "text-fg")}>
                    {/* History rows carry no agent; the issue names the one that opened it (D-102). */}
                    {it.kind === "opened" && issue.created_by?.agent ? (
                      <Who person={issue.created_by} fallback="Someone" className="font-medium" />
                    ) : (
                      <span className={cn("text-fg", page ? "font-medium" : "font-semibold")}>{d.who}</span>
                    )}{" "}
                    {d.text}
                    {!page && it.kind === "merged" && mergedClause(it.pull_request, issue.links, project)}
                    {page && it.automatic && <Auto />}
                  </span>
                  <span className="flex-none font-mono text-xs text-muted">{compactAgo(it.created_at, now)}</span>
                </li>
              );
            })}
            {next && (
              <li className="flex items-start gap-2.5 text-[13px] leading-[19px]">
                <Glyph d={ICON.dot} dashed className="mt-[3px] text-muted" />
                <span className="min-w-0 flex-1 text-muted">
                  <span className="font-medium text-fg">Shipped</span> once the deploy is live <Auto />
                </span>
                <span className="flex-none font-mono text-xs text-muted">next</span>
              </li>
            )}
          </ol>
        )}
      </Read>
      <Dialog open={deleting !== null} onOpenChange={(o) => !o && setDeleting(null)}>
        <DialogContent className="max-w-[460px]">
          <DialogHeader>
            <DialogTitle>Delete this comment?</DialogTitle>
            <DialogDescription>It goes from the issue’s history for everyone, and can’t be brought back.</DialogDescription>
          </DialogHeader>
          <div className="mt-4 flex justify-end gap-2">
            <button type="button" onClick={() => setDeleting(null)} className="touch-target h-9 rounded-lg border border-subtle px-3 text-[13px] font-medium text-fg hover:bg-raised">
              Cancel
            </button>
            <button type="button" disabled={remove.isPending} onClick={() => deleting && remove.mutate(deleting)} className="touch-target h-9 rounded-lg bg-danger px-3.5 text-[13px] font-semibold text-on-danger hover:bg-danger/90 disabled:opacity-50">
              Delete comment
            </button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}

const prop = "touch-target inline-flex min-w-0 items-center gap-1.5 rounded-md px-1 -mx-1 text-left text-fg hover:bg-raised focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:hover:bg-transparent";

/** Priority, assignee and labels, each changed in place by writers. */
function Properties({ issue, projectId, page }: { issue: IssueRead; projectId: string; page?: boolean }) {
  const canWrite = useCanWriteIssues();
  const svc = issueService(projectId);
  const refresh = useIssueRefresh(projectId);
  const qc = useQueryClient();
  const members = useQuery({ queryKey: ["org-members"], queryFn: () => orgService.listMembers(), enabled: canWrite });
  const labels = useQuery({ queryKey: ["issue-labels", projectId], queryFn: () => svc.labels(), enabled: canWrite });
  const [newLabel, setNewLabel] = useState("");
  const [parenting, setParenting] = useState(false);
  const types = useQuery({ ...issueTypesQuery, enabled: canWrite });
  const cycles = useQuery(projectCyclesQuery(projectId, () => svc.cycles()));
  const scale = cycles.data?.estimates ?? "off";
  const container = issue.type.kind === "container";
  const current = cycles.data?.cycles.find((c) => c.state === "current");
  const update = useMutation({
    mutationFn: (input: Parameters<typeof svc.update>[1]) => svc.update(issue.number, input),
    onSuccess: (saved) => {
      // The labels' new version, so the next toggle needs no re-read.
      qc.setQueryData(["issue", projectId, issue.number], saved);
      refresh(issue.number);
    },
    onError: (err) => {
      if (changedSinceRead(err)) {
        toast.error("These labels changed since you opened this issue, so nothing was saved. Try again.");
        refresh(issue.number);
        return;
      }
      toast.error(errorMessage(err, "Couldn't change the issue"));
    },
  });
  const assignable = (members.data ?? []).filter((m) => m.role !== "viewer");
  const toggle = (name: string) => {
    const set = issue.labels.includes(name) ? issue.labels.filter((l) => l !== name) : [...issue.labels, name];
    update.mutate({ labels: set, version: issue.version });
  };
  return (
    <dl className={cn("m-0 grid text-[13px]", page ? "grid-cols-[auto_minmax(0,1fr)] gap-x-5 gap-y-3" : "grid-cols-[84px_minmax(0,1fr)] gap-x-3 gap-y-2")}>
      {page && (
        <>
          <dt className="self-center text-muted">Status</dt>
          <dd>
            <StatusMenu issue={issue} projectId={projectId} />
          </dd>
        </>
      )}
      <dt className="self-center text-muted">Type</dt>
      <dd>
        <DropdownMenu>
          <DropdownMenuTrigger disabled={!canWrite} className={prop}>
            <TypeIcon type={issue.type} size={14} />
            {issue.type.name}
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            <DropdownMenuRadioGroup value={issue.type.id} onValueChange={(v) => update.mutate({ type_id: v })}>
              {(types.data ?? [])
                .filter((t) => !t.archived)
                .map((t) => (
                  <DropdownMenuRadioItem key={t.id} value={t.id}>
                    <TypeIcon type={t} size={14} />
                    {t.name}
                  </DropdownMenuRadioItem>
                ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </dd>
      <dt className="self-center text-muted">Priority</dt>
      <dd>
        <DropdownMenu>
          <DropdownMenuTrigger disabled={!canWrite} className={prop}>
            <PriorityBars priority={issue.priority} />
            {PRIORITY[issue.priority].label}
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            <DropdownMenuRadioGroup value={issue.priority} onValueChange={(v) => update.mutate({ priority: v as IssuePriority })}>
              {PRIORITY_ORDER.map((p) => (
                <DropdownMenuRadioItem key={p} value={p}>
                  {PRIORITY[p].label}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </dd>
      <dt className="self-center text-muted">Assignee</dt>
      <dd>
        <DropdownMenu>
          <DropdownMenuTrigger disabled={!canWrite} className={prop}>
            {issue.assignee?.name ?? "Unassigned"}
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            <DropdownMenuRadioGroup value={issue.assignee?.id ?? ""} onValueChange={(v) => update.mutate({ assignee_id: v })}>
              <DropdownMenuRadioItem value="">Unassigned</DropdownMenuRadioItem>
              {assignable.map((m) => (
                <DropdownMenuRadioItem key={m.user_id} value={m.user_id}>
                  {m.user_name || m.user_email}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </dd>
      <dt className="self-center text-muted">Labels</dt>
      <dd>
        <DropdownMenu>
          <DropdownMenuTrigger disabled={!canWrite} className={prop}>
            <span className="truncate">{issue.labels.length ? issue.labels.join(", ") : "None"}</span>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="max-h-80 overflow-y-auto">
            <form
              className="p-1"
              onSubmit={(e) => {
                e.preventDefault();
                if (update.isPending) return;
                const n = newLabel.trim().toLowerCase();
                // Only a save clears it: a refused one keeps what was typed.
                if (n && !issue.labels.includes(n)) update.mutate({ labels: [...issue.labels, n], version: issue.version }, { onSuccess: () => setNewLabel("") });
                else setNewLabel("");
              }}
            >
              <input
                aria-label="New label"
                placeholder="New label"
                value={newLabel}
                maxLength={40}
                onChange={(e) => setNewLabel(e.target.value)}
                onKeyDown={(e) => e.stopPropagation()}
                className="h-8 w-full rounded-md border border-strong bg-ground px-2 text-[13px] text-fg placeholder:text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
              />
            </form>
            <DropdownMenuSeparator />
            {(labels.data ?? []).map((l) => (
              <DropdownMenuItem key={l.name} disabled={update.isPending} onSelect={(e) => { e.preventDefault(); toggle(l.name); }}>
                <span className="flex w-4 justify-center">{issue.labels.includes(l.name) && <Check className="h-3.5 w-3.5" aria-hidden="true" />}</span>
                {l.name}
                {l.service && <span className="ml-auto text-xs text-muted">service</span>}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </dd>
      <dt className="self-center text-muted">Parent</dt>
      <dd>
        <button type="button" disabled={!canWrite} onClick={() => setParenting(true)} className={prop}>
          {issue.parent ? (
            <>
              <span className="font-mono text-xs text-muted">{issue.parent.key}</span>
              <span className="truncate">{issue.parent.title}</span>
            </>
          ) : (
            <span className="text-muted">{issue.type.kind === "subtask" ? "Needs one" : "None"}</span>
          )}
        </button>
        <Dialog open={parenting} onOpenChange={setParenting}>
          <DialogContent className="max-w-lg">
            <DialogHeader className="mb-0">
              <DialogTitle>Parent of {issue.key}</DialogTitle>
              <DialogDescription>An Epic or a Release it belongs to, or the issue it is a sub-task of.</DialogDescription>
            </DialogHeader>
            <div className="mt-4 flex flex-col gap-3">
              {parenting && (
                <IssuePicker
                  autoFocus
                  exclude={[issue.key, ...issue.children.map((c) => c.key)]}
                  onPick={(key) => {
                    update.mutate({ parent: key });
                    setParenting(false);
                  }}
                />
              )}
              {issue.parent && issue.type.kind !== "subtask" && (
                <button
                  type="button"
                  onClick={() => {
                    update.mutate({ parent: "" });
                    setParenting(false);
                  }}
                  className="self-start text-[13px] text-status-failed-fg hover:underline"
                >
                  Remove from {issue.parent.key}
                </button>
              )}
            </div>
          </DialogContent>
        </Dialog>
      </dd>
      {(cycles.data?.enabled || issue.cycle) &&
        (container ? (
          issue.cycles.length > 0 && (
            <>
              <dt className="self-center text-muted">Cycles</dt>
              <dd className="text-fg">
                {issue.cycles.join(issue.cycles.length === 2 ? " and " : ", ")}
                <span className="text-muted"> · where its sub-issues are</span>
              </dd>
            </>
          )
        ) : (
          <>
            <dt className="self-center text-muted">Cycle</dt>
            <dd className="flex min-w-0 flex-wrap items-center gap-1.5">
              <DropdownMenu>
                <DropdownMenuTrigger disabled={!canWrite} className={prop}>
                  {issue.cycle ? <CycleText cycle={issue.cycle} /> : <span className="text-muted">No cycle</span>}
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start">
                  <DropdownMenuRadioGroup value={String(issue.cycle?.number ?? 0)} onValueChange={(v) => update.mutate({ cycle: Number(v) })}>
                    <CycleItems cycles={cycles.data?.cycles ?? []} />
                  </DropdownMenuRadioGroup>
                </DropdownMenuContent>
              </DropdownMenu>
              {issue.carried_from != null && <CarriedChip from={issue.carried_from} />}
            </dd>
          </>
        ))}
      {(container || issue.start_on) && (
        <>
          <dt className="self-center text-muted">Start</dt>
          <dd>
            <DropdownMenu>
              <DropdownMenuTrigger disabled={!canWrite} className={prop}>
                {issue.start_on ? fmtDay(issue.start_on) : <span className="text-muted">None</span>}
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-[280px]">
                <DueItems value={issue.start_on ?? ""} onChange={(d) => update.mutate({ start_on: d })} />
              </DropdownMenuContent>
            </DropdownMenu>
          </dd>
        </>
      )}
      <dt className="self-center text-muted">{container ? "Target" : "Due"}</dt>
      <dd>
        <DropdownMenu>
          <DropdownMenuTrigger disabled={!canWrite} className={prop}>
            {issue.due_on ? <DueText due={issue.due_on} status={issue.status} /> : <span className="text-muted">None</span>}
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-[280px]">
            <DueItems
              value={issue.due_on ?? ""}
              onChange={(d) => update.mutate({ due_on: d })}
              cycleEnds={!container && current ? { name: cycleName(current), ends_on: current.ends_on } : undefined}
            />
          </DropdownMenuContent>
        </DropdownMenu>
      </dd>
      {readFailed(cycles) ? (
        <>
          <dt className="self-center text-muted">Estimate</dt>
          <dd><LoadFailed what="the estimate" onRetry={() => void cycles.refetch()} error={cycles.error} /></dd>
        </>
      ) : scale !== "off" && (
        <>
          <dt className="self-center text-muted">Estimate</dt>
          <dd>
            {container ? (
              <span className="text-fg">
                {issue.estimate ? pointsText(issue.estimate, scale) : "None"}
                {issue.estimate != null && issue.children.length > 0 && <span className="text-muted"> · its {issue.children.length} sub-issues&rsquo;</span>}
              </span>
            ) : (
              <DropdownMenu>
                <DropdownMenuTrigger disabled={!canWrite} className={prop}>
                  {issue.estimate ? pointsText(issue.estimate, scale) : <span className="text-muted">None</span>}
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start">
                  <DropdownMenuRadioGroup value={String(issue.estimate ?? 0)} onValueChange={(v) => update.mutate({ estimate: Number(v) })}>
                    <EstimateItems scale={scale} />
                  </DropdownMenuRadioGroup>
                </DropdownMenuContent>
              </DropdownMenu>
            )}
          </dd>
        </>
      )}
    </dl>
  );
}

/** A due date with how soon: "Oct 15 · tomorrow", amber or red as it nears. */
function DueText({ due, status }: { due: string; status: IssueStatus }) {
  const d = dueOf(due, status);
  const note = d.tone === "plain" ? "" : d.text.startsWith("Due ") ? d.text.slice(4) : d.text.split(" · ")[1] ?? "";
  return (
    <span className={d.tone === "late" ? "text-status-failed-fg" : d.tone === "soon" ? "text-status-pending-fg" : "text-fg"}>
      {fmtDay(due)}
      {note && <span> · {note}</span>}
    </span>
  );
}

/** The status chip, a menu for writers: moving by hand is always allowed. */
export function StatusMenu({ issue, projectId }: { issue: IssueDetail; projectId: string }) {
  const canWrite = useCanWriteIssues();
  const svc = issueService(projectId);
  const refresh = useIssueRefresh(projectId);
  const move = useMutation({
    mutationFn: (status: IssueStatus) => svc.update(issue.number, { status }),
    onSuccess: () => refresh(issue.number),
    onError: (err) => toast.error(errorMessage(err, "Couldn't move the issue")),
  });
  if (!canWrite) return <StatusChip status={issue.status} />;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger aria-label={`Status: ${STATUS[issue.status].name}. Change`} className="touch-target inline-flex items-center rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus">
        <StatusChip status={issue.status} />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        <DropdownMenuRadioGroup value={issue.status} onValueChange={(v) => move.mutate(v as IssueStatus)}>
          {STATUS_ORDER.map((s) => (
            <DropdownMenuRadioItem key={s} value={s}>
              <StatusIcon status={s} size={14} />
              {STATUS[s].name}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function Description({ issue, projectId }: { issue: IssueDetail; projectId: string }) {
  const canWrite = useCanWriteIssues();
  const svc = issueService(projectId);
  const refresh = useIssueRefresh(projectId);
  const [draft, setDraft] = useState<string | null>(null);
  const save = useMutation({
    mutationFn: (description: string) => svc.update(issue.number, { description }),
    onSuccess: () => {
      setDraft(null);
      refresh(issue.number);
    },
    onError: (err) => toast.error(errorMessage(err, "Couldn't save the description")),
  });
  if (draft !== null) {
    return (
      <div className="flex flex-col gap-2">
        <MarkdownField id={`desc-${issue.number}`} label="Description" value={draft} onChange={setDraft} rows={6} projectId={projectId} autoFocus onSubmit={() => save.mutate(draft)} />
        <div className="flex justify-end gap-2">
          <button type="button" onClick={() => setDraft(null)} className="touch-target h-8 rounded-lg px-3 text-[13px] text-fg/70 hover:text-fg">
            Cancel
          </button>
          <button type="button" disabled={save.isPending} onClick={() => save.mutate(draft)} className="touch-target h-8 rounded-lg border border-strong bg-raised px-3 text-[13px] font-medium text-fg disabled:opacity-50">
            Save
          </button>
        </div>
      </div>
    );
  }
  if (!issue.description.trim()) {
    return canWrite ? (
      <button type="button" onClick={() => setDraft("")} className="touch-target self-start text-[13px] text-muted hover:text-fg">
        Add a description…
      </button>
    ) : null;
  }
  return (
    <div className="group relative">
      <IssueMarkdown html={issue.html} projectId={projectId} className="text-[13px] leading-5" />
      {canWrite && (
        <button type="button" aria-label="Edit the description" onClick={() => setDraft(issue.description)} className="touch-target pointer-coarse:min-w-11 absolute right-0 top-0 inline-flex items-center justify-center rounded-md p-1 text-muted opacity-0 hover:text-fg focus-visible:opacity-100 group-hover:opacity-100 pointer-coarse:opacity-100">
          <Pencil className="h-3.5 w-3.5" aria-hidden="true" />
        </button>
      )}
    </div>
  );
}

function Title({ issue, projectId, page }: { issue: IssueDetail; projectId: string; page?: boolean }) {
  const canWrite = useCanWriteIssues();
  const refresh = useIssueRefresh(projectId);
  const [draft, setDraft] = useState<string | null>(null);
  const save = useMutation({
    mutationFn: (title: string) => issueService(projectId).update(issue.number, { title }),
    onSuccess: () => {
      setDraft(null);
      refresh(issue.number);
    },
    onError: (err) => toast.error(errorMessage(err, "Couldn't rename the issue")),
  });
  const cls = page ? "text-xl font-semibold leading-7 tracking-[-0.01em]" : "text-[17px] font-semibold leading-6 tracking-[-0.01em]";
  if (draft !== null) {
    return (
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (draft.trim()) save.mutate(draft);
        }}
      >
        <input
          aria-label="Title"
          autoFocus
          value={draft}
          maxLength={256}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={() => (draft.trim() && draft !== issue.title ? save.mutate(draft) : setDraft(null))}
          onKeyDown={(e) => e.key === "Escape" && setDraft(null)}
          className={cn(cls, "w-full rounded-md border border-strong bg-ground px-2 py-1 text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus")}
        />
      </form>
    );
  }
  const Tag = page ? "h1" : "h2";
  return (
    <Tag id={`issue-${issue.number}-h`} className={cn(cls, "m-0 text-fg", canWrite && "cursor-text")} onClick={() => canWrite && setDraft(issue.title)}>
      {issue.title}
    </Tag>
  );
}

function CommentBox({ issue, projectId, page }: { issue: IssueDetail; projectId: string; page?: boolean }) {
  const [body, setBody] = useState("");
  const refresh = useIssueRefresh(projectId);
  const send = useMutation({
    mutationFn: () => issueService(projectId).comment(issue.number, body),
    onSuccess: () => {
      setBody("");
      refresh(issue.number);
    },
    onError: (err) => toast.error(errorMessage(err, "Couldn't comment")),
  });
  const id = `comment-${issue.number}`;
  return (
    <div className={cn("flex flex-col gap-2", page && "border-t border-subtle pt-3.5")}>
      <label htmlFor={id} className={page ? "text-xs font-medium text-muted" : "sr-only"}>
        Comment
      </label>
      <MentionTextarea
        id={id}
        rows={2}
        value={body}
        placeholder="Write a comment. @ mentions notify people."
        onChange={setBody}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && body.trim()) {
            e.preventDefault();
            send.mutate();
          }
        }}
        className={cn("w-full resize-y rounded-lg border border-strong bg-ground px-2.5 py-2 text-[13px] leading-[19px] text-fg placeholder:text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus", !page && "h-[72px]")}
      />
      <div className="flex justify-end">
        <button type="button" disabled={!body.trim() || send.isPending} onClick={() => send.mutate()} className={cn("touch-target rounded-lg border border-strong bg-raised px-3 text-[13px] font-medium text-fg hover:bg-raised/70 disabled:opacity-50", page ? "h-8" : "h-9")}>
          Comment
        </button>
      </div>
    </div>
  );
}

/** The issue's body: the panel's one column, or the page's two (its
 *  body, children, relations and activity; its properties beside). */
export function IssueBody({ issue, projectId, project, page }: { issue: IssueRead; projectId: string; project: string; page?: boolean }) {
  const canWrite = useCanWriteIssues();
  const refresh = useIssueRefresh(projectId);
  const [now] = useState(() => Date.now());
  const [adding, setAdding] = useState(false);
  const next = nextMove(issue, project);
  const nextBox = next && !CLOSED.includes(issue.status) && (
    <div className="flex gap-2 rounded-lg border border-subtle px-3 py-2.5 text-[13px] text-fg/80">
      <Glyph d={ICON.bolt} className="mt-[3px] text-link" />
      <span>{next}</span>
    </div>
  );
  const sections = (
    <>
      <ChildrenSection issue={issue} canWrite={canWrite} onAdd={() => setAdding(true)} />
      <RelationsSection issue={issue} projectId={projectId} canWrite={canWrite} onChange={() => refresh(issue.number)} />
      <LinkedWork issue={issue} projectId={projectId} project={project} page={page} />
      {issue.docs.length > 0 && (
        <div className="flex flex-col gap-2">
          <h3 className="text-xs font-medium leading-4 text-muted">Referenced in docs</h3>
          {issue.docs.map((d) => (
            <Link key={d.id} href={`/projects/${d.project_id}/docs/${d.id}`} className="touch-target truncate text-[13px] text-fg hover:underline pointer-coarse:leading-[44px]">
              {d.title}
            </Link>
          ))}
        </div>
      )}
    </>
  );
  const addDialog = (
    <NewIssueDialog
      projectId={projectId}
      open={adding}
      onOpenChange={setAdding}
      parent={issue.key}
      subtask={issue.type.kind !== "container"}
      onCreated={() => refresh(issue.number)}
    />
  );
  if (!page) {
    return (
      <div className="flex flex-col gap-3.5">
        <Title issue={issue} projectId={projectId} />
        {issue.blocked_by.length > 0 && <BlockedChip keys={issue.blocked_by} />}
        <Properties issue={issue} projectId={projectId} />
        <Description issue={issue} projectId={projectId} />
        {nextBox}
        {sections}
        <History issue={issue} projectId={projectId} project={project} now={now} />
        {canWrite && <CommentBox issue={issue} projectId={projectId} />}
        {addDialog}
      </div>
    );
  }
  return (
    <div className="flex flex-wrap items-start gap-8">
      <div className="flex min-w-0 flex-[999_1_560px] flex-col gap-7 [&_h3]:text-[13px] [&_h3]:font-semibold [&_h3]:text-fg">
        <Description issue={issue} projectId={projectId} />
        {sections}
        <section aria-label="Activity" className="flex flex-col gap-3 border-t border-subtle pt-5">
          <History issue={issue} projectId={projectId} project={project} now={now} page />
          {canWrite && <CommentBox issue={issue} projectId={projectId} page />}
        </section>
        {addDialog}
      </div>
      <aside aria-label="Properties" className="flex min-w-0 flex-[1_1_280px] flex-col gap-4 max-lg:order-first lg:sticky lg:top-4">
        <div className="rounded-[10px] border border-subtle bg-surface px-4 py-4">
          <Properties issue={issue} projectId={projectId} page />
        </div>
        {nextBox}
        <dl className="m-0 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5 px-1 text-xs text-muted">
          <dt>Opened</dt>
          <dd className="text-fg/80">
            <Who person={issue.created_by} fallback="Someone" /> · {compactAgo(issue.created_at, now)} ago
          </dd>
          <dt>Updated</dt>
          <dd className="text-fg/80">{compactAgo(issue.updated_at, now)} ago</dd>
          <dt>Status since</dt>
          <dd className="text-fg/80">{compactAgo(issue.status_changed_at, now)} ago</dd>
        </dl>
      </aside>
    </div>
  );
}

/** Clone and Copy link, in a ⋯ menu. */
export function IssueActions({ issue, projectId, className }: { issue: IssueDetail; projectId: string; className?: string }) {
  const canWrite = useCanWriteIssues();
  const [cloning, setCloning] = useState(false);
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger aria-label={`More for ${issue.key}`} className={cn("flex h-[30px] w-[30px] items-center justify-center rounded-md text-fg/70 hover:bg-raised hover:text-fg pointer-coarse:h-11 pointer-coarse:w-11", className)}>
          <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {canWrite && (
            <DropdownMenuItem onSelect={() => setCloning(true)}>
              <CopyPlus className="h-3.5 w-3.5" aria-hidden="true" />
              Clone…
            </DropdownMenuItem>
          )}
          <DropdownMenuItem
            onSelect={() => {
              void navigator.clipboard.writeText(`${window.location.origin}${issueHref(projectId, issue.number)}`);
              toast.success("Link copied");
            }}
          >
            <Copy className="h-3.5 w-3.5" aria-hidden="true" />
            Copy link
          </DropdownMenuItem>
          <DropdownMenuItem
            onSelect={() => {
              void navigator.clipboard.writeText(issue.key);
              toast.success(`${issue.key} copied`);
            }}
          >
            <Copy className="h-3.5 w-3.5" aria-hidden="true" />
            Copy key
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      {cloning && <CloneDialog issue={issue} projectId={projectId} open={cloning} onOpenChange={setCloning} />}
    </>
  );
}

/** IssueList's right-hand panel. */
export function IssuePanel({ projectId, project, number, onClose, focus }: { projectId: string; project: string; number: number; onClose: () => void; focus?: boolean }) {
  const issue = useQuery({ queryKey: ["issue", projectId, number], queryFn: () => issueService(projectId).get(number) });
  const control = "flex h-9 w-9 items-center justify-center rounded-lg text-fg/70 hover:bg-raised hover:text-fg pointer-coarse:h-11 pointer-coarse:w-11";
  return (
    <aside aria-labelledby={`issue-${number}-h`} className="hidden min-w-0 overflow-hidden rounded-[10px] border border-subtle bg-surface min-[1200px]:block">
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
          {issue.data && <TypeIcon type={issue.data.type} size={14} />}
          <span className="font-mono text-xs text-muted">{issue.data?.key}</span>
          {issue.data && <StatusMenu issue={issue.data} projectId={projectId} />}
        </div>
        <div className="flex items-center gap-0.5">
          {issue.data && <IssueActions issue={issue.data} projectId={projectId} className={control} />}
          <Link href={issueHref(projectId, number)} aria-label={`Open ${issue.data?.key ?? "the issue"} on its own page`} className={control}>
            <ExternalLink className="h-[15px] w-[15px]" aria-hidden="true" />
          </Link>
          <button type="button" aria-label="Close the issue panel" autoFocus={focus} onClick={onClose} className={control}>
            <X className="h-[15px] w-[15px]" aria-hidden="true" />
          </button>
        </div>
      </div>
      <div className="p-4">
        <Read query={issue} what="this issue" loading={<div role="status" aria-label="Loading" className="h-64 rounded-lg bg-raised motion-safe:animate-pulse" />}>
          {(d) => <IssueBody issue={d} projectId={projectId} project={project} />}
        </Read>
      </div>
    </aside>
  );
}

export { Title as IssueTitle };
