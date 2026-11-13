"use client";

import { useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMutation, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  BookOpen,
  Bookmark,
  Bug,
  Check,
  CornerDownRight,
  FlaskConical,
  Link2,
  Plus,
  Rocket,
  Search,
  Siren,
  Wrench,
  X,
  Zap,
  type LucideIcon,
} from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ApiError } from "@/lib/api/client";
import { LoadFailed } from "@/components/shell/unreachable";
import { issueService, searchWorkspace, type IssueDetail, type IssueRelated, type IssueTypeRef, type RelationKind } from "@/lib/api/issue.service";
import { projectsQuery } from "@/lib/queries";
import { cn } from "@/lib/utils";
import { field, label, primary } from "@/app/projects/_lib/ui";
import { AssigneeDot, issueHref, STATUS, StatusIcon } from "./meta";

// Issues, deeper (D-83): types, blocked and progress chips, the issue
// picker, relations, children, and the Link and Clone dialogs.

const GLYPH: Record<IssueTypeRef["icon"], LucideIcon> = {
  task: Check,
  bug: Bug,
  feature: Bookmark,
  epic: Zap,
  release: Rocket,
  subtask: CornerDownRight,
  story: BookOpen,
  chore: Wrench,
  incident: Siren,
  spike: FlaskConical,
};
export const TYPE_ICONS = Object.keys(GLYPH) as IssueTypeRef["icon"][];

const FILL: Record<IssueTypeRef["color"], string> = {
  gray: "bg-type-gray",
  blue: "bg-type-blue",
  red: "bg-type-red",
  green: "bg-type-green",
  violet: "bg-type-violet",
  amber: "bg-type-amber",
  teal: "bg-type-teal",
};
export const TYPE_COLORS = Object.keys(FILL) as IssueTypeRef["color"][];

/** A type's tile: its colour, its glyph in the ground's colour. */
export function TypeIcon({ type, size = 16, className }: { type: Pick<IssueTypeRef, "icon" | "color" | "name">; size?: number; className?: string }) {
  const G = GLYPH[type.icon] ?? Check;
  return (
    <span
      role="img"
      aria-label={type.name}
      title={type.name}
      style={{ width: size, height: size }}
      className={cn("inline-flex flex-none items-center justify-center rounded-sm text-[var(--ground)]", FILL[type.color] ?? FILL.gray, className)}
    >
      <G style={{ width: size * 0.66, height: size * 0.66 }} strokeWidth={2.6} aria-hidden="true" />
    </span>
  );
}

/** "Blocked by STO-16" while an open issue blocks it. */
export function BlockedChip({ keys }: { keys: string[] }) {
  if (!keys.length) return null;
  return (
    <span className="inline-flex h-5 flex-none items-center gap-1 rounded-sm bg-status-failed-soft px-1.5 text-[11px] font-medium text-status-failed-fg">
      Blocked by {keys.join(", ")}
    </span>
  );
}

/** A parent's children: a bar and "2/5". */
export function ProgressPill({ done, total, wide }: { done: number; total: number; wide?: boolean }) {
  const pct = total ? Math.round((done / total) * 100) : 0;
  return (
    <span className="inline-flex flex-none items-center gap-1.5 text-[11px] text-muted" aria-label={`${done} of ${total} children done`}>
      <span className={cn("h-1.5 overflow-hidden rounded-full bg-strong", wide ? "w-24" : "w-10")}>
        <span className="block h-full rounded-full bg-status-running" style={{ width: `${pct}%` }} />
      </span>
      <span className="font-mono">
        {done}/{total}
      </span>
    </span>
  );
}

