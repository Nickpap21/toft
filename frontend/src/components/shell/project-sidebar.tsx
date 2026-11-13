"use client";

import Link from "next/link";
import { useParams, usePathname } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { Check, Plus } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { codeService, gitInfoQuery } from "@/lib/api/code.service";
import { inboxItems } from "@/components/home/home";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import { environmentsQuery, needsYouQuery, projectQuery, projectsQuery, workspaceServicesQuery } from "@/lib/queries";
import { cn } from "@/lib/utils";
import { healthLabel, projectHealth, runningIn } from "./health";
import { ACTIVE_ITEM, NAV_COUNT, NAV_IDLE, NAV_ROW, PROJECT_TABS, projectTabFor, type ProjectTab } from "./nav-config";
import { BackIcon, InboxIcon, UpDownIcon } from "./nav-icons";
import { PROJECT_LETTER } from "./sidebar-projects";
import { ALL, useEnvContext, withEnv } from "./env-context";
import { EnvSwitcher } from "./env-switcher";

/**
 * What the shell shows of the project a page under /projects/[id] belongs
 * to (D-136): the project, its environments and services, the environment
 * looked at (a service page pins its own, D-55), its repository (D-73) and
 * where each of its pages is. Uses the environment context, so its callers
 * sit inside a Suspense boundary.
 */
export function useProjectShell() {
  const params = useParams<{ id?: string; repo?: string }>();
  const pathname = usePathname();
  const { token } = useAuth();
  const projectId = params.id ?? "";

  const { data: project, isError } = useQuery({ ...projectQuery(projectId), enabled: !!token && !!projectId });
  const { data: workspace } = useQuery({
    ...workspaceServicesQuery,
    enabled: !!token && !!projectId,
    meta: { quietFailure: "The sidebar’s health tint, count, meta line and, on a service page, the environment it pins: on failure each is drawn as absent, never as “No services” or another environment, and the project’s pages show the failure." },
  });
  const { data: environments } = useQuery({ ...environmentsQuery(projectId), enabled: !!project });
  const ctx = useEnvContext(projectId, environments);
  const { data: git } = useQuery({ ...gitInfoQuery, enabled: !!token });
  const { data: repos } = useQuery({
    queryKey: ["repositories", projectId],
    queryFn: () => codeService.repositories(projectId),
    enabled: !!project && git?.enabled === true,
  });
  // The repository on screen (Code pages), else the project's first.
  const repo = repos?.find((r) => params.repo && r.name === decodeURIComponent(params.repo)) ?? repos?.[0];

  const services = workspace?.filter((s) => s.project_id === projectId);
  const base = `/projects/${projectId}`;
  const serviceId = /\/services\/([^/?]+)/.exec(pathname)?.[1];
  const onServicePage = !!serviceId && serviceId !== "new";
  const onService = onServicePage ? services?.find((s) => s.id === serviceId) : undefined;
  // A service page names the service's own environment (D-55): until the
  // services are read it has none to name, not the context's.
  const envUnknown = onServicePage && !workspace;
  const shown = onService ? environments?.find((e) => e.id === onService.environment_id) : envUnknown ? undefined : ctx.env;
  const envId = shown?.id ?? (onService ? ALL : ctx.envId);
  const scoped = envId === ALL ? services : services?.filter((s) => s.environment_id === envId || s.scope === "project");
  const href: Record<ProjectTab["id"], string> = {
    overview: withEnv(base, envId),
    code: `${base}/code`,
    issues: `${base}/issues`,
    docs: `${base}/docs`,
    services: withEnv(`${base}/services`, envId),
    environments: shown ? `${base}/environments/${shown.id}` : environments?.[0] ? `${base}/environments/${environments[0].id}` : `${base}#environments`,
    settings: `${base}/settings`,
  };
  const counts: Partial<Record<ProjectTab["id"], number>> = {
    code: repos?.reduce((n, r) => n + r.open_pulls, 0),
    issues: project?.open_issues,
    docs: project?.doc_pages,
    services: scoped?.length,
  };
  const running = runningIn(services, envId);
  return {
    projectId,
    // A project that doesn't exist, or isn't this workspace's: the page says so.
    missing: !projectId || isError,
    project,
    environments,
    services,
    ctx,
    onService,
    shown,
    envUnknown,
    repo,
    href,
    counts,
    current: projectTabFor(pathname, projectId),
    health: services?.length ? projectHealth(services).health : "idle",
    // Said to screen readers: the tile's tint is the only other signal.
    healthText: services?.length ? healthLabel(projectHealth(services)) : "",
    meta: scoped ? (scoped.length === 0 ? "No services" : `${running} running`) : undefined,
  };
}

