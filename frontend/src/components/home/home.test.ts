import { describe, expect, it } from "vitest";
import type { OpenAlert, ProjectDeploy, RecentDeployment } from "@/lib/api/dashboard.service";
import type { AuditEntry } from "@/lib/api/org.service";
import type { WorkspaceService } from "@/lib/api/service.service";
import type { NeedsYouItem } from "@/lib/api/inbox.service";
import { activity, formatDuration, healthParts, homeNeeds, inboxItems, projectRows, timeAgo } from "./home";

const NOW = Date.parse("2026-10-13T12:00:00Z");

describe("timeAgo and formatDuration", () => {
  it("compacts like the board", () => {
    expect(timeAgo("2026-10-13T11:59:30Z", NOW)).toBe("now");
    expect(timeAgo("2026-10-13T11:56:00Z", NOW)).toBe("4m");
    expect(timeAgo("2026-10-13T10:00:00Z", NOW)).toBe("2h");
    expect(timeAgo("2026-10-11T12:00:00Z", NOW)).toBe("2d");
    expect(formatDuration(48.7)).toBe("49s");
    expect(formatDuration(84)).toBe("1m 24s");
    expect(formatDuration(3900)).toBe("1h 5m");
  });
});

const alert = (kind: string, subject: string, extra: Partial<OpenAlert> = {}): OpenAlert => ({
  id: kind, kind, subject, subject_name: "fsn1-prod-02", detail: "/ is 91% full", project_id: "", project_name: "",
  environment_name: "", fired_at: "2026-10-13T10:00:00Z", ...extra,
});

describe("activity", () => {
  it("shows deploy outcomes and audit entries, skipping deploys in progress", () => {
    const deploy = (id: string, status: string, finished: string | null): RecentDeployment => ({
      id, status, service_id: "s1", service_name: "web", project_id: "p1", project_name: "storefront",
      environment_name: "production", created_at: "2026-10-13T11:00:00Z", finished_at: finished,
    });
    const audit: AuditEntry = {
      id: "a1", org_id: "o", actor_id: "u", actor: "alex", action: "deployment.triggered", resource: "web", created_at: "2026-10-13T11:59:00Z",
      target: { kind: "service", id: "s", name: "web", deleted: false }, detail: {},
    };
    const deleted: AuditEntry = { ...audit, id: "a2", resource: "043d2059-de4d-4f0f-9d52-0c077d170dbe", created_at: "2026-10-13T11:59:30Z" };
    const items = activity([deploy("d1", "running", "2026-10-13T11:58:00Z"), deploy("d2", "building", null)], [audit, deleted]);
    expect(items.map((i) => `${i.who} ${i.text} ${i.target}`.trim())).toEqual([
      "alex deployed a deleted service",
      "alex deployed web",
      "Deploy went live for storefront / web",
    ]);
  });
  it("names a created project with the board's folder", () => {
    const created: AuditEntry = {
      id: "a3", org_id: "o", actor_id: "u", actor: "admin", action: "project.created", resource: "storefront", created_at: "2026-10-13T12:00:00Z",
      target: { kind: "project", id: "p1", name: "storefront", deleted: false }, detail: { name: "storefront" },
    };
    expect(activity([], [created])).toMatchObject([{ who: "admin", text: "created project", target: "storefront", icon: "project" }]);
    const invited: AuditEntry = { ...created, id: "a4", action: "member.invited", resource: "Invite link", detail: { role: "owner" } };
    expect(activity([], [invited])).toMatchObject([{ text: "created an invite link for", target: "an owner", icon: "invite" }]);
  });
});

