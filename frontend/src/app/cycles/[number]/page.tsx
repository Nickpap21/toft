"use client";

import { useCallback, useMemo, useState } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ChevronDown, ChevronLeft, ChevronRight, Pencil, Plus, SlidersHorizontal } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { ApiError, notFound } from "@/lib/api/client";
import { LoadFailed, NextPageFailed, Read, readFailed } from "@/components/shell/unreachable";
import { cycleService, type CycleDetail, type CycleIssue, type EstimateScale, type IssueStatus } from "@/lib/api/issue.service";
import { useOrgRole } from "@/context/AuthContext";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { field, outline, primary, small } from "@/app/projects/_lib/ui";
import { AssigneeDot, issueHref, LabelChip, PriorityBars, STATUS, StatusIcon, UrgentChip } from "@/components/issues/meta";
import { TypeIcon } from "@/components/issues/deep";
import { useCanWriteIssues } from "@/components/issues/issue-detail";
import { CarriedChip, cycleDates, cycleName, day, DueChip, EstimateBox, fmtDay, iso, addDays, today, WEEKDAYS, workspaceIssueSettingsQuery } from "@/components/issues/cycles";
import { AddToCycleDialog } from "@/components/issues/add-to-cycle";

// A cycle (board Cycles, D-90): its progress and chart, its issues across
// the workspace's projects, its assignees and projects, and the cycles
// around it.

const ORDER: IssueStatus[] = ["in_progress", "in_review", "todo", "backlog", "shipped", "cancelled"];
const PRIO = { urgent: 4, high: 3, medium: 2, low: 1, none: 0 } as const;
const card = "overflow-hidden rounded-[10px] border border-subtle bg-surface";

