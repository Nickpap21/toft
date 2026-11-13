import { describe, expect, it } from "vitest";
import type { PanelContainer, PanelStatus, UpgradeRun } from "@/lib/api/instance.service";
import type { DiskReport, HostUpdates } from "@/lib/api/server.service";
import { maintenanceCount } from "@/app/servers/[id]/server-view";
import type { PanelCertificate } from "@/lib/api/instance.service";
import { formatDateTime } from "@/lib/utils";
import { certAskNote, certTile, chartPaths, dumpWords, needsAttention, oldTabRoute, overviewAttention, retryUpgrade, panelMaintenance, systemWords, containerStatus, cpuPeak, healthChip, imageTag, stepStates, upFor, upgradeCard, uptimeWords } from "./instance-view";

const c = (over: Partial<PanelContainer>): PanelContainer => ({
  name: "paas-frontend",
  role: "web",
  image: "ghcr.io/x/paas-frontend:v0.1.2",
  state: "running",
  health: "healthy",
  started_at: null,
  restarts: 0,
  cpu_percent: null,
  memory_bytes: null,
  memory_limit_bytes: null,
  ...over,
});

const status = (containers: PanelContainer[], healthy: boolean): PanelStatus => ({
  version: "v0.1.2",
  healthy,
  containers,
  certificate: null,
  uptime_seconds: 0,
  shell: false,
});

describe("healthChip", () => {
  it("names the one container that is not well", () => {
    expect(healthChip(status([c({ name: "paas-backend" }), c({ state: "restarting" })], false))).toEqual({
      text: "paas-frontend restarting",
      tone: "pending",
    });
  });
  it("counts several", () => {
    expect(healthChip(status([c({ state: "exited" }), c({ health: "unhealthy" })], false)).text).toBe("2 containers not healthy");
  });
  it("is Healthy when the panel says so", () => {
    expect(healthChip(status([c({})], true)).text).toBe("Healthy");
  });
});

describe("containerStatus", () => {
  it("tells a missing, a stopped and an unhealthy container apart", () => {
    expect(containerStatus(c({ state: "missing" })).text).toBe("Missing");
    expect(containerStatus(c({ state: "exited" })).text).toBe("Exited");
    expect(containerStatus(c({ health: "unhealthy" })).tone).toBe("failed");
    expect(containerStatus(c({ health: "" })).text).toBe("Running");
  });
});

it("imageTag reads the tag, never a registry port", () => {
  expect(imageTag("ghcr.io/x/paas-backend:v0.1.2")).toBe("v0.1.2");
  expect(imageTag("localhost:5000/paas-backend")).toBe("localhost:5000/paas-backend");
  expect(imageTag("postgres:16-alpine@sha256:abc")).toBe("16-alpine");
});

it("upFor and uptimeWords", () => {
  expect(upFor(21 * 86_400 + 5)).toBe("21 d");
  expect(upFor(40)).toBe("40 s");
  expect(uptimeWords(86_400)).toBe("1 day");
  expect(uptimeWords(5 * 3_600)).toBe("5 hours");
  expect(uptimeWords(10)).toBe("1 minute");
});

describe("certTile", () => {
  const now = Date.parse("2026-10-18T12:00:00Z");
  it("is fine with weeks left", () => {
    expect(certTile({ domain: "p.example", issuer: "Let's Encrypt", not_after: "2026-12-18T12:00:00Z", ask: null }, now)).toMatchObject({
      value: "61 days left",
      tone: "running",
    });
  });
  it("warns under 14 days, and when Traefik serves its own", () => {
    expect(certTile({ domain: "p.example", issuer: "Let's Encrypt", not_after: "2026-10-27T13:00:00Z", ask: null }, now).tone).toBe("pending");
    expect(certTile({ domain: "p.example", issuer: "TRAEFIK DEFAULT CERT", not_after: "2027-10-27T13:00:00Z", ask: null }, now).value).toBe("Self-signed");
    expect(certTile({ domain: "p.example", issuer: "Let's Encrypt", not_after: "2026-10-17T13:00:00Z", ask: null }, now).value).toBe("Expired");
  });
});

