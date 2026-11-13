"use client";

import { Suspense, useId } from "react";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { PanelLeftClose, PanelLeftOpen } from "lucide-react";
import { WorkspaceSwitcher } from "@/components/workspace-switcher";
import { useAuth, useIsOperator } from "@/context/AuthContext";
import { serversQuery, workspaceServicesQuery } from "@/lib/queries";
import { inboxCountsQuery } from "@/lib/api/inbox.service";
import { cycleService } from "@/lib/api/issue.service";
import { workspaceIssueSettingsQuery } from "@/components/issues/cycles";
import { panelStatusQuery, upgradeQuery } from "@/lib/api/instance.service";
import { needsAttention } from "@/components/instance/instance-view";
import { waitingForAgent } from "@/components/servers/agent";
import { cn } from "@/lib/utils";
import { ACTIVE_ITEM, currentFor, INSTANCE_ITEM, isActive, NAV_COUNT, NAV_IDLE, NAV_ROW, navPathname, SETTINGS_ITEM, visibleSections, type NavItem } from "./nav-config";
import { SidebarProjects } from "./sidebar-projects";
import { ProjectSidebarTop, useNeedsYouCount } from "./project-sidebar";
import { useSidebar } from "./sidebar-state";
import { UserMenu } from "./user-menu";

function NavLink({ item, pathname, collapsed, count, badge }: { item: NavItem; pathname: string; collapsed: boolean; count?: number; badge?: React.ReactNode }) {
  const active = isActive(pathname, item.href);
  const Icon = item.icon;
  return (
    <Link
      href={item.href}
      aria-current={currentFor(pathname, item.href)}
      title={collapsed ? item.label : undefined}
      className={cn(NAV_ROW, "flex-1", active ? ACTIVE_ITEM : NAV_IDLE)}
    >
      <Icon aria-hidden="true" className="h-4 w-4 shrink-0" />
      <span className="min-w-0 flex-1 truncate rail-collapsed:sr-only">{item.label}</span>
      {!!count && <span className={NAV_COUNT}>{count}</span>}
      {badge}
    </Link>
  );
}

interface SidebarProps {
  pathname: string;
  /** "rail" is the desktop sidebar, which can collapse; "sheet" is the
   *  mobile navigation drawer, always expanded. */
  variant: "rail" | "sheet";
  /** The ⌘K trigger, supplied by the shell. */
  search?: React.ReactNode;
}

