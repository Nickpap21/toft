import { describe, expect, it } from "vitest";
import { isContainerAlias, localLokiAddress, localLogsWarning, logsCell, lokiState, serverLogsLine, stackOf } from "./observability-view";

describe("Loki's address", () => {
  it("knows an alias from an address every server reaches", () => {
    expect(isContainerAlias("http://loki:3100")).toBe(true);
    expect(isContainerAlias("https://loki.founders.example")).toBe(false);
    expect(isContainerAlias("http://10.0.0.5:3100")).toBe(false);
    expect(isContainerAlias("not a url")).toBe(false);
    expect(lokiState(undefined)).toBe("none");
    expect(lokiState("http://loki:3100")).toBe("alias");
    expect(lokiState("https://loki.founders.example")).toBe("ok");
  });
});

describe("logsCell", () => {
  const loki = "https://loki.founders.example";
  const push = `${loki}/loki/api/v1/push`;

  it("ships a container that logs to Loki at the address now set", () => {
    expect(logsCell([{ name: "web", driver: "loki", loki_url: push }], true, false, loki)).toMatchObject({ label: "Loki", ships: true });
  });

  it("says when it still pushes to an older address", () => {
    const c = logsCell([{ name: "web", driver: "loki", loki_url: "http://loki:3100/loki/api/v1/push" }], true, false, loki);
    expect(c).toMatchObject({ label: "Loki, old address", ships: false });
    expect(c.why).toBe("pushes to http://loki:3100 until its next deploy");
  });

  it("still ships at an older address that reaches Loki", () => {
    const local = [{ name: "web", driver: "loki", loki_url: "http://127.0.0.1:39100/loki/api/v1/push" }];
    expect(logsCell(local, true, false, loki)).toMatchObject({ label: "Loki, old address", tone: "pending", ships: true });
    expect(logsCell(local, true, false, undefined)).toMatchObject({ label: "Loki, old address", ships: false }); // no Loki to reach any more
  });

  it("keeps logs on the server, and says why", () => {
    const json = [{ name: "web", driver: "json-file" }];
    expect(logsCell(json, true, false, loki).why).toBe("started before Loki was set up; ships from its next deploy");
    expect(logsCell(json, true, false, "http://loki:3100").why).toBe("Loki’s address is an alias");
    expect(logsCell(json, true, false, undefined)).toMatchObject({ label: "This server only", tone: "muted" });
    expect(logsCell(json, true, true, loki).why).toBe("compose stacks keep their logs on the server");
    expect(logsCell([], false, false, loki).label).toBe("Not running");
  });

  it("ships to Loki on its own server when no address is set (D-148)", () => {
    const local = [{ name: "web", driver: "loki", loki_url: "http://127.0.0.1:39100/loki/api/v1/push" }];
    const json = [{ name: "web", driver: "json-file" }];
    expect(logsCell(local, true, false, undefined, "app")).toMatchObject({ label: "Loki, on this server", ships: true });
    expect(logsCell(json, true, false, undefined, "app")).toMatchObject({ tone: "pending", why: "ships to Loki on this server from its next deploy" });
    expect(logsCell(json, true, false, undefined, "loki").why).toBe("Loki keeps its own logs here");
    expect(logsCell(json, true, false, undefined, "elsewhere")).toMatchObject({ label: "This server only", why: "needs an external Loki address" });
    expect(logsCell(json, true, true, undefined, "app").why).toBe("compose stacks keep their logs on the server");
    expect(logsCell([{ name: "web", driver: "local" }], true, false, undefined, "app", true)).toMatchObject({ label: "Loki, on this server", ships: true });
    expect(logsCell(local, true, false, "http://loki:3100", "app")).toMatchObject({ ships: true }); // an old alias doesn't hide it
  });
});

describe("the stack", () => {
  const svcs = [
    { id: "l1", scope: "org" as const, addon_slug: "loki" },
    { id: "g1", scope: "environment" as const, addon_slug: "grafana" },
  ];
  it("finds each by its registration, else the workspace's catalog service", () => {
    expect(stackOf({ grafana_service_id: "g1" }, svcs).map((s) => s.svc?.id)).toEqual(["l1", undefined, "g1"]);
    expect(stackOf({}, svcs).map((s) => s.svc?.id)).toEqual(["l1", undefined, undefined]);
  });
});

