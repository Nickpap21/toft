"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { CalendarDays, RefreshCw } from "lucide-react";
import { DropdownMenuItem, DropdownMenuRadioItem, DropdownMenuSeparator } from "@/components/ui/dropdown-menu";
import { ApiError } from "@/lib/api/client";
import {
  cycleService,
  issueTypeService,
  type CycleRef,
  type CycleSchedule,
  type EstimateScale,
  type IssueStatus,
} from "@/lib/api/issue.service";
import { cn } from "@/lib/utils";
import { field, ghostSm, primarySm, saveBar } from "@/app/projects/_lib/ui";
import { CardHead, Segmented } from "@/components/settings/settings-frame";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";

// Cycles, due dates and estimates (D-90, boards CycleSettings, IssueDates,
// Cycles): the workspace's settings cards, and the chips rows and panels
// show.

const err = (e: unknown, fallback: string) => (e instanceof ApiError ? e.message : fallback);

export const workspaceIssueSettingsQuery = {
  queryKey: ["workspace-issue-settings"],
  queryFn: () => issueTypeService.settings(),
  staleTime: 60_000,
};

// ─── dates ───────────────────────────────────────────────────────────────────

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
export const WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

/** A YYYY-MM-DD as a UTC date. */
export function day(s: string): Date {
  const [y, m, d] = s.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d));
}
export const iso = (d: Date) => d.toISOString().slice(0, 10);
export const addDays = (d: Date, n: number) => new Date(d.getTime() + n * 86_400_000);
/** "Oct 17". */
export const fmtDay = (s: string) => {
  const d = day(s);
  return `${MONTHS[d.getUTCMonth()]} ${d.getUTCDate()}`;
};
/** Today where the browser is, as YYYY-MM-DD. */
export function today(): string {
  const n = new Date();
  return iso(new Date(Date.UTC(n.getFullYear(), n.getMonth(), n.getDate())));
}
const daysBetween = (a: string, b: string) => Math.round((day(b).getTime() - day(a).getTime()) / 86_400_000);

/** How a due date reads: late once past while open, soon two days before. */
export function dueOf(due: string, status: IssueStatus, now = today()): { text: string; tone: "late" | "soon" | "plain" } {
  const open = status !== "shipped" && status !== "cancelled";
  const left = daysBetween(now, due);
  if (open && left < 0) return { text: `${fmtDay(due)} · ${-left === 1 ? "1 day" : `${-left} days`} late`, tone: "late" };
  if (open && left === 0) return { text: "Due today", tone: "soon" };
  if (open && left === 1) return { text: "Due tomorrow", tone: "soon" };
  if (open && left === 2) return { text: fmtDay(due), tone: "soon" };
  return { text: fmtDay(due), tone: "plain" };
}

/** A cycle's name, as "Cycle 14" or its own. */
export const cycleName = (c: { number: number; name: string }) => c.name || `Cycle ${c.number}`;
export const cycleDates = (c: { starts_on: string; ends_on: string }) => `${fmtDay(c.starts_on)} – ${fmtDay(c.ends_on)}`;

// ─── the schedule, as the backend draws it (cycles.go) ───────────────────────

/** The last day of a cycle from start: weeks long, and up to six days longer
 *  so the next one starts on startDay. */
export function cycleEnd(start: Date, weeks: number, startDay: number): Date {
  let end = addDays(start, weeks * 7 - 1);
  while (addDays(end, 1).getUTCDay() !== startDay) end = addDays(end, 1);
  return end;
}
const lastStartDay = (d: Date, wd: number) => addDays(d, -((d.getUTCDay() - wd + 7) % 7));

// ─── chips ───────────────────────────────────────────────────────────────────

const TONE = {
  late: "bg-status-failed-soft text-status-failed-fg",
  soon: "bg-status-pending-soft text-status-pending-fg",
  plain: "bg-raised text-fg/70",
};

/** A row's due date. */
export function DueChip({ due, status, className }: { due: string; status: IssueStatus; className?: string }) {
  const d = dueOf(due, status);
  return (
    <span
      aria-label={`Due: ${d.text}`}
      title={`Due ${fmtDay(due)}`}
      className={cn("inline-flex h-5 flex-none items-center gap-1 whitespace-nowrap rounded px-1.5 text-[11px]", TONE[d.tone], className)}
    >
      <CalendarDays className="h-[11px] w-[11px]" aria-hidden="true" />
      {d.text}
    </span>
  );
}

