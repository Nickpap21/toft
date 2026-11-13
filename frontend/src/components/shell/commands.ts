import { defaultFilter } from "cmdk";
import type { OrgMembership } from "@/lib/api/org.service";
import type { Project } from "@/lib/api/project.service";
import type { Server } from "@/lib/api/server.service";
import type { WorkspaceService } from "@/lib/api/service.service";
import type { WorkspaceEnvironment } from "@/lib/api/environment.service";
import { serviceHref } from "@/components/home/home";
import { isServing, isStoppable } from "@/lib/status";
import { otherSide } from "@/lib/theme/resolve";
import { prefLabel, THEME_PREFS, THEMES, type ThemeId, type ThemePref } from "@/lib/theme/themes";
import { allNavItems } from "./nav-config";

/**
 * ⌘K (D-32, D-50): go anywhere; find projects, services and servers; deploy,
 * roll back, restart or stop a service; create things; switch theme or
 * workspace. Scoped to a project, it lists that project's actions and
 * services first (board: Palette).
 */

export type CommandGroup = "Actions" | "Projects" | "Services" | "Servers" | "Go to" | "Theme";
/** The order of groups that match a query equally well, and of a scoped
 *  palette's groups. */
export const GROUP_ORDER: CommandGroup[] = ["Actions", "Projects", "Services", "Servers", "Go to", "Theme"];
/** With nothing typed and no scope, what there is comes before what to do. */
const EMPTY_ORDER: CommandGroup[] = ["Projects", "Services", "Servers", "Actions", "Go to", "Theme"];
/** What a ">" query lists. */
const COMMANDS: ReadonlySet<CommandGroup> = new Set(["Actions", "Go to", "Theme"]);
/** At most this many projects, services or servers each, after filtering. */
export const GROUP_LIMIT = 8;
const CAPPED: ReadonlySet<CommandGroup> = new Set(["Projects", "Services", "Servers"]);

export type CommandAction =
  | { type: "deploy"; serviceId: string; projectId: string; name: string }
  | { type: "theme"; pref: ThemePref }
  | { type: "switch-org"; orgId: string }
  | { type: "toggle-sidebar" }
  | { type: "sign-out" };

/** The row's icon (board: Palette). */
export type CommandIcon =
  | "deploy" | "rollback" | "restart" | "stop" | "new" | "go" | "project" | "service" | "server"
  | "theme" | "workspace" | "sidebar" | "sign-out";

export interface CommandItem {
  id: string;
  group: CommandGroup;
  icon: CommandIcon;
  label: string;
  /** Muted, after the label: "storefront / api from main". */
  detail?: string;
  /** The project the item belongs to, for the scope chip; "" for a
   *  workspace service's, which belongs to none. */
  projectId?: string;
  /** Shown at the right, muted: a project name, an IP, "Current". */
  hint?: string;
  /** Extra words the search matches. */
  keywords: string[];
  href?: string;
  action?: CommandAction;
  /** Infrastructure values (IPs) render in mono. */
  monoHint?: boolean;
  /** Listed only once something is typed: one per project or service would
   *  fill the empty palette. "unscoped": also listed, untyped, when the
   *  palette is scoped to the item's project. */
  searchOnly?: boolean | "unscoped";
}

export interface CommandSources {
  projects?: Project[];
  /** Every service of the workspace, of every scope. */
  services?: WorkspaceService[];
  /** Every environment of the workspace: a protected one's Deploy confirms. */
  environments?: WorkspaceEnvironment[];
  servers?: Server[];
  orgs?: OrgMembership[];
  currentOrgId?: string;
  pref?: ThemePref | null;
  theme?: ThemeId | null;
  isOperator?: boolean;
  /** The member's role in the open workspace. */
  role?: string | null;
  sidebarCollapsed?: boolean;
  /** Below md: no keyboard sidebar to collapse. */
  touch?: boolean;
}

/** Every Settings tab and its main actions (D-46), with the words people
 *  search for them by. */
