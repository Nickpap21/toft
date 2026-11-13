"use client";

import { useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { Search } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { ApiError } from "@/lib/api/client";
import { issueService, type Cycle, type Issue } from "@/lib/api/issue.service";
import { LoadFailed } from "@/components/shell/unreachable";
import { projectsQuery } from "@/lib/queries";
import { cn } from "@/lib/utils";
import { primary } from "@/app/projects/_lib/ui";
import { PriorityBars, STATUS } from "./meta";
import { cycleDates, cycleName, estimateText, workspaceIssueSettingsQuery } from "./cycles";

// Add issues to a cycle (board AddToCycle, D-90): the workspace's open
// issues, those in no cycle by default, picked across projects; one PATCH
// each.

export function AddToCycleDialog({ cycle, open, onOpenChange, onAdded }: { cycle: Cycle; open: boolean; onOpenChange: (o: boolean) => void; onAdded: () => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[600px] gap-0 p-0">{open && <Picker cycle={cycle} onDone={() => onOpenChange(false)} onAdded={onAdded} />}</DialogContent>
    </Dialog>
  );
}

type Pick = { projectId: string; number: number; key: string; estimate: number };

function Picker({ cycle, onDone, onAdded }: { cycle: Cycle; onDone: () => void; onAdded: () => void }) {
  const projects = useQuery(projectsQuery);
  const settings = useQuery(workspaceIssueSettingsQuery);
  const scale = settings.data?.estimates ?? "off";
  const [q, setQ] = useState("");
  const [project, setProject] = useState("");
  const [unplanned, setUnplanned] = useState(true);
  const [picked, setPicked] = useState<Pick[]>([]);
  const [saving, setSaving] = useState(false);
  const withIssues = (projects.data ?? []).filter((p) => p.issue_key);
  const lists = useQueries({
    queries: withIssues.map((p) => ({
      queryKey: ["issues", p.id, "add-to-cycle", unplanned, q],
      queryFn: () => issueService(p.id).list({ status: ["in_progress", "in_review", "todo", "backlog"], cycle: unplanned ? "none" : undefined, q: q || undefined, order: "priority" }, undefined, 50),
    })),
  });
  const rows: (Issue & { projectId: string })[] = lists
    .flatMap((l, i) => (l.data?.data ?? []).map((it) => ({ ...it, projectId: withIssues[i].id })))
    .filter((it) => it.type.kind !== "container" && it.cycle?.number !== cycle.number && (!project || it.projectId === project));
  const failedLists = lists.filter((l) => !l.data && l.error);
  const projectsFailed = !projects.data && !!projects.error;
  const isPicked = (it: { projectId: string; number: number }) => picked.some((p) => p.projectId === it.projectId && p.number === it.number);
  const toggle = (it: Issue & { projectId: string }) =>
    setPicked((ps) =>
      isPicked(it) ? ps.filter((p) => !(p.projectId === it.projectId && p.number === it.number)) : [...ps, { projectId: it.projectId, number: it.number, key: it.key, estimate: it.estimate ?? 0 }],
    );
  const points = picked.reduce((n, p) => n + p.estimate, 0);
  const add = async () => {
    setSaving(true);
    const failed: string[] = [];
    for (const p of picked) {
      try {
        await issueService(p.projectId).update(p.number, { cycle: cycle.number });
      } catch (e) {
        failed.push(`${p.key}: ${e instanceof ApiError ? e.message : "failed"}`);
      }
    }
    setSaving(false);
    onAdded();
    if (failed.length) toast.error(`Not added: ${failed.join("; ")}`);
    else toast.success(`${picked.length === 1 ? picked[0].key : `${picked.length} issues`} added to ${cycleName(cycle)}`);
    if (!failed.length) onDone();
  };
  const k = cycle.counts;
  return (
    <div className="flex flex-col">
      <div className="flex flex-col gap-1 px-5 pb-3.5 pt-[18px]">
        <DialogTitle className="text-[17px] font-semibold leading-6 text-fg">Add issues to {cycleName(cycle)}</DialogTitle>
        <DialogDescription className="text-[13px] text-fg/70">
          {cycleDates(cycle)} · {k.scope} {k.scope === 1 ? "issue" : "issues"}
          {scale !== "off" ? ` and ${k.points} points` : ""} planned so far. The issues stay in their projects.
        </DialogDescription>
      </div>
      <div className="flex flex-col gap-2.5 px-5 pb-3">
        <label className="flex h-9 items-center gap-2 rounded-lg border border-strong bg-ground px-2.5 text-muted">
          <Search className="h-3.5 w-3.5" aria-hidden="true" />
          <span className="sr-only">Search open issues</span>
          <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search open issues by key or title" className="flex-1 bg-transparent text-[13px] text-fg outline-none placeholder:text-muted" />
        </label>
        <div className="flex flex-wrap items-center gap-1">
          {[{ id: "", name: "All projects" }, ...withIssues.map((p) => ({ id: p.id, name: p.name }))].map((p) => (
            <button
              key={p.id || "all"}
              type="button"
              aria-pressed={project === p.id}
              onClick={() => setProject(p.id)}
              className={cn("h-7 rounded-md px-2.5 text-[13px]", project === p.id ? "bg-raised font-medium text-fg" : "text-fg/70 hover:text-fg")}
            >
              {p.name}
            </button>
          ))}
          <span className="flex-1" />
          <label className="inline-flex cursor-pointer items-center gap-2 text-[13px] text-fg/70">
            <input type="checkbox" checked={unplanned} onChange={(e) => setUnplanned(e.target.checked)} className="h-4 w-4 accent-[var(--accent)]" />
            Not in a cycle yet
          </label>
        </div>
      </div>
      <ul aria-label="Open issues" className="max-h-[440px] overflow-y-auto border-t border-subtle">
        {(projectsFailed || failedLists.length > 0) && (
          <li className="border-b border-subtle/70 px-5 py-3">
            <LoadFailed
              what={projectsFailed ? "the projects" : failedLists.length === 1 ? "one project’s open issues" : `${failedLists.length} projects’ open issues`}
              onRetry={() => {
                if (projectsFailed) void projects.refetch();
                failedLists.forEach((l) => void l.refetch());
              }}
            />
          </li>
        )}
        {rows.length === 0 && !projectsFailed && failedLists.length === 0 && <li className="px-5 py-6 text-[13px] text-fg/70">{projects.isLoading || lists.some((l) => l.isLoading) ? "Loading…" : "No open issues match."}</li>}
        {rows.map((it) => {
          const on = isPicked(it);
          return (
            <li key={`${it.projectId}-${it.number}`} className={cn("border-b border-subtle/70", on && "bg-accent-soft")}>
              <label className="flex min-h-10 cursor-pointer items-center gap-2.5 px-5">
                <input type="checkbox" checked={on} onChange={() => toggle(it)} className="h-4 w-4 flex-none accent-[var(--accent)]" />
                <PriorityBars priority={it.priority} />
                <span className="w-[58px] flex-none font-mono text-xs text-muted">{it.key}</span>
                <span className="min-w-0 flex-auto truncate text-[13px] font-medium text-fg">{it.title}</span>
                {it.cycle && <span className="hidden flex-none text-xs text-muted sm:inline">{cycleName(it.cycle)}</span>}
                <span className="hidden flex-none text-xs text-muted sm:inline">{STATUS[it.status].name}</span>
                {scale !== "off" && (
                  <span className="flex h-5 min-w-[22px] flex-none items-center justify-center rounded-sm border border-subtle px-1 font-mono text-[11px] text-fg/80">
                    {it.estimate ? estimateText(it.estimate, scale) : "–"}
                  </span>
                )}
              </label>
            </li>
          );
        })}
      </ul>
      <div className="flex flex-wrap items-center gap-2.5 px-5 py-3.5">
        <span className="text-[13px] text-fg/70">{picked.length ? `${picked.length} selected${scale !== "off" ? ` · ${points} points` : ""}` : `Pick the issues to plan in ${cycleName(cycle)}`}</span>
        <span className="flex-1" />
        <button type="button" onClick={onDone} className="h-9 rounded-lg px-3 text-[13px] text-fg/70 hover:text-fg">
          Cancel
        </button>
        <button type="button" disabled={!picked.length || saving} onClick={add} className={primary}>
          {picked.length === 1 ? "Add 1 issue" : `Add ${picked.length} issues`}
        </button>
      </div>
    </div>
  );
}