/** "from Cycle 13": an issue the cycle before carried in. */
export function CarriedChip({ from }: { from: number }) {
  return (
    <span
      title={`Unfinished when Cycle ${from} ended`}
      className="inline-flex h-5 flex-none items-center gap-1 whitespace-nowrap rounded bg-raised px-1.5 text-[11px] text-fg/70"
    >
      <RefreshCw className="h-[11px] w-[11px]" aria-hidden="true" />
      from Cycle {from}
    </span>
  );
}

const TSHIRT: Record<number, string> = { 1: "XS", 2: "S", 3: "M", 5: "L", 8: "XL" };
export const SCALES: Record<Exclude<EstimateScale, "off">, number[]> = {
  fibonacci: [1, 2, 3, 5, 8, 13],
  linear: [1, 2, 3, 4, 5],
  tshirt: [1, 2, 3, 5, 8],
};
/** An estimate as its scale shows it: points, or the nearest T-shirt size. */
export function estimateText(n: number, scale: EstimateScale): string {
  if (scale !== "tshirt") return String(n);
  const near = SCALES.tshirt.reduce((a, b) => (Math.abs(b - n) < Math.abs(a - n) ? b : a));
  return TSHIRT[near];
}
export const pointsText = (n: number, scale: EstimateScale) => (scale === "tshirt" ? estimateText(n, scale) : n === 1 ? "1 point" : `${n} points`);

/** A row's estimate, in a small box. */
export function EstimateBox({ value, scale }: { value: number; scale: EstimateScale }) {
  return (
    <span
      role="img"
      aria-label={`Estimate ${pointsText(value, scale)}`}
      className="flex h-5 min-w-[22px] flex-none items-center justify-center rounded-sm border border-subtle px-1 font-mono text-[11px] text-fg/80"
    >
      {estimateText(value, scale)}
    </span>
  );
}

/** The cycle an issue is in, as a property's value. */
export function CycleText({ cycle }: { cycle: CycleRef }) {
  const left = cycle.state === "current" ? daysBetween(today(), cycle.ends_on) : 0;
  return (
    <span>
      {cycleName(cycle)}
      <span className="text-muted">
        {" "}
        · {cycle.state === "current" ? (left <= 0 ? "ends today" : `${left} ${left === 1 ? "day" : "days"} left`) : cycle.state === "ended" ? "ended" : cycleDates(cycle)}
      </span>
    </span>
  );
}

// ─── Settings → Workspace (board CycleSettings) ──────────────────────────────

const zones = (): string[] => {
  try {
    return (Intl as unknown as { supportedValuesOf: (k: string) => string[] }).supportedValuesOf("timeZone");
  } catch {
    return ["UTC"];
  }
};
const browserZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

/** Cycles: on or off, their length, start day and time zone, and the next
 *  cycles they make. Owners change them. */