const SETTINGS: { id: string; label: string; keywords: string[]; href: string; owner?: boolean; operator?: boolean }[] = [
  { id: "go:members", label: "Go to Members", keywords: ["settings", "workspace", "roles", "team", "people", "invites", "leave", "delete workspace"], href: "/settings" },
  { id: "go:agents", label: "Go to Agents", keywords: ["settings", "mcp", "claude", "ai", "oauth", "policy", "approval"], href: "/settings/agents" },
  { id: "go:api-keys", label: "Go to API keys", keywords: ["settings", "token", "tokens", "ci", "api"], href: "/settings/api-keys" },
  { id: "go:alerts", label: "Go to Alerts", keywords: ["settings", "webhook", "slack", "notifications"], href: "/settings/alerts" },
  { id: "go:storage", label: "Go to Storage", keywords: ["settings", "shared volumes", "volume", "disk"], href: "/settings/storage" },
  { id: "go:backups", label: "Go to Backups", keywords: ["settings", "backup", "restic", "restore"], href: "/settings/backups" },
  { id: "go:account", label: "Go to Account", keywords: ["settings", "profile", "password", "sessions", "sign out"], href: "/settings/account" },
  { id: "go:appearance", label: "Go to Appearance", keywords: ["theme", "settings", "dark", "light"], href: "/settings/appearance" },
  { id: "go:instance-server", label: "Go to the panel’s server", keywords: ["instance", "panel", "usage", "cpu", "memory", "disk", "host"], href: "/instance/server", operator: true },
  { id: "go:accounts", label: "Go to Accounts", keywords: ["instance", "operator", "users", "deactivate", "grant", "revoke"], href: "/instance/accounts", operator: true },
  { id: "go:deploy-policy", label: "Go to Deploy policy report", keywords: ["instance", "operator", "violations", "exceptions"], href: "/instance/deploy-policy", operator: true },
  { id: "go:instance-audit", label: "Go to the instance audit log", keywords: ["instance", "operator", "history", "changes"], href: "/instance/audit-log", operator: true },
  { id: "new-api-key", label: "New API key", keywords: ["create", "token", "ci"], href: "/settings/api-keys?new" },
  { id: "invite-member", label: "Invite member", keywords: ["invite", "team", "add", "people", "link"], href: "/settings?invite", owner: true },
];

/** What a deploy of s builds or pulls, as the board says it: "main", or the
 *  image's last path segment; "" for a compose stack. */
export function deployFrom(s: Pick<WorkspaceService, "source_type" | "git_branch" | "docker_image">): string {
  if (s.source_type === "git") return s.git_branch || "main";
  if (s.source_type === "docker-image" && s.docker_image) return s.docker_image.slice(s.docker_image.lastIndexOf("/") + 1);
  return "";
}

