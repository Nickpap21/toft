import { describe, expect, it } from "vitest";
import type { AuditEntry } from "@/lib/api/org.service";
import { actionGroups, agentOutcome, auditAction, auditDetail, auditHref, auditTargetName, humanizeAction } from "./audit";

const entry = (over: Partial<AuditEntry>): AuditEntry => ({
  id: "a",
  org_id: "o",
  actor_id: "u",
  actor: "admin",
  action: "deployment.triggered",
  resource: "storefront / web",
  created_at: "2026-10-13T14:04:00Z",
  target: { kind: "service", id: "s", name: "storefront / web", deleted: false, project_id: "p", service_id: "s", environment: "production" },
  detail: {},
  ...over,
});

describe("audit catalogue", () => {
  it("labels known actions and spells out unknown ones", () => {
    expect(auditAction("apikey.revoked").label).toBe("Revoked an API key");
    expect(humanizeAction("deployment.triggered")).toBe("deployed");
    expect(auditAction("something.new").label).toBe("something new");
  });
  it("groups the filter in the board's order", () => {
    expect(actionGroups().map((g) => g.group)).toEqual(["Deploys", "Services", "Variables and secrets", "Servers", "Backups", "Access", "Observability", "Alerts", "Deploy policy", "Code", "Docs", "Agents"]);
  });
});

describe("a Settings save", () => {
  it("names the settings it changed, and is in the filter", () => {
    expect(auditAction("service.settings_changed")).toMatchObject({ label: "Changed a service’s settings", group: "Services" });
    expect(actionGroups().find((g) => g.group === "Services")?.actions.map((a) => a.action)).toContain("service.settings_changed");
    expect(auditDetail(entry({ action: "service.settings_changed", detail: { fields: ["start_command", "ip_allowlist", "basic_auth_password", "new_thing"] } })).text)
      .toBe("Start command, IP allowlist, basic auth password, new thing");
  });
  it("names the domain, file, mounts, server or volume of the other Settings saves", () => {
    const services = actionGroups().find((g) => g.group === "Services")?.actions.map((a) => a.action);
    for (const a of ["service.domain_added", "service.domain_removed", "service.config_file_saved", "service.config_file_deleted", "service.shared_volumes_changed", "service.server_changed", "service.volume_purged"])
      expect(services).toContain(a);
    expect(auditAction("service.volume_purged")).toMatchObject({ label: "Destroyed a volume’s data", phrase: "destroyed the data in a volume of" });
    expect(auditAction("service.domain_removed")?.phrase).toBe("removed a domain from");
    expect(auditAction("service.config_file_saved")?.phrase).toBe("saved a config file for");
    expect(auditDetail(entry({ action: "service.domain_added", detail: { hostname: "shop.example.com" } }))).toEqual({ text: "", code: "shop.example.com" });
    expect(auditDetail(entry({ action: "service.config_file_deleted", detail: { path: "/etc/app.conf" } }))).toEqual({ text: "", code: "/etc/app.conf" });
    expect(auditDetail(entry({ action: "service.volume_purged", detail: { mount: "data" } }))).toEqual({ text: "", code: "data" });
    expect(auditDetail(entry({ action: "service.shared_volumes_changed", detail: { mounts: [{ name: "images", target: "/data", read_only: true }, { name: "cache", target: "/cache", read_only: false }] } })).text)
      .toBe("images at /data (read-only), cache at /cache");
    expect(auditDetail(entry({ action: "service.shared_volumes_changed", detail: { mounts: [] } })).text).toBe("None now");
    expect(auditDetail(entry({ action: "service.server_changed", detail: { from: "srv-a", to: "" } })).text).toBe("To the panel host");
    expect(auditDetail(entry({ action: "service.server_changed", detail: { from: "", to: "srv-a" } })).text).toBe("From the panel host to another server");
  });
});