describe("projectRows", () => {
  it("summarises each project's services and latest deploy", () => {
    const svc = (id: string, project_id: string, runtime_status: WorkspaceService["runtime_status"]) =>
      ({ id, name: id, scope: "environment", project_id, environment_id: "e", runtime_status, source_type: "docker-image", addon_slug: "", docker_image: "", git_repo_url: "" }) as WorkspaceService;
    const last: ProjectDeploy = { project_id: "p1", status: "failed", service_name: "worker", source_type: "git", git_branch: "main", docker_image: "", created_at: "2026-10-13T11:56:00Z" };
    const rows = projectRows(
      [{ id: "p1", name: "billing-api" }, { id: "p2", name: "handbook" }],
      [svc("a", "p1", "running"), svc("b", "p1", "error"), svc("c", "p1", "deploying")],
      [last],
    );
    expect(rows[0]).toMatchObject({ servicesText: "1 failed · 1 running · 1 deploying", servicesTone: "failed", health: "failed", work: "",
      bar: [{ tone: "running", n: 1 }, { tone: "building", n: 1 }, { tone: "failed", n: 1 }] });
    expect(rows[1]).toMatchObject({ servicesText: "No services", servicesTone: "neutral", health: "idle", bar: [], last: null });
  });

  it("says each project's work, and a docs-only project as such (board ReworkHome)", () => {
    const rows = projectRows(
      [{ id: "p1", name: "storefront", open_issues: 23, open_pulls: 2, doc_pages: 0 }, { id: "p2", name: "handbook", open_issues: 6, open_pulls: 0, doc_pages: 18 }, { id: "p3", name: "x", open_issues: 1, open_pulls: 1, doc_pages: 1 }],
      [],
      [],
    );
    expect(rows.map((r) => [r.work, r.servicesText])).toEqual([
      ["23 issues · 2 open PRs", "No services"],
      ["6 issues · 18 docs", "Docs only, no services"],
      ["1 issue · 1 open PR · 1 doc", "Docs only, no services"],
    ]);
  });

  it("counts unhealthy services on their own", () => {
    const svc = (id: string, runtime_status: WorkspaceService["runtime_status"]) =>
      ({ id, project_id: "p1", runtime_status }) as WorkspaceService;
    const [row] = projectRows([{ id: "p1", name: "api" }], [svc("a", "running"), svc("b", "unhealthy")], []);
    expect(row).toMatchObject({ servicesText: "1 unhealthy · 1 running", servicesTone: "pending", bar: [{ tone: "running", n: 1 }, { tone: "pending", n: 1 }] });
  });
});

