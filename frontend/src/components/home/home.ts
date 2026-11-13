import type { AuditEntry } from "@/lib/api/org.service";
import type { NeedsYouItem } from "@/lib/api/inbox.service";
import type { DashboardData, ProjectDeploy, RecentDeployment } from "@/lib/api/dashboard.service";
import type { WorkspaceService } from "@/lib/api/service.service";
import { humanizeAction } from "@/lib/audit";
import { projectHealth, type ProjectHealth } from "@/components/shell/health";
import type { StatusTone } from "@/lib/status";
import { formatDateTime } from "@/lib/utils";

export { formatDuration, timeAgo } from "@/lib/time";

// Home (board: private/design/canvas/Main.dc.html). The pure parts: the page
// renders what these return.

/** "project / service", or the service alone when it is workspace-shared. */
export function serviceTitle(projectName: string, serviceName: string): string {
  return projectName ? `${projectName} / ${serviceName}` : serviceName;
}

/** A service's page; a workspace service has no project, so its page is
 *  under Workspace services (D-47). */
export function serviceHref(projectId: string, serviceId: string): string {
  if (!projectId) return `/shared/${serviceId}`;
  return `/projects/${projectId}/services/${serviceId}`;
}

/** A deployment's page, or the service's Deployments tab without one (D-44). */
export function deploymentHref(projectId: string, serviceId: string, deploymentId = ""): string {
  return `${serviceHref(projectId, serviceId)}?tab=deployments${deploymentId ? `&deployment=${deploymentId}` : ""}`;
}

// ─── Needs you ────────────────────────────────────────────────────────────────

/** "panel" items show under All only: the panel itself (D-68). */
export type NeedsTab = "deploys" | "services" | "servers" | "panel" | "code" | "issues" | "docs";
export type NeedsIcon = "failed" | "service" | "server" | "disk" | "volume" | "issue" | "doc" | "preview" | "agent" | "backup" | "kit";

export interface NeedsItem {
  key: string;
  tab: NeedsTab;
  icon: NeedsIcon;
  tone: StatusTone;
  kind: string;
  title: string;
  meta: string;
  code: string;
  at: string;
  action: string;
  href: string;
  /** How much it needs someone, from the server (D-140). */
  severity: Severity;
  /** Shown on Home past its cap: a failed deploy, an offline server, a failed backup. */
  always: boolean;
  /** What Home's footer calls it, one and several: "a stale runbook", "stale runbooks". */
  noun: [string, string];
  /** The issue a failed deploy already opened (the board's "Triage opened BIL-31"). */
  related?: { label: string; key: string; href: string };
  /** A disk alert: Free space… opens its dialog (null: the panel's own server). */
  disk?: { serverId: string | null; where: string };
  /** Marking it done only puts it off for 7 days: its cause is still there. */
  snoozes?: boolean;
}

export type Severity = NeedsYouItem["severity"];
type Built = Omit<NeedsItem, "severity" | "always">;

const SERVICE_ALERTS: Record<string, { kind: string; tone: StatusTone }> = {
  service_down: { kind: "Service down", tone: "failed" },
  container_exited: { kind: "Container exited", tone: "failed" },
  service_unhealthy: { kind: "Unhealthy", tone: "pending" },
  service_restarting: { kind: "Restarting", tone: "pending" },
};

const SERVICE_NOUNS: Record<string, [string, string]> = {
  service_down: ["a service down", "services down"],
  container_exited: ["an exited container", "exited containers"],
  service_unhealthy: ["an unhealthy service", "unhealthy services"],
  service_restarting: ["a restarting service", "restarting services"],
};

const PANEL_HOST = "the panel host";

type AlertLike = Pick<DashboardData["open_alerts"][number], "id" | "kind" | "subject" | "subject_name" | "detail" | "project_id" | "project_name" | "environment_name" | "fired_at">;

/** An open alert as Needs you shows it; a kind it doesn't name gets a
 *  plain row, so every item the server counts has one. */