describe("deploys", () => {
  it("say why Toft redeployed on its own (D-150)", () => {
    expect(auditDetail(entry({}))).toEqual({ text: "To production" });
    expect(auditDetail(entry({ actor_id: "", actor: "Toft", detail: { reason: "certificate issued: switch to HTTPS" } }))).toEqual({ text: "Certificate issued: switch to HTTPS" });
  });
});

describe("host shells and commands", () => {
  it("say where from, how long and what ran", () => {
    expect(auditAction("server.shell_opened").label).toBe("Opened a host shell");
    expect(auditAction("server.agent_revoked")).toMatchObject({ label: "Revoked a server agent", group: "Servers" });
    expect(auditDetail(entry({ action: "server.agent_joined", detail: { version: "0.17.3", from: "198.51.100.24", fingerprint: "SHA256:ab" } }))).toEqual({ text: "Agent 0.17.3 from 198.51.100.24", code: "SHA256:ab" });
    expect(auditDetail(entry({ action: "server.shell_opened", detail: { from: "198.51.100.24" } })).text).toBe("From 198.51.100.24");
    expect(auditDetail(entry({ action: "server.shell_closed", detail: { seconds: 720 } })).text).toBe("Open 12 min");
    expect(auditDetail(entry({ action: "server.shell_closed", detail: { seconds: 40, restart: true } })).text).toBe("Ended by a restart, open 40 s");
    expect(auditDetail(entry({ action: "server.command_run", detail: { command: "systemctl restart docker" } }))).toEqual({ text: "", code: "systemctl restart docker" });
  });
});

describe("auditDetail", () => {
  it("writes the board's Details", () => {
    expect(auditDetail(entry({})).text).toBe("To production");
    expect(auditDetail(entry({ action: "apikey.created", detail: { name: "release-ci", project_name: "storefront", scopes: ["deploy"] } }))).toEqual({
      text: "storefront only:",
      code: "deploy",
    });
    expect(auditDetail(entry({ action: "apikey.revoked", detail: { name: "deploy-test", scopes: [], created_at: "2026-08-20T10:00:00Z" } })).text).toBe(
      "Full account key, created 2026-08-20",
    );
    expect(auditDetail(entry({ action: "member.role_changed", detail: { from: "developer", to: "owner" } })).text).toBe("Developer → Owner");
    expect(auditDetail(entry({ action: "deploy_policy.exception.granted", detail: { allowed_rules: ["group_add"], reason: "nginx" } }))).toEqual({
      text: "Allows",
      code: "group_add",
      after: "“nginx”",
    });
    expect(auditDetail(entry({ action: "server.shared", target: { kind: "server", id: "x", name: "fsn1", deleted: false, other_workspace: "acme" } })).text).toBe(
      "With acme",
    );
    expect(auditDetail(entry({ action: "environment.protection_changed", detail: { name: "production", protected: false } })).text).toBe("No longer protected");
    expect(auditDetail(entry({ action: "observability.loki_changed", detail: { url: "https://user:xxxxx@logs.example.com" } }))).toEqual({
      text: "Now",
      code: "https://user:xxxxx@logs.example.com",
    });
  });
});

describe("a server's disk and updates", () => {
  it("say how a clean went: a failed one writes the same action", () => {
    expect(auditDetail(entry({ action: "server.disk_cleaned", detail: { outcome: "succeeded", freed_bytes: 3_200_000_000 } })).text).toBe("Freed 3.2 GB");
    expect(auditDetail(entry({ action: "server.disk_cleaned", detail: { outcome: "failed", error: "the server didn’t answer" } })).text).toBe("Failed: the server didn’t answer");
    expect(auditDetail(entry({ action: "server.disk_cleaned", detail: { outcome: "failed" } })).text).toBe("Failed");
    expect(auditDetail(entry({ action: "server.disk_cleaned", detail: {} })).text).toBe("");
  });
  it("say when a clean was the automatic one", () => {
    expect(auditDetail(entry({ action: "server.disk_clean_started", detail: { automatic: true } })).text).toBe("The automatic clean");
    expect(auditDetail(entry({ action: "server.disk_clean_started", detail: { automatic: false } })).text).toBe("");
  });
  it("name the removed volume, with its size when Docker reported one", () => {
    expect(auditDetail(entry({ action: "server.volume_removed", detail: { name: "old_data", bytes: 205_000_000 } }))).toEqual({ text: "", code: "old_data", after: "205 MB" });
    expect(auditDetail(entry({ action: "server.volume_removed", detail: { name: "old_data", bytes: -1 } }))).toEqual({ text: "", code: "old_data", after: "" });
  });
  it("say why updates failed", () => {
    expect(auditDetail(entry({ action: "server.updates_failed", detail: { action: "install", error: "dpkg was locked" } })).text).toBe("dpkg was locked");
  });
});

