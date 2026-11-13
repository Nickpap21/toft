import { describe, expect, it } from "vitest";
import { lineTone, stagesOf } from "./provisioning";

// The lines of a re-provision as the provisioner writes them (Server board).
const rerun = [
  "DNS provider cloudflare: Traefik gets the letsencrypt-dns resolver for wildcard domains",
  "Connecting via SSH...",
  "Docker already installed: Docker version 29.1.3, build f52814d",
  "Docker address pools already set in /etc/docker/daemon.json",
  "Loki Docker log driver already installed",
  "paas-network already exists",
  "Traefik's configuration changed: restarting it, so this server's routes pause for a few seconds (certificates are kept)",
  "Writing Traefik dynamic config...",
  "Starting Traefik v3.6.10 with Let's Encrypt ACME...",
  "Traefik v3.6.10 started successfully",
  "Firewall already active — leaving existing rules untouched.",
  "fail2ban already active.",
  "Host firewall already loaded",
  "Enabling automatic security updates...",
  "Server provisioned successfully!",
];

describe("stagesOf", () => {
  it("reads what each stage found from a finished run", () => {
    expect(stagesOf(rerun, "ok").map((s) => [s.label, s.state, s.sub])).toEqual([
      ["SSH", "done", "host key matched"],
      ["Docker", "done", "29.1.3, unchanged"],
      ["Networks", "done", "unchanged"],
      ["Traefik", "done", "restarted"],
      ["Firewall", "done", "unchanged"],
    ]);
  });

  it("doesn't let the DNS line before SSH jump to Traefik", () => {
    expect(stagesOf(rerun.slice(0, 1), "running").map((s) => s.state)).toEqual(["todo", "todo", "todo", "todo", "todo"]);
    expect(stagesOf(rerun.slice(0, 2), "running")[0].state).toBe("now");
  });

  it("marks where a fresh install is, and where a failed one stopped", () => {
    const fresh = [
      "Connecting via SSH...",
      "Pinned SSH host key ssh-ed25519 SHA256:abc (first connection)",
      "Installing Docker (this may take a minute)...",
      "  Version:          29.1.3",
      "Set Docker's app network address pool to 10.203.0.0/16 (/24 networks)",
      "Installing Loki Docker log driver plugin...",
      "Created paas-network",
      "Writing Traefik dynamic config...",
    ];
    expect(stagesOf(fresh, "running").map((s) => [s.state, s.sub])).toEqual([
      ["done", "host key pinned"],
      ["done", "installed 29.1.3"],
      ["done", "10.203.0.0/16"],
      ["now", "starting"],
      ["todo", "—"],
    ]);
    expect(stagesOf(fresh, "failed")[3]).toMatchObject({ state: "failed", sub: "failed" });
  });

  it("puts a failure before any line on SSH", () => {
    expect(stagesOf([], "failed")[0].state).toBe("failed");
  });
});

describe("lineTone", () => {
  it("colours lines as the board does", () => {
    expect(lineTone("paas-network already exists")).toBe("muted");
    expect(lineTone("Server provisioned with warnings, missing the firewall (ufw, fail2ban): fix what the warnings above say, then Re-provision")).toBe("warn");
    expect(lineTone("Traefik v3.6.10 started successfully")).toBe("ok");
    expect(lineTone("Created paas-network")).toBe("ok");
    expect(lineTone(rerun[6])).toBe("warn");
    expect(lineTone("Warning: firewall setup failed (non-fatal): x")).toBe("warn");
    expect(lineTone("Writing Traefik dynamic config...")).toBe("text");
  });
});
