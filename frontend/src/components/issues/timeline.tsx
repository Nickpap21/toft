"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { useMutation, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, CalendarDays, ChevronDown, ChevronRight, RefreshCw } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { issueService, type EstimateScale, type IssueFilters, type IssueTimelineEntry } from "@/lib/api/issue.service";
import { ReadFailed } from "@/components/shell/unreachable";
import { cn } from "@/lib/utils";
import { field, small } from "@/app/projects/_lib/ui";
import { addDays, cycleName, day, dueOf, fmtDay, iso, pointsText, today } from "./cycles";
import { TypeIcon } from "./deep";
import { STATUS } from "./meta";
import { useIssueRefresh } from "./issue-detail";

// The Timeline (board Timeline, D-90): a project's releases and epics with
// their issues, then its dated or started issues outside them; the cycles
// along the top, today, and the blocks between them. Owners and developers
// drag a bar to move it or its ends to change one date; Edit dates does the
// same from the keyboard.

export type Zoom = "weeks" | "months" | "quarters";
const ZOOM: Record<Zoom, { px: number; days: number }> = {
  weeks: { px: 17, days: 49 },
  months: { px: 5, days: 182 },
  quarters: { px: 2.5, days: 365 },
};
const ROW = 40;
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

const dayOf = (t: string | null | undefined) => (t ? t.slice(0, 10) : null);
const diff = (a: string, b: string) => Math.round((day(b).getTime() - day(a).getTime()) / 86_400_000);
const minS = (xs: (string | null)[]) => xs.filter(Boolean).sort()[0] ?? null;
const maxS = (xs: (string | null)[]) => xs.filter(Boolean).sort().at(-1) ?? null;
const barId = (key: string) => `tl-bar-${key}`;

type Bar = { start: string | null; end: string | null; own: boolean };
type Row =
  | { kind: "group"; title: string }
  | { kind: "item"; it: IssueTimelineEntry; depth: number; bar: Bar; children: number; open: boolean };

/** Where an item sits: its own dates, else when it started and shipped; a
 *  container without dates spans its children. */
function barOf(it: IssueTimelineEntry, kids: IssueTimelineEntry[], all: Map<string, IssueTimelineEntry[]>): Bar {
  const start = it.start_on ?? dayOf(it.started_at);
  const end = it.due_on ?? dayOf(it.shipped_at);
  if (it.type.kind !== "container" || (start && end)) return { start, end, own: !!(it.start_on || it.due_on) };
  const spans = kids.map((k) => barOf(k, all.get(k.key) ?? [], all));
  return { start: start ?? minS(spans.map((b) => b.start ?? b.end)), end: end ?? maxS(spans.map((b) => b.end ?? b.start)), own: false };
}

