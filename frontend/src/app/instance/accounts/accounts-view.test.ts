import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import { auditWords, changeError, deactivatedNote, deletedNote, doneText, filterAccounts, lastSignedIn, operatorHow, resetNote, statusNote } from "./accounts-view";

const now = new Date("2026-10-15T14:10:00").getTime();

describe("operatorHow", () => {
  const names = new Map([["u-admin", "admin"]]);
  it("says why someone is an operator", () => {
    expect(operatorHow({ source: "first_run", granted_by: null }, names)).toBe("First account on this instance");
    expect(operatorHow({ source: "api", granted_by: "u-admin" }, names)).toBe("Granted by admin");
    expect(operatorHow({ source: "api", granted_by: null }, names)).toBe("Granted by an operator");
    expect(operatorHow({ source: "env", granted_by: null }, names)).toBe("Listed in TOFT_OPERATOR_EMAILS");
  });
});

describe("statusNote", () => {
  const u = { is_active: true, is_operator: true, created_at: "2025-01-02T10:00:00", deactivated_at: null, deactivated_by: "" };
  it("dates an operator from the grant, never from the account's creation", () => {
    expect(statusNote(u, "2026-10-05T09:00:00", now)).toBe("Operator · since 2026-10-05");
  });
  it("says just Operator while the grant isn't loaded", () => {
    expect(statusNote(u, undefined, now)).toBe("Operator");
  });
  it("dates a non-operator from the account's creation", () => {
    expect(statusNote({ ...u, is_operator: false }, "2026-10-05T09:00:00", now)).toBe("since 2025-01-02");
  });
  it("gives a deactivated account its deactivation note", () => {
    const d = { ...u, is_active: false, deactivated_at: new Date(now - 12 * 60_000).toISOString(), deactivated_by: "admin" };
    expect(statusNote(d, "2026-10-05T09:00:00", now)).toBe("12 min ago by admin");
  });
});

describe("deactivatedNote", () => {
  it("is relative within a day, else ISO, with who", () => {
    expect(deactivatedNote({ deactivated_at: new Date(now - 12 * 60_000).toISOString(), deactivated_by: "admin" }, now)).toBe("12 min ago by admin");
    expect(deactivatedNote({ deactivated_at: "2026-10-12T03:12:00", deactivated_by: "" }, now)).toBe("2026-10-12 03:12");
    expect(deactivatedNote({ deactivated_at: null, deactivated_by: "" }, now)).toBe("");
  });
});

describe("lastSignedIn", () => {
  it("counts days, is relative within a day, and says never", () => {
    expect(lastSignedIn(new Date(now - 3 * 86_400_000 - 60_000).toISOString(), now)).toBe("Last signed in 3 days ago");
    expect(lastSignedIn(new Date(now - 86_400_000).toISOString(), now)).toBe("Last signed in 1 day ago");
    expect(lastSignedIn(new Date(now - 5 * 60_000).toISOString(), now)).toBe("Last signed in 5 min ago");
    expect(lastSignedIn(null, now)).toBe("Never signed in");
  });
});