export function CyclesSettingsCard({ isOwner }: { isOwner: boolean }) {
  const qc = useQueryClient();
  const settings = useQuery(workspaceIssueSettingsQuery);
  const current = useQuery({ queryKey: ["cycles", "current"], queryFn: () => cycleService.list("current", undefined, 1) });
  const [edit, setEdit] = useState<CycleSchedule | null>(null);
  const saved = settings.data?.cycles;
  const draft = edit ?? (saved ? { ...saved, time_zone: saved.enabled ? saved.time_zone : browserZone() } : null);
  const dirty =
    !!draft && !!saved && (draft.enabled !== saved.enabled || (draft.enabled && (draft.weeks !== saved.weeks || draft.start_day !== saved.start_day || draft.time_zone !== saved.time_zone)));
  const save = useMutation({
    mutationFn: (c: CycleSchedule) => issueTypeService.saveSettings({ cycles: c }),
    onSuccess: (out) => {
      qc.setQueryData(workspaceIssueSettingsQuery.queryKey, out);
      qc.invalidateQueries({ queryKey: ["cycles"] });
      qc.invalidateQueries({ queryKey: ["project-cycles"] });
      setEdit(null);
      toast.success(out.cycles.enabled ? "Cycles saved" : "Cycles turned off");
    },
    onError: (e) => toast.error(err(e, "Couldn't save the cycles")),
  });
  const allZones = useMemo(() => zones(), []);
  const cur = current.data?.data[0];
  const preview = (() => {
    if (!draft) return [];
    const out: { name: string; state: string; dates: string }[] = [];
    let start: Date;
    let n: number;
    if (cur && saved?.enabled) {
      out.push({ name: cycleName(cur), state: "current", dates: cycleDates(cur) });
      start = addDays(day(cur.ends_on), 1);
      n = cur.number + 1;
    } else {
      start = lastStartDay(day(today()), draft.start_day);
      n = (cur?.number ?? 0) + 1;
    }
    while (out.length < 3) {
      const end = cycleEnd(start, draft.weeks, draft.start_day);
      out.push({ name: `Cycle ${n}`, state: out.length === 0 ? "current" : out.length === 1 ? "next" : "then", dates: `${fmtDay(iso(start))} – ${fmtDay(iso(end))}` });
      start = addDays(end, 1);
      n++;
    }
    return out;
  })();

  if (!draft) {
    if (!readFailed(settings)) return null;
    return (
      <section aria-labelledby="cyc-h" className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
        <CardHead id="cyc-h" title="Cycles" sum="fixed rounds of work for the whole workspace" />
        <div className="p-4">
          <LoadFailed what="the cycles" onRetry={() => void settings.refetch()} />
        </div>
      </section>
    );
  }
  const on = draft.enabled;
  const set = (patch: Partial<CycleSchedule>) => setEdit({ ...draft, ...patch });
  const changedSchedule = !!saved?.enabled && on && (draft.weeks !== saved.weeks || draft.start_day !== saved.start_day);
  return (
    <section aria-labelledby="cyc-h" className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
      <CardHead id="cyc-h" title="Cycles" sum="fixed rounds of work for the whole workspace">
        <span className="inline-flex min-h-8 items-center gap-2.5 text-[13px] text-muted">
          <span aria-hidden="true">{on ? "On" : "Off"}</span>
          <button
            type="button"
            role="switch"
            aria-checked={on}
            aria-label="Use cycles"
            disabled={!isOwner}
            onClick={() => set({ enabled: !on })}
            className={cn(
              "relative h-5 w-9 rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-60 pointer-coarse:before:absolute pointer-coarse:before:-inset-x-1 pointer-coarse:before:-inset-y-3 pointer-coarse:before:content-['']",
              on ? "bg-accent" : "bg-strong",
            )}
          >
            <span className={cn("absolute top-0.5 h-4 w-4 rounded-full bg-on-accent transition-[translate] left-0.5", on && "translate-x-4")} />
          </button>
        </span>
      </CardHead>

      {on ? (
        <div className="flex flex-col gap-3.5 p-4">
          <div className="grid grid-cols-3 gap-3.5 max-sm:grid-cols-1">
            <div className="flex min-w-0 flex-col gap-1.5">
              <span className="text-[13px] font-medium text-fg">Length</span>
              <Segmented
                label="Cycle length"
                options={[1, 2, 3, 4].map((w) => ({ value: w, label: `${w} wk` }))}
                value={draft.weeks}
                onChange={(w) => set({ weeks: w })}
                disabled={!isOwner}
              />
            </div>
            <div className="flex min-w-0 flex-col gap-1.5">
              <label htmlFor="cyc-start" className="text-[13px] font-medium text-fg">
                Starts on
              </label>
              <select id="cyc-start" value={draft.start_day} disabled={!isOwner} onChange={(e) => set({ start_day: Number(e.target.value) })} className={field}>
                {[1, 2, 3, 4, 5, 6, 0].map((d) => (
                  <option key={d} value={d}>
                    {WEEKDAYS[d]}
                  </option>
                ))}
              </select>
            </div>
            <div className="flex min-w-0 flex-col gap-1.5">
              <label htmlFor="cyc-tz" className="text-[13px] font-medium text-fg">
                Time zone
              </label>
              <select
                id="cyc-tz"
                value={draft.time_zone}
                disabled={!isOwner}
                onChange={(e) => set({ time_zone: e.target.value })}
                aria-describedby="cyc-tz-help"
                className={field}
              >
                {(allZones.includes(draft.time_zone) ? allZones : [draft.time_zone, ...allZones]).map((z) => (
                  <option key={z} value={z}>
                    {z}
                  </option>
                ))}
              </select>
              <span id="cyc-tz-help" className="text-xs text-muted">
                A cycle starts and ends at midnight here.
              </span>
            </div>
          </div>
          <dl aria-label="Next cycles" className="flex flex-col text-[13px]">
            {preview.map((p) => (
              <div key={p.name} className="flex items-center justify-between gap-3 py-1.5">
                <dt className="flex items-center gap-1.5 text-fg">
                  {p.name}
                  {p.state === "current" ? (
                    <span className="inline-flex h-[18px] items-center rounded-full bg-status-building-soft px-2 text-xs font-medium text-status-building-fg">current</span>
                  ) : p.state === "next" ? (
                    <span className="text-muted">· next</span>
                  ) : null}
                </dt>
                <dd className="font-mono text-xs text-fg">{p.dates}</dd>
              </div>
            ))}
          </dl>
          <p className={cn("text-xs leading-[18px]", changedSchedule ? "text-status-pending-fg" : "text-muted")}>
            {changedSchedule
              ? "The cycles after the current one follow the new schedule once you save; issues planned in them keep their cycle."
              : saved?.enabled
                ? "A new length or start day applies from the next cycle; the current one keeps its dates."
                : "The first cycle starts on this week’s start day, so one is running as soon as you save."}
          </p>
        </div>
      ) : (
        <p className="p-4 text-[13px] text-fg/70">
          Off. Issues keep the cycle they were in, and past cycles stay readable; no new cycle starts, and the cycle filter and Cycles leave the sidebar.
        </p>
      )}

      {isOwner ? (
        dirty && (
          <div className={saveBar}>
            <span>{draft.enabled !== saved?.enabled ? (on ? "Unsaved: cycles turned on" : "Unsaved: cycles turned off") : "Unsaved changes to the schedule"}</span>
            <span className="flex gap-1">
              <button type="button" onClick={() => setEdit(null)} className={ghostSm}>
                Cancel
              </button>
              <button type="button" disabled={save.isPending} onClick={() => save.mutate(draft)} className={primarySm}>
                {save.isPending ? "Saving…" : "Save"}
              </button>
            </span>
          </div>
        )
      ) : (
        <p className={saveBar}>Owners change the cycles.</p>
      )}
    </section>
  );
}

