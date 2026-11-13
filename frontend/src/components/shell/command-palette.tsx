"use client";

import { useDeferredValue, useEffect, useRef, useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import { Command } from "cmdk";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeftRight,
  ArrowRight,
  Box,
  FileText,
  Folder,
  Layers,
  LogOut,
  PanelLeft,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  Server,
  Square,
  SunMoon,
  X,
  type LucideIcon,
} from "lucide-react";
import { toast } from "sonner";
import { useAuth, useIsOperator } from "@/context/AuthContext";
import { useTheme } from "@/components/theme-provider";
import { deploymentHref } from "@/components/home/home";
import { CapacityDialog, capacityWarningFrom, type CapacityWarning } from "@/components/ui/capacity-dialog";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { deploymentService } from "@/lib/api/deployment.service";
import { searchWorkspace } from "@/lib/api/issue.service";
import { ICON, STATUS } from "@/components/issues/meta";
import { TypeIcon } from "@/components/issues/deep";
import { projectsQuery, serversQuery, workspaceEnvironmentsQuery, workspaceServicesQuery } from "@/lib/queries";
import { cn } from "@/lib/utils";
import { buildCommands, filterCommands, type CommandIcon, type CommandItem } from "./commands";
import { projectHealth } from "./health";
import { PROJECT_LETTER } from "./sidebar-projects";
import { useSidebar } from "./sidebar-state";

interface CommandPaletteProps {
  open: boolean;
  /** Typed in the search when it opens. */
  initialQuery?: string;
  onOpenChange: (open: boolean) => void;
  onCloseAutoFocus?: (e: Event) => void;
}

type DeployAction = Extract<CommandItem["action"], { type: "deploy" }>;

// --muted falls under 4.5:1 on the selected row's grey (Graphite).
const rowMuted = "text-muted group-data-[selected=true]:text-[var(--selected-muted)]";

const ICONS: Record<CommandIcon, LucideIcon> = {
  deploy: Layers,
  rollback: RotateCcw,
  restart: RefreshCw,
  stop: Square,
  new: Plus,
  go: ArrowRight,
  project: Folder,
  service: Box,
  server: Server,
  theme: SunMoon,
  workspace: ArrowLeftRight,
  sidebar: PanelLeft,
  "sign-out": LogOut,
};

/**
 * ⌘K: cmdk inside our Radix Dialog, so it has a title and description
 * (AGENTS.md). The provider returns focus to wherever it was opened from.
 * A deploy started here outlives the palette, so it runs from here, with the
 * capacity dialog when the server is short.
 */
export function CommandPalette({ open, initialQuery = "", onOpenChange, onCloseAutoFocus }: CommandPaletteProps) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const [capacity, setCapacity] = useState<{ warning: CapacityWarning; target: DeployAction } | null>(null);
  const deploy = useMutation({
    mutationFn: ({ target, force }: { target: DeployAction; force: boolean }) =>
      deploymentService.trigger(target.serviceId, {}, force),
    onSuccess: (d, { target }) => {
      toast.success(`Deploying ${target.name}`, {
        action: { label: "Follow", onClick: () => router.push(deploymentHref(target.projectId, target.serviceId, d.id)) },
      });
      queryClient.invalidateQueries({ queryKey: workspaceServicesQuery.queryKey });
      queryClient.invalidateQueries({ queryKey: ["service", target.serviceId] });
    },
    onError: (err: unknown, { target }) => {
      const warning = capacityWarningFrom(err);
      if (warning) {
        setCapacity({ warning, target });
        return;
      }
      toast.error(err instanceof Error && err.message ? `${target.name}: ${err.message}` : `Failed to deploy ${target.name}`);
    },
  });

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent
          showClose={false}
          onCloseAutoFocus={onCloseAutoFocus}
          className="top-[12vh] mx-0 w-[calc(100%-2rem)] max-w-[640px] translate-y-0 overflow-hidden rounded-xl p-0"
        >
          <DialogTitle className="sr-only">Command palette</DialogTitle>
          <DialogDescription className="sr-only">
            Go to a page; find a project, service, server, issue or pull request; deploy, roll back, restart or stop a
            service; or switch theme or workspace. Start with # for issues only, ! for pull requests only. Arrow keys move, Enter runs, Tab changes the project scope, Escape closes.
          </DialogDescription>
          {/* Mounted only while open, so each opening starts with an empty search. */}
          {open && (
            <PaletteBody initialQuery={initialQuery} close={() => onOpenChange(false)} onDeploy={(target) => deploy.mutate({ target, force: false })} />
          )}
        </DialogContent>
      </Dialog>
      <CapacityDialog
        warning={capacity?.warning ?? null}
        serviceName={capacity?.target.name}
        onCancel={() => setCapacity(null)}
        onDeployAnyway={() => {
          if (capacity) deploy.mutate({ target: capacity.target, force: true });
          setCapacity(null);
        }}
      />
    </>
  );
}

