import { describe, expect, it } from "vitest";
import type { AuditEntry } from "@/lib/api/org.service";
import {
  callTarget,
  clientLabel,
  expiresSoon,
  loosens,
  minutesLeft,
  outcomeChip,
  projectDiffs,
  projectLoosens,
  reaches,
  resetLoosens,
  waitingTitle,
  envColumns,
} from "./view";

describe("agents view", () => {
  it("knows what loosens", () => {
    expect(loosens("never", "allow")).toBe(true);
    expect(loosens("ask", "allow")).toBe(true);
    expect(loosens("allow", "ask")).toBe(false);
    expect(loosens("ask", "never")).toBe(false);
  });

  it("titles a waiting request by its tool", () => {
    expect(waitingTitle({ agent: "Claude Code", tool: "rollback", target: "billing-api / worker in production" }))
      .toBe("Claude Code wants to roll back billing-api / worker in production");
    expect(waitingTitle({ agent: "Claude Code", tool: "deploy", target: "storefront / api in production" }))
      .toBe("Claude Code wants to deploy storefront / api in production");
  });

  it("titles every tool", () => {
    expect(waitingTitle({ agent: "Claude Code", tool: "set_secret", target: "billing-api / api in staging" }))
      .toBe("Claude Code wants to set a secret of billing-api / api in staging");
    expect(waitingTitle({ agent: "Claude Code", tool: "run_runbook_step", target: "storefront / worker in production" }))
      .toBe("Claude Code wants to run a runbook step in storefront / worker in production");
  });

  it("lists a project's own cells and knows when a change loosens", () => {
    const ws = [
      { action: "deploy" as const, env_class: "other" as const, decision: "allow" as const },
      { action: "runbooks" as const, env_class: "protected" as const, decision: "ask" as const },
      { action: "code" as const, env_class: "any" as const, decision: "allow" as const },
    ];
    const own = [
      { action: "runbooks" as const, env_class: "protected" as const, decision: "never" as const },
      { action: "deploy" as const, env_class: "other" as const, decision: "ask" as const },
      { action: "code" as const, env_class: "any" as const, decision: "ask" as const },
    ];
    expect(projectDiffs(own, ws)).toEqual([
      { where: "Deploy or roll back · Other environments", decision: "ask", ws: "allow" },
      { where: "Branches and pull requests", decision: "ask", ws: "allow" },
      { where: "Runbook steps · Protected", decision: "never", ws: "ask" },
    ]);
    // Dropping a stricter rule loosens; setting a stricter one doesn't.
    expect(projectLoosens("ask", "allow", "")).toBe(true);
    expect(projectLoosens(undefined, "allow", "ask")).toBe(false);
    expect(projectLoosens("never", "ask", "ask")).toBe(true);
    expect(projectLoosens("allow", "ask", "")).toBe(false);
    expect(resetLoosens(own, ws)).toBe(true);
    expect(resetLoosens([{ action: "deploy", env_class: "other", decision: "allow" }], [{ action: "deploy", env_class: "other", decision: "ask" }])).toBe(false);
  });

  it("counts minutes left, rounded up, never negative", () => {
    const now = Date.parse("2026-10-22T10:00:00Z");
    expect(minutesLeft("2026-10-22T10:13:10Z", now)).toBe(14);
    expect(minutesLeft("2026-10-22T09:00:00Z", now)).toBe(0);
  });

  it("labels the client and what it reaches", () => {
    expect(clientLabel({ client_kind: "cimd", client: "https://claude.ai/oauth/meta" })).toBe("OAuth · claude.ai");
    expect(clientLabel({ client_kind: "dcr", client: "toft_client_x" })).toBe("OAuth · registered client");
    expect(reaches({ all_projects: true, projects: [] })).toBe("Every project");
    expect(reaches({ all_projects: false, projects: ["a", "b"] })).toBe("a, b");
  });

  it("flags expiry within a week", () => {
    const now = Date.parse("2026-10-22T10:00:00Z");
    expect(expiresSoon("2026-10-26T10:00:00Z", now)).toBe(true);
    expect(expiresSoon("2027-01-20T10:00:00Z", now)).toBe(false);
  });

  it("maps outcomes to chips and builds the target", () => {
    expect(outcomeChip("allowed")).toEqual({ label: "Allowed", tone: "ok" });
    expect(outcomeChip("asked")).toEqual({ label: "Asked a person", tone: "wait" });
    expect(outcomeChip("refused").tone).toBe("no");
    expect(outcomeChip(undefined).label).toBe("Unknown");
    const e = (over: Partial<AuditEntry>): AuditEntry => ({
      id: "a", org_id: "o", actor_id: "u", actor: "admin", action: "agent.call", resource: "storefront / api", created_at: "2026-10-22T10:00:00Z",
      target: { kind: "service", id: "s", name: "storefront / api", deleted: false, environment: "staging" }, detail: {}, ...over,
    });
    expect(callTarget(e({}), "Founders")).toBe("storefront / api · staging");
    expect(callTarget(e({ resource: "billing-api / worker", target: { kind: "service", id: "s", name: "billing-api / worker", deleted: false }, detail: { detail: "agents_paused" } }), "Founders"))
      .toBe("billing-api / worker · agents paused");
    expect(callTarget(e({ detail: { level: "workspace" } }), "Founders")).toBe("storefront / api · staging · Founders’ rule");
    expect(callTarget(e({ detail: { level: "project" } }), "Founders")).toBe("storefront / api · staging · storefront’s rule");
  });
});

describe("the policy's columns (board Agents)", () => {
  it("name the workspace's environments of each class", () => {
    const cols = envColumns([
      { name: "staging", protected: false }, { name: "dev", protected: false }, { name: "staging", protected: false },
      { name: "production", protected: true }, { name: "pr-42", preview: { pull_number: 42 }, protected: false },
    ]);
    expect(cols.map((c) => c.hint)).toEqual(["pr-42", "staging, dev", "production"]);
    expect(envColumns([])[0].hint).toBe("pull requests’ previews");
    expect(envColumns(["a", "b", "c", "d"].map((name) => ({ name, protected: false })))[1].hint).toBe("a, b, c, …");
  });
});