const SCALE_ROWS: { id: EstimateScale; name: string; values: string }[] = [
  { id: "off", name: "Off", values: "no points" },
  { id: "fibonacci", name: "Fibonacci", values: "1 2 3 5 8 13" },
  { id: "linear", name: "Linear", values: "1 2 3 4 5" },
  { id: "tshirt", name: "T-shirt", values: "XS S M L XL" },
];

/** Estimates: the workspace's scale. Owners change it. */
export function EstimatesSettingsCard({ isOwner }: { isOwner: boolean }) {
  const qc = useQueryClient();
  const settings = useQuery(workspaceIssueSettingsQuery);
  const [scale, setScale] = useState<EstimateScale | null>(null);
  const saved = settings.data?.estimates;
  const value = scale ?? saved ?? "off";
  const dirty = !!saved && value !== saved;
  const save = useMutation({
    mutationFn: () => issueTypeService.saveSettings({ estimates: value }),
    onSuccess: (out) => {
      qc.setQueryData(workspaceIssueSettingsQuery.queryKey, out);
      qc.invalidateQueries({ queryKey: ["project-cycles"] });
      setScale(null);
      toast.success("Estimates saved");
    },
    onError: (e) => toast.error(err(e, "Couldn't save the estimates")),
  });
  if (!saved) {
    if (!readFailed(settings)) return null;
    return (
      <section aria-labelledby="est-h" className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
        <CardHead id="est-h" title="Estimates" sum="points on issues, summed in cycles, epics and releases" />
        <div className="p-4">
          <LoadFailed what="the estimates" onRetry={() => void settings.refetch()} />
        </div>
      </section>
    );
  }
  const row = SCALE_ROWS.find((r) => r.id === value)!;
  return (
    <section aria-labelledby="est-h" className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
      <CardHead id="est-h" title="Estimates" sum="points on issues, summed in cycles, epics and releases" />
      <div className="flex flex-col gap-1.5 p-4">
        <span className="text-[13px] font-medium text-fg">Scale</span>
        <Segmented label="Estimate scale" options={SCALE_ROWS.map((r) => ({ value: r.id, label: r.name }))} value={value} onChange={setScale} disabled={!isOwner} />
        <p className="text-xs leading-[18px] text-muted">
          {value === "off" ? (
            "Off hides points everywhere."
          ) : (
            <>
              {row.name}: <span className="font-mono">{row.values}</span>
              {value === "tshirt" && (
                <>
                  , read as <span className="font-mono">1 2 3 5 8</span>
                </>
              )}
              . A new scale keeps each issue&rsquo;s points; a value off the scale shows as the nearest one until someone picks again.
            </>
          )}
        </p>
      </div>
      {isOwner ? (
        dirty && (
          <div className={saveBar}>
            <span>Unsaved change to the scale</span>
            <span className="flex gap-1">
              <button type="button" onClick={() => setScale(null)} className={ghostSm}>
                Cancel
              </button>
              <button type="button" disabled={save.isPending} onClick={() => save.mutate()} className={primarySm}>
                {save.isPending ? "Saving…" : "Save"}
              </button>
            </span>
          </div>
        )
      ) : (
        <p className={saveBar}>Owners change the scale.</p>
      )}
    </section>
  );
}