/** Another issue as a row: type, key, title, status, assignee. */
export function RelatedRow({ issue, after }: { issue: IssueRelated; after?: React.ReactNode }) {
  return (
    <span className="group flex min-h-9 items-center gap-2.5 rounded-md px-2 hover:bg-raised">
      <TypeIcon type={issue.type} size={14} />
      <Link href={issueHref(issue.project_id, issue.number)} className="flex min-w-0 flex-1 items-center gap-2.5 text-[13px] text-fg">
        <span className="flex-none font-mono text-xs text-muted">{issue.key}</span>
        <span className={cn("min-w-0 truncate", issue.status === "shipped" || issue.status === "cancelled" ? "text-fg/60 line-through decoration-fg/30" : "")}>{issue.title}</span>
      </Link>
      <span className={cn("inline-flex flex-none items-center gap-1.5 text-xs", STATUS[issue.status].tint)}>
        <StatusIcon status={issue.status} size={13} />
        <span className="hidden sm:inline">{STATUS[issue.status].name}</span>
      </span>
      {issue.assignee !== undefined && <AssigneeDot name={issue.assignee?.name} />}
      {after}
    </span>
  );
}

/** Picks one of the workspace's issues by key or title words. */
export function IssuePicker({
  projectId,
  onPick,
  exclude = [],
  placeholder = "Search by key or title",
  autoFocus,
}: {
  projectId?: string;
  onPick: (key: string) => void;
  exclude?: string[];
  placeholder?: string;
  autoFocus?: boolean;
}) {
  const [q, setQ] = useState("");
  const found = useQuery({
    queryKey: ["issue-picker", q, projectId],
    queryFn: () => searchWorkspace(q, "issues", projectId, 12),
    placeholderData: (prev) => prev,
  });
  const rows = (found.data?.issues ?? []).filter((i) => !exclude.includes(i.key));
  return (
    <div className="flex flex-col gap-2">
      <label className="flex h-9 items-center gap-2 rounded-lg border border-strong bg-ground px-2.5">
        <Search className="h-3.5 w-3.5 text-muted" aria-hidden="true" />
        <span className="sr-only">Find an issue</span>
        <input
          autoFocus={autoFocus}
          value={q}
          placeholder={placeholder}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && rows[0]) {
              e.preventDefault();
              onPick(rows[0].key);
            }
          }}
          className="min-w-0 flex-1 bg-transparent text-[13px] text-fg outline-none placeholder:text-muted"
        />
      </label>
      <ul className="flex max-h-64 flex-col overflow-y-auto">
        {rows.map((i) => (
          <li key={i.key}>
            <button type="button" onClick={() => onPick(i.key)} className="touch-target flex h-9 w-full items-center gap-2.5 rounded-md px-2 text-left text-[13px] text-fg hover:bg-raised focus-visible:bg-raised focus-visible:outline-none">
              {i.type && <TypeIcon type={i.type} size={14} />}
              <span className="flex-none font-mono text-xs text-muted">{i.key}</span>
              <span className="min-w-0 flex-1 truncate">{i.title}</span>
              <StatusIcon status={i.status} size={13} />
            </button>
          </li>
        ))}
        {!found.data && found.error != null && (
          <li className="px-2 py-2">
            <LoadFailed what="the issues" onRetry={() => void found.refetch()} />
          </li>
        )}
        {found.data && rows.length === 0 && <li className="px-2 py-2 text-xs text-muted">No issue matches.</li>}
      </ul>
    </div>
  );
}

export const RELATION_LABEL: Record<IssueRelatedKind, string> = {
  blocks: "Blocks",
  blocked_by: "Blocked by",
  relates: "Relates to",
  duplicates: "Duplicates",
  duplicated_by: "Duplicated by",
  clones: "Clones",
  cloned_by: "Cloned by",
};
type IssueRelatedKind = IssueDetail["relations"][number]["kind"];
const RELATION_ORDER: IssueRelatedKind[] = ["blocked_by", "blocks", "relates", "duplicates", "duplicated_by", "clones", "cloned_by"];

function err(e: unknown, fallback: string) {
  return e instanceof ApiError ? e.message : fallback;
}