export function buildCommands(src: CommandSources): CommandItem[] {
  const items: CommandItem[] = [];
  const canChange = src.role === "owner" || src.role === "developer";
  const projectName = new Map((src.projects ?? []).map((p) => [p.id, p.name]));

  // A service's actions (D-50), owners and developers: Deploy runs at once,
  // except in a protected environment (D-55), or one not read yet; the
  // others, and that Deploy, open the service's own confirmation.
  const guarded = new Set((src.environments ?? []).filter((e) => e.protected).map((e) => e.id));
  const confirms = (s: WorkspaceService) => !!s.environment_id && (!src.environments || guarded.has(s.environment_id));
  if (canChange) {
    for (const s of src.services ?? []) {
      const project = projectName.get(s.project_id);
      const path = project ? `${project} / ${s.name}` : s.name;
      const hint = s.environment_name || (s.scope === "org" ? "Workspace" : "Project");
      const keywords = [s.name, project ?? "", s.environment_name].filter(Boolean);
      // A workspace service's project is "": no scope keeps it.
      const base = { group: "Actions" as const, hint, projectId: s.project_id };
      const href = serviceHref(s.project_id, s.id);
      if (s.runtime_status !== "deploying") {
        const from = deployFrom(s);
        const confirm = confirms(s);
        items.push({
          ...base,
          id: `deploy:${s.id}`,
          icon: "deploy",
          label: "Deploy",
          detail: `${from ? `${path} from ${from}` : path}${confirm ? "…" : ""}`,
          keywords: [...keywords, "redeploy", "release", "ship"],
          ...(confirm ? { href: `${href}?confirm=deploy` } : { action: { type: "deploy" as const, serviceId: s.id, projectId: s.project_id, name: path } }),
          searchOnly: "unscoped",
        });
      }
      if (isServing(s.runtime_status)) {
        items.push({ ...base, id: `rollback:${s.id}`, icon: "rollback", label: "Roll back", detail: `${path}…`, keywords: [...keywords, "rollback", "revert", "undo", "previous"], href: `${href}?tab=deployments&rollback=1`, searchOnly: true });
        items.push({ ...base, id: `restart:${s.id}`, icon: "restart", label: "Restart", detail: `${path}…`, keywords: [...keywords, "reboot"], href: `${href}?confirm=restart`, searchOnly: true });
      }
      if (isStoppable(s.runtime_status)) {
        items.push({ ...base, id: `stop:${s.id}`, icon: "stop", label: "Stop", detail: `${path}…`, keywords: [...keywords, "halt", "down"], href: `${href}?confirm=stop`, searchOnly: true });
      }
    }
  }

  for (const nav of allNavItems(src.isOperator)) {
    items.push({ id: `go:${nav.id}`, group: "Go to", icon: "go", label: `Go to ${nav.label}`, keywords: ["navigate", "open"], href: nav.href });
  }
  for (const s of SETTINGS) {
    if ((s.operator && !src.isOperator) || (s.owner && src.role !== "owner")) continue;
    const go = s.id.startsWith("go:");
    items.push({ id: s.id, group: go ? "Go to" : "Actions", icon: go ? "go" : "new", label: s.label, keywords: s.keywords, href: s.href });
  }
  // Creating (D-47): owners and developers.
  if (canChange) {
    items.push({ id: "new-project", group: "Actions", icon: "new", label: "New project", keywords: ["create", "add", "project"], href: "/projects?new" });
    items.push({ id: "add-domain", group: "Actions", icon: "new", label: "Add domain", keywords: ["new", "create", "domain", "hostname", "dns", "certificate"], href: "/domains?add" });
    items.push({ id: "new-workspace-service", group: "Actions", icon: "new", label: "New workspace service", keywords: ["create", "add", "shared", "loki", "grafana"], href: "/shared/new" });
    for (const p of src.projects ?? []) {
      items.push({ id: `new-service:${p.id}`, group: "Actions", icon: "new", label: `New service in ${p.name}`, keywords: ["create", "add", "service", "catalog"], href: `/projects/${p.id}/services/new`, projectId: p.id, searchOnly: "unscoped" });
      items.push({ id: `new-issue:${p.id}`, group: "Actions", icon: "new", label: `New issue in ${p.name}`, keywords: ["create", "add", "issue", "bug", "task"], href: `/projects/${p.id}/issues?new=1`, projectId: p.id, searchOnly: "unscoped" });
    }
  }
  if (src.role === "owner") {
    items.push({ id: "add-server", group: "Actions", icon: "new", label: "Add server", keywords: ["new", "create", "server", "ssh", "provision", "machine"], href: "/servers/new" });
  }
  for (const org of src.orgs ?? []) {
    if (org.org_id === src.currentOrgId) continue;
    items.push({ id: `org:${org.org_id}`, group: "Actions", icon: "workspace", label: `Switch to ${org.name}`, hint: "Workspace", keywords: ["workspace", "org", org.role], action: { type: "switch-org", orgId: org.org_id } });
  }
  if (!src.touch) {
    items.push({
      id: "sidebar",
      group: "Actions",
      icon: "sidebar",
      label: src.sidebarCollapsed ? "Expand sidebar" : "Collapse sidebar",
      keywords: ["sidebar", "rail", "navigation"],
      action: { type: "toggle-sidebar" },
    });
  }
  items.push({ id: "sign-out", group: "Actions", icon: "sign-out", label: "Sign out", keywords: ["log out", "logout"], action: { type: "sign-out" } });

  if (src.theme) {
    const other = THEMES[otherSide(src.theme)];
    items.push({ id: "theme:other", group: "Theme", icon: "theme", label: `Use ${other.label}`, keywords: ["theme", other.mode, "toggle"], action: { type: "theme", pref: other.id } });
  }
  for (const pref of THEME_PREFS) {
    items.push({
      id: `theme:${pref}`,
      group: "Theme",
      icon: "theme",
      label: `Theme: ${prefLabel(pref)}`,
      hint: pref === src.pref ? "Current" : undefined,
      keywords: ["theme", "appearance", pref.startsWith("system:") ? "system" : THEMES[pref as ThemeId].mode],
      action: { type: "theme", pref },
    });
  }

  for (const p of src.projects ?? []) {
    items.push({ id: `project:${p.id}`, group: "Projects", icon: "project", label: p.name, hint: "Project", keywords: ["project", p.description ?? ""], href: `/projects/${p.id}`, projectId: p.id });
  }

  for (const s of src.services ?? []) {
    const project = projectName.get(s.project_id);
    items.push({
      id: `service:${s.id}`,
      group: "Services",
      icon: "service",
      label: s.name,
      hint: s.scope === "org" ? "Shared" : [project, s.environment_name].filter(Boolean).join(" · "),
      keywords: ["service", project ?? "", s.environment_name ?? "", s.addon_slug ?? "", s.docker_image ?? "", s.git_repo_url ?? "", s.source_type].filter(Boolean),
      href: serviceHref(s.project_id, s.id),
      projectId: s.project_id || undefined,
    });
  }

  for (const srv of src.servers ?? []) {
    // A server shared with this workspace comes without its address.
    items.push({ id: `server:${srv.id}`, group: "Servers", icon: "server", label: srv.name, hint: srv.ip_address, monoHint: true, keywords: ["server", srv.ip_address].filter(Boolean), href: `/servers/${srv.id}` });
  }

  return items;
}

