import { describe, expect, it } from "vitest";
import type { ProvisionRun } from "@/lib/api/server.service";
import { addState, leftOnServer, logTail, namedFromHost, osOf, progressOf, stoppedAt, validHost, validServerName } from "./add-server";

const run = (log: string[], finished: boolean | null, error = ""): ProvisionRun => ({
  started_at: "2026-11-03T14:51:00Z",
  started_by: "Ada",
  finished_at: finished === null ? null : "2026-11-03T14:53:00Z",
  ok: finished === null ? null : finished,
  error,
  log: log.join("\n") + "\n",
  warnings: [],
});

const start = ["Connecting via SSH...", "Named hel1-prod-03, from its hostname", "Requirements met: Ubuntu 24.04.4 LTS · amd64, kernel 6.8.0, cgroup v2, no Docker yet"];
const docker = ["Installing Docker...", " Version:           29.1.2"];
const waiting = { connection: "agent" as const, agent: null, provisioned_at: null };

describe("add a server", () => {
  it("accepts addresses and hostnames, not typos", () => {
    expect(validHost("203.0.113.12")).toBe(true);
    expect(validHost("fsn1.example.com")).toBe(true);
    expect(validHost("2a01:4f8::1")).toBe(true);
    expect(validHost("203.0.113.300")).toBe(false);
    expect(validHost("a b")).toBe(false);
    expect(validHost("")).toBe(false);
  });

  it("holds names to the API's rule", () => {
    for (const ok of ["hel1-prod-03", "a", "9", "a".repeat(63)]) expect(validServerName(ok), ok).toBe(true);
    for (const bad of ["", "Hel1", "-web", "web-", "web_1", "web 1", "wéb", "a".repeat(64)]) expect(validServerName(bad), bad).toBe(false);
  });

  it("reads the state from the run first, then the connection", () => {
    const joined = { ...waiting, agent: { key_fingerprint: "SHA256:x" } as never };
    expect(addState(waiting, null, true)).toBe("command");
    expect(addState(waiting, null, false)).toBe("expired");
    expect(addState(joined, null, false)).toBe("installing");
    expect(addState({ ...waiting, connection: "ssh" }, null, false)).toBe("verify");
    expect(addState(joined, run(start, null), false)).toBe("installing");
    expect(addState(joined, run(["Connecting via SSH..."], false, "the server doesn't meet Toft's requirements"), false)).toBe("unmet");
    expect(addState(joined, run([...start, ...docker, "Starting Traefik v3.6.10 with Let's Encrypt ACME..."], false), false)).toBe("failed");
    expect(addState({ ...joined, provisioned_at: "2026-11-03T14:53:00Z" }, run(start, true), false)).toBe("joined");
  });

  it("says what the run found and where it is", () => {
    const r = run(start, null);
    expect(osOf(r)).toBe("Ubuntu 24.04");
    expect(osOf(run(["Connecting via SSH..."], null))).toBe("");
    expect(namedFromHost(r)).toBe(true);
    expect(progressOf("installing", run(["Connecting via SSH..."], null), 0)).toEqual([{ tone: "now", text: "Checking the requirements" }]);
    expect(progressOf("installing", r, 0).map((p) => p.text)).toEqual(["Requirements met", "Installing Docker, Traefik and the firewall · about 2 minutes"]);
    expect(progressOf("unmet", r, 1)[0].text).toBe("1 requirement not met");
    expect(progressOf("unmet", r, 2)[0].text).toBe("2 requirements not met");
    expect(progressOf("joined", run([...start, ...docker, "Server provisioned successfully!"], true), 0)[0].text).toBe("Docker 29.1.2, firewall and Traefik ready");
  });

  it("never says the firewall is ready when the run finished with warnings", () => {
    const warned = {
      ...run([...start, ...docker, "Server provisioned with warnings, missing the firewall (ufw, fail2ban): fix what the warnings above say, then Re-provision"], true),
      warnings: ["the firewall (ufw, fail2ban)"],
    };
    expect(progressOf("joined", warned, 0)).toEqual([
      { tone: "ok", text: "Docker 29.1.2 and Traefik ready" },
      { tone: "warn", text: "Provisioned without the firewall (ufw, fail2ban): the server’s log says why; Re-provision there once it’s fixed" },
    ]);
  });

  it("says where a failed install stopped and what is in place", () => {
    const traefik = run([...start, ...docker, "Created paas-network", "Starting Traefik v3.6.10 with Let's Encrypt ACME...", "Error response from daemon: no such host"], false, "Traefik: exit status 1");
    expect(progressOf("failed", traefik, 0)[1].text).toBe("Installing Traefik failed");
    expect(stoppedAt(traefik)).toBe("Installing stopped at Traefik. Docker and the networks are in place; nothing else changed.");
    expect(logTail(traefik)).toBe("Starting Traefik v3.6.10 with Let's Encrypt ACME...\nError response from daemon: no such host\nTraefik: exit status 1");
    expect(stoppedAt(run([...start, ...docker], false))).toBe("Installing stopped at Docker. Nothing else changed.");
  });

  it("lists what a joined server keeps when it's removed", () => {
    const agent = { connection: "agent" as const, agent: { key_fingerprint: "SHA256:x" } as never };
    expect(leftOnServer({ connection: "agent", agent: null }, null)).toEqual([]);
    expect(leftOnServer(agent, run(["Connecting via SSH..."], false, "the server doesn't meet Toft's requirements"))).toEqual(["the agent (toft-agent.service)"]);
    expect(leftOnServer(agent, run([...start, ...docker, "Created paas-network", "Starting Traefik v3.6.10 with Let's Encrypt ACME..."], false))).toEqual([
      "the agent (toft-agent.service)",
      "Docker",
      "its networks",
    ]);
    expect(leftOnServer({ connection: "ssh", agent: null }, null)).toEqual(["the key you added for root"]);
  });
});