// Why the panel's domain still serves Traefik's own certificate, from the
// backend's look at it each minute and Let's Encrypt's answers in Traefik's log.
describe("certAskNote", () => {
  const now = Date.parse("2026-11-11T15:00:00Z");
  type Ask = NonNullable<PanelCertificate["ask"]>;
  const ask = (over: Partial<Ask>): PanelCertificate => ({
    domain: "panel.example",
    issuer: "TRAEFIK DEFAULT CERT",
    not_after: "2027-11-11T15:00:00Z",
    ask: { reaches_panel: true, addresses: ["198.51.100.20"], last_try_at: "2026-11-11T14:58:00Z", next_ask_at: "2026-11-11T15:13:00Z", error: null, ...over },
  });
  const err = (type: NonNullable<Ask["error"]>["type"], detail = "", retry_after: string | null = null) => ({ type, detail, at: "2026-11-11T14:58:30Z", retry_after });
  const next = `Toft has Traefik ask again at ${formatDateTime("2026-11-11T15:13:00Z")}.`;

  it("before the panel has looked", () => {
    expect(certAskNote({ ...ask({}), ask: null }, now)).toBe("Let’s Encrypt hasn’t issued one yet. Traefik’s log says why.");
  });
  it("says where the domain points while it doesn't reach this panel, and that there is no record", () => {
    expect(certAskNote(ask({ reaches_panel: false, addresses: ["203.0.113.10"], next_ask_at: null }), now)).toBe(
      "panel.example points at 203.0.113.10, which doesn’t lead to this panel: another server, or a router that won’t send this server’s own requests back in. Toft has Traefik ask once it reaches here.",
    );
    expect(certAskNote(ask({ reaches_panel: false, addresses: [], next_ask_at: null }), now)).toContain("has no DNS record yet");
  });
  it("says when Traefik asked and when Toft asks again, with no answer yet", () => {
    expect(certAskNote(ask({}), now)).toBe(`Traefik asked Let’s Encrypt at ${formatDateTime("2026-11-11T14:58:00Z")}. ${next}`);
  });
  it("names the places that block port 80", () => {
    const note = certAskNote(ask({ error: err("connection", "Timeout during connect (likely firewall problem)") }), now);
    expect(note).toContain("Port 80 doesn’t reach this server from the internet: check the server’s firewall, your provider’s firewall (such as Hetzner Cloud Firewall), or a router in front.");
    expect(note).toContain(next);
  });
  it("says something else answered the check", () => {
    expect(certAskNote(ask({ error: err("unauthorized", "Invalid response: 404") }), now)).toContain("Something other than this panel answered Let’s Encrypt’s check on port 80");
  });
  it("gives Let's Encrypt's words for DNS and other refusals, and the CAA record", () => {
    expect(certAskNote(ask({ error: err("dns", "NXDOMAIN looking up A for panel.example") }), now)).toContain("couldn’t look panel.example up: NXDOMAIN looking up A for panel.example");
    expect(certAskNote(ask({ error: err("caa") }), now)).toContain("panel.example’s CAA record doesn’t allow Let’s Encrypt.");
    expect(certAskNote(ask({ error: err("other", "badNonce") }), now)).toContain("Let’s Encrypt refused it: badNonce");
  });
  it("says until when a rate limit lasts, and that Toft waits for it", () => {
    const limited = ask({ error: err("rate_limited", "too many failed authorizations, retry after 2026-11-11 16:00:00 UTC: see https://letsencrypt.org/docs/rate-limits/", "2026-11-11T16:00:00Z"), next_ask_at: "2026-11-11T16:00:00Z" });
    expect(certAskNote(limited, now)).toBe(
      `Let’s Encrypt has rate-limited panel.example until ${formatDateTime("2026-11-11T16:00:00Z")} (too many failed authorizations). Toft asks again then: asking sooner would extend it.`,
    );
    expect(certAskNote(limited, Date.parse("2026-11-11T16:01:00Z"))).not.toContain("until");
  });
});

it("cpuPeak finds the busiest sample", () => {
  const s = (at: string, peak: number) => ({ at, cpu_percent: 1, cpu_peak: peak, memory_used_bytes: 0, disk_used_bytes: 0 });
  expect(cpuPeak([s("a", 3), s("b", 9), s("c", 4)])?.at).toBe("b");
  expect(cpuPeak([])).toBeNull();
});

it("chartPaths scales to the box and closes the area", () => {
  const p = chartPaths([[0, 0], [0.5, 50], [1, 100]], 100, 100, 10);
  expect(p.line).toBe("M0.0 10.0L50.0 5.0L100.0 0.0");
  expect(p.area.endsWith("L100.0 10L0.0 10Z")).toBe(true);
  expect(chartPaths([], 100).line).toBe("");
});