function alertItem(a: AlertLike, tab: NeedsTab): Built {
  const base = { key: `alert-${a.id}`, code: "", at: a.fired_at };
  const service = SERVICE_ALERTS[a.kind];
  if (service) {
    return {
      ...base, tab: "services", icon: "service", ...service, noun: SERVICE_NOUNS[a.kind],
      title: serviceTitle(a.project_name, a.subject_name),
      meta: [a.environment_name, a.detail].filter(Boolean).join(" · "),
      action: "Open service",
      href: serviceHref(a.project_id, a.subject),
    };
  }
  // The panel's own (D-68): about the panel, opened in Instance.
  if (a.kind === "panel_container" || a.kind === "panel_certificate" || a.kind === "panel_upgrade_failed") {
    const kind = { panel_container: "Panel container", panel_certificate: "Panel certificate", panel_upgrade_failed: "Upgrade failed" }[a.kind];
    return { ...base, tab: "panel", icon: a.kind === "panel_upgrade_failed" ? "failed" : "server", tone: a.kind === "panel_certificate" ? "pending" : "failed", kind, title: "This panel", meta: a.detail, action: "Open Instance", href: "/instance", noun: ["a panel problem", "panel problems"] };
  }
  const panel = a.subject === "control-plane";
  const server = panel ? PANEL_HOST : a.subject_name;
  const serverHref = panel ? "/instance/server" : `/servers/${a.subject}`;
  switch (a.kind) {
    case "server_unreachable":
      return { ...base, tab: "servers", icon: "server", tone: "failed", kind: "Unreachable", title: server, meta: a.detail, action: "Open server", href: serverHref, noun: ["an unreachable server", "unreachable servers"] };
    case "agent_offline":
      return { ...base, tab: "servers", icon: "server", tone: "failed", kind: "Agent offline", title: server, meta: a.detail, action: "Open server", href: serverHref, noun: ["an offline server", "offline servers"] };
    case "agent_frozen":
      return { ...base, tab: "servers", icon: "server", tone: "pending", kind: "Kept its own state", title: server, meta: a.detail, action: "Choose a state", href: serverHref, noun: ["a server with its own state", "servers with their own state"] };
    case "disk_full": {
      const pct = a.detail.match(/(\d+)% full/)?.[1];
      return { ...base, tab: "servers", icon: "disk", tone: "pending", kind: pct ? `Disk ${pct}% used` : "Disk almost full", title: server, meta: a.detail, action: "Free space…",
        href: panel ? "/instance/maintenance#disk" : `${serverHref}#disk`, noun: ["a full disk", "full disks"], disk: { serverId: panel ? null : a.subject, where: server } };
    }
    case "backup_failed":
      return { ...base, tab: "servers", icon: "backup", tone: "failed", kind: "Backup failed", title: server, meta: a.detail, action: "Backup settings", href: panel ? "/settings/backups#panel-host" : `/servers/${a.subject}#backup`, noun: ["a failed backup", "failed backups"] };
    case "volume_full":
      return { ...base, tab: "servers", icon: "volume", tone: "pending", kind: "Volume almost full", title: a.subject_name, meta: a.detail, action: "Open storage", href: "/settings/storage", noun: ["a full volume", "full volumes"] };
    case "mirror_failing":
      return {
        ...base, tab: "services", icon: "failed", tone: "failed", kind: "Mirror failing",
        title: a.project_name ? `${a.project_name} / ${a.subject_name}` : a.subject_name, meta: a.detail,
        action: "Open mirrors", href: `/projects/${a.project_id}/code/${encodeURIComponent(a.subject_name)}/settings`, noun: ["a failing mirror", "failing mirrors"],
      };
  }
  // A kind added after this page (security updates, a reboot, an advisory).
  const kind = a.kind.replaceAll("_", " ");
  const onServer = tab === "servers";
  return {
    ...base, tab, icon: onServer ? "server" : "service", tone: "pending", kind: kind[0]?.toUpperCase() + kind.slice(1), title: a.subject_name || server, meta: a.detail,
    action: onServer ? "Open server" : "Open service", href: onServer ? (panel ? "/instance/maintenance#updates" : serverHref) : serviceHref(a.project_id, a.subject), noun: ["an alert", "alerts"],
  };
}