describe("inboxItems", () => {
  it("names a public admin tool and opens its access control", () => {
    const [row] = inboxItems([
      {
        key: "exposed_tool:s9", kind: "exposed_tool", category: "deploys", title: "acme / pma",
        detail: "Admin tool open to anyone at pma.example.com · staging: no basic auth or IP allowlist",
        at: "2026-10-13T11:54:00Z", project_id: "p1", project: "acme", service_id: "s9",
      } as NeedsYouItem,
    ], NOW);
    expect([row.kind, row.tab, row.tone, row.title, row.action, row.href]).toEqual(
      ["Admin tool is public", "services", "pending", "acme / pma", "Protect it", "/projects/p1/services/s9?tab=settings#set-access"],
    );
  });
  it("asks to turn off an app's temporary login once its admin account is set up", () => {
    const [row] = inboxItems([
      {
        key: "setup_login:s9", kind: "setup_login", category: "deploys", title: "Set up blog’s admin account, then turn off its temporary login",
        detail: "acme · Ghost · production: whoever opens it first becomes its admin; the temporary login keeps others out until then",
        at: "2026-10-13T11:54:00Z", project_id: "p1", project: "acme", service_id: "s9",
      } as NeedsYouItem,
    ], NOW);
    expect([row.kind, row.tab, row.tone, row.action, row.href]).toEqual(
      ["Temporary login is on", "services", "pending", "Open settings", "/projects/p1/services/s9?tab=settings#set-access"],
    );
  });
  it("asks for the deploy that switches a temporary address to HTTPS", () => {
    const [row] = inboxItems([
      {
        key: "https_redeploy:s9:1793800000", kind: "https_redeploy", category: "deploys", title: "Redeploy web to switch it to HTTPS",
        detail: "web.203-0-113-10.sslip.io · production: its certificate is out, and it serves plain HTTP until its next deploy",
        at: "2026-10-13T11:54:00Z", project_id: "p1", project: "shop", service_id: "s9",
      } as NeedsYouItem,
    ], NOW);
    expect([row.kind, row.tab, row.tone, row.title, row.meta, row.action, row.href]).toEqual([
      "HTTPS waits for a deploy", "services", "pending", "Redeploy web to switch it to HTTPS",
      "shop · web.203-0-113-10.sslip.io · production: its certificate is out, and it serves plain HTTP until its next deploy",
      "Open service", "/projects/p1/services/s9",
    ]);
  });
  it("names a failed panel upgrade and opens its full log", () => {
    const [row] = inboxItems([
      {
        key: "upgrade_failed:v0.40.0:1794060000", kind: "upgrade_failed", category: "servers", severity: "problem",
        title: "Upgrade to v0.40.0 failed and rolled back", detail: "v0.39.0 is running again", error: "v0.40.0 was not healthy after 2 minutes",
        at: "2026-11-07T14:03:00Z", project_id: null, project: "", snoozes: false,
      },
    ], NOW);
    expect([row.kind, row.tab, row.tone, row.title, row.meta, row.code, row.action, row.href]).toEqual([
      "Upgrade failed", "panel", "failed", "Upgrade to v0.40.0 failed and rolled back", "v0.39.0 is running again",
      "v0.40.0 was not healthy after 2 minutes", "Full log", "/instance/maintenance?log=upgrade",
    ]);
  });
  it("names a server nothing backs up and opens its backups", () => {
    const item = (server_id?: string): NeedsYouItem => ({
      key: `no_backup:${server_id ?? "panel"}:false:2:3`, kind: "no_backup", category: "servers", title: server_id ? "fsn1" : "The panel host",
      detail: "2 databases, 3 volumes, no backups", at: "2026-10-13T11:54:00Z", project_id: null, project: "", server_id, severity: "warning", snoozes: true,
    });
    const [row] = inboxItems([item("v1")], NOW);
    expect([row.kind, row.tab, row.tone, row.title, row.meta, row.action, row.href]).toEqual(
      ["No backups", "servers", "pending", "fsn1", "2 databases, 3 volumes, no backups", "Set up backups", "/servers/v1#backup"],
    );
    expect(inboxItems([item()], NOW)[0].href).toBe("/settings/backups#panel-host");
  });
  it("sends an operator with no watchdog to set one up", () => {
    const [row] = inboxItems([
      { key: "no_watchdog", kind: "no_watchdog", category: "servers", title: "The panel host", detail: "Nobody hears if this server goes down", at: "2026-10-13T11:54:00Z", project_id: null, project: "", severity: "warning", snoozes: true },
    ], NOW);
    expect([row.kind, row.tab, row.tone, row.title, row.meta, row.action, row.href]).toEqual(
      ["No watchdog", "panel", "pending", "The panel host", "Nobody hears if this server goes down", "Set up", "/instance/server#wd-h"],
    );
  });
  it("sends an operator with an out-of-date recovery kit to the kit", () => {
    const [row] = inboxItems([
      { key: "kit_out_of_date:1760781600", kind: "kit_out_of_date", category: "servers", title: "This panel", detail: "The encryption key or the panel host’s backup settings changed after the recovery kit was stored. A restore with that kit fails.", at: "2026-10-13T11:54:00Z", project_id: null, project: "", severity: "warning", snoozes: true },
    ], NOW);
    expect([row.kind, row.tab, row.icon, row.tone, row.title, row.action, row.href]).toEqual(
      ["Recovery kit out of date", "panel", "kit", "pending", "This panel", "Show kit", "/instance/kit"],
    );
    expect(row.meta).toMatch(/^The encryption key or the panel host’s backup settings changed/);
  });
  it("puts an agent's request under Deploys, Review only, with when it expires", () => {
    const [row] = inboxItems([
      {
        key: "agent:ap1", kind: "agent_approval", category: "deploys", title: "Deploy storefront / api in production",
        detail: "Claude Code for admin · “Fixes STO-118.”", at: "2026-10-13T11:54:00Z", project_id: null, project: "",
        service_id: "s1", approval_id: "ap1", expires_at: "2026-10-13T12:14:00Z",
      } as NeedsYouItem,
    ], NOW);
    expect([row.kind, row.title, row.tab, row.icon, row.action, row.href]).toEqual(
      ["An agent asks to deploy", "storefront / api in production", "deploys", "agent", "Review", "/settings/agents/requests/ap1"],
    );
    expect(row.meta).toMatch(/^Expires \d\d:\d\d, in 14 min · Claude Code for admin · “Fixes STO-118.”$/);
  });

  it("says what an agent's request asks for, whatever the tool", () => {
    const row = (title: string) =>
      inboxItems([{ key: "agent:ap1", kind: "agent_approval", category: "deploys", title, detail: "", at: "2026-10-13T11:54:00Z", project_id: null, project: "", approval_id: "ap1", expires_at: "2026-10-13T12:14:00Z" } as NeedsYouItem], NOW)[0];
    const T = "storefront / api in staging";
    for (const [verb, kind] of [
      ["Deploy", "deploy"], ["Roll back", "roll back"], ["Set a secret on", "set a secret on"], ["Set a variable on", "set a variable on"],
      ["Run a runbook step in", "run a runbook step in"], ["Make a branch in", "make a branch in"], ["Open a pull request in", "open a pull request in"],
      ["Open an issue in", "open an issue in"], ["Comment in", "comment in"], ["Start a preview in", "start a preview in"],
    ]) {
      expect(row(`${verb} ${T}`)).toMatchObject({ kind: `An agent asks to ${kind}`, title: T });
    }
    expect(row(T)).toMatchObject({ kind: "An agent asks", title: T });
  });
});