export function TimelineView({
  projectId,
  filters,
  canWrite,
  scale,
  zoom,
  deps,
}: {
  projectId: string;
  filters: IssueFilters;
  canWrite: boolean;
  scale: EstimateScale;
  zoom: Zoom;
  deps: boolean;
}) {
  const now = today();
  const z = ZOOM[zoom];
  // The window opens on the Monday a week before today.
  const from = useMemo(() => {
    const d = addDays(day(now), -7);
    return iso(addDays(d, -((d.getUTCDay() + 6) % 7)));
  }, [now]);
  const to = iso(addDays(day(from), z.days - 1));
  const width = Math.round(z.days * z.px);
  const x = (d: string) => diff(from, d) * z.px;

  const svc = issueService(projectId);
  const q = { from, assignee: filters.assignee, label: filters.label, type: filters.type };
  const data = useQuery({ queryKey: ["issue-timeline", projectId, q], queryFn: () => svc.issueTimeline(q) });
  const refresh = useIssueRefresh(projectId);
  const [closed, setClosed] = useState<string[]>([]);
  const [picked, setPicked] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const save = useMutation({
    mutationFn: ({ n, start_on, due_on }: { n: number; start_on?: string; due_on?: string }) => svc.update(n, { start_on, due_on }),
    onSuccess: (_, v) => {
      refresh(v.n);
      data.refetch();
    },
    onError: (e) => toast.error(e instanceof ApiError ? e.message : "Couldn't change the dates"),
  });

  const rows: Row[] = useMemo(() => {
    const items = data.data?.items ?? [];
    const keys = new Set(items.map((i) => i.key));
    const kids = new Map<string, IssueTimelineEntry[]>();
    for (const i of items) if (i.parent && keys.has(i.parent)) kids.set(i.parent, [...(kids.get(i.parent) ?? []), i]);
    const sortBy = (a: IssueTimelineEntry, b: IssueTimelineEntry) => {
      const sa = barOf(a, kids.get(a.key) ?? [], kids).start ?? "9999";
      const sb = barOf(b, kids.get(b.key) ?? [], kids).start ?? "9999";
      return sa.localeCompare(sb) || a.number - b.number;
    };
    const out: Row[] = [];
    const walk = (it: IssueTimelineEntry, depth: number) => {
      const ks = (kids.get(it.key) ?? []).sort(sortBy);
      const open = !closed.includes(it.key);
      out.push({ kind: "item", it, depth, bar: barOf(it, ks, kids), children: ks.length, open });
      if (open) for (const k of ks) walk(k, depth + 1);
    };
    const roots = items.filter((i) => !i.parent || !keys.has(i.parent));
    const tops = roots.filter((i) => i.type.kind === "container");
    const rest = roots.filter((i) => i.type.kind !== "container");
    tops.sort((a, b) => (a.type.icon === "release" ? 0 : 1) - (b.type.icon === "release" ? 0 : 1) || sortBy(a, b)).forEach((i) => walk(i, 0));
    if (rest.length) {
      out.push({ kind: "group", title: tops.length ? "Not in an epic or release" : "Dated and started issues" });
      rest.sort(sortBy).forEach((i) => walk(i, 0));
    }
    return out;
  }, [data.data, closed]);

  const yOf = (key: string) => rows.findIndex((r) => r.kind === "item" && r.it.key === key) * ROW + ROW / 2;
  const barFor = (key: string) => (rows.find((r) => r.kind === "item" && r.it.key === key) as Extract<Row, { kind: "item" }> | undefined)?.bar;
  const itemOf = (key: string) => (rows.find((r) => r.kind === "item" && r.it.key === key) as Extract<Row, { kind: "item" }> | undefined)?.it;
  // A blocker blocks while it is open; a conflict is the blocked issue
  // starting before its open blocker is due.
  const blocking = (fromKey: string) => {
    const s = itemOf(fromKey)?.status;
    return s !== undefined && s !== "shipped" && s !== "cancelled";
  };
  const conflict = (fromKey: string, toKey: string) => {
    const a = barFor(fromKey);
    const b = barFor(toKey);
    return blocking(fromKey) && !!(a?.end && b?.start && b.start < a.end);
  };
  const sel = rows.find((r) => r.kind === "item" && r.it.key === picked) as Extract<Row, { kind: "item" }> | undefined;
  const blocks = data.data?.blocks ?? [];
  // A popover whose row went (its dates cleared, say) takes focus with it.
  const root = useRef<HTMLDivElement>(null);
  const gone = picked !== null && !sel;
  useEffect(() => {
    if (gone && document.activeElement === document.body) root.current?.focus();
  }, [gone]);

  if (!data.data && data.error) return <ReadFailed heading="h2" error={data.error} onRetry={() => void data.refetch()} />;
  if (!data.data) return <div role="status" aria-label="Loading" className="h-80 rounded-[10px] bg-surface motion-safe:animate-pulse" />;
  const cycles = (data.data?.cycles ?? []).filter((c) => c.ends_on >= from && c.starts_on <= to);
  const ticks = ticksOf(from, z.days, zoom);
  const title = "w-[150px] md:w-[298px]";
  const left = "left-[150px] md:left-[298px]";

  return (
    <>
      <div ref={root} role="group" tabIndex={-1} aria-label="Timeline" className="overflow-x-auto rounded-[10px] border border-subtle bg-surface">
        <div className="relative w-max min-w-full">
          <div className="flex border-b border-subtle bg-[color-mix(in_oklab,var(--surface)_70%,var(--raised))]">
            <div className={cn(title, "sticky left-0 z-20 flex flex-none items-end border-r border-subtle bg-[color-mix(in_oklab,var(--surface)_70%,var(--raised))] px-3.5 pb-2 text-xs text-muted")}>
              Epics, releases and issues
            </div>
            <div className="flex flex-none flex-col" style={{ width }}>
              <div className="relative h-[30px]">
                {ticks.map((t) => (
                  <span
                    key={t.at}
                    className={cn("absolute top-0 flex h-full items-center border-l border-subtle/70 pl-2 text-xs", t.now ? "font-medium text-link" : "text-fg/70")}
                    style={{ left: x(t.at), width: t.width * z.px }}
                  >
                    {t.label}
                  </span>
                ))}
              </div>
              <div className="relative h-[26px] border-t border-subtle/70 pointer-coarse:h-11">
                {cycles.map((c) => {
                  const s = c.starts_on < from ? from : c.starts_on;
                  const w = (diff(s, c.ends_on) + 1) * z.px - 2;
                  return (
                    <Link
                      key={c.number}
                      href={`/cycles/${c.number}`}
                      title={`${cycleName(c)}: ${fmtDay(c.starts_on)} – ${fmtDay(c.ends_on)}`}
                      className={cn(
                        "absolute bottom-1 top-1 ml-px flex items-center gap-1 overflow-hidden whitespace-nowrap rounded-sm px-2 text-[11px] font-medium pointer-coarse:bottom-0 pointer-coarse:top-0",
                        c.state === "current" ? "bg-status-building-soft text-status-building-fg" : "bg-raised text-fg/70",
                      )}
                      style={{ left: x(s), width: Math.max(w, 4) }}
                    >
                      <RefreshCw className="h-[11px] w-[11px] flex-none" aria-hidden="true" />
                      {cycleName(c)}
                      {c.state === "current" && " · current"}
                    </Link>
                  );
                })}
              </div>
            </div>
          </div>

          <div className="relative">
            {now >= from && now <= to && (
              <div data-timeline-today aria-hidden="true" className={cn("pointer-events-none absolute bottom-0 top-0 z-[5] border-l border-link", left)} style={{ marginLeft: x(now) + z.px / 2 }} />
            )}
            <ul className="m-0 list-none p-0">
              {rows.map((r, i) =>
                r.kind === "group" ? (
                  <li key={`g${i}`} className="flex h-10 border-t border-subtle bg-[color-mix(in_oklab,var(--surface)_70%,var(--raised))]">
                    <div className={cn(title, "sticky left-0 z-10 flex flex-none items-center border-r border-subtle bg-[color-mix(in_oklab,var(--surface)_70%,var(--raised))] px-3.5 text-xs font-medium text-muted")}>
                      {r.title}
                    </div>
                    <div className="flex-none" style={{ width }} />
                  </li>
                ) : (
                  <ItemRow
                    key={r.it.key}
                    r={r}
                    first={i === 0}
                    width={width}
                    title={title}
                    x={x}
                    px={z.px}
                    from={from}
                    to={to}
                    now={now}
                    scale={scale}
                    canWrite={canWrite}
                    selected={picked === r.it.key}
                    blocked={blocks.some((b) => b.to === r.it.key && blocking(b.from))}
                    onToggle={() => setClosed((c) => (c.includes(r.it.key) ? c.filter((k) => k !== r.it.key) : [...c, r.it.key]))}
                    onPick={() => {
                      setPicked(picked === r.it.key ? null : r.it.key);
                      setEditing(false);
                    }}
                    onDates={(start_on, due_on) => save.mutate({ n: r.it.number, start_on, due_on })}
                  />
                ),
              )}
            </ul>

            {deps && blocks.length > 0 && (
              <svg aria-hidden="true" className={cn("pointer-events-none absolute top-0 z-[6] overflow-visible", left)} width={width} height={rows.length * ROW}>
                {blocks.map((b) => {
                  const a = barFor(b.from);
                  const t = barFor(b.to);
                  const y1 = yOf(b.from);
                  const y2 = yOf(b.to);
                  if (!a || !t || y1 < 0 || y2 < 0) return null;
                  const x1 = x(a.end ?? a.start ?? from) + z.px;
                  const x2 = x(t.start ?? t.end ?? from);
                  const bad = conflict(b.from, b.to);
                  const mid = y2 > y1 ? y1 + ROW : y1 - ROW;
                  const d = x2 - 10 > x1 + 10 ? `M${x1} ${y1} H${x1 + 10} V${y2} H${x2 - 4}` : `M${x1} ${y1} h10 V${mid} H${x2 - 10} V${y2} H${x2 - 4}`;
                  const color = bad ? "var(--status-failed)" : "var(--muted)";
                  return (
                    <g key={`${b.from}>${b.to}`}>
                      <path d={d} fill="none" stroke={color} strokeWidth={1.5} strokeLinejoin="round" />
                      <path d={`M${x2 - 5} ${y2 - 4} L${x2} ${y2} L${x2 - 5} ${y2 + 4} Z`} fill={color} />
                    </g>
                  );
                })}
              </svg>
            )}

            {sel && (
              <Popover
                key={sel.it.key}
                projectId={projectId}
                r={sel}
                top={yOf(sel.it.key) - ROW / 2 + 6}
                leftPx={x(sel.bar.end ?? sel.bar.start ?? from) + z.px + 22}
                left={left}
                width={width}
                scale={scale}
                canWrite={canWrite}
                editing={editing}
                onEdit={() => setEditing(true)}
                blocks={blocks.filter((b) => b.from === sel.it.key && conflict(b.from, b.to)).map((b) => ({ key: b.to, start: barFor(b.to)?.start ?? "" }))}
                onSave={(start_on, due_on) => {
                  save.mutate({ n: sel.it.number, start_on, due_on });
                  setEditing(false);
                }}
                onClose={() => {
                  setPicked(null);
                  (document.getElementById(barId(sel.it.key)) ?? root.current)?.focus();
                }}
              />
            )}
          </div>

          {(data.data?.undated ?? 0) > 0 && (
            <div className="flex min-h-10 items-center gap-2 border-t border-subtle px-3.5 text-[13px] text-fg/70">
              <CalendarDays className="h-3.5 w-3.5 text-muted" aria-hidden="true" />
              {data.data!.undated === 1 ? "1 open issue has" : `${data.data!.undated} open issues have`} no dates and {data.data!.undated === 1 ? "isn’t" : "aren’t"} under an epic or release.
              <Link href={`/projects/${projectId}/issues?status=backlog,todo`} className="inline-link">
                List them
              </Link>
            </div>
          )}
          {rows.length === 0 && (
            <p className="border-t border-subtle px-4 py-6 text-[13px] text-fg/70">
              Nothing to draw yet: give an issue a due date, or an epic a start and a target, and it shows here.
            </p>
          )}
        </div>
      </div>
      <p className="text-xs text-muted">
        A bar runs from an issue&rsquo;s start date to its due date. Without a start date it starts when the issue moved to In progress; an epic or release spans its
        sub-issues unless it has dates of its own. {canWrite ? "Drag a bar to move it, or its ends to change one date; Edit dates does the same from the keyboard." : ""}
      </p>
    </>
  );
}