/** The alerts Home shows past its cap (D-140), with failed deploys. */
const ALWAYS = new Set(["server_unreachable", "agent_offline", "backup_failed"]);
/** What a waiting agent's request asks for, as the backend starts its title
 *  (inbox/service.go agentAsks): change both. The rest is its target. */
const AGENT_ASKS = /^(Deploy|Roll back|Set a secret on|Set a variable on|Run a runbook step in|Make a branch in|Open a pull request in|Open an issue in|Comment in|Start a preview in) /;

/** The inbox's Needs you (D-88, D-140) as Home and the Inbox show it: in
 *  the server's order, one row per item, so every count agrees. */
export function inboxItems(items: NeedsYouItem[], now = Date.now()): NeedsItem[] {
  return items.map((it) => {
    const built = row(it, now);
    return {
      ...built,
      key: it.key,
      meta: it.snoozed_until ? [built.meta, `Back on ${formatDateTime(it.snoozed_until).slice(0, 10)}`].filter(Boolean).join(" · ") : built.meta,
      severity: it.severity,
      always: it.kind === "deploy_failed" || (it.kind === "alert" && ALWAYS.has(it.alert_kind ?? "")),
      snoozes: it.snoozes,
    };
  });
}

function row(it: NeedsYouItem, now: number): Built {
  const project = it.project_id ?? "";
  const base = { key: it.key, at: it.at, code: "" };
  switch (it.kind) {
    case "deploy_failed":
      return {
        ...base, tab: "deploys", icon: "failed", tone: "failed", kind: "Deploy failed", title: it.title,
        meta: it.detail.replace(/^Deploy failed( · )?/, ""), code: it.error ?? "",
        action: "View logs", href: deploymentHref(project, it.service_id ?? "", it.deployment_id ?? ""), noun: ["a failed deploy", "failed deploys"],
        related: it.issue_key && project
          ? { label: "Triage opened", key: it.issue_key, href: `/projects/${project}/issues/${it.issue_key.split("-").pop()?.replace(/^0+/, "")}` }
          : undefined,
      };
    case "alert":
      return alertItem({
        id: it.key.replace(/^alert:/, ""), kind: it.alert_kind ?? "", subject: it.service_id || it.server_id || "control-plane",
        subject_name: it.title, detail: it.detail, project_id: project, project_name: it.project, environment_name: "", fired_at: it.at,
      }, it.category as NeedsTab);
    case "assigned": {
      const urgent = it.issue?.priority === "urgent";
      return {
        ...base, tab: "issues", icon: "issue", tone: "pending",
        kind: `Assigned${it.issue?.priority ? `, ${it.issue.priority}` : ""}`,
        title: `${it.issue?.key ?? ""} ${it.title}`.trim(), meta: `${it.project} · assigned to you`,
        action: "Open issue", href: `/projects/${project}/issues/${it.issue?.number ?? ""}`,
        noun: urgent ? ["an urgent issue", "urgent issues"] : ["an issue assigned to you", "issues assigned to you"],
      };
    }
    case "due": {
      const late = it.detail.endsWith("late");
      return {
        ...base, tab: "issues", icon: "issue", tone: late ? "failed" : "pending", kind: it.detail,
        title: `${it.issue?.key ?? ""} ${it.title}`.trim(), meta: `${it.project} · assigned to you`,
        action: "Open issue", href: `/projects/${project}/issues/${it.issue?.number ?? ""}`, noun: ["an issue due", "issues due"],
      };
    }
    case "stale_page":
      return {
        ...base, tab: "docs", icon: "doc", tone: "pending", kind: "Runbook may be stale", title: it.title,
        meta: `${it.project} / ${it.detail}`, action: "Review page", href: `/projects/${project}/docs/${it.page_id ?? ""}`, noun: ["a stale runbook", "stale runbooks"],
      };
    case "preview_approval":
      // Approved on the pull request's page, after reading the commit (D-97).
      return {
        ...base, tab: "code", icon: "preview", tone: "pending", kind: "Preview to approve", title: it.title, meta: it.detail,
        action: "Review", href: `/projects/${project}/code/${encodeURIComponent(it.repository ?? "")}/pulls/${it.pull_number ?? ""}`,
        noun: ["a preview to approve", "previews to approve"],
      };
    case "exposed_tool":
      return {
        ...base, tab: "services", icon: "service", tone: "pending", kind: "Admin tool is public", title: it.title,
        meta: it.detail, action: "Protect it", href: `${serviceHref(project, it.service_id ?? "")}?tab=settings#set-access`, noun: ["a public admin tool", "public admin tools"],
      };
    case "setup_login":
      // Basic auth in front of an app whoever opens first would own,
      // until its admin account is set up: then it's turned off in Access.
      return {
        ...base, tab: "services", icon: "service", tone: "pending", kind: "Temporary login is on", title: it.title,
        meta: it.detail, action: "Open settings", href: `${serviceHref(project, it.service_id ?? "")}?tab=settings#set-access`, noun: ["a temporary login", "temporary logins"],
      };
    case "https_redeploy":
      // Toft didn't redeploy it itself (D-159): a person deploys, through
      // the service page's own confirm for a protected environment.
      return {
        ...base, tab: "services", icon: "service", tone: "pending", kind: "HTTPS waits for a deploy", title: it.title,
        meta: it.project ? `${it.project} · ${it.detail}` : it.detail, action: "Open service", href: serviceHref(project, it.service_id ?? ""),
        noun: ["a deploy for HTTPS", "deploys for HTTPS"],
      };
    case "no_backup":
      return {
        ...base, tab: "servers", icon: "server", tone: "pending", kind: "No backups", title: it.title, meta: it.detail,
        action: "Set up backups", href: it.server_id ? `/servers/${it.server_id}#backup` : "/settings/backups#panel-host",
        noun: ["a server without backups", "servers without backups"],
      };
    case "no_watchdog":
      return {
        ...base, tab: "panel", icon: "server", tone: "pending", kind: "No watchdog", title: it.title, meta: it.detail,
        action: "Set up", href: "/instance/server#wd-h", noun: ["a panel without a watchdog", "panels without a watchdog"],
      };
    case "kit_out_of_date":
      // For operators (D-163): until one of them marks the new kit as stored.
      return {
        ...base, tab: "panel", icon: "kit", tone: "pending", kind: "Recovery kit out of date", title: it.title, meta: it.detail,
        action: "Show kit", href: "/instance/kit", noun: ["an out-of-date recovery kit", "out-of-date recovery kits"],
      };
    case "upgrade_failed":
      // The panel's own (D-68), for operators: until a later upgrade succeeds.
      return {
        ...base, tab: "panel", icon: "failed", tone: "failed", kind: "Upgrade failed", title: it.title, meta: it.detail, code: it.error ?? "",
        action: "Full log", href: "/instance/maintenance?log=upgrade", noun: ["a failed upgrade", "failed upgrades"],
      };
    case "agent_approval": {
      // Review only: a person reads the reason and the plan before deciding (D-100).
      const exp = new Date(it.expires_at ?? it.at);
      const mins = Math.max(1, Math.ceil((exp.getTime() - now) / 60_000));
      const verb = it.title.match(AGENT_ASKS)?.[1];
      return {
        ...base, tab: "deploys", icon: "agent", tone: "pending", kind: verb ? `An agent asks to ${verb[0].toLowerCase()}${verb.slice(1)}` : "An agent asks",
        title: it.title.replace(AGENT_ASKS, ""),
        // The expiry first: a long reason is clamped, and this can't be.
        meta: `Expires ${exp.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })}, in ${mins} min · ${it.detail}`,
        action: "Review", href: `/settings/agents/requests/${it.approval_id ?? ""}`, noun: ["an agent’s request", "agents’ requests"],
      };
    }
  }
  // A kind added after this page: a plain row, so the counts still agree.
  return { ...base, tab: it.category as NeedsTab, icon: "service", tone: "pending", kind: it.kind, title: it.title, meta: it.detail, action: "Open Inbox", href: "/inbox", noun: ["an item", "items"] };
}

