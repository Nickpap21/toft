"use client";

import { useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { useMutation, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { CalendarDays, ChevronRight, CornerDownRight, Layers, RefreshCw, Tag, UserRound } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { ApiError } from "@/lib/api/client";
import { issueService, issueTypesQuery, type IssuePriority, type IssueStatus } from "@/lib/api/issue.service";
import { orgService } from "@/lib/api/org.service";
import { projectQuery } from "@/lib/queries";
import { cn } from "@/lib/utils";
import { primary } from "@/app/projects/_lib/ui";
import { MarkdownField } from "@/components/code/pulls";
import { PROJECT_LETTER } from "@/components/shell/sidebar-projects";
import { useIssueRefresh } from "./issue-detail";
import { IssuePicker, TypeIcon } from "./deep";
import { issueHref, PRIORITY, PRIORITY_ORDER, PriorityBars, STATUS, STATUS_ORDER, StatusIcon } from "./meta";
import { CycleItems, cycleName, DueItems, EstimateItems, fmtDay, pointsText, projectCyclesQuery } from "./cycles";

// New issue (D-83; no board, built from IssueList's parts): the type first,
// a title and a description, then the properties as pills, and the key it
// gets. ⌘↵ creates; Create more keeps the dialog open for the next one.
export function NewIssueDialog({
  projectId,
  open,
  onOpenChange,
  status = "backlog",
  parent,
  subtask,
  onCreated,
  cycle,
}: {
  projectId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  status?: IssueStatus;
  /** The cycle the list shows: a new issue goes in it (D-90). */
  cycle?: number;
  /** A parent's key: the new issue goes under it. */
  parent?: string;
  /** Under an issue that isn't an Epic or a Release: a sub-task. */
  subtask?: boolean;
  onCreated?: (number: number) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent showClose={false} className="top-[14vh] max-w-[640px] translate-y-0 gap-0 overflow-visible p-0">
        {open && <Form projectId={projectId} initial={status} parent={parent} subtask={subtask} cycle={cycle} onDone={() => onOpenChange(false)} onCreated={onCreated} />}
      </DialogContent>
    </Dialog>
  );
}

const pill =
  "inline-flex h-7 max-w-[220px] items-center gap-1.5 rounded-full border border-subtle bg-raised/60 px-2.5 text-xs text-fg/85 hover:border-strong hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus data-[state=open]:border-strong";

function Form({
  projectId,
  initial,
  parent: initialParent,
  subtask,
  onDone,
  onCreated,
  cycle: initialCycle,
}: {
  projectId: string;
  initial: IssueStatus;
  parent?: string;
  subtask?: boolean;
  onDone: () => void;
  onCreated?: (n: number) => void;
  cycle?: number;
}) {
  const router = useRouter();
  const refresh = useIssueRefresh(projectId);
  const svc = issueService(projectId);
  const { data: project } = useQuery(projectQuery(projectId));
  const settings = useQuery({ queryKey: ["issue-settings", projectId], queryFn: () => svc.settings() });
  const types = useQuery(issueTypesQuery);
  const members = useQuery({ queryKey: ["org-members"], queryFn: () => orgService.listMembers() });
  const labels = useQuery({ queryKey: ["issue-labels", projectId], queryFn: () => svc.labels() });
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [picked, setTypeId] = useState("");
  const [status, setStatus] = useState<IssueStatus>(initial);
  const [priority, setPriority] = useState<IssuePriority>("none");
  const [assignee, setAssignee] = useState("");
  const [chosen, setChosen] = useState<string[]>([]);
  const [newLabel, setNewLabel] = useState("");
  const [parent, setParent] = useState(initialParent ?? "");
  const [picking, setPicking] = useState(false);
  const [more, setMore] = useState(false);
  // Cycle, due date and estimate (D-90): an untouched cycle lets a
  // sub-issue take its parent's.
  const cycles = useQuery(projectCyclesQuery(projectId, () => svc.cycles()));
  const [cycle, setCycle] = useState<number | undefined>(initialCycle);
  const [due, setDue] = useState("");
  const [estimate, setEstimate] = useState(0);
  const titleRef = useRef<HTMLInputElement>(null);

  // The type it opens with: a sub-task under an issue, else the project's
  // default, else the workspace's first standard type.
  const usable = (types.data ?? []).filter((t) => !t.archived && !(settings.data?.hidden_type_ids ?? []).includes(t.id));
  const fallback = subtask ? usable.find((t) => t.kind === "subtask") : (usable.find((t) => t.id === settings.data?.default_type_id) ?? usable.find((t) => t.kind === "standard"));
  const typeId = picked || fallback?.id || "";
  const type = usable.find((t) => t.id === typeId);
  const who = members.data?.find((m) => m.user_id === assignee);
  const container = type?.kind === "container";
  const scale = cycles.data?.estimates ?? "off";
  const allCycles = cycles.data?.cycles ?? [];
  const inCycle = allCycles.find((c) => c.number === cycle);
  const current = allCycles.find((c) => c.state === "current");

  const create = useMutation({
    mutationFn: () =>
      svc.create({
        title,
        description,
        status,
        priority,
        assignee_id: assignee || undefined,
        labels: chosen,
        type_id: typeId || undefined,
        parent: parent || undefined,
        cycle: container ? undefined : cycle,
        due_on: due || undefined,
        estimate: container ? undefined : estimate || undefined,
      }),
    onSuccess: (issue) => {
      refresh();
      onCreated?.(issue.number);
      toast.success(`Opened ${issue.key}`, { action: { label: "Open", onClick: () => router.push(issueHref(projectId, issue.number)) } });
      if (more) {
        setTitle("");
        setDescription("");
        void settings.refetch();
        titleRef.current?.focus();
      } else onDone();
    },
    onError: (err) => toast.error(err instanceof ApiError ? err.message : "Couldn't open the issue"),
  });
  const submit = () => title.trim() && !create.isPending && create.mutate();

  return (
    <form
      className="flex flex-col"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
          e.preventDefault();
          submit();
        }
      }}
    >
      <div className="flex items-center gap-2 px-5 pt-4 text-[13px]">
        <span aria-hidden="true" className={cn("flex h-5 w-5 items-center justify-center rounded text-[10px] font-bold", PROJECT_LETTER.idle)}>
          {project?.name.trim()[0]?.toUpperCase() ?? "?"}
        </span>
        <span className="text-fg/70">{project?.name}</span>
        <ChevronRight className="h-3.5 w-3.5 text-muted" aria-hidden="true" />
        <DialogTitle className="text-[13px] font-medium text-fg">New issue</DialogTitle>
        <DialogDescription className="sr-only">A title, a description and its properties. Its status then follows the work.</DialogDescription>
        <span className="flex-1" />
        <DropdownMenu>
          <DropdownMenuTrigger className={pill} aria-label={`Type: ${type?.name ?? "none"}. Change`}>
            {type && <TypeIcon type={type} size={14} />}
            {type?.name ?? "Type"}
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuRadioGroup value={typeId} onValueChange={setTypeId}>
              {usable.map((t) => (
                <DropdownMenuRadioItem key={t.id} value={t.id}>
                  <TypeIcon type={t} size={14} />
                  {t.name}
                  {t.kind !== "standard" && <span className="ml-auto pl-3 text-xs text-muted">{t.kind === "container" ? "holds issues" : "needs a parent"}</span>}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <div className="flex flex-col gap-1 px-5 pb-3 pt-3">
        <label htmlFor="ni-title" className="sr-only">
          Title
        </label>
        <input
          id="ni-title"
          ref={titleRef}
          autoFocus
          required
          maxLength={256}
          value={title}
          placeholder="Issue title"
          onChange={(e) => setTitle(e.target.value)}
          className="w-full bg-transparent text-[17px] font-semibold leading-7 tracking-[-0.01em] text-fg outline-none placeholder:text-muted/70"
        />
        <MarkdownField id="ni-desc" label="Description" value={description} onChange={setDescription} rows={4} projectId={projectId} onSubmit={submit} placeholder="What and why. Markdown; STO-12 links an issue." />
      </div>

      <div className="flex flex-wrap items-center gap-1.5 border-t border-subtle px-5 py-3">
        <DropdownMenu>
          <DropdownMenuTrigger className={pill} aria-label={`Status: ${STATUS[status].name}. Change`}>
            <StatusIcon status={status} size={13} />
            {STATUS[status].name}
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            <DropdownMenuRadioGroup value={status} onValueChange={(v) => setStatus(v as IssueStatus)}>
              {STATUS_ORDER.filter((s) => s !== "shipped" && s !== "cancelled").map((s) => (
                <DropdownMenuRadioItem key={s} value={s}>
                  <StatusIcon status={s} size={14} />
                  {STATUS[s].name}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
        <DropdownMenu>
          <DropdownMenuTrigger className={pill} aria-label={`Priority: ${PRIORITY[priority].label}. Change`}>
            <PriorityBars priority={priority} />
            {priority === "none" ? "Priority" : PRIORITY[priority].label}
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            <DropdownMenuRadioGroup value={priority} onValueChange={(v) => setPriority(v as IssuePriority)}>
              {PRIORITY_ORDER.map((p) => (
                <DropdownMenuRadioItem key={p} value={p}>
                  <PriorityBars priority={p} />
                  {PRIORITY[p].label}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
        <DropdownMenu>
          <DropdownMenuTrigger className={pill} aria-label={`Assignee: ${who?.user_name ?? "nobody"}. Change`}>
            <UserRound className="h-3.5 w-3.5" aria-hidden="true" />
            <span className="truncate">{who ? who.user_name || who.user_email : "Assignee"}</span>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            <DropdownMenuRadioGroup value={assignee} onValueChange={setAssignee}>
              <DropdownMenuRadioItem value="">Unassigned</DropdownMenuRadioItem>
              {(members.data ?? [])
                .filter((m) => m.role !== "viewer")
                .map((m) => (
                  <DropdownMenuRadioItem key={m.user_id} value={m.user_id}>
                    {m.user_name || m.user_email}
                  </DropdownMenuRadioItem>
                ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
        <DropdownMenu>
          <DropdownMenuTrigger className={pill} aria-label={`Labels: ${chosen.join(", ") || "none"}. Change`}>
            <Tag className="h-3.5 w-3.5" aria-hidden="true" />
            <span className="truncate">{chosen.length ? chosen.join(", ") : "Labels"}</span>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="max-h-80 overflow-y-auto">
            <div className="p-1">
              <input
                aria-label="New label"
                placeholder="New label, then Enter"
                value={newLabel}
                maxLength={40}
                onChange={(e) => setNewLabel(e.target.value)}
                onKeyDown={(e) => {
                  e.stopPropagation();
                  if (e.key === "Enter") {
                    e.preventDefault();
                    const n = newLabel.trim().toLowerCase();
                    if (n && !chosen.includes(n)) setChosen([...chosen, n]);
                    setNewLabel("");
                  }
                }}
                className="h-8 w-full rounded-md border border-strong bg-ground px-2 text-[13px] text-fg placeholder:text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
              />
            </div>
            <DropdownMenuSeparator />
            {(labels.data ?? []).map((l) => (
              <DropdownMenuItem
                key={l.name}
                onSelect={(e) => {
                  e.preventDefault();
                  setChosen((c) => (c.includes(l.name) ? c.filter((x) => x !== l.name) : [...c, l.name]));
                }}
              >
                <span className="w-4 text-center">{chosen.includes(l.name) ? "✓" : ""}</span>
                {l.name}
                {l.service && <span className="ml-auto pl-3 text-xs text-muted">service</span>}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
        {cycles.data?.enabled && !container && (
          <DropdownMenu>
            <DropdownMenuTrigger className={pill} aria-label={`Cycle: ${inCycle ? cycleName(inCycle) : "none"}. Change`}>
              <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
              <span className="truncate">{inCycle ? cycleName(inCycle) : "Cycle"}</span>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">
              <DropdownMenuRadioGroup value={String(cycle ?? 0)} onValueChange={(v) => setCycle(Number(v))}>
                <CycleItems cycles={allCycles} />
              </DropdownMenuRadioGroup>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
        <DropdownMenu>
          <DropdownMenuTrigger className={pill} aria-label={`${container ? "Target" : "Due date"}: ${due ? fmtDay(due) : "none"}. Change`}>
            <CalendarDays className="h-3.5 w-3.5" aria-hidden="true" />
            <span className="truncate">{due ? `${container ? "Target" : "Due"} ${fmtDay(due)}` : container ? "Target" : "Due date"}</span>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-[280px]">
            <DueItems value={due} onChange={setDue} cycleEnds={current ? { name: cycleName(current), ends_on: current.ends_on } : undefined} />
          </DropdownMenuContent>
        </DropdownMenu>
        {scale !== "off" && !container && (
          <DropdownMenu>
            <DropdownMenuTrigger className={pill} aria-label={`Estimate: ${estimate ? pointsText(estimate, scale) : "none"}. Change`}>
              <Layers className="h-3.5 w-3.5" aria-hidden="true" />
              {estimate ? pointsText(estimate, scale) : "Estimate"}
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">
              <DropdownMenuRadioGroup value={String(estimate)} onValueChange={(v) => setEstimate(Number(v))}>
                <EstimateItems scale={scale} />
              </DropdownMenuRadioGroup>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
        <button type="button" onClick={() => setPicking((p) => !p)} className={pill} aria-expanded={picking}>
          <CornerDownRight className="h-3.5 w-3.5" aria-hidden="true" />
          <span className="truncate">{parent || (type?.kind === "subtask" ? "Parent (needed)" : "Parent")}</span>
        </button>
        {picking && (
          <div className="mt-2 w-full rounded-lg border border-subtle bg-ground p-2">
            <IssuePicker
              autoFocus
              placeholder="An Epic, a Release, or the issue it belongs to"
              onPick={(k) => {
                setParent(k);
                setPicking(false);
              }}
            />
            {parent && (
              <button type="button" onClick={() => setParent("")} className="mt-1 px-2 text-xs text-muted hover:text-fg">
                No parent
              </button>
            )}
          </div>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-3 border-t border-subtle px-5 py-3">
        {settings.data?.next_key ? (
          <span className="text-xs text-muted">
            Opens as <span className="font-mono text-fg/80">{settings.data.next_key}</span>
          </span>
        ) : (
          <span className="text-xs text-muted">The project&apos;s first issue gets its key from its name.</span>
        )}
        <span className="flex-1" />
        <label className="flex items-center gap-2 text-xs text-fg/80">
          <input type="checkbox" checked={more} onChange={(e) => setMore(e.target.checked)} className="h-3.5 w-3.5 accent-[var(--accent)]" />
          Create more
        </label>
        <button type="button" onClick={onDone} className="h-8 rounded-lg px-3 text-[13px] text-fg/70 hover:text-fg">
          Cancel
        </button>
        <button type="submit" disabled={!title.trim() || create.isPending || (type?.kind === "subtask" && !parent)} className={cn(primary, "h-8")}>
          Create issue
          <kbd className="font-mono text-[11px] text-on-accent/75">⌘↵</kbd>
        </button>
      </div>
    </form>
  );
}