export interface CommandResults {
  group: CommandGroup;
  items: CommandItem[];
}

const words = (text: string) => text.toLowerCase().split(/[^a-z0-9]+/).filter(Boolean);

/** How well q (lower case) names item: 3 its whole label, 2 the start of its
 *  label, 1 the start of a word of its label, detail or keywords (every word
 *  of q must), 0 only cmdk's fuzzy match. */
function matchTier(item: CommandItem, q: string): number {
  const label = item.label.toLowerCase();
  if (label === q) return 3;
  if (label.startsWith(q)) return 2;
  const own = [...words(item.label), ...words(item.detail ?? ""), ...item.keywords.flatMap(words)];
  return words(q).every((w) => own.some((o) => o.startsWith(w))) ? 1 : 0;
}

/** A state change that surprises when Enter runs it unasked (switch
 *  workspace, sign out, the theme, the sidebar). Deploy is not one: its row
 *  matches only through the service's own name or the word "deploy". */
const changesState = (item: CommandItem) => !!item.action && item.action.type !== "deploy";

/** Filters with cmdk's own scoring, then caps the data groups, so a search
 *  can still reach the twentieth project while the list stays short.
 *
 *  With a query, the groups are ordered by their best match, so the first
 *  row (the one Enter runs) is the closest project, service or server rather
 *  than an action: actions and themes need a word that starts with the
 *  query, and one that changes state ranks last unless its label does.
 *
 *  scope (a project's ID) keeps that project's items, and the commands
 *  that belong to no project once something is typed. A query starting
 *  with ">" lists only commands. */
export function filterCommands(items: CommandItem[], query: string, scope?: string): CommandResults[] {
  const commandsOnly = query.trimStart().startsWith(">");
  const q = (commandsOnly ? query.trimStart().slice(1) : query).trim().toLowerCase();
  const visible = items.filter((item) => {
    if (commandsOnly && !COMMANDS.has(item.group)) return false;
    if (!scope) return true;
    // The chip already names the project.
    if (item.group === "Projects") return false;
    if (item.projectId !== undefined) return item.projectId === scope;
    return !!q && COMMANDS.has(item.group);
  });
  const shownUntyped = (item: CommandItem) =>
    !item.searchOnly || (item.searchOnly === "unscoped" && !!scope && item.projectId === scope);
  const order = q || scope ? GROUP_ORDER : EMPTY_ORDER;
  const scored = visible
    .map((item, index) => {
      if (!q) return { item, index, score: shownUntyped(item) ? 1 : 0, rank: 0 };
      const tier = matchTier(item, q);
      if (tier === 0 && !CAPPED.has(item.group)) return { item, index, score: 0, rank: 0 };
      const rank = changesState(item) && tier < 2 ? -1 : tier;
      return { item, index, score: defaultFilter(item.label, q, item.keywords), rank };
    })
    .filter((r) => r.score > 0);
  const byMatch = (a: (typeof scored)[number], b: (typeof scored)[number]) =>
    b.rank - a.rank || b.score - a.score || a.index - b.index;
  return order.map((group, order) => {
    const ranked = scored.filter((r) => r.item.group === group).sort(byMatch);
    return { group, order, best: ranked[0], items: ranked.slice(0, CAPPED.has(group) ? GROUP_LIMIT : undefined).map((r) => r.item) };
  })
    .filter((g) => g.items.length > 0)
    .sort((a, b) => (q ? byMatch(a.best, b.best) : 0) || a.order - b.order)
    .map(({ group, items }) => ({ group, items }));
}