export default function CyclePage() {
  const n = Number(useParams<{ number: string }>().number);
  const router = useRouter();
  const qc = useQueryClient();
  const canWrite = useCanWriteIssues();
  const owner = useOrgRole() === "owner";
  const settings = useQuery({ ...workspaceIssueSettingsQuery, meta: { quietFailure: "Only writes the footnote on how cycles run and offers the Points switch: a failed read leaves both out and the page counts issues." } });
  const cycle = useQuery({ queryKey: ["cycles", "detail", n], queryFn: () => cycleService.get(n), enabled: n > 0, retry: false });
  const issues = useQuery({ queryKey: ["cycles", "issues", n], queryFn: () => cycleService.issues(n), enabled: n > 0 });
  const [project, setProject] = useState("");
  const [assignee, setAssignee] = useState<string | null>(null);
  const [by, setBy] = useState<"status" | "project" | "assignee">("status");
  const [points, setPoints] = useState(false);
  const [adding, setAdding] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const c = cycle.data;
  useDocumentTitle(`${c ? cycleName(c) : `Cycle ${n}`} · Toft`);
  const scale: EstimateScale = settings.data?.estimates ?? "off";

  if (notFound(cycle.error)) {
    return (
      <div className="flex flex-col gap-3">
        <h1 className="text-xl font-semibold leading-7 text-fg">No Cycle {n}</h1>
        <p className="text-[13px] text-fg/70">
          The workspace has no cycle with this number. <Link href="/cycles" className="inline-link">The current cycle</Link>
        </p>
      </div>
    );
  }
  if (!c) return <Read query={cycle} what="this cycle" heading="h1" loading={<div role="status" aria-label="Loading" className="h-80 rounded-[10px] bg-surface motion-safe:animate-pulse" />}>{() => null}</Read>;

  const all = issues.data?.data ?? [];
  const rows = all.filter((i) => (!project || i.project_id === project) && (assignee === null || (i.assignee?.id ?? "") === assignee));
  const now = today();
  const dayN = Math.min(Math.max(Math.round((day(now).getTime() - day(c.starts_on).getTime()) / 86_400_000) + 1, 1), length(c));
  const sub =
    c.state === "current"
      ? `Day ${dayN} of ${length(c)} · ${c.days_left === 0 ? "its last day" : `${c.days_left} ${c.days_left === 1 ? "day" : "days"} left`} · what is unfinished when it ends moves to ${c.next ? `Cycle ${c.next}` : "the next cycle"}`
      : c.state === "upcoming"
        ? `Starts ${WEEKDAYS[day(c.starts_on).getUTCDay()]}, ${fmtDay(c.starts_on)} · ${length(c)} days`
        : `Ended ${fmtDay(c.ends_on)} · ${c.counts.shipped} of ${c.counts.scope} shipped${c.counts.carried ? ` · ${c.counts.carried} carried over to Cycle ${c.next}` : ""}`;
  const chip = { current: "bg-status-building-soft text-status-building-fg", upcoming: "bg-raised text-fg/70", ended: "bg-raised text-muted" }[c.state];

  return (
    <div className="flex flex-col">
      <header className="-mx-4 -mt-4 mb-5 flex flex-wrap items-center justify-between gap-3 border-b border-subtle px-4 py-4 md:-mx-8 md:-mt-8 md:px-8">
        <div className="flex min-w-0 flex-col gap-0.5">
          <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
            <h1 className="min-w-0 text-xl font-semibold leading-7 tracking-[-0.01em] text-fg [overflow-wrap:anywhere]">{cycleName(c)}</h1>
            {c.name && <span className="text-sm text-muted">Cycle {c.number}</span>}
            <span className="text-sm text-fg/70">{cycleDates(c)}</span>
            <span className={cn("inline-flex h-[22px] items-center self-center rounded-full px-2 text-xs font-medium capitalize", chip)}>{c.state}</span>
            {canWrite && (
              <button type="button" onClick={() => setRenaming(true)} aria-label={`Rename ${cycleName(c)}`} className="touch-target pointer-coarse:min-w-11 inline-flex items-center justify-center self-center text-muted hover:text-fg">
                <Pencil className="h-3.5 w-3.5" aria-hidden="true" />
              </button>
            )}
          </div>
          <p className="text-[13px] text-muted">{sub}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <span role="group" aria-label="Cycle" className="touch-target inline-flex h-9 items-center overflow-hidden rounded-lg border border-strong bg-raised">
            <NavArrow to={c.previous} label="Previous" />
            <CyclePicker current={c} onPick={(m) => router.push(`/cycles/${m}`)} />
            <NavArrow to={c.next} label="Next" next />
          </span>
          {owner && (
            <Link href="/settings#cyc-h" className={outline}>
              <SlidersHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
              Cycle settings
            </Link>
          )}
          {canWrite && c.state !== "ended" && (
            <button type="button" onClick={() => setAdding(true)} className={primary}>
              <Plus className="h-3.5 w-3.5" aria-hidden="true" />
              Add issues
            </button>
          )}
        </div>
      </header>

      <div className="flex flex-wrap items-start gap-6">
        <div className="flex min-w-0 flex-[999_1_620px] flex-col gap-5">
          <Progress c={c} points={points && scale !== "off"} onPoints={scale !== "off" ? setPoints : undefined} />

          <section aria-labelledby="issues-h" className="flex flex-col gap-3">
            <div className="flex flex-wrap items-center gap-2">
              <h2 id="issues-h" className="mr-1.5 text-[15px] font-semibold leading-[22px] text-fg">
                Issues
              </h2>
              <nav aria-label="Project" className="flex flex-wrap gap-1">
                {[{ id: "", name: "All projects", total: all.length }, ...c.projects.map((p) => ({ id: p.id, name: p.name, total: all.filter((i) => i.project_id === p.id).length }))].map((p) => (
                  <button
                    key={p.id || "all"}
                    type="button"
                    aria-pressed={project === p.id}
                    onClick={() => setProject(p.id)}
                    className={cn("touch-target inline-flex h-[30px] items-center gap-1.5 rounded-md px-2.5 text-[13px]", project === p.id ? "bg-raised font-medium text-fg" : "text-fg/70 hover:text-fg")}
                  >
                    {p.name}
                    <span className="font-mono text-[11px] text-muted">{issues.data ? p.total : "—"}</span>
                  </button>
                ))}
              </nav>
              <span className="flex-1" />
              <DropdownMenu>
                <DropdownMenuTrigger
                  className={
                    assignee === null
                      ? "touch-target h-[30px] rounded-md border border-dashed border-strong px-2.5 text-[13px] text-fg/70 hover:text-fg"
                      : "touch-target h-[30px] rounded-md border border-strong bg-surface px-2.5 text-[13px] text-fg"
                  }
                >
                  {assignee === null ? "Assignee" : (c.assignees.find((a) => a.id === assignee)?.name || "Unassigned")}
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuRadioGroup value={assignee ?? "*"} onValueChange={(v) => setAssignee(v === "*" ? null : v)}>
                    <DropdownMenuRadioItem value="*">Anyone</DropdownMenuRadioItem>
                    {c.assignees.map((a) => (
                      <DropdownMenuRadioItem key={a.id || "none"} value={a.id}>
                        {a.name || "Unassigned"}
                      </DropdownMenuRadioItem>
                    ))}
                  </DropdownMenuRadioGroup>
                </DropdownMenuContent>
              </DropdownMenu>
              <DropdownMenu>
                <DropdownMenuTrigger className="touch-target inline-flex h-[30px] items-center gap-1.5 rounded-md border border-strong bg-surface px-2.5 text-[13px] text-fg">
                  <SlidersHorizontal className="h-[13px] w-[13px]" aria-hidden="true" />
                  {{ status: "Status", project: "Project", assignee: "Assignee" }[by]} · priority
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuRadioGroup value={by} onValueChange={(v) => setBy(v as typeof by)}>
                    <DropdownMenuRadioItem value="status">Group by status</DropdownMenuRadioItem>
                    <DropdownMenuRadioItem value="project">Group by project</DropdownMenuRadioItem>
                    <DropdownMenuRadioItem value="assignee">Group by assignee</DropdownMenuRadioItem>
                  </DropdownMenuRadioGroup>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
            <Read
              query={issues}
              what="this cycle’s issues"
              isEmpty={(d) => d.data.length === 0}
              empty={
                <p className={cn(card, "px-4 py-6 text-[13px] text-fg/70")}>
                  {c.state === "ended" ? "Nothing was planned in this cycle." : "Nothing is planned in this cycle yet. Add issues from any project, or pick this cycle on an issue."}
                </p>
              }
            >
              {() => <Groups rows={rows} by={by} scale={scale} carriedOut={c.state === "ended"} />}
            </Read>
            <p className="text-xs text-muted">
              A cycle holds issues from every project in the workspace. Epics and releases span cycles, so their sub-issues go in one instead. Each project&rsquo;s Issues
              tab filters to its own share.
            </p>
          </section>
        </div>

        <aside className="flex min-w-0 flex-[1_1_300px] flex-col gap-4">
          <Shares title="Assignees" rows={c.assignees.map((a) => ({ ...a, dot: a.name || null }))} />
          <Shares title="Projects" rows={c.projects.map((p) => ({ ...p, href: `/projects/${p.id}/issues?cycle=${c.state === "current" ? "current" : c.number}` }))} />
          <CycleList current={c.number} />
          {settings.data && (
            <p className="text-xs leading-[18px] text-muted">
              {settings.data.cycles.enabled
                ? `Cycles run ${settings.data.cycles.weeks === 1 ? "a week" : `${settings.data.cycles.weeks} weeks`} from ${WEEKDAYS[settings.data.cycles.start_day]}, ${settings.data.cycles.time_zone} time. Toft starts each one on time and keeps the next two ready to plan. When a cycle ends, its unfinished issues move to the next one, and the issue's history says so.`
                : "The workspace doesn't use cycles now: no new cycle starts. Past cycles stay readable."}
            </p>
          )}
        </aside>
      </div>

      <AddToCycleDialog
        cycle={c}
        open={adding}
        onOpenChange={setAdding}
        onAdded={() => {
          qc.invalidateQueries({ queryKey: ["cycles"] });
          qc.invalidateQueries({ queryKey: ["issues"] });
          qc.invalidateQueries({ queryKey: ["project-cycles"] });
        }}
      />
      <RenameDialog c={c} open={renaming} onOpenChange={setRenaming} />
    </div>
  );
}