describe("auditHref and auditTargetName", () => {
  it("links what still exists and names what is gone", () => {
    expect(auditHref(entry({}))).toBe("/projects/p/services/s");
    const gone = entry({ action: "service.deleted", target: { kind: "service", id: "s9", name: "s9", deleted: true } });
    expect(auditHref(gone)).toBeUndefined();
    expect(auditTargetName(gone)).toBe("a deleted service");
    // A repository and a docs page open where they live (D-104).
    expect(auditHref(entry({ target: { kind: "repository", id: "r1", name: "storefront / shop", project_id: "p", deleted: false } }))).toBe("/projects/p/code/shop");
    expect(auditHref(entry({ target: { kind: "doc_page", id: "pg", name: "storefront / Runbook", project_id: "p", deleted: false } }))).toBe("/projects/p/docs/pg");
    expect(auditTargetName(entry({ target: { kind: "doc_page", id: "pg", name: "pg", deleted: true } }))).toBe("a deleted page");
    expect(auditDetail(entry({ action: "doc.step_run", detail: { step: 0, host: false, outcome: "ok" } }), "Northwind").text).toBe("Step 1 in its service’s container");
    expect(auditDetail(entry({ action: "doc.step_run", detail: { step: 2, host: true, outcome: "failed" } }), "Northwind").text).toBe("Step 3 on its server’s host: it failed");
    expect(auditTargetName(entry({ target: { kind: "service", id: "s8", name: "cron-legacy", deleted: true } }))).toBe("cron-legacy");
    expect(auditHref(entry({ action: "alerts.secret_rotated", target: { kind: "org", id: "o", name: "Alert webhook", deleted: false } }))).toBe("/settings/alerts");
  });
});

describe("a revoked project key", () => {
  it("says its project, scopes and age", () => {
    expect(auditDetail(entry({ action: "apikey.revoked", detail: { project_name: "storefront", scopes: ["deploy"], created_at: "2026-10-15T10:00:00Z" } }))).toEqual({
      text: "storefront only:",
      code: "deploy",
      after: "· created 2026-10-15",
    });
  });
});

