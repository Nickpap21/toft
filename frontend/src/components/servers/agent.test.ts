import { describe, expect, it } from "vitest";
import { agentAway, agentChip, agentFrozen, agentStatus, awayFor, desiredRow, frozenItems, frozenRow, placeable } from "./agent";

describe("servers a service can be put on", () => {
  const s = (id: string, o: object) => ({ id, connection: "agent" as const, agent: null, provisioned_at: null, ...o });
  it("leaves out a server no agent has joined yet", () => {
    const waiting = s("waiting", {});
    const joined = s("joined", { agent: { key_fingerprint: "SHA256:abc" } });
    const provisioned = s("provisioned", { provisioned_at: "2026-10-22T14:16:22Z" });
    const ssh = s("ssh", { connection: "ssh" });
    const shared = { id: "shared", provisioned_at: null, shared: true };
    expect(placeable([waiting, joined, provisioned, ssh, shared as never]).map((x) => x.id)).toEqual(["joined", "provisioned", "ssh", "shared"]);
  });
  it("keeps the order and leaves an empty list empty", () => {
    expect(placeable([])).toEqual([]);
  });
});

describe("agent state", () => {
  it("is null for an SSH server and waiting before an agent joins", () => {
    expect(agentStatus({ connection: "ssh", agent: null })).toBeNull();
    expect(agentStatus({ connection: "agent", agent: null })).toBe("waiting");
  });
  it("counts offline, waiting and revoked as away, never a reconnect", () => {
    const s = (status: "offline" | "reconnecting" | "revoked") => ({ connection: "agent" as const, agent: { status } as never });
    expect(agentAway(s("offline"))).toBe(true);
    expect(agentAway(s("revoked"))).toBe(true);
    expect(agentAway(s("reconnecting"))).toBe(false);
    expect(agentAway({ connection: "ssh", agent: null })).toBe(false);
  });
  it("labels each state as the boards do", () => {
    expect(agentChip("connected").label).toBe("Online");
    expect(agentChip("offline")).toEqual({ label: "Agent offline", tone: "failed", pulse: false });
    expect(agentChip("reconnecting").pulse).toBe(true);
  });
  it("says how long it has been away", () => {
    const now = Date.parse("2026-10-22T14:17:00Z");
    expect(awayFor("2026-10-22T14:16:22Z", now)).toBe("38 s");
    expect(awayFor("2026-10-22T12:03:00Z", now)).toBe("2 h 14 min");
    expect(awayFor("2026-10-22T12:17:00Z", now)).toBe("2 h");
    expect(awayFor("2026-10-19T12:17:00Z", now)).toBe("3 d");
  });
});

describe("desired state row", () => {
  const now = Date.parse("2026-10-22T14:17:00Z");
  const at = "2026-10-22T14:16:48Z";
  const d = (o: object) => ({ spec_generation: 4, observed_generation: 4, drift: [], checked_at: at, ...o });
  it("is not reported until the agent answers", () => {
    expect(desiredRow(null, "connected", {}, now).text).toBe("Not reported yet");
  });
  it("is in step, catching up, or lists the drift by service name", () => {
    expect(desiredRow(d({}), "connected", {}, now)).toMatchObject({ text: "In step", tone: "running" });
    expect(desiredRow(d({}), "connected", {}, now).note).toMatch(/^Checked 12 s ago\./);
    expect(desiredRow(d({ observed_generation: 3 }), "connected", {}, now).text).toBe("Catching up");
    const row = desiredRow(
      d({ drift: [
        { service_id: "svc-1", kind: "missing", container: null },
        { service_id: "abcdef12-0000", kind: "crash_loop", container: null },
        { service_id: null, kind: "orphan", container: "paas-old-1" },
      ] }),
      "connected",
      { "svc-1": "api" },
      now,
    );
    expect(row.text).toBe("3 differences");
    expect(row.list).toEqual([
      { who: "api", id: false, what: "isn’t running" },
      { who: "abcdef12", id: true, what: "keeps restarting" },
      { who: "paas-old-1", id: false, what: "runs here, but the panel has no such service" },
    ]);
    expect(row.note).toMatch(/: redeploy those services, or remove the container\.$/);
  });
  it("shows five differences and counts the rest, with the step they need", () => {
    const drift = Array.from({ length: 7 }, (_, i) => ({ service_id: null, kind: "orphan" as const, container: `paas-old-${i}` }));
    const row = desiredRow(d({ drift }), "connected", {}, now);
    expect(row.text).toBe("7 differences");
    expect(row.list).toHaveLength(5);
    expect(row.more).toBe(2);
    expect(row.note).toMatch(/on its own yet: remove those containers\.$/);
  });
  it("is muted and dated while the agent is away", () => {
    const row = desiredRow(d({}), "offline", {}, now);
    expect(row.tone).toBeNull();
    expect(row.note).toMatch(/^As of (\d{4}-\d\d-\d\d )?\d\d:\d\d\./);
  });
  it("dates a check from another day", () => {
    expect(desiredRow(d({ checked_at: "2026-10-19T09:03:00Z" }), "offline", {}, now).note).toMatch(/^As of 2026-10-19 \d\d:\d\d\./);
  });
});