const length = (c: { starts_on: string; ends_on: string }) => Math.round((day(c.ends_on).getTime() - day(c.starts_on).getTime()) / 86_400_000) + 1;

function NavArrow({ to, label, next }: { to: number | null; label: string; next?: boolean }) {
  const Icon = next ? ChevronRight : ChevronLeft;
  const cls = "flex h-[34px] w-[34px] items-center justify-center text-fg/70 hover:text-fg pointer-coarse:h-11 pointer-coarse:w-11";
  return to ? (
    <Link href={`/cycles/${to}`} aria-label={`${label}: Cycle ${to}`} className={cls}>
      <Icon className="h-[15px] w-[15px]" aria-hidden="true" />
    </Link>
  ) : (
    <span aria-hidden="true" className={cn(cls, "opacity-40")}>
      <Icon className="h-[15px] w-[15px]" />
    </span>
  );
}

function CyclePicker({ current, onPick }: { current: CycleDetail; onPick: (n: number) => void }) {
  const list = useQuery({ queryKey: ["cycles", "list", "picker"], queryFn: () => cycleService.list(undefined, undefined, 12) });
  return (
    <DropdownMenu>
      <DropdownMenuTrigger className="touch-target inline-flex h-[34px] min-w-0 max-w-[180px] items-center gap-1.5 border-x border-subtle px-2.5 text-[13px] text-fg">
        <span className="min-w-0 truncate">{cycleName(current)}</span>
        <ChevronDown className="h-3 w-3 text-muted" aria-hidden="true" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="max-h-80 overflow-y-auto">
        {(list.data?.data ?? []).map((c) => (
          <DropdownMenuItem key={c.number} onSelect={() => onPick(c.number)} className={cn(c.number === current.number && "bg-accent-soft")}>
            {cycleName(c)}
            <span className="ml-auto pl-3 text-xs text-muted">{c.state === "current" ? "current" : cycleDates(c)}</span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** The strip and the burn-up chart (one point a day the panel ran). */
function Progress({ c, points, onPoints }: { c: CycleDetail; points: boolean; onPoints?: (p: boolean) => void }) {
  const k = c.counts;
  const open = Math.max(k.started - k.shipped, 0);
  const strip = [
    { label: "Scope", value: k.scope, note: `issues${k.points ? ` · ${k.points} points` : ""}${c.added ? ` · ${c.added} added since ${fmtDay(c.starts_on)}` : ""}`, grow: 2 },
    c.state === "ended"
      ? { label: "Carried over", value: k.carried, note: c.next ? `to Cycle ${c.next}` : "", grow: 1 }
      : { label: "Started", value: open, note: "in progress or in review", grow: 1 },
    { label: "Shipped", value: k.shipped, note: `${k.scope ? Math.round((k.shipped * 100) / k.scope) : 0}%${k.shipped_points ? ` · ${k.shipped_points} points` : ""}`, grow: 1 },
    { label: c.previous ? `From Cycle ${c.previous}` : "Carried in", value: c.carried_in, note: "carried over", grow: 1 },
  ];
  return (
    <section aria-labelledby="progress-h" className={card}>
      <h2 id="progress-h" className="sr-only">
        Progress
      </h2>
      <dl className="flex flex-wrap border-b border-subtle">
        {strip.map((s) => (
          <div key={s.label} className="flex flex-col gap-0.5 border-r border-subtle px-4 py-3" style={{ flex: `${s.grow} 1 150px` }}>
            <dt className="text-xs text-muted">{s.label}</dt>
            <dd className="flex items-baseline gap-1.5">
              <span className="text-xl font-semibold leading-7 text-fg">{s.value}</span>
              <span className="text-xs text-muted">{s.note}</span>
            </dd>
          </div>
        ))}
      </dl>
      <div className="px-4 pb-3 pt-3.5">
        <Chart c={c} points={points} />
        <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs text-fg/70">
          <span className="inline-flex items-center gap-1.5">
            <span aria-hidden="true" className="h-0.5 w-3.5 bg-fg/60" />
            Scope: issues in the cycle
          </span>
          <span className="inline-flex items-center gap-1.5">
            <span aria-hidden="true" className="h-0 w-3.5 border-t-2 border-dashed border-muted" />
            Started: in progress, in review or shipped
          </span>
          <span className="inline-flex items-center gap-1.5">
            <span aria-hidden="true" className="h-2 w-3.5 border-t-2 border-link bg-accent-soft" />
            Shipped
          </span>
          <span className="flex-1" />
          {onPoints && (
            <span role="group" aria-label="Count" className="inline-flex gap-0.5 rounded-lg border border-subtle bg-ground p-0.5">
              {[false, true].map((p) => (
                <button
                  key={String(p)}
                  type="button"
                  aria-pressed={points === p}
                  onClick={() => onPoints(p)}
                  className={cn("touch-target h-6 rounded-sm px-2 text-xs", points === p ? "bg-raised text-fg" : "text-fg/70 hover:text-fg")}
                >
                  {p ? "Points" : "Issues"}
                </button>
              ))}
            </span>
          )}
        </div>
      </div>
    </section>
  );
}

function Chart({ c, points }: { c: CycleDetail; points: boolean }) {
  // Drawn at its real width, so its 11 px labels are 11 px on a phone too.
  const [W, setW] = useState(760);
  const measure = useCallback((el: SVGSVGElement | null) => {
    if (!el) return;
    const ro = new ResizeObserver(([e]) => e.contentRect.width > 0 && setW(Math.round(e.contentRect.width)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const total = length(c);
  const now = today();
  const days = c.days;
  const pick = (d: CycleDetail["days"][number]) => (points ? [d.scope_points, d.started_points, d.shipped_points] : [d.scope, d.started, d.shipped]);
  const top = Math.max(5, ...days.map((d) => pick(d)[0]));
  const max = Math.ceil(top / 4) * 4;
  const right = W - 20;
  const X = (i: number) => 40 + i * ((right - 40) / Math.max(total - 1, 1));
  const Y = (v: number) => 174 - (v / max) * 150;
  const idx = (s: string) => Math.round((day(s).getTime() - day(c.starts_on).getTime()) / 86_400_000);
  const pts = days.map((d) => ({ i: idx(d.day), v: pick(d) })).filter((p) => p.i >= 0 && p.i < total);
  const line = (k: number) => pts.map((p, j) => `${j ? "L" : "M"}${X(p.i).toFixed(1)} ${Y(p.v[k]).toFixed(1)}`).join(" ");
  const step = pts.map((p, j) => (j ? `H${X(p.i).toFixed(1)} V${Y(p.v[0]).toFixed(1)}` : `M${X(p.i).toFixed(1)} ${Y(p.v[0]).toFixed(1)}`)).join(" ");
  const last = pts.at(-1);
  const todayI = c.state === "current" ? idx(now) : c.state === "ended" ? total - 1 : -1;
  const grid = [0, 0.25, 0.5, 0.75, 1].map((f) => Math.round(f * max));
  // Below 640 a date's label needs about 56 px, or they overlap.
  const every = W < 640 ? Math.max(2, Math.ceil(total / Math.max(Math.floor((right - 40) / 56), 2))) : 2;
  const labels = Array.from({ length: total }, (_, i) => i).filter((i) => i % every === 0 || (W >= 640 && i === total - 1));
  const unit = points ? "points" : "issues";
  const desc = last ? `${cycleName(c)}: ${last.v[0]} ${unit} in scope, ${last.v[1]} started, ${last.v[2]} shipped.` : `${cycleName(c)} has no days yet.`;
  return (
    <svg ref={measure} viewBox={`0 0 ${W} 206`} width="100%" role="img" aria-label={desc} className="block">
      <path d={grid.map((v) => `M40 ${Y(v)} H${right}`).join(" ")} stroke="var(--border)" strokeWidth={1} fill="none" />
      {grid.map((v) => (
        <text key={v} x={30} y={Y(v) + 4} textAnchor="end" fill="var(--muted)" fontSize={11} fontFamily="var(--font-geist-mono), ui-monospace, monospace">
          {v}
        </text>
      ))}
      {c.state === "current" && todayI >= 0 && <rect x={X(todayI)} y={24} width={right - X(todayI)} height={150} fill="var(--ground)" />}
      {pts.length > 0 && (
        <>
          <path d={`${line(2)} L${X(last!.i).toFixed(1)} 174 L${X(pts[0].i).toFixed(1)} 174 Z`} fill="color-mix(in oklab, var(--accent) var(--accent-soft-alpha), transparent)" />
          <path d={step} fill="none" stroke="color-mix(in oklab, var(--text) 60%, transparent)" strokeWidth={2} strokeLinejoin="round" />
          <path d={line(1)} fill="none" stroke="var(--muted)" strokeWidth={2} strokeDasharray="6 4" strokeLinejoin="round" />
          <path d={line(2)} fill="none" stroke="var(--link)" strokeWidth={2} strokeLinejoin="round" />
        </>
      )}
      {c.state === "current" && todayI >= 0 && (
        <>
          <line x1={X(todayI)} x2={X(todayI)} y1={20} y2={178} stroke="var(--link)" strokeWidth={1} strokeDasharray="3 3" />
          <text x={X(todayI)} y={13} textAnchor="middle" fill="var(--link)" fontSize={11}>
            Today
          </text>
        </>
      )}
      {last &&
        [
          // DESIGN.md → Data and numbers: neutral inks, told apart by style; labels in text.
          [0, "in scope", "color-mix(in oklab, var(--text) 60%, transparent)"],
          [1, "started", "var(--muted)"],
          [2, "shipped", "var(--link)"],
        ].map(([k, word, color]) => (
          <g key={word as string}>
            <circle cx={X(last.i)} cy={Y(last.v[k as number])} r={3.5} fill={color as string} />
            <text x={X(last.i) + (W - X(last.i) < 90 ? -9 : 9)} y={Y(last.v[k as number]) + 4} textAnchor={W - X(last.i) < 90 ? "end" : "start"} fill="var(--text)" fontSize={12} fontWeight={500}>
              {last.v[k as number]} {word}
            </text>
          </g>
        ))}
      {labels.map((i) => (
        <text key={i} x={X(i)} y={196} textAnchor="middle" fill={i === todayI && c.state === "current" ? "var(--link)" : "var(--muted)"} fontSize={11}>
          {fmtDay(iso(addDays(day(c.starts_on), i)))}
        </text>
      ))}
    </svg>
  );
}

function Groups({ rows, by, scale, carriedOut }: { rows: CycleIssue[]; by: "status" | "project" | "assignee"; scale: EstimateScale; carriedOut: boolean }) {
  const [closed, setClosed] = useState<string[]>(["shipped", "cancelled"]);
  const sorted = [...rows].sort((a, b) => PRIO[b.priority] - PRIO[a.priority] || a.key.localeCompare(b.key));
  const groups = useMemo(() => {
    if (by === "status") return ORDER.map((s) => ({ id: s, name: STATUS[s].name, icon: <StatusIcon status={s} />, rows: sorted.filter((i) => i.status === s) })).filter((g) => g.rows.length);
    const key = (i: CycleIssue) => (by === "project" ? i.project : (i.assignee?.name ?? ""));
    return [...new Set(sorted.map(key))]
      .sort((a, b) => (a === "" ? 1 : b === "" ? -1 : a.localeCompare(b)))
      .map((k) => ({ id: k || "none", name: k || "Unassigned", icon: by === "assignee" ? <AssigneeDot name={k || null} /> : null, rows: sorted.filter((i) => key(i) === k) }));
  }, [sorted, by]);
  return (
    <div className="overflow-x-auto rounded-[10px] border border-subtle bg-surface">
      <div className="min-w-0 md:min-w-[640px]">
        {groups.map((g, gi) => {
          const open = !closed.includes(g.id);
          const pts = g.rows.reduce((n, i) => n + (i.estimate ?? 0), 0);
          return (
            <section key={g.id} aria-labelledby={`cg-${g.id}`}>
              <div className={cn("flex min-h-10 items-center gap-2.5 bg-[color-mix(in_oklab,var(--surface)_70%,var(--raised))] px-2", gi > 0 && "border-t border-subtle")}>
                <h3 id={`cg-${g.id}`} className="m-0">
                  <button
                    type="button"
                    aria-expanded={open}
                    onClick={() => setClosed((c) => (c.includes(g.id) ? c.filter((x) => x !== g.id) : [...c, g.id]))}
                    className="touch-target flex h-8 items-center gap-2 rounded-md px-2 text-fg"
                  >
                    {open ? <ChevronDown className="h-3 w-3 text-muted" strokeWidth={2.2} aria-hidden="true" /> : <ChevronRight className="h-3 w-3 text-muted" strokeWidth={2.2} aria-hidden="true" />}
                    {g.icon}
                    <span className="text-[13px] font-semibold">{g.name}</span>
                    <span className="font-mono text-xs text-muted">{g.rows.length}</span>
                  </button>
                </h3>
                <span className="flex-1" />
                {scale !== "off" && pts > 0 && <span className="pr-2 font-mono text-xs text-muted">{pts} points</span>}
              </div>
              {open && (
                <ul className="m-0 list-none p-0">
                  {g.rows.map((it) => (
                    <li key={`${it.project_id}-${it.number}`} className="flex min-h-10 items-center gap-2.5 border-t border-subtle/70 py-1.5 pl-4 pr-3.5 md:py-0">
                      <PriorityBars priority={it.priority} />
                      <TypeIcon type={it.type} size={14} />
                      <span className="w-[58px] flex-none font-mono text-xs text-muted">{it.key}</span>
                      <Link href={issueHref(it.project_id, it.number)} className="touch-target flex min-h-7 min-w-0 flex-auto items-center gap-2 text-[13px] font-medium text-fg hover:underline">
                        <span className="min-w-0 truncate">{it.title}</span>
                        {it.priority === "urgent" && <UrgentChip />}
                      </Link>
                      <span className="hidden min-w-0 items-center gap-2 md:flex">
                        {it.carried_from != null && <CarriedChip from={it.carried_from} />}
                        {carriedOut && it.carried_to != null && (
                          <span className="inline-flex h-5 flex-none items-center rounded bg-raised px-1.5 text-[11px] text-fg/70">to Cycle {it.carried_to}</span>
                        )}
                        {it.due_on && <DueChip due={it.due_on} status={it.status} />}
                        <span className="flex min-w-0 max-w-[244px] flex-[0_1_auto] gap-1 overflow-hidden">
                          {it.labels.map((l) => (
                            <LabelChip key={l} name={l} />
                          ))}
                        </span>
                        {scale !== "off" && it.estimate != null && <EstimateBox value={it.estimate} scale={scale} />}
                      </span>
                      <AssigneeDot name={it.assignee?.name} />
                    </li>
                  ))}
                </ul>
              )}
            </section>
          );
        })}
      </div>
    </div>
  );
}

function Shares({ title, rows }: { title: string; rows: { id: string; name: string; total: number; shipped: number; dot?: string | null; href?: string }[] }) {
  if (!rows.length) return null;
  return (
    <section aria-label={title} className={card}>
      <h2 className="px-4 pb-1 pt-3 text-[13px] font-semibold text-fg">{title}</h2>
      <ul className="pb-2 pt-1">
        {rows.map((r) => (
          <li key={r.id || "none"} className="flex min-h-[34px] items-center gap-2.5 px-4 text-[13px]">
            {"dot" in r && <AssigneeDot name={r.dot} />}
            {r.href ? (
              <Link href={r.href} className="min-w-0 flex-auto truncate text-fg hover:underline">
                {r.name}
              </Link>
            ) : (
              <span className={cn("min-w-0 flex-auto truncate", r.name ? "text-fg" : "text-fg/70")}>{r.name || "Unassigned"}</span>
            )}
            <span aria-hidden="true" className="flex h-1.5 w-16 flex-none overflow-hidden rounded-xs bg-subtle">
              <span className="bg-status-running" style={{ width: `${r.total ? (r.shipped * 100) / r.total : 0}%` }} />
            </span>
            <span className="w-[52px] flex-none text-right font-mono text-xs text-fg/70">
              {r.shipped} of {r.total}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

/** The cycles around this one: upcoming, current, then ended, newest first. */
function CycleList({ current }: { current: number }) {
  const list = useInfiniteQuery({
    queryKey: ["cycles", "list"],
    queryFn: ({ pageParam }) => cycleService.list(undefined, pageParam, 5),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next,
  });
  const all = list.data?.pages.flatMap((p) => p.data) ?? [];
  return (
    <section aria-labelledby="cycles-h" className={card}>
      <h2 id="cycles-h" className="px-4 pb-1.5 pt-3 text-[13px] font-semibold text-fg">
        Cycles
      </h2>
      {readFailed(list) && (
        <div className="px-4 pb-3">
          <LoadFailed what="the cycles" onRetry={() => void list.refetch()} />
        </div>
      )}
      <ol className="pb-1.5">
        {all.map((c) => {
          const now = c.number === current;
          const k = c.counts;
          const summary =
            c.state === "upcoming"
              ? k.scope
                ? `${k.scope} ${k.scope === 1 ? "issue" : "issues"} planned${k.points ? ` · ${k.points} points` : ""}`
                : "Nothing planned yet"
              : c.state === "current"
                ? `${k.shipped} of ${k.scope} shipped · ${c.days_left === 0 ? "last day" : `${c.days_left} days left`}`
                : `${k.shipped} of ${k.scope} shipped${k.carried ? ` · ${k.carried} carried over` : ""}${k.cancelled ? ` · ${k.cancelled} cancelled` : ""}`;
          return (
            <li key={c.number} className={cn("border-t border-subtle/70", now && "bg-selected")}>
              <Link href={`/cycles/${c.number}`} aria-current={now ? "page" : undefined} className="flex flex-col gap-0.5 px-4 py-2 text-fg">
                <span className="flex items-baseline gap-2 text-[13px]">
                  <span className="font-medium">{cycleName(c)}</span>
                  <span className="text-xs text-muted">{cycleDates(c)}</span>
                  <span className="flex-1" />
                  <span className={cn("text-xs capitalize", c.state === "current" ? "text-status-building-fg" : "text-muted")}>{c.state}</span>
                </span>
                <span className="text-xs text-fg/70">{summary}</span>
              </Link>
            </li>
          );
        })}
      </ol>
      <NextPageFailed query={list} what="earlier cycles" className="border-t border-subtle px-4 py-2" />
      {list.hasNextPage && (
        <button type="button" onClick={() => list.fetchNextPage()} disabled={list.isFetchingNextPage} className="touch-target block w-full border-t border-subtle px-4 pb-3 pt-2 text-left text-[13px] text-link hover:underline">
          Earlier cycles
        </button>
      )}
    </section>
  );
}

function RenameDialog({ c, open, onOpenChange }: { c: CycleDetail; open: boolean; onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState(c.name);
  const save = useMutation({
    mutationFn: () => cycleService.rename(c.number, name.trim()),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["cycles"] });
      qc.invalidateQueries({ queryKey: ["project-cycles"] });
      onOpenChange(false);
    },
    onError: (e) => toast.error(e instanceof ApiError ? e.message : "Couldn't rename the cycle"),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <DialogHeader className="mb-0">
            <DialogTitle>Name Cycle {c.number}</DialogTitle>
            <DialogDescription>Everyone in the workspace sees it beside the number. Leave it empty to go back to &ldquo;Cycle {c.number}&rdquo;.</DialogDescription>
          </DialogHeader>
          <input aria-label="Name" autoFocus maxLength={60} placeholder="Checkout push" value={name} onChange={(e) => setName(e.target.value)} className={field} />
          <div className="flex justify-end gap-2">
            <button type="button" onClick={() => onOpenChange(false)} className="touch-target h-9 rounded-lg px-3 text-[13px] text-fg/70 hover:text-fg">
              Cancel
            </button>
            <button type="submit" disabled={save.isPending} className={small}>
              Save name
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
