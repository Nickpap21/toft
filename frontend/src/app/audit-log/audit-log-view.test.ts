import { describe, expect, it } from "vitest";
import { AUDIT_ACTIONS } from "@/lib/audit";
import { ACTION_FILTERS, emptyLine, fromLink, periodSince, whoParams } from "./audit-log-view";

describe("the Who select", () => {
  it("maps each group to the API's parameters", () => {
    expect(whoParams("")).toEqual({});
    expect(whoParams("people")).toEqual({ agent: "none", api_key: "none" });
    expect(whoParams("agents")).toEqual({ agent: "any" });
    expect(whoParams("keys")).toEqual({ api_key: "any" });
  });
  it("reads a link: any and none are groups, anything else a connection", () => {
    expect(fromLink("any")).toEqual({ who: "agents", agent: "" });
    expect(fromLink("none")).toEqual({ who: "people", agent: "" });
    expect(fromLink("c-1")).toEqual({ who: "", agent: "c-1" });
    expect(fromLink("")).toEqual({ who: "", agent: "" });
  });
});

describe("the Action select", () => {
  it("has the board's labels first, then the catalogue's other groups", () => {
    expect(ACTION_FILTERS.map((a) => a.label)).toEqual([
      "Deploys and rollbacks", "Agent calls", "Approvals", "Access", "Variables and secrets",
      "Services", "Servers", "Backups", "Observability", "Alerts", "Deploy policy", "Code", "Docs", "Agents",
    ]);
  });
  it("sends the actions of the chosen label", () => {
    const of = (label: string) => ACTION_FILTERS.find((a) => a.label === label)!.actions;
    expect(of("Agent calls")).toEqual(["agent.call"]);
    expect(of("Approvals")).toEqual(["agent.approved", "agent.denied"]);
    expect(of("Deploys and rollbacks")).toContain("deployment.rollback");
  });
  it("leaves no action out", () => {
    const listed = new Set(ACTION_FILTERS.flatMap((a) => a.actions));
    expect(AUDIT_ACTIONS.filter((a) => !listed.has(a.action))).toEqual([]);
  });
});

describe("the Period select", () => {
  const now = new Date(2026, 10, 2, 14, 22);
  it("starts Today at local midnight", () => {
    expect(periodSince("today", now)).toBe(new Date(2026, 10, 2).toISOString());
  });
  it("counts back the days, and Everything has no start", () => {
    expect(periodSince("7", now)).toBe(new Date(now.getTime() - 7 * 86_400_000).toISOString());
    expect(periodSince("30", now)).toBe(new Date(now.getTime() - 30 * 86_400_000).toISOString());
    expect(periodSince("all", now)).toBeUndefined();
  });
});

describe("the empty table", () => {
  const base = { hasMore: false, search: "", filtered: false, phrase: "in the last 7 days" };
  it("never says nothing while a next page exists", () => {
    const line = emptyLine({ ...base, hasMore: true, filtered: true, search: "api" });
    expect(line).not.toMatch(/Nothing happened|No entries match/);
    expect(line).toMatch(/older entries/i);
  });
  it("says what didn't match, or that nothing happened", () => {
    expect(emptyLine(base)).toBe("Nothing happened in the last 7 days.");
    expect(emptyLine({ ...base, filtered: true })).toBe("No entries match these filters in the last 7 days.");
    expect(emptyLine({ ...base, filtered: true, search: "api" })).toBe("No entries match “api” in the last 7 days.");
  });
  it("reads as a sentence for Everything", () => {
    expect(emptyLine({ ...base, phrase: "kept" })).toBe("Nothing has happened in this workspace yet.");
    expect(emptyLine({ ...base, phrase: "kept", filtered: true })).toBe("No entries match these filters.");
  });
});
