"use client";

import { Suspense, useEffect, useMemo, useRef, useState } from "react";
import { flushSync } from "react-dom";
import Link from "next/link";
import { useParams, useRouter, useSearchParams } from "next/navigation";
import { useInfiniteQuery, useMutation, useQueries, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { ChevronDown, ChevronRight, MoreHorizontal, Plus, RefreshCw, Search, X } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuSeparator, DropdownMenuSub, DropdownMenuSubContent, DropdownMenuSubTrigger, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { EnvChip } from "@/components/ui/env-chip";
import { ApiError } from "@/lib/api/client";
import { NextPageFailed, Read, ReadFailed } from "@/components/shell/unreachable";
import {
  issueService,
  issueTypesQuery,
  type EstimateScale,
  type Issue,
  type IssueCounts,
  type IssueFilters,
  type IssueQuery,
  type IssueStatus,
  type IssueView,
  type ProjectCycles,
} from "@/lib/api/issue.service";
import { orgService } from "@/lib/api/org.service";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import { environmentsQuery, projectQuery } from "@/lib/queries";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { compactAgo } from "@/components/code/repo";
import { useEnvContext } from "@/components/shell/env-context";
import { IssuePanel, useCanWriteIssues, useIssueRefresh } from "@/components/issues/issue-detail";
import {
  AssigneeDot,
  BOARD_COLUMNS,
  CLOSED,
  Glyph,
  ICON,
  issueHref,
  LabelChip,
  PRIORITY,
  PRIORITY_ORDER,
  PriorityBars,
  STATUS,
  STATUS_ORDER,
  StatusIcon,
  UrgentChip,
  WorkChip,
} from "@/components/issues/meta";
import { NewIssueDialog } from "@/components/issues/new-issue";
import { BlockedChip, ProgressPill, TypeIcon } from "@/components/issues/deep";
import { btnSm, field, ghostSm, primary } from "@/app/projects/_lib/ui";
import { cycleDates, cycleName, DueChip, EstimateBox } from "@/components/issues/cycles";
import { TimelineView, type Zoom } from "@/components/issues/timeline";
import { countQuery, filtersFrom, moreCount, sum } from "@/components/issues/view";

// Issues (boards ReworkIssues, Issues and Timeline; D-81, D-90, D-136): the
// list grouped by status with saved views and the selected issue's panel, the
// board by status, or the Timeline; the cycle filter and its progress.

type Group = "status" | "priority" | "assignee" | "type" | "cycle";
type Order = "priority" | "updated" | "created" | "due";
const OPEN: IssueStatus[] = ["backlog", "todo", "in_progress", "in_review"];
/** Backlog shows three and "Show N more", as drawn; a status loads 50 at a time. */
const BACKLOG_PAGE = 3;
const STATUS_PAGE = 50;

const BUILTIN: { id: string; name: string; filters: IssueFilters }[] = [
  { id: "all", name: "All issues", filters: {} },
  { id: "active", name: "Active", filters: { status: ["in_progress", "in_review", "todo"] } },
  { id: "cycle", name: "Current cycle", filters: { cycle: "current" } },
  { id: "mine", name: "Assigned to me", filters: { assignee: "me" } },
  { id: "unassigned", name: "Unassigned", filters: { assignee: "none", status: OPEN } },
];

export default function IssuesPage() {
  return (
    <Suspense>
      <Issues />
    </Suspense>
  );
}

function Issues() {
  const projectId = useParams<{ id: string }>().id;
  const sp = useSearchParams();
  const router = useRouter();
  const { data: project } = useQuery(projectQuery(projectId));
  const { data: environments } = useQuery(environmentsQuery(projectId));
  const { env } = useEnvContext(projectId, environments);
  const svc = useMemo(() => issueService(projectId), [projectId]);
  const canWrite = useCanWriteIssues();
  // ?new=1 (⌘K's New issue in …) opens New issue.
  const [newOpen, setNewOpen] = useState<IssueStatus | null>(() => (sp.get("new") ? "backlog" : null));
  // A row opened from the keyboard hands the panel the focus: it comes after every row in tab order.
  const [panelFocus, setPanelFocus] = useState(false);
  // Below 768 Search is an icon; its input opens, and stays open while a search is active.
  const [searching, setSearching] = useState(false);
  const searchInput = useRef<HTMLInputElement>(null);
  useDocumentTitle(`Issues · ${project?.name ?? "Project"} · Toft`);

  const layout = sp.get("layout") === "board" ? "board" : sp.get("layout") === "timeline" ? "timeline" : "list";
  const zoom: Zoom = sp.get("zoom") === "months" ? "months" : sp.get("zoom") === "quarters" ? "quarters" : "weeks";
  const board = layout === "board";
  const viewId = sp.get("view") ?? "all";
  const cycles = useQuery({ queryKey: ["project-cycles", projectId], queryFn: () => svc.cycles(), staleTime: 60_000 });
  const views = useQuery({ queryKey: ["issue-views", projectId], queryFn: () => svc.views() });
  const saved = views.data?.find((v) => v.id === viewId);
  const viewFilters: IssueFilters = saved?.filters ?? BUILTIN.find((v) => v.id === viewId)?.filters ?? {};
  const filters = filtersFrom(sp, viewFilters);
  const group: Group = filters.group ?? "status";
  const order: Order = filters.order ?? "priority";
  const selected = Number(sp.get("issue")) || 0;

  const set = (patch: Record<string, string | null>) => {
    const p = new URLSearchParams(sp.toString());
    for (const [k, v] of Object.entries(patch)) {
      if (v === null || v === "") p.delete(k);
      else p.set(k, v);
    }
    const s = p.toString();
    router.replace(`/projects/${projectId}/issues${s ? `?${s}` : ""}`, { scroll: false });
  };
  const pickView = (id: string) => {
    const p = new URLSearchParams();
    if (layout !== "list") p.set("layout", layout);
    if (id !== "all") p.set("view", id);
    if (selected) p.set("issue", String(selected));
    const s = p.toString();
    setSearching(false);
    router.replace(`/projects/${projectId}/issues${s ? `?${s}` : ""}`, { scroll: false });
  };

  // The box follows the URL's search (a tab drops it), unless it's being typed in.
  useEffect(() => {
    if (searchInput.current && document.activeElement !== searchInput.current) searchInput.current.value = filters.q ?? "";
  }, [filters.q]);

  // "C" opens New issue, unless typing or in a menu.
  useEffect(() => {
    if (!canWrite) return;
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      if (e.key.toLowerCase() !== "c" || e.metaKey || e.ctrlKey || e.altKey || t?.closest("input, textarea, select, [contenteditable='true'], [role='dialog'], [role='menu']")) return;
      e.preventDefault();
      setNewOpen("backlog");
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [canWrite]);

  const q: IssueQuery = { ...filters, status: filters.status, order };
  const base = countQuery(filters);
  const tabBase = countQuery(filtersFrom(sp, {}));
  const useCycles = !!cycles.data?.enabled;
  const scale: EstimateScale = cycles.data?.estimates ?? "off";
  const counts = useQuery({ queryKey: ["issue-counts", projectId, base], queryFn: () => svc.counts(base) });
  // All issues and Active: not this tab's own filters, but a cycle chip and a search still reach them.
  const tabCounts = useQuery({ queryKey: ["issue-counts", projectId, tabBase], queryFn: () => svc.counts(tabBase) });

  return (
    <>
      <div className="flex flex-col gap-3">
        <div className="flex min-h-5 flex-wrap items-center gap-1.5 text-[13px] text-muted">
          {env && <EnvChip env={env} size="md" className="hidden md:max-[1199px]:inline-flex" />}
          <span>{project?.name}</span>
        </div>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 flex-wrap items-center gap-3">
            <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">Issues</h1>
            <span role="group" aria-label="View" className="inline-flex gap-0.5 rounded-lg border border-subtle p-0.5">
              {(["list", "board", "timeline"] as const).map((l) => (
                <button
                  key={l}
                  type="button"
                  aria-pressed={layout === l}
                  onClick={() => set({ layout: l === "list" ? null : l })}
                  className={cn("touch-target h-[26px] rounded-md px-2.5 text-[13px]", layout === l ? "bg-subtle font-medium text-fg" : "text-fg/70 hover:text-fg")}
                >
                  {l === "list" ? "List" : l === "board" ? "Board" : "Timeline"}
                </button>
              ))}
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-2 max-md:w-full">
            <label className={cn("touch-target h-9 w-60 items-center gap-2 rounded-lg border border-strong bg-ground px-2.5 text-muted focus-within:ring-2 focus-within:ring-focus max-md:w-full md:inline-flex", searching || filters.q ? "inline-flex" : "hidden")}>
              <Search className="h-3.5 w-3.5 flex-none" aria-hidden="true" />
              <span className="sr-only">Search issues</span>
              <input
                ref={searchInput}
                type="search"
                placeholder="Search issues"
                defaultValue={filters.q ?? ""}
                onChange={(e) => set({ q: e.target.value || null })}
                onBlur={(e) => !e.target.value && setSearching(false)}
                className="min-w-0 flex-1 bg-transparent text-[13px] text-fg outline-none placeholder:text-muted"
              />
            </label>
            {!searching && !filters.q && (
              <button
                type="button"
                aria-label="Search issues"
                onClick={() => {
                  flushSync(() => setSearching(true));
                  searchInput.current?.focus();
                }}
                className="flex h-9 w-9 flex-none items-center justify-center rounded-lg border border-subtle bg-raised text-fg pointer-coarse:h-11 pointer-coarse:w-11 md:hidden"
              >
                <Search className="h-[17px] w-[17px]" aria-hidden="true" />
              </button>
            )}
            {canWrite && (
              <button type="button" onClick={() => setNewOpen("backlog")} className={cn(primary, "justify-center px-3 max-md:flex-1")}>
                <Plus className="h-[15px] w-[15px]" aria-hidden="true" />
                New issue
                <kbd className="ml-0.5 rounded-sm border border-on-accent/35 px-[5px] font-mono text-[11px] leading-4 text-on-accent pointer-coarse:hidden">C</kbd>
              </button>
            )}
          </div>
        </div>
        {layout === "list" && (
          <ViewsNav projectId={projectId} views={views.data} viewId={viewId} counts={tabCounts.data} filters={filters} onPick={pickView} canWrite={canWrite} cycles={useCycles} />
        )}
        <FilterBar projectId={projectId} filters={filters} set={set} layout={layout} group={group} order={order} cycles={cycles.data} />
      </div>

      {layout === "timeline" ? (
        <TimelineView projectId={projectId} filters={filters} canWrite={canWrite} scale={scale} zoom={zoom} deps={sp.get("deps") !== "off"} />
      ) : board ? (
        <BoardView projectId={projectId} q={q} counts={counts.data} countsFailed={!!counts.error} canWrite={canWrite} onNew={setNewOpen} scale={scale} />
      ) : (
        <div className={cn("grid grid-cols-[minmax(0,1fr)] items-start gap-6", selected > 0 ? "min-[1200px]:grid-cols-[minmax(0,1040px)_minmax(340px,1fr)]" : "min-[1200px]:max-w-[1040px]")}>
          <div className="flex min-w-0 flex-col gap-5 max-sm:gap-4">
            <ListView
              projectId={projectId}
              q={q}
              group={group}
              counts={counts.data}
              countsError={counts.error}
              onRetry={() => void counts.refetch()}
              selected={selected}
              onSelect={(n, keyboard) => {
                setPanelFocus(keyboard);
                set({ issue: n ? String(n) : null });
              }}
              onClear={() => {
                setSearching(false);
                set({ q: null });
              }}
              canWrite={canWrite}
              onNew={setNewOpen}
              viewName={saved?.name ?? BUILTIN.find((v) => v.id === viewId)?.name ?? "All issues"}
              scale={scale}
            />
            <p className="max-w-[760px] text-xs leading-[18px] text-muted">Status follows the work: a pushed branch, an opened pull request and a live deploy each move the linked issue.</p>
          </div>
          {selected > 0 && <IssuePanel key={selected} focus={panelFocus} projectId={projectId} project={project?.name ?? ""} number={selected} onClose={() => {
            // Back to the row that opened it, not to the top of the page.
            document.querySelector<HTMLElement>("a[aria-current='true']")?.focus();
            set({ issue: null });
          }} />}
        </div>
      )}
      <NewIssueDialog
        projectId={projectId}
        open={newOpen !== null}
        status={newOpen ?? "backlog"}
        cycle={filters.cycle ? cycles.data?.cycles.find((c) => cycleMatches(c, filters.cycle!, cycles.data!.cycles))?.number : undefined}
        onOpenChange={(o) => !o && setNewOpen(null)}
      />
    </>
  );
}

// ─── saved views ─────────────────────────────────────────────────────────────

function ViewsNav({
  projectId,
  views,
  viewId,
  counts,
  filters,
  onPick,
  canWrite,
  cycles,
}: {
  projectId: string;
  views?: IssueView[];
  viewId: string;
  counts?: IssueCounts;
  filters: IssueFilters;
  onPick: (id: string) => void;
  canWrite: boolean;
  cycles: boolean;
}) {
  const svc = issueService(projectId);
  const { user } = useAuth();
  const owner = useOrgRole() === "owner";
  const [saving, setSaving] = useState(false);
  const [name, setName] = useState("");
  const refetch = useIssueRefresh(projectId);
  const mine = useQuery({ queryKey: ["issue-counts", projectId, { assignee: "me" }], queryFn: () => svc.counts({ assignee: "me" }) });
  const none = useQuery({ queryKey: ["issue-counts", projectId, { assignee: "none" }], queryFn: () => svc.counts({ assignee: "none" }) });
  const inCycle = useQuery({ queryKey: ["issue-counts", projectId, { cycle: "current" }], queryFn: () => svc.counts({ cycle: "current" }), enabled: cycles });
  const savedCounts = useQueries({
    queries: (views ?? []).map((v) => {
      const vq = countQuery(v.filters);
      return { queryKey: ["issue-counts", projectId, vq], queryFn: () => svc.counts(vq) };
    }),
  });
  const save = useMutation({
    mutationFn: () => svc.saveView(name, filters),
    onSuccess: (v) => {
      setSaving(false);
      setName("");
      refetch();
      onPick(v.id);
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn't save the view"),
  });
  const remove = useMutation({
    mutationFn: (id: string) => svc.deleteView(id),
    onSuccess: () => onPick("all"),
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn't delete the view"),
  });
  const tab = (active: boolean) =>
    cn(
      "touch-target -mb-px inline-flex h-[38px] items-center gap-1.5 whitespace-nowrap border-b-2 px-3 text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus",
      active ? "border-link text-fg" : "border-transparent text-fg/70 hover:text-fg",
    );
  const builtinCount: Record<string, number | undefined> = {
    all: sum(counts),
    active: sum(counts, ["in_progress", "in_review", "todo"]),
    mine: sum(mine.data),
    unassigned: sum(none.data, OPEN),
    cycle: sum(inCycle.data),
  };
  return (
    <nav aria-label="Saved views" className="flex gap-1 overflow-x-auto border-b border-subtle">
      {BUILTIN.filter((v) => cycles || v.id !== "cycle").map((v) => (
        <button key={v.id} type="button" aria-pressed={viewId === v.id} onClick={() => onPick(v.id)} className={tab(viewId === v.id)}>
          {v.name}
          <span className="font-mono text-xs text-muted">{builtinCount[v.id] ?? ""}</span>
        </button>
      ))}
      {(views ?? []).map((v, i) => (
        <span key={v.id} className="inline-flex flex-none items-center">
          <button type="button" aria-pressed={viewId === v.id} onClick={() => onPick(v.id)} className={tab(viewId === v.id)}>
            {v.name}
            <span className="font-mono text-xs text-muted">{sum(savedCounts[i]?.data, v.filters.status as IssueStatus[] | undefined) ?? ""}</span>
            {v.owner && <span className="text-xs font-normal text-muted">· {v.owner.id === user?.id ? "you" : v.owner.name}</span>}
          </button>
          {viewId === v.id && canWrite && (v.owner?.id === user?.id || owner) && (
            <DropdownMenu>
              <DropdownMenuTrigger aria-label={`${v.name}: more`} className="flex h-[38px] w-6 items-center justify-center rounded-md text-muted hover:text-fg pointer-coarse:h-11 pointer-coarse:w-11">
                <MoreHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start">
                <DropdownMenuItem onSelect={() => remove.mutate(v.id)}>Delete this view</DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </span>
      ))}
      {canWrite && (
        <button type="button" onClick={() => setSaving(true)} className={cn(tab(false), "text-muted max-md:hidden")}>
          <Plus className="h-[13px] w-[13px]" aria-hidden="true" />
          Save view
        </button>
      )}
      <Dialog open={saving} onOpenChange={setSaving}>
        <DialogContent>
          <form
            className="flex flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault();
              if (name.trim()) save.mutate();
            }}
          >
            <DialogHeader className="mb-0">
              <DialogTitle>Save view</DialogTitle>
              <DialogDescription>Everyone in the project sees it, with your name. It keeps the filters, grouping and order you have now.</DialogDescription>
            </DialogHeader>
            <input aria-label="Name" autoFocus required maxLength={60} placeholder="Payments" value={name} onChange={(e) => setName(e.target.value)} className={field} />
            <div className="flex justify-end gap-2">
              <button type="button" onClick={() => setSaving(false)} className="touch-target h-9 rounded-lg px-3 text-[13px] text-fg/70 hover:text-fg">
                Cancel
              </button>
              <button type="submit" disabled={!name.trim() || save.isPending} className={primary}>
                Save view
              </button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </nav>
  );
}

// ─── filters ─────────────────────────────────────────────────────────────────

const dashed = "touch-target h-[30px] rounded-md border border-dashed border-strong px-2.5 text-xs font-medium text-fg/70 hover:text-fg";

/** Whether a project cycle is the one a cycle filter names. */
function cycleMatches(c: ProjectCycles["cycles"][number], key: string, all: ProjectCycles["cycles"]): boolean {
  if (key === "current") return c.state === "current";
  if (key === "next") {
    const upcoming = all.filter((x) => x.state === "upcoming").sort((a, b) => a.number - b.number);
    return upcoming[0]?.number === c.number;
  }
  return String(c.number) === key;
}

function FilterBar({
  projectId,
  filters,
  set,
  layout,
  group,
  order,
  cycles,
}: {
  projectId: string;
  filters: IssueFilters;
  set: (p: Record<string, string | null>) => void;
  layout: "list" | "board" | "timeline";
  group: Group;
  order: Order;
  cycles?: ProjectCycles;
}) {
  const { user } = useAuth();
  const labels = useQuery({ queryKey: ["issue-labels", projectId], queryFn: () => issueService(projectId).labels() });
  const members = useQuery({ queryKey: ["org-members"], queryFn: () => orgService.listMembers() });
  const chosen = filters.label ?? [];
  const services = (labels.data ?? []).filter((l) => l.service);
  const plain = (labels.data ?? []).filter((l) => !l.service);
  const toggleLabel = (n: string) => set({ label: (chosen.includes(n) ? chosen.filter((l) => l !== n) : [...chosen, n]).join(",") || null });
  const prios = filters.priority ?? [];
  const who =
    filters.assignee === "me"
      ? "me"
      : filters.assignee === "none"
        ? "nobody"
        : (members.data?.find((m) => m.user_id === filters.assignee)?.user_name ?? (filters.assignee ? "someone" : ""));
  const chip = (title: string, value: string, clear: () => void) => (
    <span key={title + value} className="touch-target inline-flex h-[30px] max-w-full items-center overflow-hidden rounded-md border border-strong bg-surface text-[13px]">
      <span className="flex-none pl-2.5 pr-1 text-muted">{title} is</span>
      <span title={value} className="min-w-0 truncate pr-1.5 text-fg">{value}</span>
      <button type="button" onClick={clear} aria-label={`Remove ${title.toLowerCase()} filter`} className="flex h-7 w-7 flex-none items-center justify-center border-l border-subtle text-muted hover:text-fg pointer-coarse:h-11 pointer-coarse:w-11">
        <X className="h-3 w-3" aria-hidden="true" />
      </button>
    </span>
  );
  const chosenServices = chosen.filter((l) => services.some((s) => s.name === l));
  const chosenLabels = chosen.filter((l) => !chosenServices.includes(l));
  const groupName = { status: "Status", priority: "Priority", assignee: "Assignee", type: "Type", cycle: "Cycle" }[group];
  const orderName = { priority: "priority", updated: "updated", created: "created", due: "due date" }[order];
  const all = cycles?.cycles ?? [];
  const picked = filters.cycle && filters.cycle !== "none" ? all.find((c) => cycleMatches(c, filters.cycle!, all)) : undefined;
  const types = useQuery(issueTypesQuery);
  const chosenTypes = filters.type ?? [];
  const toggleType = (id: string) => set({ type: (chosenTypes.includes(id) ? chosenTypes.filter((t) => t !== id) : [...chosenTypes, id]).join(",") || null });
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        {cycles?.enabled && layout !== "timeline" && (
          <DropdownMenu>
            <DropdownMenuTrigger
              className={
                filters.cycle
                  ? cn(btnSm, "max-w-full")
                  : cn(dashed, "inline-flex items-center gap-1.5")
              }
            >
              <RefreshCw className="h-[13px] w-[13px]" aria-hidden="true" />
              {picked ? (
                <>
                  <span className="min-w-0 truncate">{cycleName(picked)}</span> <span className="flex-none text-muted">{cycleDates(picked)}</span>
                </>
              ) : filters.cycle === "none" ? (
                "No cycle"
              ) : (
                "Cycle"
              )}
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="max-h-80 overflow-y-auto">
              <DropdownMenuRadioGroup value={filters.cycle ?? ""} onValueChange={(v) => set({ cycle: v || null })}>
                <DropdownMenuRadioItem value="">Any cycle</DropdownMenuRadioItem>
                {[...all]
                  .sort((a, b) => a.number - b.number)
                  .filter((c) => c.state !== "ended")
                  .map((c) => (
                    <DropdownMenuRadioItem key={c.number} value={c.state === "current" ? "current" : String(c.number)}>
                      {cycleName(c)}
                      <span className="ml-auto pl-3 text-xs text-muted">{c.state === "current" ? "current" : cycleDates(c)}</span>
                    </DropdownMenuRadioItem>
                  ))}
                {all.some((c) => c.state === "ended") && <DropdownMenuSeparator />}
                {all
                  .filter((c) => c.state === "ended")
                  .map((c) => (
                    <DropdownMenuRadioItem key={c.number} value={String(c.number)}>
                      {cycleName(c)}
                      <span className="ml-auto pl-3 text-xs text-muted">{cycleDates(c)}</span>
                    </DropdownMenuRadioItem>
                  ))}
                <DropdownMenuSeparator />
                <DropdownMenuRadioItem value="none">No cycle</DropdownMenuRadioItem>
              </DropdownMenuRadioGroup>
              {picked && (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem asChild>
                    <Link href={`/cycles/${picked.number}`}>Open {cycleName(picked)}</Link>
                  </DropdownMenuItem>
                </>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
        {who && chip("Assignee", who, () => set({ assignee: null }))}
        {chosenLabels.length > 0 && chip("Label", chosenLabels.join(", "), () => set({ label: chosenServices.join(",") || null }))}
        {chosenServices.length > 0 && chip("Service", chosenServices.join(", "), () => set({ label: chosenLabels.join(",") || null }))}
        {prios.length > 0 && chip("Priority", prios.map((p) => PRIORITY[p].label.toLowerCase()).join(", "), () => set({ priority: null }))}
        {chosenTypes.length > 0 &&
          chip("Type", chosenTypes.map((id) => types.data?.find((t) => t.id === id)?.name.toLowerCase() ?? "?").join(", "), () => set({ type: null }))}
        {filters.parent && chip("Parent", filters.parent, () => set({ parent: null }))}
        <DropdownMenu>
          <DropdownMenuTrigger className={ghostSm}>
            <Plus className="h-[13px] w-[13px]" aria-hidden="true" />
            Filter
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>Type</DropdownMenuSubTrigger>
              <DropdownMenuSubContent>
                {(types.data ?? [])
                  .filter((t) => !t.archived)
                  .map((t) => (
                    <DropdownMenuItem key={t.id} onSelect={(e) => { e.preventDefault(); toggleType(t.id); }}>
                      <span className="w-4 text-center">{chosenTypes.includes(t.id) ? "✓" : ""}</span>
                      <TypeIcon type={t} size={14} />
                      {t.name}
                    </DropdownMenuItem>
                  ))}
              </DropdownMenuSubContent>
            </DropdownMenuSub>
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>Assignee</DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="max-h-80 overflow-y-auto">
                <DropdownMenuRadioGroup value={filters.assignee ?? ""} onValueChange={(v) => set({ assignee: v || null })}>
                  <DropdownMenuRadioItem value="">Anyone</DropdownMenuRadioItem>
                  <DropdownMenuRadioItem value="me">Me</DropdownMenuRadioItem>
                  <DropdownMenuRadioItem value="none">Unassigned</DropdownMenuRadioItem>
                  {(members.data ?? [])
                    .filter((m) => m.role !== "viewer" && m.user_id !== user?.id)
                    .map((m) => (
                      <DropdownMenuRadioItem key={m.user_id} value={m.user_id}>
                        {m.user_name || m.user_email}
                      </DropdownMenuRadioItem>
                    ))}
                </DropdownMenuRadioGroup>
              </DropdownMenuSubContent>
            </DropdownMenuSub>
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>Label</DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="max-h-80 overflow-y-auto">
                {!labels.data && labels.error ? (
                  <DropdownMenuItem onSelect={(e) => { e.preventDefault(); void labels.refetch(); }}>Couldn’t load the labels. Retry</DropdownMenuItem>
                ) : (
                  labels.data && plain.length === 0 && <DropdownMenuLabel className="text-xs text-muted">No labels yet</DropdownMenuLabel>
                )}
                {plain.map((l) => (
                  <DropdownMenuItem key={l.name} onSelect={(e) => { e.preventDefault(); toggleLabel(l.name); }}>
                    <span className="w-4 text-center">{chosen.includes(l.name) ? "✓" : ""}</span>
                    {l.name}
                    <span className="ml-auto font-mono text-[11px] text-muted">{l.open}</span>
                  </DropdownMenuItem>
                ))}
              </DropdownMenuSubContent>
            </DropdownMenuSub>
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>Service</DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="max-h-80 overflow-y-auto">
                <DropdownMenuLabel className="max-w-56 text-xs font-normal text-muted">Issues labelled with a service&apos;s name</DropdownMenuLabel>
                {services.map((l) => (
                  <DropdownMenuItem key={l.name} onSelect={(e) => { e.preventDefault(); toggleLabel(l.name); }}>
                    <span className="w-4 text-center">{chosen.includes(l.name) ? "✓" : ""}</span>
                    {l.name}
                    <span className="ml-auto font-mono text-[11px] text-muted">{l.open}</span>
                  </DropdownMenuItem>
                ))}
              </DropdownMenuSubContent>
            </DropdownMenuSub>
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>Priority</DropdownMenuSubTrigger>
              <DropdownMenuSubContent>
                {PRIORITY_ORDER.map((p) => (
                  <DropdownMenuItem
                    key={p}
                    onSelect={(e) => {
                      e.preventDefault();
                      set({ priority: (prios.includes(p) ? prios.filter((x) => x !== p) : [...prios, p]).join(",") || null });
                    }}
                  >
                    <span className="w-4 text-center">{prios.includes(p) ? "✓" : ""}</span>
                    <PriorityBars priority={p} />
                    {PRIORITY[p].label}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuSubContent>
            </DropdownMenuSub>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        {picked && layout !== "timeline" && <CycleMeter c={picked} />}
        {layout === "timeline" && <TimelineControls set={set} />}
        {layout === "list" && (
          <DropdownMenu>
            <DropdownMenuTrigger className={btnSm}>
              <Glyph d={ICON.sort} size={13} />
              {groupName} · {orderName}
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuLabel className="text-xs text-muted">Group by</DropdownMenuLabel>
              <DropdownMenuRadioGroup value={group} onValueChange={(v) => set({ group: v === "status" ? null : v })}>
                <DropdownMenuRadioItem value="status">Status</DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="priority">Priority</DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="assignee">Assignee</DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="type">Type</DropdownMenuRadioItem>
                {cycles?.enabled && <DropdownMenuRadioItem value="cycle">Cycle</DropdownMenuRadioItem>}
              </DropdownMenuRadioGroup>
              <DropdownMenuSeparator />
              <DropdownMenuLabel className="text-xs text-muted">Order by</DropdownMenuLabel>
              <DropdownMenuRadioGroup value={order} onValueChange={(v) => set({ order: v === "priority" ? null : v })}>
                <DropdownMenuRadioItem value="priority">Priority</DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="updated">Last updated</DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="created">Created</DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="due">Due date</DropdownMenuRadioItem>
              </DropdownMenuRadioGroup>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
    </div>
  );
}

// ─── the list ────────────────────────────────────────────────────────────────

function useIssues(projectId: string, q: IssueQuery, enabled = true, limit = 100, nextLimit = limit) {
  const svc = issueService(projectId);
  return useInfiniteQuery({
    queryKey: ["issues", projectId, q, limit],
    queryFn: ({ pageParam }) => svc.list(q, pageParam, pageParam ? nextLimit : limit),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next,
    enabled,
  });
}

function ListView({
  projectId,
  q,
  group,
  counts,
  countsError,
  onRetry,
  selected,
  onSelect,
  onClear,
  canWrite,
  onNew,
  viewName,
  scale,
}: {
  projectId: string;
  q: IssueQuery;
  group: Group;
  counts?: IssueCounts;
  countsError: unknown;
  onRetry: () => void;
  selected: number;
  onSelect: (n: number, keyboard: boolean) => void;
  onClear: () => void;
  canWrite: boolean;
  onNew: (s: IssueStatus) => void;
  viewName: string;
  scale: EstimateScale;
}) {
  const [now] = useState(() => Date.now());
  // Shipped and Cancelled load when opened.
  const [open, setOpen] = useState<IssueStatus[]>(q.q ? STATUS_ORDER : STATUS_ORDER.filter((s) => !CLOSED.includes(s)));
  const viewStatuses = q.status?.length ? q.status : STATUS_ORDER;

  // From 1200 a row opens the panel; below it, its link goes to the issue's page.
  const pick = (e: React.MouseEvent, n: number) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0 || !window.matchMedia("(min-width: 1200px)").matches) return;
    e.preventDefault();
    // A click with no pointer behind it (detail 0) is Enter on the row's link.
    onSelect(n === selected ? 0 : n, e.detail === 0);
  };
  const row = (it: Issue) => <Row key={it.number} it={it} now={now} selected={it.number === selected} href={issueHref(projectId, it.number)} onPick={(e) => pick(e, it.number)} scale={scale} />;

  if (!counts && countsError) return <ReadFailed heading="h2" error={countsError} onRetry={onRetry} />;
  if (!counts) return <div role="status" aria-label="Loading" className="h-80 rounded-[10px] bg-surface motion-safe:animate-pulse" />;
  const total = viewStatuses.reduce((n, s) => n + (counts[s] ?? 0), 0);
  if (total === 0) {
    return q.q ? (
      <section aria-labelledby="empty-h" className="flex flex-col items-start gap-2 rounded-[10px] border border-subtle bg-surface px-6 py-7">
        <Search className="h-5 w-5 text-muted" aria-hidden="true" />
        <h2 id="empty-h" className="text-[15px] font-semibold leading-[22px] text-fg">
          No issues match “{q.q}”
        </h2>
        <p className="max-w-[520px] text-[13px] text-fg/70">Search looks at the key and title of every issue in {viewName}, Shipped included.</p>
        <button type="button" onClick={onClear} className="mt-1 h-8 rounded-lg border border-strong bg-raised px-3 text-[13px] font-medium text-fg">
          Clear search
        </button>
      </section>
    ) : (
      <section aria-labelledby="empty-h" className="flex flex-col items-start gap-2 rounded-[10px] border border-subtle bg-surface px-6 py-7">
        <h2 id="empty-h" className="text-[15px] font-semibold leading-[22px] text-fg">
          No issues here yet
        </h2>
        <p className="max-w-[560px] text-[13px] text-fg/70">
          Open one, then push a branch that names its key: the issue moves to In progress, to In review when a pull request opens, and to Shipped when the merge is live.
        </p>
        {canWrite && (
          <button type="button" onClick={() => onNew("backlog")} className={cn(primary, "mt-1")}>
            <Plus className="h-3.5 w-3.5" aria-hidden="true" />
            New issue
          </button>
        )}
      </section>
    );
  }

  if (group !== "status") return <OtherGroups projectId={projectId} q={{ ...q, status: viewStatuses }} group={group} row={row} canWrite={canWrite} onNew={onNew} />;
  return (
    <div className={listCard}>
      {viewStatuses
        .filter((s) => STATUS_ORDER.includes(s) && (counts[s] ?? 0) > 0)
        .sort((a, b) => STATUS_ORDER.indexOf(a) - STATUS_ORDER.indexOf(b))
        .map((s) => (
          <StatusGroup
            key={s}
            projectId={projectId}
            q={q}
            status={s}
            count={counts[s] ?? 0}
            expanded={open.includes(s) || !!q.q}
            onToggle={() => setOpen((o) => (o.includes(s) ? o.filter((x) => x !== s) : [...o, s]))}
            row={row}
            canWrite={canWrite}
            onNew={onNew}
          />
        ))}
    </div>
  );
}

/** The list's card; @container so rows drop chips by the card's own width. */
const listCard = "@container overflow-hidden rounded-[10px] border border-subtle bg-surface";

type G = { id: string; name: string; count: number; icon: React.ReactNode; rule?: string; status?: IssueStatus };

function GroupRows({ g, expanded, onToggle, canWrite, onNew, children }: { g: G; expanded: boolean; onToggle?: () => void; canWrite: boolean; onNew: (s: IssueStatus) => void; children: React.ReactNode }) {
  return (
    <section aria-labelledby={`grp-${g.id}`} className="border-t border-subtle first:border-t-0">
      <div className="flex min-h-10 items-center gap-2 bg-[color-mix(in_oklab,var(--surface)_70%,var(--raised))] py-1 pl-3 pr-2">
        <h2 id={`grp-${g.id}`} className="m-0">
          <button type="button" aria-expanded={expanded} disabled={!onToggle} onClick={onToggle} className="touch-target inline-flex min-h-8 items-center gap-2 px-1 text-[13px] font-semibold text-fg disabled:cursor-default">
            {onToggle &&
              (expanded ? <ChevronDown className="h-[13px] w-[13px] text-muted" strokeWidth={2.2} aria-hidden="true" /> : <ChevronRight className="h-[13px] w-[13px] text-muted" strokeWidth={2.2} aria-hidden="true" />)}
            {g.icon}
            {g.name}
            <span className="font-mono text-xs font-normal text-muted">{g.count}</span>
          </button>
        </h2>
        {g.rule && <span className="hidden text-xs text-muted min-[1200px]:inline">{g.rule}</span>}
        {canWrite && g.status && (
          <button type="button" aria-label={`Add issue to ${g.name}`} onClick={() => onNew(g.status!)} className="ml-auto flex h-9 w-9 flex-none items-center justify-center rounded-lg text-muted hover:bg-raised hover:text-fg pointer-coarse:h-11 pointer-coarse:w-11">
            <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          </button>
        )}
      </div>
      {expanded && <ul className="m-0 list-none p-0">{children}</ul>}
    </section>
  );
}

function StatusGroup({
  projectId,
  q,
  status,
  count,
  expanded,
  onToggle,
  row,
  canWrite,
  onNew,
}: {
  projectId: string;
  q: IssueQuery;
  status: IssueStatus;
  count: number;
  expanded: boolean;
  onToggle: () => void;
  row: (it: Issue) => React.ReactNode;
  canWrite: boolean;
  onNew: (s: IssueStatus) => void;
}) {
  const rows = useIssues(projectId, { ...q, status: [status] }, expanded, status === "backlog" ? BACKLOG_PAGE : STATUS_PAGE, STATUS_PAGE);
  const issues = rows.data?.pages.flatMap((p) => p.data) ?? [];
  const more = moreCount(count, issues.length);
  const s = STATUS[status];
  return (
    <GroupRows g={{ id: status, name: s.name, count, icon: <StatusIcon status={status} />, rule: s.rule, status }} expanded={expanded} onToggle={onToggle} canWrite={canWrite} onNew={onNew}>
      {!rows.data && rows.error ? (
        <li className="flex items-center justify-between gap-3 border-t border-subtle/70 px-4 py-1.5 text-[13px] text-status-failed">
          <span role="alert">Couldn’t load {s.name}.</span>
          <button type="button" onClick={() => void rows.refetch()} className={btnSm}>
            Retry
          </button>
        </li>
      ) : !rows.data ? (
        <li aria-hidden="true" className="h-10 border-t border-subtle/70 bg-raised/40 motion-safe:animate-pulse" />
      ) : (
        issues.map(row)
      )}
      <NextPageFailed query={rows} what={`more in ${s.name}`} className="border-t border-subtle px-4 py-2" />
      {more > 0 && rows.hasNextPage && (
        <li>
          {/* 146 is where the titles start: padding, bars, type and key columns, their gaps. */}
          <button type="button" onClick={() => rows.fetchNextPage()} disabled={rows.isFetchingNextPage} className="touch-target flex min-h-10 w-full items-center border-t border-subtle pl-[146px] pr-4 text-left text-[13px] text-link hover:underline disabled:opacity-60 max-sm:pl-4">
            Show {more} more in {s.name}
          </button>
        </li>
      )}
    </GroupRows>
  );
}

/** Priority, Assignee, Type and Cycle: one query, its pages at the end.
 *  The headers count the rows loaded. */
function OtherGroups({
  projectId,
  q,
  group,
  row,
  canWrite,
  onNew,
}: {
  projectId: string;
  q: IssueQuery;
  group: Group;
  row: (it: Issue) => React.ReactNode;
  canWrite: boolean;
  onNew: (s: IssueStatus) => void;
}) {
  const rows = useIssues(projectId, q);
  const all = rows.data?.pages.flatMap((p) => p.data) ?? [];

  if (!rows.data && rows.error) return <ReadFailed heading="h2" error={rows.error} onRetry={() => void rows.refetch()} />;
  if (!rows.data) return <div role="status" aria-label="Loading" className="h-80 rounded-[10px] bg-surface motion-safe:animate-pulse" />;

  let groups: (G & { rows: Issue[] })[];
  if (group === "priority") {
    groups = PRIORITY_ORDER.map((p) => {
      const r = all.filter((i) => i.priority === p);
      return { id: p, name: PRIORITY[p].label, count: r.length, icon: <PriorityBars priority={p} />, rows: r };
    }).filter((g) => g.count > 0);
  } else if (group === "cycle") {
    const seen = new Map<number, NonNullable<Issue["cycle"]>>();
    for (const i of all) if (i.cycle) seen.set(i.cycle.number, i.cycle);
    groups = [...seen.values()]
      .sort((a, b) => a.number - b.number)
      .map((c) => {
        const r = all.filter((i) => i.cycle?.number === c.number);
        return { id: `c${c.number}`, name: `${cycleName(c)} · ${cycleDates(c)}`, count: r.length, icon: <RefreshCw className="h-[13px] w-[13px] text-muted" aria-hidden="true" />, rows: r };
      });
    const none = all.filter((i) => !i.cycle);
    if (none.length) groups.push({ id: "c0", name: "No cycle", count: none.length, icon: <RefreshCw className="h-[13px] w-[13px] text-muted" aria-hidden="true" />, rows: none });
  } else if (group === "type") {
    const seen = new Map<string, Issue["type"]>();
    for (const i of all) seen.set(i.type.id, i.type);
    groups = [...seen.values()].map((t) => {
      const r = all.filter((i) => i.type.id === t.id);
      return { id: t.id, name: t.name, count: r.length, icon: <TypeIcon type={t} size={15} />, rows: r };
    });
  } else {
    const people = new Map<string, string>();
    for (const i of all) people.set(i.assignee?.id ?? "", i.assignee?.name ?? "Unassigned");
    groups = [...people.entries()]
      .sort(([a, an], [b, bn]) => (a === "" ? 1 : b === "" ? -1 : an.localeCompare(bn)))
      .map(([id, name]) => {
        const r = all.filter((i) => (i.assignee?.id ?? "") === id);
        return { id: id || "none", name, count: r.length, icon: <AssigneeDot name={id ? name : null} />, rows: r };
      });
  }
  return (
    <div className={listCard}>
      {groups.map((g) => (
        <GroupRows key={g.id} g={g} expanded canWrite={canWrite} onNew={onNew}>
          {g.rows.map(row)}
        </GroupRows>
      ))}
      <NextPageFailed query={rows} what="more issues" className="border-t border-subtle px-4 py-2" />
      {rows.hasNextPage && (
        <button type="button" onClick={() => rows.fetchNextPage()} disabled={rows.isFetchingNextPage} className="touch-target flex min-h-10 w-full items-center border-t border-subtle px-4 text-left text-[13px] text-link hover:underline disabled:opacity-60">
          Show more
        </button>
      )}
    </div>
  );
}

function Row({ it, now, selected, href, onPick, scale }: { it: Issue; now: number; selected: boolean; href: string; onPick: (e: React.MouseEvent) => void; scale: EstimateScale }) {
  return (
    <li
      className={cn(
        "touch-target relative grid min-h-10 grid-cols-[16px_14px_minmax(64px,max-content)_minmax(160px,1fr)_auto_24px_36px] items-center gap-x-3 border-t border-subtle/70 px-4 py-1.5",
        "max-sm:grid-cols-[16px_14px_minmax(0,1fr)_auto] max-sm:gap-x-2.5 max-sm:gap-y-1.5 max-sm:py-3 max-sm:[grid-template-areas:'pri_type_key_age'_'title_title_title_title'_'meta_meta_meta_av']",
        selected && "bg-selected",
      )}
    >
      <span className="flex max-sm:[grid-area:pri]">
        <PriorityBars priority={it.priority} />
      </span>
      <span className="flex max-sm:[grid-area:type]">
        <TypeIcon type={it.type} size={14} />
      </span>
      <span className="whitespace-nowrap font-mono text-xs text-muted max-sm:[grid-area:key]">{it.key}</span>
      <span className="min-w-0 text-[13px] font-medium leading-5 text-fg [overflow-wrap:anywhere] max-sm:[grid-area:title]">
        <Link
          href={href}
          aria-current={selected ? "true" : undefined}
          onClick={onPick}
          className="after:absolute after:inset-0 after:content-[''] focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-inset focus-visible:after:ring-focus"
        >
          {it.title}
        </Link>
        {(it.priority === "urgent" || it.blocked_by.length > 0) && (
          <span className="ml-1.5 inline-flex flex-wrap gap-1 align-[1px]">
            {it.priority === "urgent" && <UrgentChip />}
            <BlockedChip keys={it.blocked_by} />
          </span>
        )}
      </span>
      <span className="flex min-w-0 flex-wrap items-center justify-end gap-2 max-sm:justify-start max-sm:gap-1.5 max-sm:[grid-area:meta]">
        <span className="hidden max-sm:contents lg:@[740px]:contents">
          {it.progress && <ProgressPill done={it.progress.done} total={it.progress.total} />}
          {it.due_on && <DueChip due={it.due_on} status={it.status} />}
          {scale !== "off" && it.estimate != null && <EstimateBox value={it.estimate} scale={scale} />}
        </span>
        {it.parent && (
          <span title={it.parent.title} className="relative z-10 pointer-coarse:pointer-events-none hidden h-5 max-w-[160px] min-w-0 items-center gap-1 truncate rounded-sm border border-subtle px-1.5 text-[11px] text-fg/70 max-sm:inline-flex lg:@[640px]:inline-flex">
            <span className="font-mono text-muted">{it.parent.key}</span>
            <span className="truncate">{it.parent.title}</span>
          </span>
        )}
        <span className="hidden max-sm:contents lg:@[640px]:contents">
          <WorkChip work={it.work} issueKey={it.key} />
        </span>
        {it.labels.length > 0 && (
          <span className="hidden gap-1 max-sm:flex max-sm:flex-wrap min-[1600px]:flex">
            {it.labels.map((l) => (
              <LabelChip key={l} name={l} />
            ))}
          </span>
        )}
      </span>
      <span className="flex justify-center max-sm:justify-end max-sm:[grid-area:av]">
        <AssigneeDot name={it.assignee?.name} />
      </span>
      <span className="w-9 text-right font-mono text-xs text-muted max-sm:w-auto max-sm:[grid-area:age]">{compactAgo(it.updated_at, now)}</span>
    </li>
  );
}

// ─── the board ───────────────────────────────────────────────────────────────

function BoardView({
  projectId,
  q,
  counts,
  countsFailed,
  canWrite,
  onNew,
  scale,
}: {
  projectId: string;
  q: IssueQuery;
  counts?: IssueCounts;
  countsFailed: boolean;
  canWrite: boolean;
  onNew: (s: IssueStatus) => void;
  scale: EstimateScale;
}) {
  const refresh = useIssueRefresh(projectId);
  const [over, setOver] = useState<IssueStatus | null>(null);
  const move = useMutation({
    mutationFn: ({ n, status }: { n: number; status: IssueStatus }) => issueService(projectId).update(n, { status }),
    onSuccess: (_, v) => refresh(v.n),
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn't move the issue"),
  });
  const columns = q.status?.length ? BOARD_COLUMNS.filter((s) => q.status!.includes(s)) : BOARD_COLUMNS;
  return (
    <div className="-mx-4 flex snap-x snap-mandatory items-start gap-3 overflow-x-auto px-4 pb-2 md:mx-0 md:px-0">
      {columns.map((s) => (
        <Column
          key={s}
          projectId={projectId}
          status={s}
          q={q}
          count={counts?.[s]}
          countFailed={countsFailed}
          canWrite={canWrite}
          onNew={onNew}
          over={over === s}
          onOver={setOver}
          onDropIssue={(n) => move.mutate({ n, status: s })}
          onMove={(n, status) => move.mutate({ n, status })}
          scale={scale}
        />
      ))}
    </div>
  );
}

function Column({
  projectId,
  status,
  q,
  count,
  countFailed,
  canWrite,
  onNew,
  over,
  onOver,
  onDropIssue,
  onMove,
  scale,
}: {
  projectId: string;
  status: IssueStatus;
  q: IssueQuery;
  count?: number;
  countFailed: boolean;
  canWrite: boolean;
  onNew: (s: IssueStatus) => void;
  over: boolean;
  onOver: (s: IssueStatus | null) => void;
  onDropIssue: (n: number) => void;
  onMove: (n: number, s: IssueStatus) => void;
  scale: EstimateScale;
}) {
  const rows = useIssues(projectId, { ...q, status: [status] }, true, 10);
  const cards = rows.data?.pages.flatMap((p) => p.data) ?? [];
  const more = (count ?? 0) - cards.length;
  const s = STATUS[status];
  return (
    <section
      aria-labelledby={`col-${status}`}
      onDragOver={(e) => {
        if (!canWrite) return;
        e.preventDefault();
        onOver(status);
      }}
      onDragLeave={() => onOver(null)}
      onDrop={(e) => {
        e.preventDefault();
        onOver(null);
        const n = Number(e.dataTransfer.getData("text/x-toft-issue"));
        if (n) onDropIssue(n);
      }}
      className={cn(
        "flex w-[82vw] flex-[1_0_228px] snap-start flex-col gap-2 rounded-[10px] bg-[color-mix(in_oklab,var(--ground)_55%,var(--surface))] p-2 sm:w-auto sm:min-w-[228px]",
        over && "outline outline-2 -outline-offset-2 outline-focus",
      )}
    >
      <div className="flex flex-col gap-0.5 px-1 pb-1.5 pt-1">
        <div className="flex items-center gap-2">
          <StatusIcon status={status} />
          <h2 id={`col-${status}`} className="text-[13px] font-semibold leading-5 text-fg">
            {s.name}
          </h2>
          <span className="font-mono text-xs text-muted">{count ?? (countFailed ? "—" : "")}</span>
          <span className="flex-1" />
          {canWrite && (
            <button type="button" aria-label={`Add issue to ${s.name}`} onClick={() => onNew(status)} className="flex h-7 w-7 items-center justify-center rounded-md text-muted hover:bg-raised hover:text-fg pointer-coarse:h-11 pointer-coarse:w-11">
              <Plus className="h-3.5 w-3.5" aria-hidden="true" />
            </button>
          )}
        </div>
        {s.rule && (
          <span className="flex items-center gap-[5px] text-[11px] leading-4 text-muted">
            <Glyph d={ICON.bolt} size={11} />
            {s.rule.replace("a pull request", "a PR")}
          </span>
        )}
      </div>
      <Read query={rows} what={s.name}>
        {() => (
          <>
            {cards.map((it) => (
              <Card key={it.number} it={it} projectId={projectId} canWrite={canWrite} onMove={onMove} scale={scale} />
            ))}
            <NextPageFailed query={rows} what="more issues" />
            {more > 0 && (
              <button type="button" onClick={() => rows.fetchNextPage()} disabled={!rows.hasNextPage || rows.isFetchingNextPage} className="touch-target h-8 rounded-md px-1.5 text-left text-xs text-muted hover:text-fg">
                {more} more
              </button>
            )}
          </>
        )}
      </Read>
    </section>
  );
}

function Card({
  it,
  projectId,
  canWrite,
  onMove,
  scale,
}: {
  it: Issue;
  projectId: string;
  canWrite: boolean;
  onMove: (n: number, s: IssueStatus) => void;
  scale: EstimateScale;
}) {
  return (
    <div
      draggable={canWrite}
      onDragStart={(e) => {
        e.dataTransfer.setData("text/x-toft-issue", String(it.number));
        e.dataTransfer.effectAllowed = "move";
      }}
      className="group relative flex flex-col gap-2 rounded-lg border border-subtle bg-[color-mix(in_oklab,var(--surface)_70%,var(--raised))] px-3 py-2.5 text-fg"
    >
      <span className="flex items-center gap-2">
        <TypeIcon type={it.type} size={14} />
        <span className="font-mono text-[11px] text-muted">{it.key}</span>
        <span className="flex-1" />
        {it.priority === "urgent" && <UrgentChip />}
        <PriorityBars priority={it.priority} />
        {canWrite && (
          <DropdownMenu>
            <DropdownMenuTrigger aria-label={`Move ${it.key}`} className="relative z-10 -mr-1 flex h-5 w-5 items-center justify-center rounded text-muted opacity-0 hover:text-fg focus-visible:opacity-100 group-hover:opacity-100 data-[state=open]:opacity-100 pointer-coarse:h-11 pointer-coarse:w-11 pointer-coarse:opacity-100">
              <MoreHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuLabel className="text-xs text-muted">Move to</DropdownMenuLabel>
              {STATUS_ORDER.filter((s) => s !== it.status).map((s) => (
                <DropdownMenuItem key={s} onSelect={() => onMove(it.number, s)}>
                  <StatusIcon status={s} size={14} />
                  {STATUS[s].name}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </span>
      <Link href={issueHref(projectId, it.number)} className="text-[13px] font-medium leading-[18px] after:absolute after:inset-0 after:content-['']">
        {it.title}
      </Link>
      {it.blocked_by.length > 0 && (
        <span>
          <BlockedChip keys={it.blocked_by} />
        </span>
      )}
      {it.progress && <ProgressPill done={it.progress.done} total={it.progress.total} wide />}
      <WorkChip work={it.work} block />
      {it.due_on && (
        <span>
          <DueChip due={it.due_on} status={it.status} />
        </span>
      )}
      {(it.labels.length > 0 || it.assignee || (scale !== "off" && it.estimate != null)) && (
        <span className="flex items-center gap-1.5">
          {it.labels.map((l) => (
            <LabelChip key={l} name={l} />
          ))}
          <span className="flex-1" />
          {scale !== "off" && it.estimate != null && <EstimateBox value={it.estimate} scale={scale} />}
          {it.assignee && <AssigneeDot name={it.assignee.name} />}
        </span>
      )}
    </div>
  );
}

/** "4 of 18 shipped", with the bar the boards draw: shipped, started. */
function CycleMeter({ c }: { c: ProjectCycles["cycles"][number] }) {
  const n = c.counts;
  const pct = (v: number) => `${n.scope ? (v * 100) / n.scope : 0}%`;
  const started = Math.max(n.started - n.shipped, 0);
  return (
    <span className="inline-flex items-center gap-2 text-xs text-muted">
      <span aria-hidden="true" className="inline-flex h-1.5 w-16 overflow-hidden rounded-xs bg-subtle">
        <span className="bg-status-running" style={{ width: pct(n.shipped) }} />
        <span className="bg-status-building" style={{ width: pct(started) }} />
      </span>
      {n.shipped} of {n.scope} shipped
    </span>
  );
}

/** The Timeline's Dependencies, zoom and Today, kept in the URL. */
function TimelineControls({ set }: { set: (p: Record<string, string | null>) => void }) {
  const sp = useSearchParams();
  const zoom = sp.get("zoom") ?? "weeks";
  const deps = sp.get("deps") !== "off";
  return (
    <>
      <label className="touch-target inline-flex h-[30px] cursor-pointer items-center gap-2 text-[13px] text-fg/70">
        <input type="checkbox" checked={deps} onChange={() => set({ deps: deps ? "off" : null })} className="h-4 w-4 accent-[var(--accent)]" />
        Dependencies
      </label>
      <span role="group" aria-label="Zoom" className="inline-flex gap-0.5 rounded-lg border border-subtle bg-surface p-0.5">
        {(["weeks", "months", "quarters"] as const).map((z) => (
          <button
            key={z}
            type="button"
            aria-pressed={zoom === z}
            onClick={() => set({ zoom: z === "weeks" ? null : z })}
            className={cn("touch-target h-[26px] rounded-md px-2.5 text-[13px] capitalize", zoom === z ? "bg-subtle font-medium text-fg" : "text-fg/70 hover:text-fg")}
          >
            {z}
          </button>
        ))}
      </span>
      <button
        type="button"
        onClick={() => document.querySelector("[data-timeline-today]")?.scrollIntoView({ inline: "center", block: "nearest", behavior: "smooth" })}
        className="touch-target h-[30px] rounded-md border border-strong bg-surface px-2.5 text-[13px] text-fg"
      >
        Today
      </button>
    </>
  );
}
