"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import { projectsQuery, workspaceServicesQuery } from "@/lib/queries";
import { cn } from "@/lib/utils";
import { HealthDot } from "./health-dot";
import { projectHealth, type ProjectHealth } from "./health";
import {
  ACTIVE_ITEM,
  currentFor,
  isActive,
  NAV_COUNT,
  NAV_IDLE,
  NAV_ROW,
  PROJECTS_ITEM,
} from "./nav-config";
import { PlusIcon } from "./nav-icons";

const ProjectsIcon = PROJECTS_ITEM.icon;

/** How many projects the sidebar lists before "All projects". */
export const SIDEBAR_PROJECT_LIMIT = 15;

/** The project's letter takes its health's tint, as on the board: green
 *  while it runs, red while something fails; plain when nothing runs. */
export const PROJECT_LETTER: Record<ProjectHealth, string> = {
  running: "bg-status-running-soft text-status-running-fg",
  failed: "bg-status-failed-soft text-status-failed-fg",
  building: "bg-status-building-soft text-status-building-fg",
  unhealthy: "bg-status-pending-soft text-status-pending-fg",
  idle: "bg-raised text-fg/75",
};

export function SidebarProjects({
  pathname,
  labelId,
}: {
  pathname: string;
  labelId: string;
}) {
  const { token } = useAuth();
  const role = useOrgRole();
  const { data: projects } = useQuery({
    ...projectsQuery,
    enabled: !!token,
    meta: { quietFailure: "Navigation only: a failed read draws no rows and no count, “No projects yet” needs an answer, and All projects leads to the page that shows the failure." },
  });
  const visible = [...(projects ?? [])]
    .sort((a, b) => a.name.localeCompare(b.name))
    .slice(0, SIDEBAR_PROJECT_LIMIT);
  // Every service of the workspace in one request, shared with ⌘K.
  const { data: services } = useQuery({
    ...workspaceServicesQuery,
    enabled: !!token,
    meta: { quietFailure: "Only tints the rows: a failed read leaves them plain with no dot, as before the read answers, and states nothing about a project’s services." },
  });
  const allActive = pathname === "/projects";

  return (
    <div className="flex flex-col gap-0.5">
      <div className="flex h-7 items-center justify-between pl-2.5 pr-1 rail-collapsed:sr-only">
        <p id={labelId} className="text-xs font-medium text-muted">
          Projects
        </p>
        {(role === "owner" || role === "developer") && (
          <Link
            href="/projects?new"
            aria-label="New project"
            title="New project"
            className="flex h-7 w-7 items-center justify-center rounded-md text-muted transition-colors hover:bg-raised hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
          >
            <PlusIcon
              aria-hidden="true"
              className="h-3.5 w-3.5"
              strokeWidth={2}
            />
          </Link>
        )}
      </div>
      <ul
        aria-labelledby={labelId}
        className="flex flex-col gap-0.5 rail-collapsed:hidden"
      >
        {projects?.length === 0 && (
          <li className="px-2.5 py-1.5 text-[13px] leading-[19px] text-muted">No projects yet</li>
        )}
        {visible.map((p) => {
          const active = isActive(pathname, `/projects/${p.id}`);
          const projectServices = services?.filter(
            (s) => s.project_id === p.id,
          );
          // A project with no services (docs only, or new) shows no state.
          const summary = projectServices?.length
            ? projectHealth(projectServices)
            : null;
          return (
            <li key={p.id}>
              <Link
                href={`/projects/${p.id}`}
                aria-current={currentFor(pathname, `/projects/${p.id}`)}
                className={cn(NAV_ROW, active ? ACTIVE_ITEM : NAV_IDLE)}
              >
                <span
                  aria-hidden="true"
                  className={cn(
                    "flex h-[18px] w-[18px] shrink-0 items-center justify-center rounded-sm text-[11px] font-bold",
                    PROJECT_LETTER[summary?.health ?? "idle"],
                  )}
                >
                  {p.name.trim()[0]?.toUpperCase() ?? "?"}
                </span>
                <span className="min-w-0 flex-1 truncate">{p.name}</span>
                <HealthDot summary={summary} />
              </Link>
            </li>
          );
        })}
      </ul>
      <Link
        href="/projects"
        aria-current={allActive ? "page" : undefined}
        title="All projects"
        className={cn(NAV_ROW, allActive ? ACTIVE_ITEM : NAV_IDLE)}
      >
        <ProjectsIcon aria-hidden="true" className="h-4 w-4 shrink-0" />
        <span className="min-w-0 flex-1 truncate rail-collapsed:sr-only">
          All projects
        </span>
        {!!projects?.length && (
          <span className={NAV_COUNT}>{projects.length}</span>
        )}
      </Link>
    </div>
  );
}