export function Sidebar({ pathname: path, variant, search }: SidebarProps) {
  const pathname = navPathname(path);
  const { collapsed: railCollapsed, toggle } = useSidebar();
  const collapsed = variant === "rail" && railCollapsed;

  // The board's counts, from queries the shell already makes.
  const { token } = useAuth();
  const isOperator = useIsOperator();
  // The board's chip on Instance while a release is out (D-68).
  const upgrade = useQuery({ ...upgradeQuery, enabled: isOperator, retry: false }).data;
  const update = upgrade?.install === "installer" && upgrade.available && upgrade.run?.outcome !== "running";
  // The board's dot while the panel needs an operator; checked every minute
  // here, every 5 seconds on Instance's own pages.
  const panel = useQuery({ ...panelStatusQuery, enabled: isOperator, retry: false, refetchInterval: 60_000 });
  const alert = needsAttention(panel.data, upgrade, panel.dataUpdatedAt);
  const { data: servers } = useQuery({ ...serversQuery, enabled: !!token });
  const { data: services } = useQuery({ ...workspaceServicesQuery, enabled: !!token });
  const { data: inbox } = useQuery({ ...inboxCountsQuery, enabled: !!token, retry: false });
  const needsYou = useNeedsYouCount();
  const settings = useQuery({ ...workspaceIssueSettingsQuery, enabled: !!token, retry: false });
  const cyclesOn = !!settings.data?.cycles.enabled;
  const sections = visibleSections().map((s) => ({ ...s, items: s.items.filter((i) => i.id !== "cycles" || cyclesOn) }));
  const current = useQuery({ queryKey: ["cycles", "current"], queryFn: () => cycleService.list("current", undefined, 1), enabled: cyclesOn, staleTime: 60_000 });
  const cur = current.data?.data[0];
  const counts: Record<string, number | undefined> = {
    "my-issues": inbox?.my_issues,
    // Servers waiting for their first agent aren't counted (D-153).
    servers: servers?.filter((s) => !waitingForAgent(s)).length,
    shared: services?.filter((s) => s.project_id === "").length,
  };
  // Both the rail and the mobile sheet can be mounted at once.
  const idPrefix = useId();
  // Inside a project the sidebar is the project's (D-136).
  const inProject = /^\/projects\/[^/]+/.test(pathname);

  const toggleButton = variant === "rail" && (
    <button
      type="button"
      onClick={toggle}
      aria-expanded={!railCollapsed}
      aria-controls="sidebar-rail"
      aria-label={railCollapsed ? "Expand sidebar" : "Collapse sidebar"}
      title={railCollapsed ? "Expand sidebar" : "Collapse sidebar"}
      className={cn(
        "flex h-9 w-9 shrink-0 items-center justify-center rounded-lg text-muted transition hover:bg-raised hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:h-11 pointer-coarse:w-11 rail-collapsed:static rail-collapsed:w-full",
        !inProject &&
          "pointer-events-none absolute right-0 top-0 opacity-0 focus-visible:pointer-events-auto focus-visible:opacity-100 group-hover/sidebar:pointer-events-auto group-hover/sidebar:opacity-100 rail-collapsed:pointer-events-auto rail-collapsed:opacity-100",
      )}
    >
      {railCollapsed ? (
        <PanelLeftOpen aria-hidden="true" className="h-4 w-4" strokeWidth={1.8} />
      ) : (
        <PanelLeftClose aria-hidden="true" className="h-4 w-4" strokeWidth={1.8} />
      )}
    </button>
  );

  if (inProject) {
    return (
      <div className="group/sidebar flex h-full min-h-0 flex-col gap-3 px-2.5 pb-3.5 pt-2.5">
        <Suspense>
          <ProjectSidebarTop search={search} idPrefix={idPrefix} />
        </Suspense>
        <div className="flex items-center gap-1 border-t border-subtle/70 pt-2.5 rail-collapsed:flex-col-reverse">
          <div className="min-w-0 flex-1 rail-collapsed:w-full">
            <UserMenu />
          </div>
          {toggleButton}
        </div>
      </div>
    );
  }

  return (
    <div className="group/sidebar flex h-full min-h-0 flex-col gap-[18px] px-2.5 pb-3.5 pt-3">
      <div className="flex flex-col gap-2.5">
        <WorkspaceSwitcher />
        {search}
      </div>

      <nav aria-label="Primary" className="-mx-2.5 flex min-h-0 flex-1 flex-col gap-[18px] overflow-y-auto px-2.5">
        {sections.map((section) => (
          <div key={section.id} className="flex flex-col gap-0.5">
            {section.label && (
              <p id={`${idPrefix}-${section.id}`} className="flex h-7 items-center px-2.5 text-xs font-medium text-muted rail-collapsed:sr-only">
                {section.label}
              </p>
            )}
            <ul aria-labelledby={section.label ? `${idPrefix}-${section.id}` : undefined} className="flex flex-col gap-0.5">
              {section.items.map((item) => (
                <li key={item.id}>
                  <NavLink
                    item={item}
                    pathname={pathname}
                    collapsed={collapsed}
                    count={counts[item.id]}
                    badge={
                      item.id === "inbox" && !!needsYou ? (
                        <span className="min-w-5 rounded-full bg-subtle px-1.5 text-center font-mono text-xs leading-5 text-fg rail-collapsed:sr-only">
                          {needsYou}
                          <span className="sr-only"> need you</span>
                        </span>
                      ) : item.id === "cycles" && cur ? (
                        <span className={NAV_COUNT}>{cur.days_left === 0 ? "last day" : `${cur.days_left}d left`}</span>
                      ) : null
                    }
                  />
                </li>
              ))}
            </ul>
            {section.id === "workspace" && (
              <div className="mt-[18px]">
                <SidebarProjects pathname={pathname} labelId={`${idPrefix}-projects`} />
              </div>
            )}
          </div>
        ))}
      </nav>

      <div className="flex flex-col gap-0.5 border-t border-subtle/70 pt-2.5">
        {/* The panel itself, for operators (D-68): apart from Servers, which
            are the servers apps run on. */}
        {isOperator && (
          <NavLink
            item={INSTANCE_ITEM}
            pathname={pathname}
            collapsed={collapsed}
            badge={
              (update || alert) && (
                <>
                  {update && (
                    <span className="rounded bg-[color-mix(in_oklab,var(--accent)_28%,transparent)] px-1.5 text-[11px] font-medium leading-[18px] text-fg rail-collapsed:hidden">
                      Update<span className="sr-only">: Toft {upgrade.latest} is available</span>
                    </span>
                  )}
                  {alert && (
                    <span className="h-1.5 w-1.5 flex-none rounded-full bg-status-pending">
                      <span className="sr-only">: needs attention</span>
                    </span>
                  )}
                </>
              )
            }
          />
        )}
        {/* Settings, and over its right end the rail's collapse toggle. The
            board doesn't draw it, so it shows while the pointer is on the
            sidebar or the keyboard is on it, and always on the collapsed rail
            (below Settings there). Hidden, it takes no width and no clicks, so
            Settings spans the row as the other items do. */}
        <div className="relative flex items-center gap-0.5 rail-collapsed:flex-col">
          <NavLink item={SETTINGS_ITEM} pathname={pathname} collapsed={collapsed} />
          {toggleButton}
        </div>
        <UserMenu />
      </div>
    </div>
  );
}