describe("D-57 notes", () => {
  it("names a live reset link, the deleted count, and an audit row", () => {
    expect(resetNote("2026-10-16T20:43:00")).toBe("Reset link live until 20:43");
    expect(resetNote(null)).toBe("");
    expect(deletedNote(0)).toBe("");
    expect(deletedNote(1)).toBe("1 deleted account isn’t listed.");
    expect(deletedNote(3)).toBe("3 deleted accounts aren’t listed.");
    expect(auditWords({ action: "user.deleted", detail: { email: "sam@paas.local" } })).toEqual({ label: "Deleted the account", note: "Was sam@paas.local" });
    expect(auditWords({ action: "user.deactivated", detail: { invites_revoked: 2 } }).note).toBe("2 pending invites revoked");
    expect(auditWords({ action: "operator.granted", detail: {} }).label).toBe("Granted operator");
    expect(auditWords({ action: "odd.thing", detail: {} }).label).toBe("odd.thing");
    expect(auditWords({ action: "panel.domain_changed", detail: { from: "", to: "panel.example" } })).toEqual({ label: "Changed the panel’s address", note: "Now panel.example" });
    expect(auditWords({ action: "panel.domain_changed", detail: { from: "panel.example", to: "" } }).note).toBe("Now the server’s IP address");
    expect(auditWords({ action: "panel.ip_answer_stopped", detail: { domain: "panel.example" } })).toEqual({ label: "Stopped the panel answering on its IP address", note: "Now only panel.example" });
    expect(auditWords({ action: "panel.ip_answer_restored", detail: {} })).toEqual({ label: "Made the panel answer on its IP address again", note: "With toft answer-on-ip on the server" });
    expect(auditWords({ action: "panel.domain_change_failed", detail: { from: "panel.example", to: "panel.example", undone: true } })).toEqual({ label: "Tried to stop the panel answering on its IP address", note: "The routes could not be written: nothing changed" });
    expect(auditWords({ action: "panel.domain_change_failed", detail: { from: "", to: "panel.example", undone: false } })).toEqual({ label: "Tried to change the panel’s address to panel.example", note: "The routes could not be written: the new address stayed saved and applies at the next restart" });
    expect(auditWords({ action: "panel.role_changed", detail: { to: "control_plane" } }).note).toBe("Now “Only Toft”");
    expect(auditWords({ action: "recovery_kit.viewed", detail: {} }).label).toBe("Viewed the recovery kit");
    expect(auditWords({ action: "recovery_kit.stored", detail: {} }).label).toBe("Marked the recovery kit as stored");
    expect(auditWords({ action: "password_reset.issued", detail: {}, target: { name: "sam" } }).label).toBe("Issued sam a reset link");
    expect(auditWords({ action: "user.deactivated", detail: {}, target: { name: "dimitra" } }).label).toBe("Deactivated dimitra");
    expect(auditWords({ action: "user.deleted", detail: { email: "sam@paas.local" }, target: { name: "Deleted account" } })).toEqual({ label: "Deleted the account", note: "Was sam@paas.local" });
  });
});

describe("doneText", () => {
  it("names the revoked invites", () => {
    expect(doneText({ name: "sam", is_active: false, invites_revoked: 1 })).toBe(
      "sam was deactivated. 1 pending invite was revoked; its workspace owner has to send a new link.",
    );
    expect(doneText({ name: "sam", is_active: false, invites_revoked: 0 })).toBe("sam was deactivated.");
    expect(doneText({ name: "sam", is_active: true, invites_revoked: 0 })).toBe("sam can sign in again.");
  });
});

describe("changeError", () => {
  it("reads the API's codes", () => {
    expect(changeError(new ApiError("your password is incorrect", 403, { data: { code: "wrong_password" } }))).toBe(
      "Your password is incorrect. Nothing was changed.",
    );
    expect(changeError(new ApiError("too many requests — slow down", 429, { data: { retry_after: 90 } }))).toBe(
      "Too many attempts. Try again in 90 seconds.",
    );
    expect(changeError(new ApiError("this would leave the instance without an active operator — grant another operator first", 409, {}))).toBe(
      "This would leave the instance without an active operator: grant another operator first.",
    );
  });
});

describe("filterAccounts", () => {
  it("matches name or email", () => {
    const users = [
      { name: "alex", email: "alex@paas.local" },
      { name: "sam", email: "sam@paas.local" },
    ];
    expect(filterAccounts(users, " SAM ")).toEqual([users[1]]);
    expect(filterAccounts(users, "paas")).toEqual(users);
  });
});

describe("auditWords for the panel's shell", () => {
  it("says where it was opened from and how long it was open", () => {
    expect(auditWords({ action: "panel.shell_opened", detail: { from: "198.51.100.24" } })).toEqual({
      label: "Opened a shell on the panel’s server",
      note: "From 198.51.100.24",
    });
    expect(auditWords({ action: "panel.shell_closed", detail: { seconds: 720 } }).note).toBe("Open 12 min");
    expect(auditWords({ action: "panel.shell_closed", detail: { seconds: 40 } }).note).toBe("Open 40 s");
    expect(auditWords({ action: "panel.shell_closed", detail: { seconds: 300, restart: true } })).toEqual({ label: "Shell on the panel’s server ended by a restart", note: "Open 5 min" });
  });
});

describe("auditWords for gVisor on the panel's server (D-97)", () => {
  it("names the release, and the step that failed", () => {
    expect(auditWords({ action: "panel.gvisor_installed", detail: { release: "20261012", platform: "systrap" } })).toEqual({
      label: "Installed gVisor 20261012 on the panel’s server",
      note: "systrap",
    });
    expect(auditWords({ action: "panel.gvisor_install_failed", detail: { release: "20261012", step: "[3/5] Installing runsc", error: "apt-get install runsc=20261012.0: exit status 100" } })).toEqual({
      label: "Installing gVisor 20261012 on the panel’s server failed",
      note: "[3/5] Installing runsc: apt-get install runsc=20261012.0: exit status 100",
    });
  });
});