/** Home's Needs you (D-140): the first 4, and past them every failed
 *  deploy, offline server and failed backup; the footer names the rest. */
export function homeNeeds(items: NeedsItem[], cap = 4): { shown: NeedsItem[]; hidden: NeedsItem[]; total: number; more: string } {
  const shown = items.filter((it, i) => i < cap || it.always);
  const hidden = items.filter((it, i) => i >= cap && !it.always);
  const groups = new Map<string, { noun: [string, string]; n: number }>();
  for (const it of hidden) {
    const g = groups.get(it.noun[0]);
    if (g) g.n++;
    else groups.set(it.noun[0], { noun: it.noun, n: 1 });
  }
  const names = [...groups.values()].map((g) => (g.n === 1 ? g.noun[0] : `${g.n} ${g.noun[1]}`));
  const list = names.length > 1 ? `${names.slice(0, -1).join(", ")} and ${names.at(-1)}` : (names[0] ?? "");
  return { shown, hidden, total: items.length, more: hidden.length ? `${hidden.length} more: ${list}` : "" };
}

export interface HealthPart {
  tone: StatusTone;
  value: string;
  sub: string;
  href?: string;
}

/** Home's health strip (D-140): state only, one part each. undefined: that
 *  read failed, and its part says so rather than state what it couldn't read. */