/** Relate this issue to another (Link issue). */
export function LinkIssueDialog({ issue, projectId, open, onOpenChange, onDone }: { issue: IssueDetail; projectId: string; open: boolean; onOpenChange: (o: boolean) => void; onDone: () => void }) {
  const [kind, setKind] = useState<RelationKind>("relates");
  const relate = useMutation({
    mutationFn: (key: string) => issueService(projectId).relate(issue.number, kind, key),
    onSuccess: () => {
      onDone();
      onOpenChange(false);
    },
    onError: (e) => toast.error(err(e, "Couldn't link the issues")),
  });
  const kinds: RelationKind[] = ["blocks", "blocked_by", "relates", "duplicates", "duplicated_by"];
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader className="mb-0">
          <DialogTitle>Link {issue.key}</DialogTitle>
          <DialogDescription>To any issue of the workspace. Each shows the other&apos;s status as it changes.</DialogDescription>
        </DialogHeader>
        <div className="mt-4 flex flex-col gap-3">
          <span role="radiogroup" aria-label="How they relate" className="flex flex-wrap gap-1">
            {kinds.map((k) => (
              <button
                key={k}
                type="button"
                role="radio"
                aria-checked={kind === k}
                onClick={() => setKind(k)}
                className={cn("touch-target h-7 rounded-md border px-2.5 text-[13px]", kind === k ? "border-link bg-accent-soft text-fg" : "border-subtle text-fg/70 hover:text-fg")}
              >
                {RELATION_LABEL[k]}
              </button>
            ))}
          </span>
          {kind === "duplicates" && <p className="text-xs text-status-pending-fg">{issue.key} is cancelled as a duplicate of the issue you pick.</p>}
          {kind === "duplicated_by" && <p className="text-xs text-status-pending-fg">The issue you pick is cancelled as a duplicate of {issue.key}.</p>}
          {open && <IssuePicker autoFocus exclude={[issue.key]} onPick={(k) => relate.mutate(k)} />}
        </div>
      </DialogContent>
    </Dialog>
  );
}

/** The issue's relations, grouped by kind, each other issue's status live. */
export function RelationsSection({ issue, projectId, canWrite, onChange }: { issue: IssueDetail; projectId: string; canWrite: boolean; onChange: () => void }) {
  const [linking, setLinking] = useState(false);
  const remove = useMutation({
    mutationFn: (id: string) => issueService(projectId).unrelate(issue.number, id),
    onSuccess: onChange,
    onError: (e) => toast.error(err(e, "Couldn't remove the link")),
  });
  const groups = RELATION_ORDER.map((k) => ({ k, rows: issue.relations.filter((r) => r.kind === k) })).filter((g) => g.rows.length);
  return (
    <section aria-labelledby={`rel-${issue.number}`} className="flex flex-col gap-1.5">
      <div className="flex items-center gap-2">
        <h3 id={`rel-${issue.number}`} className="text-xs font-medium leading-4 text-muted">
          Relations
        </h3>
        <span className="flex-1" />
        {canWrite && (
          <button type="button" onClick={() => setLinking(true)} className="touch-target inline-flex items-center gap-1 text-xs text-link hover:underline">
            <Link2 className="h-3 w-3" aria-hidden="true" />
            Link issue
          </button>
        )}
      </div>
      {groups.length === 0 && <p className="text-xs text-muted">None yet: link what blocks it, duplicates it or relates to it.</p>}
      {groups.map((g) => (
        <div key={g.k} className="flex flex-col">
          <span className={cn("px-2 pb-0.5 pt-1 text-[11px] font-medium uppercase tracking-wide", g.k === "blocked_by" ? "text-status-failed-fg" : "text-muted")}>{RELATION_LABEL[g.k]}</span>
          {g.rows.map((r) => (
            <RelatedRow
              key={r.id}
              issue={r.issue}
              after={
                canWrite && (
                  <button type="button" aria-label={`Remove the link to ${r.issue.key}`} onClick={() => remove.mutate(r.id)} className="flex h-6 w-6 flex-none items-center justify-center rounded text-muted opacity-0 hover:text-fg focus-visible:opacity-100 group-hover:opacity-100 pointer-coarse:h-11 pointer-coarse:w-11 pointer-coarse:opacity-100">
                    <X className="h-3.5 w-3.5" aria-hidden="true" />
                  </button>
                )
              }
            />
          ))}
        </div>
      ))}
      <LinkIssueDialog issue={issue} projectId={projectId} open={linking} onOpenChange={setLinking} onDone={onChange} />
    </section>
  );
}