it("chartPaths draws samples where they fall in the range", () => {
  // An hour of a week: the right edge only, not a flat week.
  const p = chartPaths([[0.99, 50], [1, 50]], 100, 100, 10);
  expect(p.line).toBe("M99.0 5.0L100.0 5.0");
  expect(p.area).toBe("M99.0 5.0L100.0 5.0L100.0 10L99.0 10Z");
  // One sample steps to now.
  expect(chartPaths([[0.5, 100]], 100, 100, 10).line).toBe("M50.0 0.0L100.0 0.0");
});

describe("upgrades", () => {
  const run = (over: Partial<UpgradeRun>): UpgradeRun => ({
    to: "v0.1.3",
    from: "v0.1.2",
    started_at: "2026-10-21T14:02:00Z",
    finished_at: null,
    step: "migrate",
    outcome: "running",
    error: "",
    output: [],
    notes: {},
    ...over,
  });
  it("marks the steps before, at and after the current one", () => {
    expect(stepStates(run({}))).toEqual(["done", "done", "done", "running", "waiting", "waiting"]);
    expect(stepStates(run({ step: "health", outcome: "rolled_back" }))).toEqual(["done", "done", "done", "done", "done", "rolled_back"]);
    expect(stepStates(run({ step: "pull", outcome: "failed" }))).toEqual(["done", "failed", "waiting", "waiting", "waiting", "waiting"]);
    expect(stepStates(run({ step: "starting" }))).toEqual(["running", "waiting", "waiting", "waiting", "waiting", "waiting"]);
    expect(stepStates(run({ step: "health", outcome: "succeeded" }))).toEqual(Array(6).fill("done"));
  });
  it("picks the board's card", () => {
    expect(upgradeCard(run({}), false).kind).toBe("running");
    expect(upgradeCard(run({}), true).kind).toBe("restarting");
    expect(upgradeCard(run({ step: "restart" }), false).title).toBe("Toft is restarting");
    expect(upgradeCard(run({ outcome: "succeeded", finished_at: "2026-10-21T14:03:52Z" }), true)).toMatchObject({
      kind: "done",
      text: expect.stringContaining("1 min 52 s"),
    });
    expect(upgradeCard(run({ outcome: "rolled_back" }), false).title).toBe("The upgrade to v0.1.3 failed. v0.1.2 is running again");
    expect(upgradeCard(run({ outcome: "failed", error: "pull denied" }), false)).toMatchObject({ kind: "failed", text: "pull denied" });
  });
});

it("systemWords says what systemd says of the firewall", () => {
  expect(systemWords({ firewall: "active", security_updates: "active", filesystem: "" })).toEqual({ firewall: "toft-firewall, loaded" });
  expect(systemWords({ firewall: "failed", security_updates: "missing", filesystem: "" })).toEqual({ firewall: "toft-firewall failed to load" });
  expect(systemWords(null)).toEqual({ firewall: "—" });
});

it("dumpWords counts the dumps kept before upgrades", () => {
  expect(dumpWords({ dumps: 0, last_dump_at: null })).toBe("None yet: the next upgrade saves one");
  expect(dumpWords({ dumps: 2, last_dump_at: "2026-10-18T17:50:19Z" })).toBe("2 kept · last 2026-10-18");
});

it("needsAttention puts the sidebar's dot on what Overview lists, and a failed upgrade for a day", () => {
  const now = Date.parse("2026-10-18T12:00:00Z");
  const well = status([c({})], true);
  expect(needsAttention(well, undefined, now)).toBe(false);
  expect(needsAttention(status([c({ state: "restarting" })], false), undefined, now)).toBe(true);
  expect(needsAttention({ ...well, certificate: { domain: "p.example", issuer: "Let's Encrypt", not_after: "2026-10-27T13:00:00Z", ask: null } }, undefined, now)).toBe(true);
  const failed = (finished_at: string) =>
    ({ run: { outcome: "rolled_back", finished_at } }) as unknown as Parameters<typeof needsAttention>[1];
  expect(needsAttention(well, failed("2026-10-18T09:00:00Z"), now)).toBe(true);
  expect(needsAttention(well, failed("2026-10-16T09:00:00Z"), now)).toBe(false);
});

