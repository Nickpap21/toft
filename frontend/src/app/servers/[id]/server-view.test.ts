import { describe, expect, it } from "vitest";
import { capabilityFacts, gvisorLeft, gvisorSteps, imageTag, levelRow, memoryLabel, removePlan, uptimeLabel } from "./server-view";

describe("server page wording", () => {
  it("reads uptime as the board does", () => {
    expect(uptimeLabel(21 * 86400 + 500)).toBe("21 days");
    expect(uptimeLabel(5 * 3600)).toBe("5 hours");
    expect(uptimeLabel(90)).toBe("1 min");
  });

  it("takes the tag of Traefik's image", () => {
    expect(imageTag("traefik:v3.6.10")).toBe("v3.6.10");
    expect(imageTag("docker.io/library/traefik:v3.6.10@sha256:abc")).toBe("v3.6.10");
    expect(imageTag("registry:5000/traefik")).toBe("registry:5000/traefik");
  });

  it("writes limits in MiB and GiB", () => {
    expect(memoryLabel("512m")).toBe("512 MiB");
    expect(memoryLabel("2g")).toBe("2 GiB");
    expect(memoryLabel("1536m")).toBe("1.5 GiB");
    expect(memoryLabel("")).toBe("none");
  });

  it("refuses removal while another workspace has services here", () => {
    const plan = removePlan(6, [{ org_id: "acme", org_name: "acme", services: 2 }], [{ org_id: "acme", org_name: "acme" }]);
    expect(plan.blockedBy).toEqual([{ name: "acme", services: 2 }]);
    expect(removePlan(6, [], [{ org_id: "acme", org_name: "acme" }])).toEqual({ blockedBy: [], services: 6, shares: ["acme"], typeName: true });
    expect(removePlan(0, [], []).typeName).toBe(false);
  });

  it("names isolation levels and the probe's facts as the board does", () => {
    expect(levelRow({ id: "sandboxed", state: "not_installed", reason: "" })).toEqual({ name: "Sandboxed", runtime: "gVisor runsc", state: "not installed" });
    expect(levelRow({ id: "dedicated", state: "available", reason: "" }).state).toBe("always available");
    expect(levelRow({ id: "sandboxed_strict", state: "available", reason: "" })).toEqual({ name: "Sandboxed, strict network", runtime: "gVisor runsc-strict", state: "runs here" });
    expect(levelRow({ id: "standard", state: "available", reason: "" }).state).toBe("runs here");
    expect(levelRow({ id: "microvm", state: "unavailable", reason: "" }).state).toBe("not available");
    const facts = capabilityFacts({
      kernel: "6.8.0-146-generic", arch: "x86_64", cgroup_v2: true, virtualization: "kvm", kvm: false, docker_version: "29.4.3",
      apparmor: true, seccomp: false, runtimes: [{ name: "runc", ok: true }], error: "", probed_at: "", levels: [],
    });
    expect(facts.map((f) => `${f.value} ${f.note}`)).toEqual([
      "6.8.0-146-generic · x86_64", "kvm · no /dev/kvm", "v2 · process and memory limits enforced", "29.4.3 · runtimes: runc", "AppArmor · seccomp off",
    ]);
  });
});

describe("gvisorSteps", () => {
  const log = ["Installing gVisor 20261012 on fsn1", "[1/5] Checking the server", "  ✓ Ubuntu", "[2/5] Adding gVisor's apt repository", "  ✓ signed", "[3/5] Installing runsc"];
  it("marks the running step", () => {
    expect(gvisorSteps(log, "running")).toEqual(["done", "done", "now", "todo", "todo", "todo"]);
  });
  it("marks the failed step and says what it left", () => {
    const s = gvisorSteps(log, "failed");
    expect(s).toEqual(["done", "done", "failed", "todo", "todo", "todo"]);
    expect(gvisorLeft(s)).toBe("The repository is added; Docker wasn’t changed, and nothing that runs here changed.");
  });
  it("is all done when the run succeeded", () => {
    expect(gvisorSteps(log, "ok")).toEqual(["done", "done", "done", "done", "done", "done"]);
  });
  it("reads the six steps of the strict network's install (D-115)", () => {
    const s = gvisorSteps([...log, "[6/6] Testing the strict network"], "failed");
    expect(s).toEqual(["done", "done", "done", "done", "done", "failed"]);
    expect(gvisorLeft(s)).toContain("Sandboxed works");
  });
  it("fails the first step when nothing was logged", () => {
    const s = gvisorSteps([], "failed");
    expect(s[0]).toBe("todo");
    expect(gvisorLeft(s)).toBe("Nothing on the server changed.");
  });
});