/** The project of a page under /projects/<id>, which the palette opens scoped to. */
const pageProject = (pathname: string) => /^\/projects\/([^/?#]+)/.exec(pathname)?.[1] ?? "";

function PaletteBody({ initialQuery, close, onDeploy }: { initialQuery: string; close: () => void; onDeploy: (target: DeployAction) => void }) {
  const router = useRouter();
  const pathname = usePathname();
  const { token, user, switchOrg, logout } = useAuth();
  const isOperator = useIsOperator();
  const { pref, theme, setPref } = useTheme();
  const { collapsed, toggle } = useSidebar();
  const [query, setQuery] = useState(initialQuery);
  const [touch] = useState(() => typeof window !== "undefined" && window.matchMedia?.("(max-width: 767px)").matches);

  // Fetched on open; most of it is already cached by the sidebar and pages.
  const enabled = !!token;
  const { data: projects } = useQuery({ ...projectsQuery, enabled });
  // Every service of the workspace in one request, shared with the sidebar.
  const { data: services } = useQuery({ ...workspaceServicesQuery, enabled });
  // A protected environment's Deploy opens its confirm instead (D-55).
  const { data: environments } = useQuery({ ...workspaceEnvironmentsQuery, enabled });
  const { data: servers } = useQuery({ ...serversQuery, enabled });

  const [scopeChoice, setScope] = useState<string | null>(null);
  // Until a choice is made, the page's project; "" is no scope.
  const scope = scopeChoice ?? pageProject(pathname ?? "");
  const scoped = projects?.find((p) => p.id === scope);
  const scopeHealth = scoped ? projectHealth((services ?? []).filter((s) => s.project_id === scoped.id)) : null;

  const commands = buildCommands({
    projects,
    services,
    environments,
    servers,
    orgs: user?.orgs,
    currentOrgId: user?.orgId,
    pref,
    theme,
    isOperator,
    role: user?.role,
    sidebarCollapsed: collapsed,
    touch,
  });
  // "#" lists only issues, "!" only pull requests; plain text both, after
  // the commands (D-81).
  const lead = query.trimStart()[0];
  const only = lead === "#" ? "issues" : lead === "!" ? "pulls" : undefined;
  const term = useDeferredValue((only ? query.trimStart().slice(1) : query).trim());
  const wantWork = !!only || (lead !== ">" && term.length >= 2);
  const work = useQuery({
    queryKey: ["palette-search", term, only, scoped?.id],
    queryFn: () => searchWorkspace(term, only, scoped?.id),
    enabled: enabled && wantWork,
    placeholderData: (prev) => prev,
    staleTime: 10_000,
  });
  const results = only ? [] : filterCommands(commands, query, scoped?.id);
  const workRows = wantWork && work.data ? [...work.data.issues.map((i) => ({ kind: "issue" as const, ...i })), ...work.data.pulls.map((p) => ({ kind: "pull" as const, ...p }))] : [];
  // A new search or scope starts at the top, where its first row is.
  const list = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (list.current) list.current.scrollTop = 0;
  }, [query, scope]);
  // Pages come with plain searches, after issues and pull requests (D-86).
  const docRows = wantWork && !only && work.data ? (work.data.docs ?? []) : [];
  const count = results.reduce((n, g) => n + g.items.length, 0) + workRows.length + docRows.length;

  function run(item: CommandItem) {
    close();
    if (item.href) {
      router.push(item.href);
      return;
    }
    switch (item.action?.type) {
      case "deploy":
        onDeploy(item.action);
        break;
      case "theme":
        setPref(item.action.pref);
        break;
      case "toggle-sidebar":
        toggle();
        break;
      case "switch-org":
        switchOrg(item.action.orgId).catch((err: unknown) =>
          toast.error(err instanceof Error ? err.message : "Failed to switch workspace"),
        );
        break;
      case "sign-out":
        logout();
        break;
    }
  }

  // Tab scopes to the selected row's project, or removes the scope;
  // Backspace in an empty search removes it too (board: Palette).
  function onKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === "Tab" && !e.shiftKey && !e.altKey && !e.ctrlKey && !e.metaKey) {
      // Not the dialog's focus trap's Tab: focus stays in the search.
      e.preventDefault();
      e.stopPropagation();
      if (scoped) {
        setScope("");
        return;
      }
      // cmdk keeps the selection; it marks the row and its value.
      const selected = e.currentTarget
        .closest("[cmdk-root]")
        ?.querySelector('[cmdk-item][data-selected="true"]')
        ?.getAttribute("data-value");
      const project = commands.find((c) => c.id === selected)?.projectId;
      if (project) {
        setScope(project);
        setQuery("");
      }
    } else if (e.key === "Backspace" && scoped && !e.currentTarget.value) {
      e.preventDefault();
      setScope("");
    }
  }

  return (
    // cmdk names its input from `label` (aria-labelledby). Its vim bindings
    // are off: Ctrl+K there would move the selection instead of closing.
    <Command
      shouldFilter={false}
      loop
      vimBindings={false}
      label="Search or run a command"
      className="flex flex-col text-fg"
    >
      <div className="flex h-[52px] items-center gap-2.5 border-b border-subtle px-4">
        <Search aria-hidden="true" className="h-[18px] w-[18px] shrink-0 text-muted" strokeWidth={1.8} />
        {scoped && (
          <span className="inline-flex h-6 shrink-0 items-center gap-1.5 rounded-md bg-raised pl-2 pr-1 text-xs font-medium text-fg">
            <span
              aria-hidden="true"
              className={cn(
                "flex h-3.5 w-3.5 items-center justify-center rounded text-[10px] font-bold",
                PROJECT_LETTER[scopeHealth?.health ?? "idle"],
              )}
            >
              {scoped.name.trim()[0]?.toUpperCase() ?? "?"}
            </span>
            {scoped.name}
            <button
              type="button"
              aria-label={`Search all of ${user?.orgName ?? "the workspace"}, not only ${scoped.name}`}
              onClick={() => setScope("")}
              // Keyboards use Tab or Backspace; focus stays in the search.
              tabIndex={-1}
              className="flex h-4 w-4 items-center justify-center rounded text-muted hover:text-fg"
            >
              <X className="h-3 w-3" aria-hidden="true" />
            </button>
          </span>
        )}
        <Command.Input
          value={query}
          onValueChange={setQuery}
          onKeyDown={onKeyDown}
          placeholder="Search or run a command…"
          className="h-9 min-w-0 flex-1 bg-transparent text-[15px] text-fg placeholder:text-muted focus-visible:outline-none"
        />
        <kbd aria-hidden="true" className="rounded border border-subtle px-1.5 font-mono text-[11px] leading-[18px] text-muted max-md:hidden">
          esc
        </kbd>
      </div>
      <Command.List ref={list} className="max-h-[min(60vh,28rem)] overflow-y-auto overscroll-contain p-1.5">
        <Command.Empty className="px-3 py-6 text-center text-sm text-muted">No results for “{query.trim()}”</Command.Empty>
        {results.map((group) => (
          <Command.Group
            key={group.group}
            heading={scoped && group.group === "Services" ? "Across the project" : group.group}
            className="[&_[cmdk-group-heading]]:px-2.5 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:pt-2.5 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-muted"
          >
            {group.items.map((item) => {
              const Icon = ICONS[item.icon];
              return (
                <Command.Item
                  key={item.id}
                  value={item.id}
                  onSelect={() => run(item)}
                  // The Palette board's selected row: a grey row, the icon in
                  // the link colour, and ↵ on it.
                  className="group flex h-10 cursor-pointer items-center gap-3 rounded-lg px-2.5 text-sm text-fg data-[selected=true]:bg-[color-mix(in_oklab,var(--raised)_50%,var(--border))]"
                >
                  <Icon
                    aria-hidden="true"
                    strokeWidth={1.9}
                    className="h-4 w-4 shrink-0 text-muted group-data-[selected=true]:text-link"
                  />
                  <span className="min-w-0 flex-1 truncate">
                    {item.label}
                    {item.detail && <>{" "}<span className={rowMuted}>{item.detail}</span></>}
                  </span>
                  {item.hint && (
                    <span className={cn("shrink-0 truncate text-xs", rowMuted, item.monoHint && "font-mono")}>{item.hint}</span>
                  )}
                  <kbd aria-hidden="true" className="hidden shrink-0 font-mono text-xs text-[var(--selected-muted)] md:group-data-[selected=true]:inline">
                    ↵
                  </kbd>
                </Command.Item>
              );
            })}
          </Command.Group>
        ))}
        {workRows.length > 0 && (
          <Command.Group
            heading={only === "issues" ? "Issues" : only === "pulls" ? "Pull requests" : scoped ? "Across the project" : "Issues and pull requests"}
            className="[&_[cmdk-group-heading]]:px-2.5 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:pt-2.5 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-muted"
          >
            {workRows.map((r) => {
              const issue = r.kind === "issue";
              const href = issue
                ? `/projects/${r.project_id}/issues/${r.number}`
                : `/projects/${r.project_id}/code/${encodeURIComponent(r.repository)}/pulls/${r.number}`;
              const state = issue
                ? { text: STATUS[r.status].name, tint: STATUS[r.status].tint }
                : r.state === "open"
                  ? { text: "Open", tint: "text-status-running" }
                  : r.state === "merged"
                    ? { text: "Merged", tint: "text-link" }
                    : { text: "Closed", tint: "text-muted" };
              return (
                <Command.Item
                  key={`${r.kind}:${r.project_id}:${issue ? "" : r.repository}:${r.number}`}
                  value={`${r.kind}:${r.project_id}:${issue ? "" : r.repository}:${r.number}`}
                  onSelect={() => {
                    close();
                    router.push(href);
                  }}
                  className="group flex h-10 cursor-pointer items-center gap-3 rounded-lg px-2.5 text-sm text-fg data-[selected=true]:bg-[color-mix(in_oklab,var(--raised)_50%,var(--border))]"
                >
                  {issue && r.type ? (
                    <TypeIcon type={r.type} size={16} />
                  ) : (
                    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.9} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" className={cn("shrink-0", issue ? "text-status-pending" : "text-link")}>
                      <path d={issue ? "M3 12a9 9 0 1 0 18 0a9 9 0 1 0-18 0M11 12a1 1 0 1 0 2 0a1 1 0 1 0-2 0" : ICON.pr} />
                    </svg>
                  )}
                  <span className={cn("shrink-0 font-mono text-xs", rowMuted)}>{issue ? r.key : `!${r.number}`}</span>
                  <span className="min-w-0 flex-1 truncate">
                    {r.title}
                    {!scoped && <>{" "}<span className={rowMuted}>{r.project}</span></>}
                  </span>
                  <span className={cn("shrink-0 text-xs", state.tint, "group-data-[selected=true]:text-[var(--selected-muted)]")}>{state.text}</span>
                </Command.Item>
              );
            })}
          </Command.Group>
        )}
        {docRows.length > 0 && (
          <Command.Group
            heading="Docs"
            className="[&_[cmdk-group-heading]]:px-2.5 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:pt-2.5 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-muted"
          >
            {docRows.map((d) => (
              <Command.Item
                key={`doc:${d.id}`}
                value={`doc:${d.id}`}
                onSelect={() => {
                  close();
                  router.push(`/projects/${d.project_id}/docs/${d.id}`);
                }}
                className="group flex h-10 cursor-pointer items-center gap-3 rounded-lg px-2.5 text-sm text-fg data-[selected=true]:bg-[color-mix(in_oklab,var(--raised)_50%,var(--border))]"
              >
                <FileText aria-hidden="true" strokeWidth={1.9} className="h-4 w-4 shrink-0 text-muted group-data-[selected=true]:text-link" />
                <span className="min-w-0 flex-1 truncate">
                  {d.title}
                  {!scoped && <>{" "}<span className={rowMuted}>{d.project}</span></>}
                </span>
              </Command.Item>
            ))}
          </Command.Group>
        )}
      </Command.List>
      <div aria-hidden="true" className="flex flex-wrap items-center gap-4 border-t border-subtle px-4 py-2.5 text-xs text-muted max-md:hidden">
        <span>
          <kbd className="font-mono text-fg/75">↑↓</kbd> move
        </span>
        <span>
          <kbd className="font-mono text-fg/75">↵</kbd> run
        </span>
        <span>
          <kbd className="font-mono text-fg/75">tab</kbd> change scope
        </span>
        <span className="flex-1" />
        <span>
          Prefix <kbd className="font-mono text-fg/75">&gt;</kbd> commands · <kbd className="font-mono text-fg/75">#</kbd> issues ·{" "}
          <kbd className="font-mono text-fg/75">!</kbd> PRs
        </span>
      </div>
      <p aria-live="polite" className="sr-only">
        {query.trim() ? (count === 1 ? "1 result" : `${count} results`) : ""}
      </p>
    </Command>
  );
}