describe("panelMaintenance", () => {
  const updates = (over: Partial<HostUpdates> = {}): HostUpdates => ({
    checked_at: "2026-10-30T12:00:00Z",
    read_at: "2026-10-30T12:00:00Z",
    error: "",
    lists_updated_at: "2026-10-30T06:02:00Z",
    security: 0,
    other: 12,
    held: 0,
    packages: [],
    removals: [],
    truncated: false,
    reboot: { required: false, since: null, packages: [] },
    docker: { version: "29.8.2", package: "docker-ce", candidate: "", advisories: [] },
    unattended: { state: "active", last_run_at: "2026-10-30T06:14:00Z", last_success_at: "2026-10-30T06:14:00Z", last_result: "ok", last_error: "" },
    ...over,
  });
  const disk = (used: number, alert = 85) => ({ size_bytes: 100, used_bytes: used, policy: { effective: { alert_percent: alert } } }) as unknown as DiskReport;

  it("counts security updates and a reboot as the board's 2, never other updates", () => {
    const items = panelMaintenance(updates({ security: 3, reboot: { required: true, since: "2026-10-28T06:14:00Z", packages: ["linux-image"] } }), disk(29));
    expect(maintenanceCount(items)).toEqual({ n: 2, tone: "warn" });
    expect(items.every((i) => i.anchor === "updates")).toBe(true);
  });

  it("counts the disk from its alert threshold, on the disk card", () => {
    expect(panelMaintenance(updates(), disk(84))).toEqual([]);
    expect(panelMaintenance(updates(), disk(85)).map((i) => [i.title, i.anchor])).toEqual([["Disk 85% full", "disk"]]);
    expect(panelMaintenance(undefined, disk(70, 60)).map((i) => i.anchor)).toEqual(["disk"]);
  });
});

