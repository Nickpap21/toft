import { describe, expect, it } from "vitest";
import type { HostUpdates } from "@/lib/api/server.service";
import type { Problem } from "@/components/servers/capacity";
import { attentionItems, attentionSummary, dnsProviderName, maintenanceCount, serverTab } from "./server-view";

const updates = (): HostUpdates => ({
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

const adv = (severity: "low" | "medium" | "high" | "critical") => ({ id: "GHSA-x", severity, summary: "", fixed_in: "29.8.2", url: "" });

describe("the server's tabs", () => {
  it("reads the tab from ?tab=, else from an old link's anchor", () => {
    expect(serverTab(null, "")).toBe("overview");
    expect(serverTab("maintenance", "")).toBe("maintenance");
    expect(serverTab("settings", "#disk")).toBe("settings");
    expect(serverTab(null, "#backup")).toBe("settings");
    expect(serverTab(null, "#dns-h")).toBe("settings");
    expect(serverTab(null, "#disk")).toBe("maintenance");
    expect(serverTab(null, "#updates")).toBe("maintenance");
    expect(serverTab("nonsense", "#nothing")).toBe("overview");
  });

  it("opens the tab of every card's anchor", () => {
    for (const a of ["dns", "isolation", "firewall", "overrides", "sharing", "remove", "connection", "agent", "traefik", "backup"]) {
      expect(serverTab(null, `#${a}`)).toBe("settings");
    }
    for (const a of ["updates", "disk", "provisioning"]) expect(serverTab(null, `#${a}`)).toBe("maintenance");
  });
});

describe("Needs attention, when reads failed", () => {
  const base = { problems: [], moveToAgent: false, canAdmin: true };

  it("names what wasn't checked, so it never reads as nothing to do", () => {
    expect(attentionItems({ ...base, unread: ["capacity", "updates"] })).toEqual([
      { key: "unread", level: "warning", title: "Couldn’t check capacity and updates", sub: "What they’d show is missing here. Reload the page to read them again." },
    ]);
  });

  it("says it of one read too", () => {
    expect(attentionItems({ ...base, unread: ["backups"] })[0]).toMatchObject({ title: "Couldn’t check backups", sub: "What it’d show is missing here. Reload the page to read it again." });
  });

  it("adds nothing when every read answered", () => {
    expect(attentionItems({ ...base, unread: [] })).toEqual([]);
    expect(attentionItems(base)).toEqual([]);
  });
});

describe("dnsProviderName", () => {
  it("names a known lego provider as its company does, and leaves the rest alone", () => {
    expect(dnsProviderName("cloudflare")).toBe("Cloudflare");
    expect(dnsProviderName("route53")).toBe("Amazon Route 53");
    expect(dnsProviderName("hetzner")).toBe("Hetzner");
    expect(dnsProviderName("somethingelse")).toBeNull();
  });
});

describe("Needs attention", () => {
  const base = { problems: [] as Problem[], moveToAgent: false, canAdmin: true };

  it("is empty on a healthy server", () => {
    expect(attentionItems({ ...base, updates: updates() })).toEqual([]);
  });

  it("names Docker's advisories as a problem, and what fixes them", () => {
    const u = updates();
    u.docker = { version: "29.4.3", package: "docker-ce", candidate: "29.8.2", advisories: [adv("high"), adv("high"), adv("high"), adv("medium"), adv("medium")] };
    const [d] = attentionItems({ ...base, updates: u });
    expect(d).toMatchObject({ key: "docker", level: "problem", title: "Docker 29.4.3 is affected by 5 advisories, 3 of them high", action: "Update Docker", tab: "maintenance", anchor: "updates" });
    expect(d.sub).toBe("29.8.2 is available. Updating Docker restarts its containers.");
  });

  it("lists a waiting reboot and security updates as warnings; other updates aren't", () => {
    const u = { ...updates(), security: 2, other: 14, reboot: { required: true, since: "2026-10-28T06:14:00", packages: ["linux-base"] } };
    const items = attentionItems({ ...base, updates: u });
    expect(items.map((i) => [i.key, i.level, i.action])).toEqual([
      ["reboot", "warning", "Reboot"],
      ["security", "warning", "Review"],
    ]);
    expect(items[0].title).toBe("Reboot required since 2026-10-28 06:14");
    expect(items[1].title).toBe("2 security updates waiting");
  });

  it("splits a problem's text into a title and a line under it, worst first", () => {
    const problems: Problem[] = [
      { kind: "disk", level: "warning", text: "Disk 87% full. 3 images no service uses take about 4.2 GB." },
      { kind: "backup", level: "problem", text: "The last backup failed 3 h ago: no bucket. The newest good backup is 27 hours old." },
      { kind: "suggestions", level: "info", text: "2 memory limits suggested, frees 768 MiB." },
    ];
    const items = attentionItems({ ...base, problems });
    expect(items.map((i) => [i.key, i.title, i.sub, i.tab, i.anchor])).toEqual([
      ["backup", "The last backup failed 3 h ago: no bucket", "The newest good backup is 27 hours old.", "settings", "backup"],
      ["disk", "Disk 87% full", "3 images no service uses take about 4.2 GB.", "maintenance", "disk"],
    ]);
  });

  it("gives the address pool's fix under the provisioning warning", () => {
    const problems: Problem[] = [{ kind: "provision", level: "warning", text: "Provisioned without the app network address pool: its log says why." }];
    const [p] = attentionItems({ ...base, problems, poolFix: "Add default-address-pools to /etc/docker/daemon.json, then restart Docker." });
    expect(p).toMatchObject({ title: "Provisioned without the app network address pool: its log says why.", sub: "Add default-address-pools to /etc/docker/daemon.json, then restart Docker.", action: "Re-provision" });
  });

  it("counts lists apt never downloaded as a warning on Updates", () => {
    const [l] = attentionItems({ ...base, updates: { ...updates(), lists_updated_at: null } });
    expect(l).toMatchObject({ key: "lists", level: "warning", title: "apt has never downloaded its package lists", action: "Review", tab: "maintenance", anchor: "updates" });
    expect(maintenanceCount([l])).toEqual({ n: 1, tone: "warn" });
  });

  it("offers developers and viewers a Review link where only an owner or operator can act", () => {
    const u = { ...updates(), security: 1, reboot: { required: true, since: null, packages: [] } };
    u.docker = { version: "29.4.3", package: "docker-ce", candidate: "29.8.2", advisories: [adv("high")] };
    const problems: Problem[] = [
      { kind: "disk", level: "warning", text: "Disk 87% full." },
      { kind: "provision", level: "warning", text: "Provisioned without the firewall." },
      { kind: "no_backup", level: "warning", text: "Nothing backs it up." },
    ];
    const admin = attentionItems({ ...base, updates: u, problems });
    expect(admin.map((i) => i.action)).toEqual(expect.arrayContaining(["Update Docker", "Reboot", "Free space", "Re-provision", "Set up backups"]));
    const items = attentionItems({ ...base, canAdmin: false, updates: u, problems });
    expect(items.map((i) => [i.key, i.action])).toEqual(admin.map((i) => [i.key, "Review"]));
    expect(items.map((i) => [i.tab, i.anchor])).toEqual(admin.map((i) => [i.tab, i.anchor]));
  });

  it("ends with the suggestion to move to the agent", () => {
    const items = attentionItems({ ...base, updates: { ...updates(), security: 1 }, moveToAgent: true });
    expect(items.map((i) => [i.key, i.level])).toEqual([["security", "warning"], ["agent", "info"]]);
  });

  it("counts what needs action for Maintenance, coloured by the worst", () => {
    const u = { ...updates(), security: 2 };
    expect(maintenanceCount(attentionItems({ ...base, updates: u, moveToAgent: true }))).toEqual({ n: 1, tone: "warn" });
    u.docker = { ...u.docker, advisories: [adv("critical")] };
    expect(maintenanceCount(attentionItems({ ...base, updates: u }))).toEqual({ n: 2, tone: "bad" });
    expect(maintenanceCount(attentionItems({ ...base, moveToAgent: true }))).toEqual({ n: 0, tone: "warn" });
    // Only what the Maintenance tab shows: a backup problem is Settings'.
    const backup: Problem = { kind: "backup", level: "problem", text: "The last backup failed." };
    const disk: Problem = { kind: "disk", level: "warning", text: "Disk 87% full." };
    expect(maintenanceCount(attentionItems({ ...base, problems: [backup, disk] }))).toEqual({ n: 1, tone: "warn" });
  });

  it("sums itself up as the board does", () => {
    const u = { ...updates(), security: 2, reboot: { required: true, since: null, packages: [] } };
    expect(attentionSummary(attentionItems({ ...base, updates: u, moveToAgent: true }))).toBe("2 items and a suggestion");
    expect(attentionSummary(attentionItems({ ...base, updates: { ...updates(), security: 1 } }))).toBe("1 item");
    expect(attentionSummary(attentionItems({ ...base, moveToAgent: true }))).toBe("a suggestion");
    expect(attentionSummary([])).toBe("nothing to do");
  });
});