// ─── pickers: New issue's pills and the properties' rows (board IssueDates) ──

export const projectCyclesQuery = (projectId: string, fetcher: () => Promise<import("@/lib/api/issue.service").ProjectCycles>) => ({
  queryKey: ["project-cycles", projectId],
  queryFn: fetcher,
  staleTime: 60_000,
});

/** The cycles an issue can go in: the current one and those after it,
 *  soonest first; 0 is none. */
export function CycleItems({ cycles }: { cycles: { number: number; name: string; state: string; starts_on: string; ends_on: string }[] }) {
  return (
    <>
      <DropdownMenuRadioItem value="0">No cycle</DropdownMenuRadioItem>
      {cycles
        .filter((c) => c.state !== "ended")
        .sort((a, b) => a.number - b.number)
        .map((c) => (
          <DropdownMenuRadioItem key={c.number} value={String(c.number)}>
            {cycleName(c)}
            <span className="ml-auto pl-3 text-xs text-muted">{c.state === "current" ? "current" : cycleDates(c)}</span>
          </DropdownMenuRadioItem>
        ))}
    </>
  );
}

/** The due date's shortcuts, a date field and Clear (board IssueDates). */
export function DueItems({ value, onChange, cycleEnds }: { value: string; onChange: (d: string) => void; cycleEnds?: { name: string; ends_on: string } }) {
  const t = day(today());
  const friday = addDays(t, (5 - t.getUTCDay() + 7) % 7 || 7);
  const monday = addDays(t, (1 - t.getUTCDay() + 7) % 7 || 7);
  const wd = (d: Date) => WEEKDAYS[d.getUTCDay()].slice(0, 3);
  const items: [string, Date][] = [
    ["Today", t],
    ["Tomorrow", addDays(t, 1)],
    ["Friday", friday],
    ...(cycleEnds ? ([[`End of ${cycleEnds.name}`, day(cycleEnds.ends_on)]] as [string, Date][]) : []),
    ["Next Monday", monday],
  ];
  items.sort((a, b) => a[1].getTime() - b[1].getTime());
  return (
    <>
      {items.map(([name, d]) => (
        <DropdownMenuItem key={name} onSelect={() => onChange(iso(d))} className={cn(iso(d) === value && "bg-accent-soft")}>
          <span className="flex-1">{name}</span>
          <span className="text-xs text-muted">
            {wd(d)}, {fmtDay(iso(d))}
          </span>
          <span className="w-3.5 text-link">{iso(d) === value ? "✓" : ""}</span>
        </DropdownMenuItem>
      ))}
      <DropdownMenuSeparator />
      <label className="flex items-center gap-2 px-2 py-1.5 text-[13px] text-fg/70">
        <span className="flex-1">On a date</span>
        <input
          type="date"
          value={value}
          onKeyDown={(e) => e.stopPropagation()}
          onChange={(e) => onChange(e.target.value)}
          className="h-[30px] rounded-md border border-strong bg-ground px-2 text-[13px] text-fg [color-scheme:dark] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
        />
      </label>
      {value && <DropdownMenuItem onSelect={() => onChange("")}>Clear due date</DropdownMenuItem>}
    </>
  );
}

/** The scale's values, and None. */
export function EstimateItems({ scale }: { scale: EstimateScale }) {
  if (scale === "off") return null;
  return (
    <>
      <DropdownMenuRadioItem value="0">No estimate</DropdownMenuRadioItem>
      {SCALES[scale].map((n) => (
        <DropdownMenuRadioItem key={n} value={String(n)}>
          {pointsText(n, scale)}
          {scale === "tshirt" && <span className="ml-auto pl-3 text-xs text-muted">{n === 1 ? "1 point" : `${n} points`}</span>}
        </DropdownMenuRadioItem>
      ))}
    </>
  );
}
