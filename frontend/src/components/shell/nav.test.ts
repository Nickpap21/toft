import { describe, expect, it } from "vitest";
import { isPublicPath } from "@/lib/routes";
import { currentFor, isActive, NAV_SECTIONS, navPathname, PROJECT_TABS, projectTabFor, visibleSections } from "./nav-config";

describe("isActive", () => {
  it("matches whole path segments", () => {
    expect(isActive("/projects/abc/services/x", "/projects")).toBe(true);
    expect(isActive("/projects", "/projects")).toBe(true);
    expect(isActive("/serversx", "/servers")).toBe(false);
    expect(isActive("/settings/appearance", "/settings")).toBe(true);
    expect(isActive("/settings", "/settings/appearance")).toBe(false);
  });
});

describe("currentFor", () => {
  it("marks the page itself as the page and its ancestors as current", () => {
    expect(currentFor("/settings", "/settings")).toBe("page");
    expect(currentFor("/settings/appearance", "/settings")).toBe("true");
    expect(currentFor("/servers", "/settings")).toBeUndefined();
  });
});

describe("navPathname", () => {
  it("marks Inbox, not Settings, on an agent's request (D-145)", () => {
    const p = navPathname("/settings/agents/requests/abc");
    expect(currentFor(p, "/inbox")).toBe("true");
    expect(currentFor(p, "/settings")).toBeUndefined();
    expect(navPathname("/settings/agents")).toBe("/settings/agents");
  });
});

describe("visibleSections", () => {
  it("shows every built module, Inbox, My issues and Cycles among them (D-32, D-88, D-90)", () => {
    const labels = visibleSections().flatMap((s) => s.items.map((i) => i.label));
    expect(labels).toEqual(["Home", "Inbox", "My issues", "Cycles", "Servers", "Domains", "Workspace services", "Audit log"]);
  });

  it("drops a section whose items are all hidden", () => {
    const sections = visibleSections([...NAV_SECTIONS, { id: "later", label: "Later", items: [{ ...NAV_SECTIONS[0].items[1], available: false }] }]);
    expect(sections.map((s) => s.id)).not.toContain("later");
  });

  it("shows Issues (D-81) and Docs (D-86)", () => {
    expect(PROJECT_TABS.filter((t) => t.enabled).map((t) => t.label)).toEqual(["Overview", "Issues", "Docs", "Services", "Environments", "Settings"]);
  });
});

describe("isPublicPath", () => {
  it.each([
    ["/", true],
    ["/login", true],
    ["/register", true],
    ["/invite/tok", true],
    ["/invite", true],
    ["/dashboard", false],
    ["/settings/appearance", false],
    ["/invites", false],
  ])("%s → %s", (path, expected) => {
    expect(isPublicPath(path)).toBe(expected);
  });
});

describe("projectTabFor", () => {
  it("puts a service and New service under Services, an environment under Environment", () => {
    expect(projectTabFor("/projects/p1", "p1")).toBe("overview");
    expect(projectTabFor("/projects/p1/services", "p1")).toBe("services");
    expect(projectTabFor("/projects/p1/services/s1", "p1")).toBe("services");
    expect(projectTabFor("/projects/p1/services/new", "p1")).toBe("services");
    expect(projectTabFor("/projects/p1/environments/e1", "p1")).toBe("environments");
    expect(projectTabFor("/projects/p1/settings", "p1")).toBe("settings");
    expect(projectTabFor("/projects/p1/code", "p1")).toBe("code");
    expect(projectTabFor("/projects/p1/code/commits/abc", "p1")).toBe("code");
    expect(projectTabFor("/projects/p10", "p1")).toBeNull();
  });
});