describe("frozen after a restore", () => {
  const c = (kind: "newer" | "same" | "unknown" | "missing", service_id: string | null, extra = {}) => ({
    kind, service_id, container: null, deployment_id: null, image: null, started_at: null, ...extra,
  });
  // As the panel sends them: newer, missing, unknown, same.
  const frozen = {
    since: "2026-10-24T09:41:00Z",
    changes: [
      c("newer", "a1", { image: "nginx:1.27.3", started_at: "2026-10-23T14:02:00Z", deployment_id: "d1" }),
      c("missing", "c3"),
      c("unknown", null, { container: "paas-9f8e7d6c-blue" }),
      c("same", "w2"),
      c("same", "x4"),
    ],
  };
  const names = { a1: "api", w2: "web", c3: "cron" };

  it("chips a connected frozen agent, not an away one", () => {
    expect(agentChip("connected", true)).toEqual({ label: "Frozen after a restore", tone: "pending", pulse: false });
    expect(agentChip("offline", true).label).toBe("Agent offline");
    expect(agentFrozen({ connection: "agent", agent: { frozen } as never })).toBe(true);
    expect(agentFrozen({ connection: "agent", agent: { frozen: null } as never })).toBe(false);
  });
  it("lists the differences in the board's order, leaving out what matches", () => {
    const row = frozenRow(frozen, names);
    expect(row.text).toBe("Frozen · 3 differences");
    expect(row.tone).toBe("pending");
    expect(row.list.map((l) => l.who)).toEqual(["api", "paas-9f8e7d6c-blue", "cron"]);
    expect(row.list[0].what).toMatch(/^runs a deploy from 2026-10-2\d \d\d:\d\d, newer than the backup$/);
    expect(frozenRow({ ...frozen, changes: [c("same", "w2")] }, names).text).toBe("Frozen · no differences");
    expect(frozenRow({ ...frozen, changes: [c("missing", "hidden-service-id")] }, names).list[0]).toMatchObject({ who: "hidden-s", id: true });
    expect(frozenRow({ ...frozen, changes: [c("newer", "a1")] }, names).list[0].what).toBe("runs a deploy newer than the backup");
  });
  it("says what adopting does, with what matches on one line", () => {
    const items = frozenItems(frozen, names, true);
    expect(items.map((i) => i.t)).toEqual(["api", "web, Service x4", "paas-9f8e7d6c-blue", "cron"]);
    expect(items[0].d).toMatch(/\(nginx:1\.27\.3\)\. The panel records it as “Adopted · you”/);
    expect(items[1].d).toBe("already match the backup: kept as they are.");
    expect(frozenItems({ ...frozen, changes: [c("same", "w2")] }, names, true)).toEqual([{ t: "web", d: "already matches the backup: kept as it is." }]);
  });
  it("says what using the panel's state leaves, and nothing about what matches", () => {
    const items = frozenItems(frozen, names, false);
    expect(items.map((i) => i.t)).toEqual(["api", "paas-9f8e7d6c-blue", "cron"]);
    expect(items[2].d).toBe("stays stopped until you deploy it.");
  });
});
