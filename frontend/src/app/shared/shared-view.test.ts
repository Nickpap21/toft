import { describe, expect, it } from "vitest";
import { liveFor, reachedFrom, runsOn, serverPart, statusLine, whoReaches } from "./shared-view";

const env = (id: string, project_id: string, name: string, preview = false) => ({ id, project_id, name, color: "gray" as const, protected: false, preview });
const svc = (id: string, server_id: string, over: Partial<{ scope: "environment" | "project" | "org"; project_id: string; environment_id: string; name: string }> = {}) => ({
  id,
  name: id,
  server_id,
  scope: "environment" as const,
  project_id: "",
  environment_id: "",
  ...over,
});
const ws = (id: string, server_id: string) => svc(id, server_id, { scope: "org" });
const servers = [
  { id: "s1", name: "fsn1-prod-01" },
  { id: "s2", name: "fsn1-prod-02" },
  { id: "s3", name: "hel1-prod-03" },
  { id: "s4", name: "empty-box" },
];
const projects = [
  { id: "sf", name: "storefront" },
  { id: "ba", name: "billing-api" },
  { id: "ms", name: "marketing-site" },
];
const envs = [
  env("e1", "sf", "production"),
  env("e2", "sf", "staging"),
  env("e3", "sf", "pr-42", true),
  env("e4", "ba", "production"),
  env("e5", "ms", "production"),
];
// The board: four workspace services on fsn1-prod-01, storefront's
// production beside them, the rest on two other servers.
const services = [
  ws("grafana", "s1"),
  ws("loki", "s1"),
  svc("web", "s1", { project_id: "sf", environment_id: "e1" }),
  svc("api", "s2", { project_id: "ba", environment_id: "e4" }),
  svc("site", "s2", { project_id: "ms", environment_id: "e5" }),
  svc("web-st", "s3", { project_id: "sf", environment_id: "e2" }),
  svc("web-pr", "s3", { project_id: "sf", environment_id: "e3" }),
];

describe("whoReaches", () => {
  it("says by alias on the workspace services' own server", () => {
    expect(whoReaches(servers, services, envs, projects, [])[0]).toEqual({ id: "s1", server: "fsn1-prod-01", text: "storefront’s production, by alias" });
  });

  it("names only what has an address on another server", () => {
    const rows = whoReaches(servers, services, envs, projects, [{ service_id: "grafana" }]);
    expect(rows[1]).toEqual({ id: "s2", server: "fsn1-prod-02", text: "billing-api, marketing-site: only grafana, at its address" });
    expect(rows[2]).toEqual({ id: "s3", server: "hel1-prod-03", text: "storefront’s staging and previews: only grafana, at its address" });
    expect(whoReaches(servers, services, envs, projects, [{ service_id: "grafana" }, { service_id: "loki" }])[1].text).toBe(
      "billing-api, marketing-site: only grafana and loki, at their addresses",
    );
  });

  it("says when no workspace service has an address", () => {
    expect(whoReaches(servers, services, envs, projects, [])[1].text).toBe("billing-api, marketing-site: none, no workspace service has an address");
  });

  it("leaves out a server running nothing, and names the panel host", () => {
    const rows = whoReaches(servers, [...services, svc("x", "", { project_id: "ba", environment_id: "e4" })], envs, projects, []);
    expect(rows.map((r) => r.server)).toEqual(["fsn1-prod-01", "fsn1-prod-02", "hel1-prod-03", "panel host"]);
  });
});

describe("the header's server part", () => {
  it("names one server, counts several, and says nothing with none", () => {
    expect(serverPart([{ server_id: "s1" }, { server_id: "s1" }], servers)).toBe("all on fsn1-prod-01");
    expect(serverPart([{ server_id: "s1" }, { server_id: "s2" }], servers)).toBe("on 2 servers");
    expect(serverPart([{ server_id: "" }], servers)).toBe("all on the panel host");
    expect(serverPart([], servers)).toBe("");
  });
});

describe("liveFor", () => {
  const now = Date.parse("2026-11-08T12:00:00Z");
  it("counts the live deployment's days, then hours and minutes", () => {
    expect(liveFor("running", "2026-10-18T11:00:00Z", now)).toBe("for 21 days");
    expect(liveFor("running", "2026-11-07T11:00:00Z", now)).toBe("for 1 day");
    expect(liveFor("running", "2026-11-08T09:30:00Z", now)).toBe("for 2 hours");
    expect(liveFor("running", "2026-11-08T11:59:30Z", now)).toBe("for 1 minute");
  });
  it("shows no duration without a live deployment, or when it isn't serving", () => {
    expect(liveFor("running", null, now)).toBe("");
    expect(liveFor("running", undefined, now)).toBe("");
    expect(liveFor("stopped", "2026-10-18T11:00:00Z", now)).toBe("");
  });
});

describe("statusLine", () => {
  it("counts each state, running first", () => {
    expect(statusLine([{ runtime_status: "running" }, { runtime_status: "running" }])).toBe("2 running");
    expect(statusLine([{ runtime_status: "failed" }, { runtime_status: "running" }, { runtime_status: "running" }])).toBe("2 running · 1 failed");
  });
});

describe("reachedFrom", () => {
  const aliased = (id: string, server_id: string) => ({ ...ws(id, server_id), connect_to: { port: 3100 } });
  it("is every project by its address when one exists and projects run elsewhere", () => {
    expect(reachedFrom(ws("grafana", "s1"), services, envs, projects, [{ service_id: "grafana" }])).toEqual({ by: "Every project", via: "by its address" });
  });
  it("is the environments on its server by alias", () => {
    expect(reachedFrom(aliased("loki", "s1"), services, envs, projects, [])).toEqual({ by: "storefront production", via: "by alias" });
  });
  it("is nothing when no project runs on its server", () => {
    expect(reachedFrom(aliased("x", "s4"), services, envs, projects, [])).toBeNull();
  });
  it("is every project by its address when it has no alias, even with no project elsewhere", () => {
    expect(reachedFrom(ws("grafana", "s1"), [ws("grafana", "s1"), svc("web", "s1")], envs, projects, [{ service_id: "grafana" }])).toEqual({ by: "Every project", via: "by its address" });
  });
  it("is no other service with neither an alias nor an address", () => {
    const none = { by: "No other service", via: "it has no alias or address" };
    expect(reachedFrom(ws("loki", "s1"), services, envs, projects, [])).toEqual(none);
    expect(reachedFrom({ ...ws("loki", "s1"), connect_to: { port: 0 } }, services, envs, projects, [])).toEqual(none); // the same test as Reached as
  });
});

describe("runsOn", () => {
  it("names who runs on a server, as the Logs table's second line", () => {
    expect(runsOn("s1", services, envs, projects)).toBe("storefront’s production, the workspace’s 2");
    expect(runsOn("s2", services, envs, projects)).toBe("billing-api, marketing-site");
    expect(runsOn("s3", services, envs, projects)).toBe("storefront’s staging, pr-42");
  });
});