export function healthParts(
  data: Pick<DashboardData, "open_alerts" | "deploys_24h">,
  services: Pick<WorkspaceService, "runtime_status">[] | undefined,
  servers: { online: number; total: number } | undefined,
): HealthPart[] {
  const n = (status: string) => (services ?? []).filter((s) => s.runtime_status === status).length;
  const [failed, unhealthy, deploying, running] = [n("error"), n("unhealthy"), n("deploying"), n("running")];
  const lead = failed ? "failed" : unhealthy ? "unhealthy" : "running";
  const svcValue = failed ? `${failed} service${failed === 1 ? "" : "s"} failed` : unhealthy ? `${unhealthy} unhealthy` : services?.length ? `${running} running` : "No services yet";
  const svcSub = [
    lead !== "unhealthy" && failed && unhealthy ? `${unhealthy} unhealthy` : "",
    deploying ? `${deploying} deploying` : "",
    lead !== "running" && running ? `${running} running` : "",
  ].filter(Boolean).join(" · ");
  const of = (kind: string) => data.open_alerts.filter((a) => a.kind === kind);
  const where = (a: { subject: string; subject_name: string }) => (a.subject === "control-plane" ? PANEL_HOST : a.subject_name);
  const names = (as: { subject: string; subject_name: string }[]) => (as.length > 2 ? `${where(as[0])} and ${as.length - 1} more` : as.map(where).join(", "));
  const backups = of("backup_failed");
  const disks = of("disk_full");
  const pct = disks.length === 1 ? disks[0].detail.match(/(\d+)% full/)?.[1] : undefined;
  const one = (as: { subject: string }[], hash: string, panelHref: string) =>
    as.length === 1 ? (as[0].subject === "control-plane" ? panelHref : `/servers/${as[0].subject}${hash}`) : "/servers";
  const d = data.deploys_24h;
  return [
    !servers
      ? { tone: "neutral", value: "Unknown", sub: "couldn’t load servers", href: "/servers" }
      : servers.total
        ? { tone: servers.online === servers.total ? "running" : "failed", value: `${servers.online} of ${servers.total} servers online`, sub: "", href: "/servers" }
        : { tone: "neutral", value: "Only the panel host", sub: "", href: "/servers" },
    services
      ? { tone: failed ? "failed" : unhealthy ? "pending" : services.length ? "running" : "neutral", value: svcValue, sub: svcSub, href: "/projects" }
      : { tone: "neutral", value: "Unknown", sub: "couldn’t load services", href: "/projects" },
    backups.length
      ? { tone: "failed", value: `${backups.length} backup${backups.length === 1 ? "" : "s"} failed`, sub: names(backups), href: one(backups, "#backup", "/settings/backups#panel-host") }
      : { tone: "running", value: "No failed backups", sub: "" },
    disks.length
      ? { tone: "pending", value: pct ? `1 disk at ${pct}%` : `${disks.length} disk${disks.length === 1 ? "" : "s"} almost full`, sub: names(disks), href: one(disks, "#disk", "/instance/maintenance#disk") }
      : { tone: "running", value: "No disk warnings", sub: "" },
    { tone: "neutral", value: `${d.total} deploy${d.total === 1 ? "" : "s"} in 24h`, sub: `${d.succeeded} live` },
  ];
}

