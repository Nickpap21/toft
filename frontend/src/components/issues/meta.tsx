"use client";

import { Copy } from "lucide-react";
import { toast } from "sonner";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import type { IssuePriority, IssueStatus, IssueWork } from "@/lib/api/issue.service";
import { cn, initials } from "@/lib/utils";
import { shortBranch } from "./view";

// Issues' shared pieces (boards IssueList, Issues; D-81): statuses as the
// boards draw them, priority bars, the chips of a row and a card.

export const STATUS_ORDER: IssueStatus[] = ["in_progress", "in_review", "todo", "backlog", "shipped", "cancelled"];
/** The Board view's columns, left to right. */
export const BOARD_COLUMNS: IssueStatus[] = ["backlog", "todo", "in_progress", "in_review", "shipped"];
/** Statuses events never move out of (D-81). */
export const CLOSED: IssueStatus[] = ["shipped", "cancelled"];

const CIRCLE = "M3 12a9 9 0 1 0 18 0a9 9 0 1 0-18 0";

export const STATUS: Record<
  IssueStatus,
  { name: string; tint: string; dash?: string; fill?: string; mark?: string; rule?: string; chip: string }
> = {
  backlog: { name: "Backlog", tint: "text-muted", dash: "3 3", chip: "bg-raised text-fg/70" },
  todo: { name: "Todo", tint: "text-muted", chip: "bg-strong/60 text-fg/80" },
  in_progress: {
    name: "In progress",
    tint: "text-status-pending",
    fill: "M12 7a5 5 0 0 1 0 10z",
    rule: "Moves here when a branch is pushed",
    chip: "bg-status-pending-soft text-status-pending-fg",
  },
  in_review: {
    name: "In review",
    tint: "text-status-building",
    fill: "M12 7a5 5 0 1 1 0 10a5 5 0 1 1 0-10z",
    rule: "Moves here when a pull request opens",
    chip: "bg-status-building-soft text-status-building-fg",
  },
  shipped: {
    name: "Shipped",
    tint: "text-status-running",
    mark: "m8.5 12 2.5 2.5 4.5-5",
    rule: "Moves here when the deploy is live",
    chip: "bg-status-running-soft text-status-running-fg",
  },
  cancelled: { name: "Cancelled", tint: "text-muted", fill: CIRCLE, mark: "m9 9 6 6m0-6-6 6", chip: "bg-raised text-muted" },
};

export function StatusIcon({ status, size = 15, className }: { status: IssueStatus; size?: number; className?: string }) {
  const s = STATUS[status];
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" className={cn("flex-none", s.tint, className)}>
      <path d={CIRCLE} strokeDasharray={s.dash} />
      {s.fill && <path d={s.fill} fill="currentColor" stroke="none" />}
      {s.mark && <path d={s.mark} stroke={s.fill ? "var(--ground)" : undefined} strokeWidth={s.fill ? 2.6 : 2.4} />}
    </svg>
  );
}

export function StatusChip({ status, small }: { status: IssueStatus; small?: boolean }) {
  return (
    <span className={cn("inline-flex flex-none items-center rounded-full font-medium", small ? "h-5 px-[7px] text-[11px]" : "h-[22px] px-2 text-xs", STATUS[status].chip)}>
      {STATUS[status].name}
    </span>
  );
}

export const PRIORITY: Record<IssuePriority, { label: string; bars: number }> = {
  none: { label: "No priority", bars: 0 },
  low: { label: "Low", bars: 1 },
  medium: { label: "Medium", bars: 2 },
  high: { label: "High", bars: 3 },
  urgent: { label: "Urgent", bars: 3 },
};
export const PRIORITY_ORDER: IssuePriority[] = ["urgent", "high", "medium", "low", "none"];

/** Three bars, as many lit as the priority; amber for urgent. */
export function PriorityBars({ priority }: { priority: IssuePriority }) {
  const lit = PRIORITY[priority].bars;
  return (
    <span role="img" aria-label={`Priority ${PRIORITY[priority].label.toLowerCase()}`} className="inline-flex h-3 w-3.5 flex-none items-end gap-0.5">
      {[4, 7, 10].map((h, i) => (
        <span
          key={h}
          style={{ height: h }}
          className={cn("w-[3px] rounded-xs", i < lit ? (priority === "urgent" ? "bg-status-pending" : "bg-fg/80") : "bg-strong")}
        />
      ))}
    </span>
  );
}

export function UrgentChip() {
  return <span className="inline-flex h-[18px] flex-none items-center rounded px-1.5 text-[11px] font-semibold bg-status-pending-soft text-status-pending-fg">Urgent</span>;
}

export function LabelChip({ name }: { name: string }) {
  return (
    <span title={name} className="inline-flex h-5 max-w-[120px] flex-none items-center rounded-full border border-strong px-1.5 text-[11px] text-fg/70">
      <span className="truncate">{name}</span>
    </span>
  );
}