describe("auditWords for upgrades and the watchdog (D-68)", () => {
  it("names the release and how it went", () => {
    expect(auditWords({ action: "panel.upgraded", detail: { to: "v0.1.3", from: "v0.1.2", seconds: 112 } })).toEqual({
      label: "Upgraded Toft to v0.1.3",
      note: "From v0.1.2 · 1 min 52 s",
    });
    expect(auditWords({ action: "panel.upgrade_rolled_back", detail: { to: "v0.1.3", from: "v0.1.2" } }).label).toBe("The upgrade to v0.1.3 rolled back");
    expect(auditWords({ action: "panel.watchdog_set", detail: { host: "hc-ping.com" } }).note).toBe("hc-ping.com");
  });
});

describe("auditWords for the disk of the panel's server", () => {
  it("says what a clean freed, and why one failed", () => {
    expect(auditWords({ action: "panel.disk_cleaned", detail: { outcome: "succeeded", freed_bytes: 3_200_000_000, seconds: 112 } })).toEqual({
      label: "Cleaned the disk",
      note: "Freed 3.2 GB · 1 min 52 s",
    });
    expect(auditWords({ action: "panel.disk_cleaned", detail: { outcome: "failed", error: "the server didn’t answer" } })).toEqual({
      label: "The disk clean failed",
      note: "the server didn’t answer",
    });
  });

  it("tolerates a missing field", () => {
    expect(auditWords({ action: "panel.disk_cleaned", detail: {} })).toEqual({ label: "Cleaned the disk", note: "" });
    expect(auditWords({ action: "panel.volume_removed", detail: {} })).toEqual({ label: "Removed a volume", note: "" });
    expect(auditWords({ action: "panel.disk_policy_changed", detail: {} })).toEqual({ label: "Changed the disk policy", note: "" });
  });

  it("says when the clean was the automatic one", () => {
    expect(auditWords({ action: "panel.disk_clean_started", detail: { automatic: true } })).toEqual({ label: "Started a disk clean", note: "The automatic clean" });
    expect(auditWords({ action: "panel.disk_clean_started", detail: { automatic: false } }).note).toBe("");
  });

  it("names the volume and the settings a policy change touched", () => {
    expect(auditWords({ action: "panel.volume_removed", detail: { name: "old_data", bytes: 205_000_000 } })).toEqual({ label: "Removed the volume old_data", note: "205 MB" });
    expect(auditWords({ action: "panel.volume_removed", detail: { name: "old_data", bytes: -1 } })).toEqual({ label: "Removed the volume old_data", note: "" });
    expect(
      auditWords({
        action: "panel.disk_policy_changed",
        detail: { old: { build_cache_limit_bytes: null, auto_clean: ["images"], alert_percent: 85 }, new: { build_cache_limit_bytes: 1e10, auto_clean: ["images"], alert_percent: 90 } },
      }).note,
    ).toBe("Build cache limit, alert level");
  });
});

describe("auditWords for the panel's server's updates and agents", () => {
  it("says what a run did", () => {
    expect(auditWords({ action: "panel.updates_started", detail: { action: "docker" } })).toEqual({ label: "Started updates on the panel’s server", note: "Updating Docker" });
    expect(auditWords({ action: "panel.updates_succeeded", detail: { action: "install", packages: 12, seconds: 40 } })).toEqual({
      label: "Finished updates on the panel’s server",
      note: "Updates installed · 12 packages · 40 s",
    });
    expect(auditWords({ action: "panel.updates_succeeded", detail: { action: "reboot", packages: 0, seconds: 40 } }).note).toBe("Rebooted · 40 s");
    expect(auditWords({ action: "panel.updates_failed", detail: { action: "install", error: "dpkg was locked" } })).toEqual({
      label: "Updates on the panel’s server failed",
      note: "dpkg was locked",
    });
  });

  it("tolerates a run with no details", () => {
    expect(auditWords({ action: "panel.updates_started", detail: {} }).note).toBe("");
    expect(auditWords({ action: "panel.updates_succeeded", detail: {} }).note).toBe("");
    expect(auditWords({ action: "panel.updates_failed", detail: {} }).note).toBe("");
  });

  it("says agents were paused on the whole panel", () => {
    expect(auditWords({ action: "agents.paused", detail: {} }).label).toBe("Paused agents on this panel");
    expect(auditWords({ action: "agents.resumed", detail: {} }).label).toBe("Resumed agents on this panel");
  });
});