describe("serverLogsLine", () => {
  const ship = { label: "Loki, on this server", tone: "running" as const, why: "", ships: true };
  const stuck = { label: "This server only", tone: "pending" as const, why: "ships from its next deploy", ships: false };
  const compose = { label: "This server only", tone: "muted" as const, why: "compose stacks keep their logs on the server", ships: false };
  const off = { label: "Not running", tone: "muted" as const, why: "", ships: false };

  it("says Loki, on this server for Loki's own server", () => {
    expect(serverLogsLine([ship, compose], "here")).toEqual({ label: "Loki, on this server", tone: "running", why: "and a copy on the server" });
  });
  it("needs an external address for a server elsewhere", () => {
    expect(serverLogsLine([stuck], "elsewhere")).toEqual({ label: "This server only", tone: "pending", why: "needs an external Loki address" });
  });
  it("counts what still ships from its next deploy", () => {
    expect(serverLogsLine([ship, stuck], "here")).toEqual({ label: "Loki, on this server", tone: "pending", why: "1 ships from its next deploy" });
    expect(serverLogsLine([stuck, stuck], "address")).toEqual({ label: "This server only", tone: "pending", why: "2 ship from their next deploy" });
  });
  it("names the old address while every shipper still pushes to it", () => {
    const old = { label: "Loki, old address", tone: "pending" as const, why: "pushes to http://127.0.0.1:39100 until its next deploy", ships: true };
    expect(serverLogsLine([old, old, compose], "address")).toEqual({ label: "Loki, old address", tone: "pending", why: "2 move to the new address at their next deploy" });
    expect(serverLogsLine([old], "address").why).toBe("1 moves to the new address at its next deploy");
    expect(serverLogsLine([ship, old], "address")).toMatchObject({ label: "Loki", tone: "pending" });
    expect(serverLogsLine([old, stuck], "address")).toMatchObject({ label: "Loki, old address", why: "1 ships from its next deploy" });
  });
  it("says why nothing ships, or that nothing runs", () => {
    expect(serverLogsLine([compose], "here")).toEqual({ label: "This server only", tone: "muted", why: "compose stacks keep their logs on the server" });
    expect(serverLogsLine([off], "here")).toEqual({ label: "Nothing running", tone: "muted", why: "" });
    expect(serverLogsLine([stuck], "none")).toEqual({ label: "This server only", tone: "muted", why: "" });
  });
});

describe("Loki's local address", () => {
  it("reads the loopback port from a container's push address", () => {
    expect(localLokiAddress([{ server_id: "s1", services: [{ service_id: "a", containers: [{ name: "a", driver: "loki", loki_url: "http://127.0.0.1:39100/loki/api/v1/push" }] }] }])).toBe("127.0.0.1:39100");
    expect(localLokiAddress([{ server_id: "s1", services: [{ service_id: "a", containers: [{ name: "a", driver: "json-file" }] }] }])).toBe("");
    expect(localLokiAddress(undefined)).toBe("");
  });
});

describe("the warning", () => {
  it("names the servers and the services that stay local", () => {
    const w = localLogsWarning("fsn1-prod-01", ["fsn1-prod-02", "hel1-prod-03"], 12);
    expect(w.title).toBe("Logs from fsn1-prod-02 and hel1-prod-03 stay on their servers");
    expect(w.text).toBe(
      "Loki runs on fsn1-prod-01, and only the services there ship to it, on a port of that server that nothing else can reach. Central logs from other servers need an external Loki address for now. Until then the 12 services on fsn1-prod-02 and hel1-prod-03 keep their logs on their own server: their Logs tabs work, Grafana doesn’t see them.",
    );
    expect(localLogsWarning("a", ["b"], 1).title).toBe("Logs from b stay on that server");
    expect(localLogsWarning("a", ["b"], 1).text).toContain("the service on b keeps its logs on its own server: its Logs tab works");
  });
});