describe("overviewAttention", () => {
  const now = Date.parse("2026-11-07T12:00:00Z");
  const ok = (name: string) => ({ name, role: "api", image: "", state: "running", health: "healthy", started_at: null, restarts: 0, cpu_percent: 0, memory_bytes: 0, memory_limit_bytes: 0 }) as PanelContainer;
  const status = (containers: PanelContainer[]) => ({ version: "v0.33.0", healthy: true, containers, certificate: null }) as unknown as PanelStatus;
  const upgrade = { install: "installer", current: "v0.33.0", latest: "v0.34.0", available: true, run: null } as unknown as Parameters<typeof overviewAttention>[0]["upgrade"];
  const reboot = { key: "reboot", level: "warning" as const, title: "Reboot required since 2026-10-28 06:14", sub: "", action: "Reboot", tab: "maintenance" as const, anchor: "updates" };
  const watchdog = { url: "https://hc-ping.com/x", last_ping_at: "2026-11-07T11:58:00Z", last_status: 200, last_error: "", held: false };

  it("lists the board's three: the release first, then Maintenance's warnings, each to its card", () => {
    const rows = overviewAttention({ status: status([ok("paas-backend")]), upgrade, maintenance: [reboot, { ...reboot, key: "agent", level: "info" }], watchdog, backup: null, now });
    expect(rows.map((r) => [r.title, r.action?.label, r.action?.href ?? r.action?.upgrade])).toEqual([
      ["Toft v0.34.0 is available", "Upgrade…", true],
      ["Reboot required since 2026-10-28 06:14", "Review", "/instance/maintenance#updates"],
    ]);
  });

  it("puts problems above the release, and offers logs only for a container that exists", () => {
    const rows = overviewAttention({
      status: status([{ ...ok("paas-frontend"), state: "missing" }, { ...ok("paas-git"), state: "exited", restarts: 2 }]),
      upgrade,
      maintenance: [],
      watchdog: { ...watchdog, last_error: "503 from hc-ping.com" },
      now,
    });
    expect(rows.map((r) => [r.level, r.title, r.action?.logs])).toEqual([
      ["problem", "paas-frontend is missing", undefined],
      ["problem", "paas-git has stopped", "paas-git"],
      ["problem", "The watchdog’s pings fail", undefined],
      ["release", "Toft v0.34.0 is available", undefined],
    ]);
  });

  it("warns of no watchdog and no backups, never a running upgrade's release", () => {
    const rows = overviewAttention({
      upgrade: { ...upgrade!, run: { outcome: "running" } } as typeof upgrade,
      maintenance: [],
      watchdog: { ...watchdog, url: "" },
      backup: null,
      noBackup: "Toft’s database, no backups",
      now,
    });
    expect(rows.map((r) => [r.title, r.action?.href])).toEqual([
      ["Toft’s database, no backups", "/settings/backups#panel-host"],
      ["No watchdog", "/instance/server#wd-h"],
    ]);
  });

  it("puts an out-of-date recovery kit first among the warnings, with the day it was stored", () => {
    const rows = overviewAttention({ upgrade, maintenance: [reboot], watchdog: { ...watchdog, url: "" }, kitOutOfDate: "2026-10-18", now });
    expect(rows.map((r) => [r.level, r.title, r.action?.label, r.action?.href ?? r.action?.upgrade])).toEqual([
      ["release", "Toft v0.34.0 is available", "Upgrade…", true],
      ["warning", "The stored recovery kit is out of date", "Show kit", "/instance/kit"],
      ["warning", "No watchdog", "Set up", "/instance/server#wd-h"],
      ["warning", "Reboot required since 2026-10-28 06:14", "Review", "/instance/maintenance#updates"],
    ]);
    expect(rows[1].sub).toContain("after it was stored on 2026-10-18");
  });

  // D-164: a restored panel whose domain hasn't come back to it.
  it("says a restored panel still answers on its IP, after the kit, with the button that stops it", () => {
    const rows = overviewAttention({ maintenance: [], watchdog, kitOutOfDate: "2026-10-18", awaitingDomain: "panel.example", now });
    expect(rows.map((r) => [r.level, r.key, r.action?.label, r.action?.stopIp])).toEqual([
      ["warning", "kit", "Show kit", undefined],
      ["warning", "ip", "Stop answering on the IP…", true],
    ]);
    expect(rows[1].sub).toContain("until panel.example points at this server and has a trusted certificate");
    expect(overviewAttention({ maintenance: [], watchdog, now })).toEqual([]);
  });

  // The certificate's Problem says where the domain points; on a restored
  // panel waiting for it, the IP row says so instead.
  it("folds a restored panel's domain pointing elsewhere into its IP row", () => {
    const certificate: PanelCertificate = {
      domain: "panel.example",
      issuer: "TRAEFIK DEFAULT CERT",
      not_after: "2027-10-18T12:00:00Z",
      ask: { reaches_panel: false, addresses: ["203.0.113.10"], last_try_at: null, next_ask_at: null, error: null },
    };
    const st = { ...status([]), certificate };
    const rows = overviewAttention({ maintenance: [], watchdog, status: st, awaitingDomain: "panel.example", now });
    expect(rows.map((r) => r.key)).toEqual(["ip"]);
    expect(rows[0].sub).toContain("It points at 203.0.113.10 now.");
    const cert = overviewAttention({ maintenance: [], watchdog, status: st, now });
    expect(cert.map((r) => [r.key, r.sub])).toEqual([["cert", "panel.example points at 203.0.113.10, which doesn’t lead to this panel: another server, or a router that won’t send this server’s own requests back in. Toft has Traefik ask once it reaches here."]]);
  });

  it("says the panel host's backups are not running while they are paused", () => {
    const backup = { last_run_at: "2026-11-06T03:31:00Z", last_ok: true, last_ok_at: "2026-11-06T03:31:00Z", paused: true, restored_at: "2026-11-07T10:00:00Z" } as Parameters<typeof overviewAttention>[0]["backup"];
    expect(overviewAttention({ maintenance: [], watchdog, backup, now }).map((r) => [r.level, r.title, r.action?.href])).toEqual([
      ["problem", "Toft’s database backups are not running", "/settings/backups#panel-host"],
    ]);
  });

  it("leaves the release out while the upgrade to it failed or rolled back, so Try again… is the one retry", () => {
    for (const outcome of ["rolled_back", "failed"])
      expect(overviewAttention({ upgrade: { ...upgrade!, run: { to: "v0.34.0", outcome } } as typeof upgrade, maintenance: [], watchdog, now })).toEqual([]);
    // A rollback from an older release doesn't hide the newer one.
    expect(overviewAttention({ upgrade: { ...upgrade!, run: { to: "v0.33.1", outcome: "rolled_back" } } as typeof upgrade, maintenance: [], watchdog, now }).map((r) => r.key)).toEqual(["release"]);
    expect(retryUpgrade({ latest: "v0.34.0", run: { to: "v0.34.0", outcome: "succeeded" } as UpgradeRun })).toBe(false);
  });
});

describe("oldTabRoute", () => {
  it("sends an old ?tab= link to its route", () => {
    expect(oldTabRoute("?tab=audit")).toBe("/instance/audit-log");
    expect(oldTabRoute("?tab=policy")).toBe("/instance/deploy-policy");
    expect(oldTabRoute("?tab=overview")).toBe("");
    expect(oldTabRoute("")).toBe("");
  });
});
