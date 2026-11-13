import { BoxesIcon, BranchIcon, CodeIcon, CyclesIcon, DocsIcon, FolderIcon, GlobeIcon, GridIcon, HomeIcon, InboxIcon, InstanceIcon, IssuesIcon, LayersIcon, ServersIcon, ShieldIcon, SlidersIcon, type NavIcon } from "./nav-icons";

/**
 * The workspace sidebar (D-05). Modules that aren't built yet stay in the
 * config but `available: false` hides them (D-32: hidden, not shown
 * disabled, so the interface never claims what doesn't exist). R2 and R3
 * flip them on as their routes land.
 */
export interface NavItem {
  id: string;
  label: string;
  href: string;
  icon: NavIcon;
  available: boolean;
}

export interface NavSection {
  id: string;
  /** Visible heading; the top section has none. */
  label?: string;
  items: NavItem[];
}

export const NAV_SECTIONS: NavSection[] = [
  {
    id: "workspace",
    items: [
      { id: "home", label: "Home", href: "/dashboard", icon: HomeIcon, available: true },
      { id: "inbox", label: "Inbox", href: "/inbox", icon: InboxIcon, available: true },
      { id: "my-issues", label: "My issues", href: "/issues/mine", icon: IssuesIcon, available: true },
      // Shown while the workspace uses cycles (D-90; the sidebar checks).
      { id: "cycles", label: "Cycles", href: "/cycles", icon: CyclesIcon, available: true },
    ],
  },
  {
    id: "infrastructure",
    label: "Infrastructure",
    items: [
      { id: "servers", label: "Servers", href: "/servers", icon: ServersIcon, available: true },
      { id: "domains", label: "Domains", href: "/domains", icon: GlobeIcon, available: true },
      { id: "shared", label: "Workspace services", href: "/shared", icon: BoxesIcon, available: true },
      { id: "audit-log", label: "Audit log", href: "/audit-log", icon: ShieldIcon, available: true },
    ],
  },
];

export const SETTINGS_ITEM: NavItem = { id: "settings", label: "Settings", href: "/settings", icon: SlidersIcon, available: true };
/** The panel itself, operators only (D-68): above Settings, apart from the
 *  workspace's pages and from Servers, which are the servers apps run on. */
export const INSTANCE_ITEM: NavItem = { id: "instance", label: "Instance", href: "/instance", icon: InstanceIcon, available: true };
export const PROJECTS_ITEM: NavItem = { id: "projects", label: "Projects", href: "/projects", icon: FolderIcon, available: true };

/** The sections with unbuilt items removed. */
export function visibleSections(sections: NavSection[] = NAV_SECTIONS): NavSection[] {
  return sections
    .map((s) => ({ ...s, items: s.items.filter((i) => i.available) }))
    .filter((s) => s.items.length > 0);
}

/** The current nav item, as the boards draw it: the accent at 20% behind
 *  full-strength text (idle rows are at 70%). Screen readers get
 *  aria-current. The founders chose the board's look over R1's bar on the
 *  left edge (2026-10-14). */
export const ACTIVE_ITEM = "bg-selected text-fg";

/** A nav row as the board draws it: 36px, medium, the item's count on the
 *  right. Inactive rows are text at 70%, between muted and full. */
export const NAV_ROW =
  "flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-sm font-medium transition-colors pointer-coarse:min-h-11 rail-collapsed:justify-center rail-collapsed:px-0";
export const NAV_IDLE = "text-fg/70 hover:bg-raised hover:text-fg";
export const NAV_COUNT = "font-mono text-xs text-muted rail-collapsed:hidden";

/** aria-current for a nav link: "page" on the page itself, "true" on an
 *  ancestor of it, nothing otherwise. */
export function currentFor(pathname: string, href: string): "page" | "true" | undefined {
  if (pathname === href) return "page";
  return isActive(pathname, href) ? "true" : undefined;
}

/** The path the sidebar marks: an agent's request is reached from the
 *  Inbox, so Inbox is current on it, not Settings (D-145). */
export function navPathname(pathname: string): string {
  return pathname.startsWith("/settings/agents/requests/") ? `/inbox${pathname}` : pathname;
}

/** Segment-prefix match: /projects is active on /projects/abc, not on /projectsx. */
export function isActive(pathname: string, href: string): boolean {
  return pathname === href || pathname.startsWith(`${href}/`);
}

/** Every navigable destination, for ⌘K and the mobile bar's section name.
 *  Instance only for operators. */
export function allNavItems(operator = false): NavItem[] {
  // Sidebar order: the top section, Projects, the other sections, Instance, Settings.
  const [top, ...rest] = visibleSections();
  return [...top.items, PROJECTS_ITEM, ...rest.flatMap((s) => s.items), ...(operator ? [INSTANCE_ITEM] : []), SETTINGS_ITEM];
}

export interface ProjectTab {
  id: "overview" | "code" | "issues" | "docs" | "services" | "environments" | "settings";
  label: string;
  icon: NavIcon;
  enabled: boolean;
}

/** The project's pages, in the project sidebar (D-136; D-05, D-47). Code
 *  shows where the project has a repository (D-73). */
export const PROJECT_TABS: ProjectTab[] = [
  { id: "overview", label: "Overview", icon: GridIcon, enabled: true },
  { id: "code", label: "Code", icon: CodeIcon, enabled: false },
  { id: "issues", label: "Issues", icon: IssuesIcon, enabled: true },
  { id: "docs", label: "Docs", icon: DocsIcon, enabled: true },
  { id: "services", label: "Services", icon: LayersIcon, enabled: true },
  { id: "environments", label: "Environments", icon: BranchIcon, enabled: true },
  { id: "settings", label: "Settings", icon: SlidersIcon, enabled: true },
];

/** The project tab a page under /projects/<id> belongs to: a service and
 *  New service are Services, an environment is Environment (D-47, D-55). */
export function projectTabFor(pathname: string, projectId: string): ProjectTab["id"] | null {
  const base = `/projects/${projectId}`;
  if (pathname === base) return "overview";
  if (pathname.startsWith(`${base}/code`)) return "code";
  if (pathname.startsWith(`${base}/issues`)) return "issues";
  if (pathname.startsWith(`${base}/docs`)) return "docs";
  if (pathname.startsWith(`${base}/services`)) return "services";
  if (pathname.startsWith(`${base}/environments`)) return "environments";
  if (pathname.startsWith(`${base}/settings`)) return "settings";
  return null;
}