/** The project's tile: its first letter, tinted by its health. */
function ProjectTile({ name, health, className }: { name: string; health: keyof typeof PROJECT_LETTER; className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={cn("flex h-6 w-6 shrink-0 items-center justify-center rounded-md text-xs font-bold", PROJECT_LETTER[health], className)}
    >
      {name.trim()[0]?.toUpperCase() ?? ""}
    </span>
  );
}

/** The project's name, which switches to another project. */
export function ProjectSwitcher({ variant }: { variant: "side" | "top" }) {
  const p = useProjectShell();
  const { token } = useAuth();
  const role = useOrgRole();
  const { data: projects } = useQuery({ ...projectsQuery, enabled: !!token });
  const name = p.project?.name ?? "";
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        title={variant === "side" ? name : undefined}
        className={cn(
          "flex min-w-0 items-center text-left font-semibold text-fg transition-colors hover:bg-raised focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus",
          variant === "side"
            ? "h-10 w-full gap-2.5 rounded-lg px-2 text-sm rail-collapsed:justify-center rail-collapsed:px-0"
            : "h-11 flex-1 gap-2 rounded-lg px-1 text-[15px]",
        )}
      >
        {variant === "side" && <ProjectTile name={name} health={p.health} />}
        <span className="min-w-0 flex-1 truncate rail-collapsed:sr-only">
          {name || "Project"}
          <span className="sr-only">
            {p.healthText && `, ${p.healthText}`}, switch project
          </span>
        </span>
        {variant === "side" && <UpDownIcon aria-hidden="true" className="h-4 w-4 shrink-0 text-muted rail-collapsed:hidden" />}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-64">
        <DropdownMenuLabel>Projects</DropdownMenuLabel>
        {[...(projects ?? [])]
          .sort((a, b) => a.name.localeCompare(b.name))
          .map((pr) => (
            <DropdownMenuItem key={pr.id} asChild>
              <Link href={`/projects/${pr.id}`}>
                <Check aria-hidden="true" className={cn("h-3.5 w-3.5 shrink-0", pr.id === p.projectId ? "opacity-100" : "opacity-0")} />
                <span className="min-w-0 flex-1 truncate">{pr.name}</span>
                {pr.id === p.projectId && <span className="sr-only">(current)</span>}
              </Link>
            </DropdownMenuItem>
          ))}
        <DropdownMenuSeparator />
        <DropdownMenuItem asChild>
          <Link href="/projects">All projects</Link>
        </DropdownMenuItem>
        {(role === "owner" || role === "developer") && (
          <DropdownMenuItem asChild className="text-link">
            <Link href="/projects?new">
              <Plus aria-hidden="true" className="h-3.5 w-3.5 shrink-0" />
              New project
            </Link>
          </DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** The environment switcher, for the sidebar or the phone's top bar. */
export function ProjectEnvSwitcher({ variant }: { variant: "side" | "top" }) {
  const p = useProjectShell();
  if (!p.project || p.envUnknown) return null;
  return (
    <EnvSwitcher
      projectId={p.projectId}
      environments={p.environments}
      services={p.services}
      current={p.onService}
      shown={p.shown}
      ctx={p.ctx}
      variant={variant}
      meta={p.meta}
    />
  );
}

/** How many Needs you rows the Inbox page shows: its own query, drawn by
 *  its own rows, so a badge never counts what the page doesn't, nor lags
 *  behind it. */
export function useNeedsYouCount(): number {
  const { token } = useAuth();
  const { data } = useQuery({ ...needsYouQuery(), enabled: !!token, retry: false });
  return data ? inboxItems(data).length : 0;
}

/** Inbox, with what needs you on it, as an icon (the project sidebar and
 *  the phone's top bar). */
export function InboxIconLink({ className }: { className?: string }) {
  const n = useNeedsYouCount();
  return (
    <Link
      href="/inbox"
      aria-label={n ? `Inbox, ${n} need you` : "Inbox"}
      title="Inbox"
      className={cn(
        "relative flex h-9 w-9 shrink-0 items-center justify-center rounded-lg text-fg/70 pointer-coarse:h-11 pointer-coarse:w-11 transition-colors hover:bg-raised hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus",
        className,
      )}
    >
      <InboxIcon aria-hidden="true" className="h-[17px] w-[17px]" />
      {!!n && (
        <span aria-hidden="true" className="absolute right-0 top-0.5 min-w-[17px] rounded-full bg-subtle px-1 text-center font-mono text-[11px] font-medium leading-[17px] text-fg">
          {n > 99 ? "99+" : n}
        </span>
      )}
    </Link>
  );
}

/**
 * The sidebar inside a project (D-136, board: ReworkService): back to the
 * workspace with Inbox, the project and environment switchers, ⌘K, then
 * the project's pages.
 */
export function ProjectSidebarTop({ search, idPrefix }: { search?: React.ReactNode; idPrefix: string }) {
  const p = useProjectShell();
  const { user } = useAuth();
  const org = user?.orgName ?? "Home";
  return (
    <>
      <div className="flex flex-col gap-3">
        <div className="flex items-center justify-between gap-2 rail-collapsed:flex-col rail-collapsed:gap-1">
          <Link
            href="/dashboard"
            aria-label={`Back to ${org}`}
            title={`Back to ${org}`}
            className="inline-flex h-8 min-w-0 items-center gap-1.5 rounded-lg pl-1 pr-2 pointer-coarse:min-h-11 text-[13px] font-medium text-fg/70 transition-colors hover:bg-raised hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus rail-collapsed:h-9 rail-collapsed:w-11 rail-collapsed:justify-center rail-collapsed:p-0"
          >
            <BackIcon aria-hidden="true" className="h-4 w-4 shrink-0" strokeWidth={2} />
            <span className="truncate rail-collapsed:sr-only">{org}</span>
          </Link>
          <InboxIconLink />
        </div>
        <div className="flex flex-col gap-1 pb-1">
          <ProjectSwitcher variant="side" />
          <ProjectEnvSwitcher variant="side" />
        </div>
        {search}
      </div>
      <nav aria-labelledby={`${idPrefix}-project`} className="-mx-2.5 flex min-h-0 flex-1 flex-col overflow-y-auto px-2.5">
        <p id={`${idPrefix}-project`} className="sr-only">
          {p.project?.name ?? "Project"}
        </p>
        <ul className="flex flex-col gap-0.5">
          {PROJECT_TABS.filter((t) => t.enabled || (t.id === "code" && !!p.repo)).map((t) => {
            const active = p.current === t.id;
            const n = p.counts[t.id];
            const Icon = t.icon;
            return (
              <li key={t.id}>
                <Link
                  href={p.href[t.id]}
                  aria-current={active ? "page" : undefined}
                  title={t.label}
                  className={cn(NAV_ROW, active ? ACTIVE_ITEM : NAV_IDLE)}
                >
                  <Icon aria-hidden="true" className="h-4 w-4 shrink-0" />
                  <span className="min-w-0 flex-1 truncate rail-collapsed:sr-only">{t.label}</span>
                  {!!n && <span className={NAV_COUNT}>{n}</span>}
                </Link>
              </li>
            );
          })}
        </ul>
      </nav>
    </>
  );
}