describe("agent rows (D-100)", () => {
  const call = (detail: Record<string, unknown>) =>
    entry({ action: "agent.call", agent_id: "c", agent_name: "Claude Code", detail });

  it("chip each outcome", () => {
    expect(agentOutcome(call({ outcome: "allowed" }))).toEqual({ label: "Allowed", tone: "neutral" });
    expect(agentOutcome(call({ outcome: "asked" }))).toEqual({ label: "Asked a person", tone: "amber" });
    expect(agentOutcome(call({ outcome: "approved", detail: "approved by sam" }))).toEqual({ label: "Ran, approved", tone: "green" });
    expect(agentOutcome(call({ outcome: "denied" }))).toEqual({ label: "Denied", tone: "red" });
    expect(agentOutcome(call({ outcome: "expired" })).tone).toBe("grey");
    // A policy refusal is the policy working: red is for failures (D-147).
    expect(agentOutcome(call({ outcome: "refused" }))).toEqual({ label: "Refused", tone: "neutral" });
    expect(agentOutcome(call({ outcome: "failed" }))).toEqual({ label: "Failed", tone: "red" });
  });

  it("say who approved a call, now the chip doesn't", () => {
    expect(auditDetail(call({ tool: "deploy", outcome: "approved", detail: "approved by admin" })).text).toBe("To production. With admin’s approval");
    expect(auditDetail(call({ tool: "deploy", outcome: "approved" })).text).toBe("To production");
  });

  it("say what an approval also allows (D-146), with no clock time", () => {
    const approved = (also_allows?: Record<string, unknown>) =>
      entry({ action: "agent.approved", detail: { agent: "Claude Code", for: "sam", tool: "deploy", target: "storefront / api", also_allows } });
    const window = "30 minutes after the deploy goes live, once";
    expect(auditDetail(approved({ rollback_to: "d", rollback_commit: "9b7d3f0aa11", window })).also)
      .toBe("Also allows: rolling back to 9b7d3f0 once, for 30 minutes after the deploy goes live");
    expect(auditDetail(approved({ rollback_to: "d", rollback_commit: "", window })).also)
      .toBe("Also allows: rolling back to the deployment that was live once, for 30 minutes after the deploy goes live");
    expect(auditDetail(approved()).also).toBeUndefined();
  });

  it("say why a call was refused, or what it did", () => {
    expect(auditDetail(call({ tool: "deploy", outcome: "refused", detail: "agents_paused" })).text).toBe("Agents were paused");
    expect(auditDetail(call({ tool: "deploy", outcome: "refused", detail: "insufficient_scope" })).text).toBe("Its connection doesn't allow this");
    expect(auditDetail(call({ tool: "deploy", outcome: "refused", detail: "rate_limited" }))).toEqual({ text: "To production", code: "rate_limited" });
    expect(auditDetail(call({ tool: "deploy", outcome: "asked", args: { reason: "Fixes STO-118." } })).text).toBe("To production. “Fixes STO-118.”");
    expect(auditDetail(call({ tool: "deploy", outcome: "allowed", args: { reason: "x".repeat(130) } })).text).toBe(`To production. “${"x".repeat(120)}…”`);
    expect(auditDetail(call({ tool: "get_deploy_logs", outcome: "allowed", args: { lines: 50 } })).text).toBe("production, last 50 lines");
    expect(auditDetail(call({ tool: "get_deploy_logs", outcome: "allowed", args: {} })).text).toBe("production, last 200 lines");
    expect(auditDetail(call({ tool: "list_projects", outcome: "allowed" })).text).toBe("");
  });

  it("describe connections, policy changes and decisions", () => {
    expect(auditDetail(entry({ action: "agent.connected", created_at: "2026-10-13T13:36:00Z", detail: { scopes: ["read", "deploy"], all_projects: false, projects: 1 } })).text)
      .toBe("1 project only: read, deploy. Until 2027-01-11");
    expect(auditDetail(entry({ action: "agent.connected", created_at: "2026-10-13T13:36:00Z", detail: { scopes: ["read"], all_projects: true, projects: 0 } })).text)
      .toBe("Every project: read. Until 2027-01-11");
    expect(auditDetail(entry({ action: "agent.policy_changed", detail: { changes: ["deploy, other: ask → allow", "deploy, protected: ask → never"] } })).text)
      .toBe("Deploy or roll back, other environments: Ask a person → Allow; Deploy or roll back, protected environments: Ask a person → Never");
    expect(auditDetail(entry({ action: "agent.approved", detail: { agent: "Claude Code", for: "admin", tool: "deploy", target: "storefront / api", note: "ok" } })))
      .toEqual({ text: "Claude Code for admin:", code: "deploy", after: "storefront / api “ok”" });
    expect(auditDetail(entry({ action: "agent.revoked", detail: { name: "Cursor", by_agent: true } })).text).toBe("The agent disconnected itself");
  });

  it("say which rule decided a call (D-101) and what the session-2 tools did", () => {
    const sec = call({ tool: "set_secret", outcome: "approved", level: "workspace", env_class: "other", args: { key: "STRIPE_WEBHOOK_SECRET" } });
    expect(auditDetail({ ...sec, target: { ...sec.target, environment: "staging" } }, "Founders").text)
      .toBe("staging, STRIPE_WEBHOOK_SECRET (value never recorded). Founders’ rule: Ask a person");
    expect(auditDetail(entry({ action: "agent.call", resource: "storefront", target: { kind: "project", id: "p", name: "storefront", deleted: false }, detail: { tool: "comment", outcome: "allowed", level: "workspace", args: { issue_number: 131 } } }), "Founders").text)
      .toBe("On issue 131. Founders’ rule: Allow");
    expect(auditDetail(entry({ action: "agent.call", resource: "storefront", target: { kind: "project", id: "p", name: "storefront", deleted: false }, detail: { tool: "open_pr", outcome: "allowed", level: "default", args: { head: "agent/sto-118", base: "main" } } }), "Founders").text)
      .toBe("agent/sto-118 into main; its preview waits for approval. The default: Allow");
    expect(auditDetail(entry({ action: "agent.call", resource: "billing-api / worker", target: { kind: "service", id: "s", name: "billing-api / worker", deleted: false }, detail: { tool: "run_runbook_step", outcome: "refused", detail: "agent_policy_never", level: "project", args: { step: 1 } } }), "Founders").text)
      .toBe("billing-api’s rule says Never");
    expect(auditDetail(call({ tool: "deploy", outcome: "refused", detail: "agent_policy_never" })).text).toBe("production: the policy says Never");
    const never = entry({ action: "agent.call", target: { kind: "service", id: "s", name: "billing-api / worker", environment: "production", deleted: false }, detail: { tool: "run_runbook_step", outcome: "refused", detail: "agent_policy_never", level: "project" } });
    expect(auditDetail(never, "Founders").text).toBe("production: billing-api’s rule says Never");
    expect(auditDetail({ ...never, target: { ...never.target, environment: undefined } }, "Founders").text).toBe("billing-api’s rule says Never");
    expect(auditDetail(call({ tool: "create_branch", outcome: "allowed", args: { name: "agent/x", from: "main" } })).text).toBe("agent/x from main");
  });

  it("describe a project's rules and an agent's push", () => {
    expect(auditAction("agent.project_policy_changed").label).toBe("Changed a project’s agent rules");
    expect(auditDetail(entry({ action: "agent.project_policy_changed", detail: { project: "billing-api", changes: ["runbooks, protected: as the workspace → never", "code, any: allow → as the workspace"] } }), "Founders").text)
      .toBe("Runbook steps, protected environments: as Founders → Never; Branches and pull requests: Allow → as Founders");
    expect(auditAction("agent.pushed").label).toBe("Pushed to its branch");
    expect(auditDetail(entry({ action: "agent.pushed", detail: { branch: "agent/sto-118", before: "9b7d3f0aa", after: "3f1a2b9cc" } })))
      .toEqual({ text: "agent/sto-118:", code: "9b7d3f0..3f1a2b9" });
    expect(auditDetail(entry({ action: "agent.pushed", detail: { branch: "agent/sto-118", before: "0000000000", after: "3f1a2b9cc" } })))
      .toEqual({ text: "Created agent/sto-118 at", code: "3f1a2b9" });
  });

  it("open a connection's target on Settings → Agents", () => {
    expect(auditHref(entry({ action: "agent.connected", target: { kind: "agent", id: "c", name: "Claude Code", deleted: false } }))).toBe("/settings/agents");
    expect(auditHref(entry({ action: "agent.approved", target: { kind: "agent_approval", id: "r1", name: "storefront / api", deleted: false } }))).toBe("/settings/agents/requests/r1");
    expect(auditHref(entry({ action: "agent.policy_changed", target: { kind: "org", id: "o", name: "the workspace", deleted: false } }))).toBe("/settings/agents");
  });
});