// One Needs you (D-140): the server's order, a row for every item, so Home,
// the Inbox and the sidebar (counts.needs_you, the server's list length)
// all say the same number.
describe("one Needs you", () => {
  const it_ = (key: string, kind: NeedsYouItem["kind"], severity: NeedsYouItem["severity"], x: Partial<NeedsYouItem> = {}): NeedsYouItem =>
    ({ key, kind, severity, category: "deploys", title: key, detail: "", at: "2026-10-13T11:00:00Z", project_id: "p1", project: "storefront", ...x }) as NeedsYouItem;
  const server = [
    it_("alert:b", "alert", "problem", { alert_kind: "backup_failed", category: "servers", server_id: "v1", at: "2026-10-13T08:00:00Z" }),
    it_("deploy:d1", "deploy_failed", "problem", { service_id: "s1", deployment_id: "d1", at: "2026-10-13T09:00:00Z" }),
    it_("agent:a1", "agent_approval", "waiting", { approval_id: "a1", expires_at: "2026-10-13T12:20:00Z", title: "Roll back storefront / api" }),
    it_("alert:sec", "alert", "warning", { alert_kind: "security_updates", category: "servers", server_id: "v1", title: "fsn1", detail: "12 security updates" }),
    it_("alert:disk", "alert", "warning", { alert_kind: "disk_full", category: "servers", server_id: "v1", title: "fsn1", detail: "/ is 86% full" }),
    it_("assigned:i1", "assigned", "request", { category: "issues", issue: { key: "STO-118", number: 118, title: "Retry", status: "todo", priority: "urgent" } }),
    it_("preview:p1:abc", "preview_approval", "request", { category: "code", repository: "web", pull_number: 42, sha: "abc1234" }),
    it_("stale:pg:1", "stale_page", "request", { category: "docs", page_id: "pg", at: "2026-10-12T11:00:00Z" }),
  ];

  it("keeps the server's order and a row for every item, an alert kind it doesn't name included", () => {
    const rows = inboxItems(server, NOW);
    expect(rows.map((r) => r.key)).toEqual(server.map((s) => s.key));
    expect(rows.find((r) => r.key === "alert:sec")).toMatchObject({ kind: "Security updates", title: "fsn1", action: "Open server", href: "/servers/v1" });
    expect(rows.find((r) => r.key === "alert:disk")).toMatchObject({ kind: "Disk 86% used", action: "Free space…", disk: { serverId: "v1", where: "fsn1" } });
    expect(rows.find((r) => r.key === "agent:a1")).toMatchObject({ kind: "An agent asks to roll back", title: "storefront / api" });
  });

  it("counts the same on Home, in the Inbox and in the sidebar", () => {
    const sidebar = server.length; // counts.needs_you is the server's list length
    const inbox = inboxItems(server, NOW).length;
    const home = homeNeeds(inboxItems(server, NOW));
    expect(inbox).toBe(sidebar);
    expect(home.total).toBe(sidebar);
    expect(home.shown.length + home.hidden.length).toBe(sidebar);
  });

  it("caps Home at 4, but a failed deploy, an offline server or a failed backup always shows", () => {
    const rows = inboxItems(server, NOW);
    const home = homeNeeds(rows);
    expect(home.shown.map((r) => r.key)).toEqual(["alert:b", "deploy:d1", "agent:a1", "alert:sec"]);
    expect(home.more).toBe("4 more: a full disk, an urgent issue, a preview to approve and a stale runbook");

    const problems = ["d1", "d2", "d3", "d4"].map((d) => it_(`deploy:${d}`, "deploy_failed", "problem", { service_id: "s1", deployment_id: d }));
    const offline = it_("alert:off", "alert", "problem", { alert_kind: "agent_offline", category: "servers", server_id: "v2" });
    const many = homeNeeds(inboxItems([...problems, offline, ...server.slice(5)], NOW));
    expect(many.shown.map((r) => r.key)).toEqual(["deploy:d1", "deploy:d2", "deploy:d3", "deploy:d4", "alert:off"]);
    expect(many.more).toBe("3 more: an urgent issue, a preview to approve and a stale runbook");

    const twice = homeNeeds(inboxItems([...server.slice(0, 4), ...server.slice(6, 7), it_("preview:p2:def", "preview_approval", "request", { category: "code", sha: "def5678" })], NOW));
    expect(twice.more).toBe("2 more: 2 previews to approve");
    expect(homeNeeds(inboxItems(server.slice(0, 3), NOW))).toMatchObject({ total: 3, hidden: [], more: "" });
  });

  it("links a failed deploy to the issue already opened from it", () => {
    const [row] = inboxItems([it_("deploy:d9", "deploy_failed", "problem", { service_id: "s1", deployment_id: "d9", issue_key: "BIL-031" })], NOW);
    expect(row.related).toEqual({ label: "Triage opened", key: "BIL-031", href: "/projects/p1/issues/31" });
  });
});