/** The header's marks: each week's Monday, or each month's first day. */
function ticksOf(from: string, days: number, zoom: Zoom): { at: string; label: string; width: number; now: boolean }[] {
  const out: { at: string; label: string; width: number; now: boolean }[] = [];
  const end = addDays(day(from), days);
  const t = today();
  if (zoom === "weeks") {
    for (let d = day(from); d < end; d = addDays(d, 7)) {
      const at = iso(d);
      out.push({ at, label: fmtDay(at), width: 7, now: t >= at && t < iso(addDays(d, 7)) });
    }
    return out;
  }
  let d = new Date(Date.UTC(day(from).getUTCFullYear(), day(from).getUTCMonth(), 1));
  while (d < end) {
    const next = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 1));
    const at = iso(d) < from ? from : iso(d);
    out.push({ at, label: MONTHS[d.getUTCMonth()] + (d.getUTCMonth() === 0 ? ` ${d.getUTCFullYear()}` : ""), width: diff(at, iso(next)), now: t >= at && t < iso(next) });
    d = next;
  }
  return out;
}

const LOOK: Record<string, { bar: string; fill: string; fg: string }> = {
  in_progress: { bar: "bg-status-pending/25", fill: "bg-status-pending/30", fg: "text-fg" },
  in_review: { bar: "bg-status-building/25", fill: "bg-status-building/30", fg: "text-fg" },
  todo: { bar: "border border-strong", fill: "bg-raised", fg: "text-fg/80" },
  backlog: { bar: "border border-dashed border-strong", fill: "bg-raised", fg: "text-fg/70" },
  shipped: { bar: "bg-status-running/25", fill: "bg-status-running/25", fg: "text-fg" },
  cancelled: { bar: "border border-subtle", fill: "bg-raised", fg: "text-muted" },
};

