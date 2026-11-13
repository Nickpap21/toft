import { describe, expect, it } from "vitest";
import type { HostUpdates, UpdateRun } from "@/lib/api/server.service";
import { advisoriesSummary, confirmCopy, dockerUpdatable, duration, installCount, listsFrom, packagesSummary, runView, unattendedDetail, updateRows } from "./updates-view";

const base = (): HostUpdates => ({
  checked_at: "2026-10-30T12:00:00Z",
  read_at: "2026-10-30T12:00:00Z",
  error: "",
  lists_updated_at: "2026-10-30T06:02:00Z",
  security: 0,
  other: 0,
  held: 0,
  packages: [],
  removals: [],
  truncated: false,
  reboot: { required: false, since: null, packages: [] },
  docker: { version: "29.8.2", package: "docker-ce", candidate: "", advisories: [] },
  unattended: { state: "active", last_run_at: "2026-10-30T06:14:00Z", last_success_at: "2026-10-30T06:14:00Z", last_result: "ok", last_error: "" },
});

const row = (u: HostUpdates, key: string) => updateRows(u).find((r) => r.key === key)!;

describe("the Updates card", () => {
  it("is all green on a current server", () => {
    expect(updateRows(base()).map((r) => [r.key, r.tone, r.state])).toEqual([
      ["security", "ok", "None waiting"],
      ["reboot", "ok", "Not needed"],
      ["docker", "ok", "29.8.2 · no advisories"],
      ["unattended", "ok", "Running"],
      ["other", "ok", "None waiting"],
    ]);
  });

  it("says what waits, and why security updates may stay", () => {
    const u = { ...base(), security: 2, other: 14 };
    expect(row(u, "security")).toMatchObject({ tone: "warn", state: "2 waiting" });
    expect(row(u, "security").detail).toContain("next nightly run");
    u.unattended = { ...u.unattended, state: "inactive" };
    expect(row(u, "security").detail).toContain("isn’t installing them");
    expect(row(u, "unattended")).toMatchObject({ tone: "bad", state: "Stopped" });
    // Other updates are a note: nothing about them needs action.
    expect(row(u, "other")).toMatchObject({ tone: "info", state: "14 waiting" });
    expect(packagesSummary({ ...u, unattended: base().unattended })).toBe("16 packages · 2 security, 14 other");
    expect(packagesSummary({ ...base(), other: 1 })).toBe("1 package · 0 security, 1 other");
  });

  it("names the reboot's packages", () => {
    const u = base();
    u.reboot = { required: true, since: null, packages: ["a", "b", "c", "d", "e"] };
    expect(row(u, "reboot")).toMatchObject({ tone: "warn", state: "Required" });
    expect(row(u, "reboot").detail).toBe("For a, b, c and 2 more. Until then the old versions keep running.");
  });

  it("rates Docker by its worst advisory", () => {
    const u = base();
    u.docker = { version: "29.4.3", package: "docker-ce", candidate: "29.8.2", advisories: [] };
    expect(row(u, "docker")).toMatchObject({ tone: "warn", state: "29.4.3 · 29.8.2 available" });
    const a = { id: "GHSA-x", severity: "medium" as const, summary: "s", fixed_in: "29.5.1", url: "u" };
    u.docker.advisories = [a];
    expect(row(u, "docker")).toMatchObject({ tone: "warn", state: "29.4.3 · 1 advisory" });
    u.docker.advisories = [{ ...a, severity: "high" }, a];
    expect(row(u, "docker")).toMatchObject({ tone: "bad", state: "29.4.3 · 2 advisories" });
    u.docker = { version: "", package: "", candidate: "", advisories: [] };
    expect(row(u, "docker")).toMatchObject({ tone: "bad", state: "Not answering" });
    u.docker = { version: "27.5.1", package: "docker.io", candidate: "", advisories: [] };
    expect(row(u, "docker")).toMatchObject({ tone: "ok", state: "27.5.1" });
    expect(row(u, "docker").detail).toContain("backport fixes");
  });

  it("says what unattended-upgrades did", () => {
    const u = base().unattended;
    expect(unattendedDetail({ ...u, state: "missing" })).toContain("isn’t installed");
    expect(unattendedDetail({ ...u, last_result: "failed", last_error: "2026-10-30 06:12:01,000 ERROR Installing the upgrades failed!" }))
      .toMatch(/and failed: Installing the upgrades failed!$/);
    expect(unattendedDetail({ ...u, last_run_at: null, last_result: "never" })).toBe("It has never run. It installs security updates each night.");
  });

  it("titles each row as the board does", () => {
    expect(updateRows(base()).map((r) => r.title)).toEqual([
      "Security updates: none waiting",
      "Reboot not needed",
      "Docker 29.8.2: no advisories",
      "unattended-upgrades is running",
      "Other updates: none waiting",
    ]);
    const u = { ...base(), security: 2, other: 14, reboot: { required: true, since: null, packages: [] } };
    u.docker = { version: "29.4.3", package: "docker-ce", candidate: "29.8.2", advisories: [{ id: "GHSA-x", severity: "high", summary: "s", fixed_in: "29.5.1", url: "u" }] };
    expect(updateRows(u).map((r) => r.title)).toEqual([
      "Security updates: 2 waiting",
      "Reboot required",
      "Docker 29.4.3: 1 advisory",
      "unattended-upgrades is running",
      "Other updates: 14 waiting",
    ]);
    expect(row({ ...u, docker: { ...u.docker, advisories: [] } }, "docker").title).toBe("Docker 29.4.3: 29.8.2 available");
    expect(row({ ...u, docker: { version: "", package: "", candidate: "", advisories: [] } }, "docker").title).toBe("Docker isn’t answering");
    expect(row({ ...u, docker: { version: "27.5.1", package: "docker.io", candidate: "", advisories: [] } }, "docker").title).toBe("Docker 27.5.1");
    expect(row({ ...u, unattended: { ...u.unattended, state: "missing" } }, "unattended").title).toBe("unattended-upgrades isn’t installed");
    expect(row({ ...u, unattended: { ...u.unattended, last_result: "failed" } }, "unattended").title).toBe("unattended-upgrades: last run failed");
    expect(row({ ...u, unattended: { ...u.unattended, state: "inactive" } }, "unattended").title).toBe("unattended-upgrades is stopped");
    expect(row({ ...u, unattended: { ...u.unattended, last_result: "never" } }, "unattended").title).toBe("unattended-upgrades has never run");
  });

  it("warns first when apt has never downloaded its lists, even with nothing waiting", () => {
    const u = { ...base(), lists_updated_at: null };
    expect(updateRows(u)[0]).toMatchObject({
      key: "lists",
      tone: "warn",
      title: "apt has never downloaded its package lists",
      detail: "Updates published since the server was set up don’t count until apt’s daily job runs.",
    });
    expect(listsFrom(u)).toBe("");
    expect(updateRows(base()).some((r) => r.key === "lists")).toBe(false);
  });

  it("says when the lists are from once apt has downloaded them", () => {
    expect(listsFrom(base())).toMatch(/^lists from 2026-10-30 \d\d:02$/);
  });

  it("sums up the advisories and what fixes them", () => {
    const a = (fixed_in: string) => ({ id: "GHSA-" + fixed_in, severity: "high" as const, summary: "s", fixed_in, url: "u" });
    const u = base();
    u.docker = { version: "29.4.3", package: "docker-ce", candidate: "29.8.2", advisories: [a("29.8.2"), a("29.5.1"), a("29.5.10")] };
    expect(advisoriesSummary(u)).toBe("3 affect 29.4.3 · 29.8.2 fixes all");
    u.docker.candidate = "29.5.2";
    expect(advisoriesSummary(u)).toBe("3 affect 29.4.3 · 29.5.2 fixes 1");
    u.docker = { ...u.docker, candidate: "", advisories: [a("29.5.1")] };
    expect(advisoriesSummary(u)).toBe("1 affects 29.4.3");
  });
});