describe("healthParts (D-140)", () => {
  const svc = (runtime_status: string) => ({ runtime_status }) as WorkspaceService;
  const services = [svc("running"), svc("running"), svc("deploying"), svc("error")];
  const data = {
    open_alerts: [
      alert("backup_failed", "v2", { subject_name: "fsn1-prod-02" }),
      alert("disk_full", "v2", { subject_name: "fsn1-prod-02", detail: "/ is 86% full" }),
    ],
    deploys_24h: { total: 9, succeeded: 8, failed: 1, median_seconds: 60 },
  };
  it("says the state in one line each, as the board", () => {
    expect(healthParts(data, services, { online: 3, total: 3 }).map((p) => [p.tone, p.value, p.sub, p.href])).toEqual([
      ["running", "3 of 3 servers online", "", "/servers"],
      ["failed", "1 service failed", "1 deploying · 2 running", "/projects"],
      ["failed", "1 backup failed", "fsn1-prod-02", "/servers/v2#backup"],
      ["pending", "1 disk at 86%", "fsn1-prod-02", "/servers/v2#disk"],
      ["neutral", "9 deploys in 24h", "8 live", undefined],
    ]);
  });
  it("reads calm when nothing is wrong", () => {
    const calm = healthParts({ open_alerts: [], deploys_24h: { total: 1, succeeded: 1, failed: 0, median_seconds: 1 } }, [svc("running")], { online: 1, total: 2 });
    expect(calm.map((p) => [p.tone, p.value, p.sub])).toEqual([
      ["failed", "1 of 2 servers online", ""],
      ["running", "1 running", ""],
      ["running", "No failed backups", ""],
      ["running", "No disk warnings", ""],
      ["neutral", "1 deploy in 24h", "1 live"],
    ]);
    expect(healthParts({ open_alerts: [], deploys_24h: { total: 0, succeeded: 0, failed: 0, median_seconds: 0 } }, [], { online: 0, total: 0 })[0].value).toBe("Only the panel host");
  });
  // H12: a part whose read failed states nothing it couldn't read.
  it("says Unknown for a part whose read failed", () => {
    const parts = healthParts(data, undefined, undefined).map((p) => [p.tone, p.value, p.sub, p.href]);
    expect(parts[0]).toEqual(["neutral", "Unknown", "couldn’t load servers", "/servers"]);
    expect(parts[1]).toEqual(["neutral", "Unknown", "couldn’t load services", "/projects"]);
  });
});