function ItemRow({
  r,
  first,
  width,
  title,
  x,
  px,
  from,
  to,
  now,
  scale,
  canWrite,
  selected,
  blocked,
  onToggle,
  onPick,
  onDates,
}: {
  r: Extract<Row, { kind: "item" }>;
  first: boolean;
  width: number;
  title: string;
  x: (d: string) => number;
  px: number;
  from: string;
  to: string;
  now: string;
  scale: EstimateScale;
  canWrite: boolean;
  selected: boolean;
  blocked: boolean;
  onToggle: () => void;
  onPick: () => void;
  onDates: (start?: string, due?: string) => void;
}) {
  const { it, bar, depth } = r;
  const drag = useRef<{ mode: "move" | "start" | "end"; x0: number; moved: boolean } | null>(null);
  const [shift, setShift] = useState<{ mode: "move" | "start" | "end"; days: number } | null>(null);
  const container = it.type.kind === "container";
  const release = it.type.icon === "release";
  const look = LOOK[it.status] ?? LOOK.todo;
  const open = it.status !== "shipped" && it.status !== "cancelled";

  let start = bar.start;
  let end = bar.end;
  if (shift && shift.days) {
    const move = (d: string | null) => (d ? iso(addDays(day(d), shift.days)) : d);
    if (shift.mode !== "end") start = move(start);
    if (shift.mode !== "start") end = move(end);
    if (start && end && start > end) [start, end] = shift.mode === "start" ? [end, end] : [start, start];
  }
  const openEnded = !!start && !end && open;
  const drawEnd = end ?? (openEnded ? now : start);
  const clipped = !!start && start < from;
  const s = start && start < from ? from : start;
  const kind: "bar" | "diamond" | "none" = start && drawEnd ? "bar" : end ? "diamond" : "none";

  let text = "";
  if (container && it.progress) text = `${it.progress.shipped} of ${it.progress.total} shipped`;
  else if (it.due_on && open && dueOf(it.due_on, it.status).tone !== "plain") {
    const d = dueOf(it.due_on, it.status).text;
    text = d.startsWith("Due ") ? `due ${d.slice(4)}` : `due ${d}`;
  }
  else if (openEnded && it.started_at) text = `in progress since ${fmtDay(it.started_at.slice(0, 10))}`;
  else if (it.status === "shipped") text = "shipped";
  else if (scale !== "off" && it.estimate != null) text = pointsText(it.estimate, scale);
  const label = `${it.key}, ${it.title}${start ? `, from ${fmtDay(start)}` : ""}${end ? `, due ${fmtDay(end)}` : ""}`;

  const down = (mode: "move" | "start" | "end", e: React.PointerEvent) => {
    if (!canWrite || e.button !== 0) return;
    e.stopPropagation();
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    drag.current = { mode, x0: e.clientX, moved: false };
  };
  const move = (e: React.PointerEvent) => {
    const d = drag.current;
    if (!d) return;
    const days = Math.round((e.clientX - d.x0) / px);
    if (days) d.moved = true;
    setShift({ mode: d.mode, days });
  };
  // A move sets both dates it shows (a started bar's start becomes its
  // own); an end sets one.
  const up = () => {
    const d = drag.current;
    drag.current = null;
    if (d?.moved && shift?.days) onDates(shift.mode !== "end" && start ? start : undefined, shift.mode !== "start" && end ? end : undefined);
    else if (d && !d.moved) onPick();
    setShift(null);
  };
  // The keyboard's click, or a viewer's mouse, which has no drag for up() to end.
  const click = (e: React.MouseEvent) => (e.detail === 0 || !canWrite) && onPick();

  const handle = "absolute bottom-[5px] top-[5px] w-[3px] rounded-sm bg-fg";
  return (
    <li className={cn("flex h-10", !first && "border-t border-subtle/70", selected && "bg-selected")}>
      <div
        className={cn(
          title,
          "sticky left-0 z-10 flex min-w-0 flex-none items-center gap-2 border-r border-subtle pr-3",
          selected ? "bg-selected" : "bg-surface",
        )}
        style={{ paddingLeft: 14 + depth * 18 }}
      >
        {r.children > 0 && (
          <button
            type="button"
            onClick={onToggle}
            aria-expanded={r.open}
            aria-label={`${r.open ? "Collapse" : "Expand"} ${it.key}`}
            className="relative -ml-1 flex h-[18px] w-[18px] flex-none items-center justify-center rounded text-muted hover:text-fg pointer-coarse:after:absolute pointer-coarse:after:-inset-[13px] pointer-coarse:after:content-['']"
          >
            {r.open ? <ChevronDown className="h-3 w-3" strokeWidth={2.2} aria-hidden="true" /> : <ChevronRight className="h-3 w-3" strokeWidth={2.2} aria-hidden="true" />}
          </button>
        )}
        <TypeIcon type={it.type} size={16} />
        <span className="hidden flex-none font-mono text-xs text-muted md:inline">{it.key}</span>
        <span className={cn("min-w-0 flex-auto truncate text-[13px]", depth === 0 || r.children > 0 ? "font-semibold" : "font-medium")}>{it.title}</span>
        {kind === "none" && <span className="hidden flex-none text-[11px] text-muted md:inline">No dates</span>}
        {blocked && <span className="hidden h-[18px] flex-none items-center rounded px-1.5 text-[11px] text-status-failed-fg md:inline-flex md:bg-status-failed-soft">Blocked</span>}
      </div>
      <div className="relative flex-none" style={{ width }} onPointerMove={move} onPointerUp={up}>
        {kind === "bar" && s && drawEnd && (
          release ? (
            <>
              <span aria-hidden="true" className="absolute top-[18px] h-1 rounded-sm bg-env-teal/40" style={{ left: x(s), width: Math.max(x(drawEnd) - x(s) + px, 2) }} />
              <span aria-hidden="true" className="absolute top-[13px] h-3.5 w-0.5 bg-env-teal" style={{ left: x(s) }} />
              <Diamond it={it} at={x(drawEnd) + px / 2} release text={`${it.title.split(":")[0]} · ${end ? fmtDay(end) : ""}${it.progress ? ` · ${it.progress.shipped} of ${it.progress.total}` : ""}`} onDown={(e) => down("end", e)} onClick={click} label={label} />
            </>
          ) : (
            <button
              id={barId(it.key)}
              type="button"
              aria-label={text ? `${text}, ${label}` : label}
              aria-pressed={selected}
              onPointerDown={(e) => down("move", e)}
              onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && (e.preventDefault(), onPick())}
              onClick={click}
              className={cn(
                "absolute flex touch-none items-center overflow-hidden focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus",
                container ? "top-[9px] h-[22px] rounded-md" : "top-2.5 h-5 rounded-sm",
                "pointer-coarse:top-0 pointer-coarse:h-10",
                look.bar,
                canWrite ? "cursor-grab active:cursor-grabbing" : "cursor-pointer",
                selected && "ring-2 ring-link",
              )}
              style={{ left: x(s), width: Math.max(x(drawEnd) - x(s) + px, 6) }}
            >
              {container && it.progress && it.progress.total > 0 && (
                <span aria-hidden="true" className={cn("self-stretch", look.fill)} style={{ width: `${(it.progress.shipped * 100) / it.progress.total}%` }} />
              )}
              <span className={cn("pointer-events-none absolute right-2 truncate text-left text-[11px] font-medium", look.fg, selected ? "left-3" : "left-2")}>{text}</span>
              {selected && canWrite && (
                <>
                  <span role="presentation" onPointerDown={(e) => down("start", e)} className={cn(handle, "left-0.5 cursor-ew-resize")} />
                  <span role="presentation" onPointerDown={(e) => down("end", e)} className={cn(handle, "right-0.5 cursor-ew-resize")} />
                </>
              )}
            </button>
          )
        )}
        {clipped && kind === "bar" && <span aria-hidden="true" className="absolute left-0.5 top-3.5 text-[11px] text-muted">‹</span>}
        {openEnded && drawEnd && drawEnd <= to && (
          <span aria-hidden="true" className="absolute top-[18px] w-[34px] border-t-2 border-dashed border-status-building" style={{ left: x(drawEnd) + px }} />
        )}
        {kind === "diamond" && end && <Diamond it={it} at={x(end) + px / 2} release={release} text={release ? `${it.title} · ${fmtDay(end)}` : `due ${fmtDay(end)}`} onDown={(e) => down("end", e)} onClick={click} label={`${it.key}, ${it.title}`} />}
      </div>
    </li>
  );
}