describe("the Updates card's runs (D-134)", () => {
  const waiting = (): HostUpdates => ({
    ...base(), security: 2, other: 14, held: 1,
    packages: [{ name: "linux-image-6.8.0-90-generic", current: "", candidate: "6.8.0-90.91", security: true }],
    docker: { version: "29.4.3", package: "docker-ce", candidate: "29.8.2", advisories: [] },
  });

  it("installs everything but Docker's", () => {
    expect(installCount(waiting())).toBe(15);
    expect(installCount({ ...base(), other: 1, held: 1 })).toBe(0);
    expect(dockerUpdatable(waiting())).toBe(true);
    expect(dockerUpdatable({ ...waiting(), docker: { ...waiting().docker, package: "" } })).toBe(false);
    expect(dockerUpdatable(base())).toBe(false);
    const containerd = { ...waiting(), docker: { ...waiting().docker, candidate: "" } };
    expect(dockerUpdatable(containerd)).toBe(true);
    expect(confirmCopy("docker", containerd, "fsn1", []).title).toBe("Update Docker on fsn1?");
  });

  it("confirms each action with what goes down", () => {
    const install = confirmCopy("install", waiting(), "fsn1", ["api"]);
    expect(install.title).toBe("Install 15 updates on fsn1?");
    expect(install.lead).toContain("The 15 updates the card lists, Docker’s apart");
    expect(install).toMatchObject({ loud: false, downTitle: "Nothing Toft runs goes down", confirm: "Install updates" });
    expect(install.note).toContain("linux-image-6.8.0-90-generic asks for a reboot afterwards");
    expect(confirmCopy("install", { ...waiting(), removals: ["old-lib"] }, "fsn1", []).note).toContain("apt removes old-lib");

    const docker = confirmCopy("docker", waiting(), "fsn1", ["api", "worker", "db", "cache", "web"]);
    expect(docker.title).toBe("Update Docker on fsn1 to 29.8.2?");
    expect(docker.down).toBe("api, worker, db, cache, Traefik and 1 more service stop and start again with Docker. Their sites don’t answer meanwhile. Deploys to this server wait until it’s done.");
    expect(confirmCopy("docker", waiting(), null, []).down).toContain("The panel, its API and git stop and start again with Docker. This page reconnects by itself.");

    const reboot = confirmCopy("reboot", { ...waiting(), reboot: { required: true, since: null, packages: ["linux-base"] } }, null, ["api"]);
    expect(reboot).toMatchObject({ title: "Reboot the panel’s server?", downTitle: "Toft and everything here are down", loud: true });
    expect(reboot.lead).toBe("Loads the kernel and library updates installed: linux-base.");
    expect(confirmCopy("reboot", waiting(), "fsn1", [], true).steps[2]).toContain("Toft’s agent connects back");
    expect(confirmCopy("reboot", waiting(), "fsn1", []).steps[2]).toBe("Docker starts the containers again by itself.");
  });

  it("follows a run and says how it ended", () => {
    const run: UpdateRun = {
      action: "install", outcome: "running", step: "install", packages: 15, started_at: "2026-10-30T18:02:00Z",
      started_by: "Nikos", finished_at: null, error: "", output: [],
    };
    const at = Date.parse("2026-10-30T18:03:12Z");
    expect(runView(run, at)).toMatchObject({ label: "Installing updates", state: "installing 15 packages · 1 min 12 s", tone: "running" });
    expect(runView({ ...run, step: "lists" }, at).state).toBe("refreshing package lists · 1 min 12 s");
    expect(runView({ ...run, action: "reboot", outcome: "rebooting", step: "reboot" }, at)).toMatchObject({ label: "Rebooting", state: "waiting for the server · 1 min 12 s" });
    const done = { ...run, outcome: "succeeded" as const, step: "done" as const, finished_at: "2026-10-30T18:04:10Z" };
    expect(runView(done, at)).toMatchObject({ label: "Updates installed", state: "15 packages", tone: "ok", detail: "Run from Toft by Nikos." });
    expect(runView(done, at).meta).toMatch(/ · 2 min 10 s$/);
    expect(runView(done, at).announce).toBe("Updates installed, 15 packages");
    expect(runView({ ...done, packages: 0 }, at).state).toBe("nothing new to install");
    expect(runView(run, at).announce).toBe("Installing updates: installing 15 packages");
    expect(runView({ ...done, outcome: "failed", started_by: "" }, at)).toMatchObject({ label: "Installing updates failed", tone: "bad", detail: "Run from Toft. Docker and its containers weren’t touched." });
    expect([duration(34_000), duration(130_000), duration(3_900_000)]).toEqual(["34 s", "2 min 10 s", "1 h 5 min"]);
  });
});