// ─── Activity ─────────────────────────────────────────────────────────────────

export type ActivityIcon = "live" | "failed" | "deploy" | "server" | "service" | "project" | "invite" | "other" | "merge" | "issue" | "doc";

export interface ActivityItem {
  key: string;
  icon: ActivityIcon;
  who: string;
  text: string;
  target: string;
  at: string;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** An audit entry's target. The log names what still exists and gives the
 *  bare ID of what was deleted since (the Audit log page keeps that ID). */
function auditTarget(action: string, resource: string): string {
  if (!UUID.test(resource)) return resource;
  const kind = action.split(".")[0];
  return kind === "deployment" || kind === "service" ? "a deleted service" : `a deleted ${kind}`;
}

function auditIcon(action: string): ActivityIcon {
  if (action.startsWith("deployment.")) return "deploy";
  if (action.startsWith("server.")) return "server";
  if (action.startsWith("service.")) return "service";
  if (action.startsWith("project.")) return "project";
  if (action === "member.invited") return "invite";
  return "other";
}

/** Who did what (the audit log) and how deploys ended (their outcome),
 *  newest first. A deploy's start is the audit log's "deployed": deploys
 *  record no one who started them. */
export function activity(deploys: RecentDeployment[], audit: AuditEntry[], limit = 8, work: DashboardData["activity"] = []): ActivityItem[] {
  const items: ActivityItem[] = [];
  // Issues, pull requests and pages (D-88).
  for (const w of work) {
    const key = `work-${w.kind}-${w.project_id}-${w.number ?? w.page_id}-${w.at}`;
    switch (w.kind) {
      case "issue_opened":
        items.push({ key, icon: "issue", who: w.actor, text: "opened", target: `${w.key ?? ""} ${w.title}`.trim(), at: w.at });
        break;
      case "issue_shipped":
        items.push({ key, icon: "live", who: "", text: `${w.key ?? ""} moved to Shipped${w.automatic ? " when its deploy went live" : ""}:`, target: w.title, at: w.at });
        break;
      case "pull_merged":
        items.push({ key, icon: "merge", who: w.actor, text: "merged", target: `!${w.number} ${w.title}`, at: w.at });
        break;
      case "page_edited":
        items.push({ key, icon: "doc", who: w.actor, text: "edited", target: w.title, at: w.at });
        break;
    }
  }
  for (const d of deploys) {
    if ((d.status !== "running" && d.status !== "failed") || !d.finished_at) continue;
    const failed = d.status === "failed";
    items.push({
      key: `deploy-${d.id}`,
      icon: failed ? "failed" : "live",
      who: "",
      text: failed ? "Deploy failed for" : "Deploy went live for",
      target: serviceTitle(d.project_name, d.service_name),
      at: d.finished_at,
    });
  }
  for (const a of audit) {
    // An invite is named by its role (board Welcome), not "Invite link".
    const role = a.action === "member.invited" && typeof a.detail?.role === "string" ? a.detail.role : "";
    const target = role ? `${role === "owner" ? "an" : "a"} ${role}` : auditTarget(a.action, a.resource);
    items.push({ key: `audit-${a.id}`, icon: auditIcon(a.action), who: a.actor, text: humanizeAction(a.action), target, at: a.created_at });
  }
  return items.sort((x, y) => y.at.localeCompare(x.at)).slice(0, limit);
}

// ─── Projects ─────────────────────────────────────────────────────────────────

export interface ProjectRow {
  id: string;
  name: string;
  /** Its tile's tint (the sidebar's). */
  health: ProjectHealth;
  /** "23 issues · 2 open PRs · 18 docs", or "". */
  work: string;
  /** Per service, the tone of its dot, healthy first (the Projects page). */
  dots: StatusTone[];
  /** The services bar: how many in each state, healthy first. */
  bar: { tone: StatusTone; n: number }[];
  servicesText: string;
  servicesTone: StatusTone;
  last: ProjectDeploy | null;
}

const DOT_ORDER: StatusTone[] = ["running", "building", "pending", "neutral", "failed"];
const RUNTIME_TONE: Record<string, StatusTone> = { running: "running", deploying: "building", unhealthy: "pending", error: "failed" };
const many = (n: number, one: string, more = `${one}s`) => `${n} ${n === 1 ? one : more}`;

/** Home's Projects table (board ReworkHome): each project's work, services and latest deploy. */
export function projectRows(
  projects: { id: string; name: string; open_issues?: number; open_pulls?: number; doc_pages?: number }[],
  services: WorkspaceService[],
  deploys: ProjectDeploy[],
): ProjectRow[] {
  return projects.map((p) => {
    const own = services.filter((s) => s.project_id === p.id);
    const h = projectHealth(own);
    // What's wrong first, as the board says it: "1 failed · 2 running".
    const parts = [
      h.failing && `${h.failing} failed`,
      h.unhealthy && `${h.unhealthy} unhealthy`,
      h.running && `${h.running} running`,
      h.deploying && `${h.deploying} deploying`,
    ].filter(Boolean);
    const idle = own.length - h.running - h.deploying - h.unhealthy - h.failing;
    if (parts.length === 0 && idle > 0) parts.push(`${idle} idle`);
    const tones = own.map((s) => RUNTIME_TONE[s.runtime_status] ?? "neutral");
    return {
      id: p.id,
      name: p.name,
      health: own.length ? h.health : "idle",
      work: [
        p.open_issues ? many(p.open_issues, "issue") : "",
        p.open_pulls ? many(p.open_pulls, "open PR") : "",
        p.doc_pages ? many(p.doc_pages, "doc") : "",
      ].filter(Boolean).join(" · "),
      // Healthy first, as the boards draw them: failing ends the row.
      dots: [...tones].sort((a, b) => DOT_ORDER.indexOf(a) - DOT_ORDER.indexOf(b)),
      bar: DOT_ORDER.map((tone) => ({ tone, n: tones.filter((t) => t === tone).length })).filter((b) => b.n > 0),
      servicesText: parts.length ? parts.join(" · ") : p.doc_pages ? "Docs only, no services" : "No services",
      servicesTone: h.failing ? "failed" : h.deploying ? "building" : h.unhealthy ? "pending" : h.running ? "running" : "neutral",
      last: deploys.find((d) => d.project_id === p.id) ?? null,
    };
  });
}

/** A deploy's state in words: Live, Failed, Building, Queued. */
export function deployState(status: string): { label: string; tone: StatusTone } {
  switch (status) {
    case "running":
      return { label: "Live", tone: "running" };
    case "failed":
      return { label: "Failed", tone: "failed" };
    case "building":
      return { label: "Building", tone: "building" };
    default:
      return { label: "Queued", tone: "pending" };
  }
}