function Diamond({
  it,
  at,
  release,
  text,
  onDown,
  onClick,
  label,
}: {
  it: IssueTimelineEntry;
  at: number;
  release?: boolean;
  text: string;
  onDown: (e: React.PointerEvent) => void;
  onClick: (e: React.MouseEvent) => void;
  label: string;
}) {
  return (
    <>
      <button
        id={barId(it.key)}
        type="button"
        aria-label={`${text}, ${label}`}
        onPointerDown={onDown}
        onClick={onClick}
        className={cn(
          "absolute top-[13px] h-3 w-3 rotate-45 touch-none rounded-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:after:absolute pointer-coarse:after:-inset-4 pointer-coarse:after:content-['']",
          release ? "bg-env-teal" : "border-2 border-fg/80 bg-surface",
        )}
        style={{ left: at - 6 }}
      />
      <span className={cn("pointer-events-none absolute top-2.5 whitespace-nowrap text-xs", release ? "font-medium text-env-teal-fg" : "text-fg/70")} style={{ left: at + 12 }}>
        {text}
      </span>
    </>
  );
}

function Popover({
  projectId,
  r,
  top,
  leftPx,
  left,
  width,
  scale,
  canWrite,
  editing,
  onEdit,
  blocks,
  onSave,
  onClose,
}: {
  projectId: string;
  r: Extract<Row, { kind: "item" }>;
  top: number;
  leftPx: number;
  left: string;
  width: number;
  scale: EstimateScale;
  canWrite: boolean;
  editing: boolean;
  onEdit: () => void;
  blocks: { key: string; start: string }[];
  onSave: (start?: string, due?: string) => void;
  onClose: () => void;
}) {
  const { it, bar } = r;
  const [start, setStart] = useState(it.start_on ?? "");
  const [due, setDue] = useState(it.due_on ?? "");
  const st = STATUS[it.status];
  const days = bar.start && bar.end ? Math.round((day(bar.end).getTime() - day(bar.start).getTime()) / 86_400_000) + 1 : 0;
  const parts = [days ? `${days} days` : "", it.assignee?.name ?? "", scale !== "off" && it.estimate != null ? pointsText(it.estimate, scale) : ""].filter(Boolean);
  // Into the popover when it opens or the form closes (the form's own field takes it otherwise).
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!editing) box.current?.focus();
  }, [editing]);
  return (
    <div
      ref={box}
      tabIndex={-1}
      role="dialog"
      aria-labelledby="tl-pop-h"
      onKeyDown={(e) => e.key === "Escape" && onClose()}
      className={cn("absolute z-30 flex w-[316px] flex-col gap-2 rounded-[10px] border border-strong bg-raised px-3.5 pb-3.5 pt-3 shadow-menu focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus", left)}
      style={{ top, marginLeft: Math.min(leftPx, width - 330) }}
    >
      <div className="flex items-center gap-2">
        <span className="font-mono text-xs text-muted">{it.key}</span>
        <span className={cn("inline-flex h-5 items-center rounded-full px-[7px] text-[11px] font-medium", st.chip)}>{st.name}</span>
      </div>
      <h2 id="tl-pop-h" className="text-sm font-semibold leading-5 text-fg">
        {it.title}
      </h2>
      {bar.start || bar.end ? (
        <p className="text-[13px] text-fg/80">
          {bar.start ? fmtDay(bar.start) : "…"} → {bar.end ? fmtDay(bar.end) : "…"}
          {parts.length > 0 && <span className="text-muted"> · {parts.join(" · ")}</span>}
        </p>
      ) : (
        <p className="text-[13px] text-muted">No dates yet.</p>
      )}
      {blocks.map((b) => (
        <p key={b.key} className="flex gap-2 rounded-lg bg-status-failed-soft px-2.5 py-2 text-xs leading-[17px] text-status-failed-fg">
          <AlertTriangle className="mt-0.5 h-[13px] w-[13px] flex-none" aria-hidden="true" />
          <span>
            Blocks {b.key}, which starts {fmtDay(b.start)}, before this is due.
          </span>
        </p>
      ))}
      {editing ? (
        <form
          className="flex flex-col gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            onSave(start === (it.start_on ?? "") ? undefined : start, due === (it.due_on ?? "") ? undefined : due);
          }}
        >
          <label className="flex items-center justify-between gap-2 text-[13px] text-fg/80">
            {it.type.kind === "container" ? "Start" : "Starts"}
            <input type="date" autoFocus value={start} max={due || undefined} onChange={(e) => setStart(e.target.value)} className={cn(field, "h-8 w-[160px] [color-scheme:dark]")} />
          </label>
          <label className="flex items-center justify-between gap-2 text-[13px] text-fg/80">
            {it.type.kind === "container" ? "Target" : "Due"}
            <input type="date" value={due} min={start || undefined} onChange={(e) => setDue(e.target.value)} className={cn(field, "h-8 w-[160px] [color-scheme:dark]")} />
          </label>
          <div className="flex gap-2">
            <button type="submit" className={small}>
              Save dates
            </button>
            <button type="button" onClick={onClose} className="touch-target h-8 rounded-lg px-2.5 text-[13px] text-fg/70 hover:text-fg">
              Cancel
            </button>
          </div>
        </form>
      ) : (
        <div className="flex gap-2">
          <Link href={`/projects/${projectId}/issues/${it.number}`} className={small}>
            Open issue
          </Link>
          {canWrite && (
            <button type="button" onClick={onEdit} className={small}>
              Edit dates
            </button>
          )}
        </div>
      )}
    </div>
  );
}

export const _test = { barOf, ticksOf };