/** A parent's children with their progress, and Add sub-issue. */
export function ChildrenSection({ issue, canWrite, onAdd }: { issue: IssueDetail; canWrite: boolean; onAdd: () => void }) {
  const container = issue.type.kind === "container";
  if (!container && issue.children.length === 0 && issue.type.kind === "subtask") return null;
  const done = issue.progress?.done ?? 0;
  return (
    <section aria-labelledby={`kids-${issue.number}`} className="flex flex-col gap-1.5">
      <div className="flex items-center gap-2">
        <h3 id={`kids-${issue.number}`} className="text-xs font-medium leading-4 text-muted">
          {container ? `In this ${issue.type.name.toLowerCase()}` : "Sub-issues"}
        </h3>
        {issue.children.length > 0 && <ProgressPill done={done} total={issue.children.length} wide />}
        <span className="flex-1" />
        {canWrite && (
          <button type="button" onClick={onAdd} className="touch-target inline-flex items-center gap-1 text-xs text-link hover:underline">
            <Plus className="h-3 w-3" aria-hidden="true" />
            {container ? "Add issue" : "Add sub-issue"}
          </button>
        )}
      </div>
      {issue.children.length === 0 ? (
        <p className="text-xs text-muted">{container ? `Ships by itself once every issue in it is shipped or cancelled.` : "Break it down: sub-issues show here with their status."}</p>
      ) : (
        <div className="flex flex-col">
          {issue.children.map((c) => (
            <RelatedRow key={c.key} issue={c} />
          ))}
        </div>
      )}
    </section>
  );
}

/** Clone: title, project, and what to keep (D-83). */
export function CloneDialog({ issue, projectId, open, onOpenChange }: { issue: IssueDetail; projectId: string; open: boolean; onOpenChange: (o: boolean) => void }) {
  const router = useRouter();
  const projects = useQuery({ ...projectsQuery, enabled: open });
  const [title, setTitle] = useState(issue.title);
  const [target, setTarget] = useState(projectId);
  const [keep, setKeep] = useState<string[]>(["labels"]);
  const clone = useMutation({
    mutationFn: () => issueService(projectId).clone(issue.number, { title, project_id: target, keep: keep as ("assignee" | "labels" | "relations" | "subtasks")[] }),
    onSuccess: (c) => {
      onOpenChange(false);
      toast.success(`Cloned as ${c.key}`);
      router.push(issueHref(target, c.number));
    },
    onError: (e) => toast.error(err(e, "Couldn't clone the issue")),
  });
  const opts: [string, string][] = [
    ["assignee", "Assignee"],
    ["labels", "Labels"],
    ["relations", "Relations"],
    ["subtasks", "Sub-tasks"],
  ];
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            clone.mutate();
          }}
        >
          <DialogHeader className="mb-0">
            <DialogTitle>Clone {issue.key}</DialogTitle>
            <DialogDescription>The clone starts in Backlog, linked to {issue.key}.</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-1.5">
            <label htmlFor="cl-title" className={label}>
              Title
            </label>
            <input id="cl-title" required maxLength={256} value={title} onChange={(e) => setTitle(e.target.value)} className={field} />
          </div>
          <div className="flex flex-col gap-1.5">
            <label htmlFor="cl-project" className={label}>
              Project
            </label>
            <select id="cl-project" value={target} onChange={(e) => setTarget(e.target.value)} className={field}>
              {(projects.data ?? []).map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </div>
          <fieldset className="flex flex-col gap-1.5">
            <legend className={cn(label, "mb-1.5")}>Keep</legend>
            {opts.map(([v, l]) => (
              <label key={v} className="flex items-center gap-2 text-[13px] text-fg">
                <input type="checkbox" checked={keep.includes(v)} onChange={(e) => setKeep((k) => (e.target.checked ? [...k, v] : k.filter((x) => x !== v)))} className="h-4 w-4 accent-[var(--accent)]" />
                {l}
              </label>
            ))}
          </fieldset>
          <div className="flex justify-end gap-2">
            <button type="button" onClick={() => onOpenChange(false)} className="h-9 rounded-lg px-3 text-[13px] text-fg/70 hover:text-fg">
              Cancel
            </button>
            <button type="submit" disabled={!title.trim() || clone.isPending} className={primary}>
              Clone
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