// The boards' icons for linked work (lucide draws these differently).
export const ICON = {
  branch: "M6 3v12M15 6a3 3 0 1 0 6 0a3 3 0 1 0-6 0M3 18a3 3 0 1 0 6 0a3 3 0 1 0-6 0M18 9a9 9 0 0 1-9 9",
  pr: "M6 9v12M3 6a3 3 0 1 0 6 0a3 3 0 1 0-6 0M15 18a3 3 0 1 0 6 0a3 3 0 1 0-6 0M13 6h3a2 2 0 0 1 2 2v7",
  merge: "M15 18a3 3 0 1 0 6 0a3 3 0 1 0-6 0M3 6a3 3 0 1 0 6 0a3 3 0 1 0-6 0M6 21V9a9 9 0 0 0 9 9",
  layers: "m12 2 10 5-10 5L2 7zM2 17l10 5 10-5M2 12l10 5 10-5",
  check: "M20 6 9 17l-5-5",
  x: "M18 6 6 18M6 6l12 12",
  dot: CIRCLE,
  user: "M19 21v-2a4 4 0 0 0-4-4H9a4 4 0 0 0-4 4v2M8 7a4 4 0 1 0 8 0a4 4 0 1 0-8 0",
  spinner: "M21 12a9 9 0 1 1-6.22-8.56",
  bolt: "M13 2 3 14h9l-1 8 10-12h-9z",
  clock: "M12 7v5l3 2M3 12a9 9 0 1 0 18 0a9 9 0 1 0-18 0",
  comment: "M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z",
  pencil: "M12 20h9M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z",
  sort: "M3 6h18M6 12h12M10 18h4",
};

export function Glyph({ d, size = 14, className, dashed }: { d: string; size?: number; className?: string; dashed?: boolean }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" strokeDasharray={dashed ? "3 3" : undefined} aria-hidden="true" className={cn("flex-none", className)}>
      <path d={d} />
    </svg>
  );
}

/** What a row or card shows of its linked work, as text, icon and tint. */
export function workLine(w: IssueWork | null | undefined): { text: string; icon: string; tint: string } | null {
  if (!w) return null;
  const sha = (w.sha ?? "").slice(0, 7);
  if (w.kind === "branch") return { text: w.branch ?? "", icon: ICON.branch, tint: "text-muted" };
  if (w.state === "merged") {
    if (w.deploy === "building") return { text: `merged · deploying ${sha}`, icon: ICON.layers, tint: "text-link" };
    if (w.deploy === "failed") return { text: `merged · deploy failed`, icon: ICON.layers, tint: "text-status-failed" };
    if (w.deploy === "live") return { text: `live in ${sha}`, icon: ICON.check, tint: "text-status-running" };
    return { text: `!${w.number} merged · ${sha}`, icon: ICON.merge, tint: "text-link" };
  }
  if (w.state === "closed") return { text: `!${w.number} closed`, icon: ICON.pr, tint: "text-muted" };
  const c = w.checks;
  if (!c) return { text: `!${w.number}`, icon: ICON.pr, tint: "text-fg/70" };
  if (c.state === "failure") return { text: `!${w.number} · ${c.failed} check${c.failed === 1 ? "" : "s"} failing`, icon: ICON.pr, tint: "text-status-failed" };
  if (c.state === "pending") return { text: `!${w.number} · ${c.passed}/${c.total} checks`, icon: ICON.pr, tint: "text-status-building" };
  return { text: `!${w.number} · ${c.passed}/${c.total} checks`, icon: ICON.pr, tint: "text-status-running" };
}

const rowChip = "inline-flex h-[22px] flex-none items-center gap-1.5 whitespace-nowrap rounded-sm border border-subtle bg-surface px-[7px] font-mono text-[11px]";

/** A row's or a card's work. On a row (issueKey) a branch shows its key's
 *  part, and tapping it opens the full name and Copy (D-136). */
export function WorkChip({ work, block, issueKey }: { work: IssueWork | null | undefined; block?: boolean; issueKey?: string }) {
  const w = workLine(work);
  if (!w) return null;
  if (block) {
    return (
      <span className={cn("flex min-w-0 items-center gap-1.5 rounded-sm bg-ground px-1.5 py-1 text-[11px] leading-4", w.tint)}>
        <Glyph d={w.icon} size={12} />
        <span className="truncate font-mono">{w.text}</span>
      </span>
    );
  }
  if (work?.kind === "branch" && issueKey) {
    const short = shortBranch(w.text, issueKey);
    return (
      <DropdownMenu>
        <DropdownMenuTrigger
          aria-label={`Branch ${w.text}`}
          title={w.text}
          className={cn(rowChip, w.tint, "relative z-10 hover:text-fg pointer-coarse:after:absolute pointer-coarse:after:-inset-y-[11px] pointer-coarse:after:inset-x-0 pointer-coarse:after:content-['']", short === w.text && "max-w-[220px]")}
        >
          <Glyph d={w.icon} size={12} />
          <span className="min-w-0 truncate">{short}</span>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="max-w-80">
          <DropdownMenuLabel className="break-all font-mono text-xs font-normal text-fg">{w.text}</DropdownMenuLabel>
          <DropdownMenuItem
            onSelect={() =>
              void (navigator.clipboard?.writeText(w.text) ?? Promise.reject(new Error("no clipboard"))).then(
                () => toast.success("Branch name copied"),
                () => toast.error("Couldn’t copy the branch name"),
              )
            }
          >
            <Copy className="h-3.5 w-3.5" aria-hidden="true" />
            Copy branch name
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    );
  }
  return (
    <span className={cn(rowChip, w.tint)}>
      <Glyph d={w.icon} size={12} />
      {w.text}
    </span>
  );
}

export function AssigneeDot({ name }: { name?: string | null }) {
  if (!name) return <span role="img" aria-label="Unassigned" className="h-[22px] w-[22px] flex-none rounded-full border border-dashed border-fg/30" />;
  return (
    <span role="img" aria-label={`Assigned to ${name}`} className="flex h-[22px] w-[22px] flex-none items-center justify-center rounded-full bg-subtle text-[10px] font-semibold text-fg">
      {initials(name)}
    </span>
  );
}

/** The issue page's address. */
export function issueHref(projectId: string, number: number): string {
  return `/projects/${projectId}/issues/${number}`;
}
