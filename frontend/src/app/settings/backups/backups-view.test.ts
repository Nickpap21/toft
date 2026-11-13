import { describe, expect, it } from "vitest";
import type { BackupDestination } from "@/lib/api/server.service";
import { formatDateTimeZone } from "@/lib/utils";
import { backupState, failureNote, summary } from "./backups-view";

const now = new Date("2026-10-15T14:00:00").getTime();
const ago = (h: number) => new Date(now - h * 3_600_000).toISOString();
const dest = (over: Partial<BackupDestination>): BackupDestination => ({
  endpoint: "https://fsn1.your-objectstorage.com",
  bucket: "founders-backups",
  access_key: "K7",
  schedule: "03:00",
  repository: "s3:https://fsn1.your-objectstorage.com/founders-backups/toft/srv",
  last_run_at: null,
  last_ok: null,
  last_ok_at: null,
  last_bytes: null,
  last_error: "",
  created_at: ago(72),
  ...over,
});

describe("backupState", () => {
  it("reads the last run", () => {
    expect(backupState(null, now)).toMatchObject({ label: "Not set up", tone: "off" });
    expect(backupState(dest({}), now)).toMatchObject({ label: "Waiting", note: "first backup at 03:00, or within 15 minutes if that has passed today" });
    expect(backupState(dest({ last_run_at: ago(11), last_ok: true, last_ok_at: ago(11), last_bytes: 1.4 * 1024 ** 3 }), now)).toMatchObject({
      label: "Succeeded",
      note: "11 h ago · 1.4 GiB",
    });
    expect(backupState(dest({ last_run_at: ago(11), last_ok: false, last_ok_at: ago(35) }), now)).toMatchObject({ label: "Failed", failing: true });
  });
  it("says paused before anything about the last run", () => {
    const ran = { last_run_at: ago(40), last_ok: true, last_ok_at: ago(40) };
    expect(backupState(dest({ ...ran, paused: true, restored_at: ago(2) }), now)).toEqual({
      label: "Not running",
      tone: "fail",
      note: "since the restore",
      failing: false,
      paused: true,
    });
    expect(backupState(dest({ paused: true }), now)).toMatchObject({ label: "Not running", note: "no settings on the host" });
  });
  it("after a restore and a resume, waits for this server's first run instead of showing the old server's result", () => {
    const ran = { last_run_at: ago(11), last_ok: true, last_ok_at: ago(11) };
    expect(backupState(dest({ ...ran, restored_at: ago(2) }), now)).toEqual({
      label: "Waiting",
      tone: "wait",
      note: "no backup since the restore",
      failing: false,
    });
    // Overdue is still missed, as the Backup failing alert says.
    expect(backupState(dest({ last_run_at: ago(40), last_ok: true, last_ok_at: ago(40), restored_at: ago(2) }), now)).toMatchObject({ label: "Missed", failing: true });
  });
  it("counts a success older than 26 hours as missed", () => {
    expect(backupState(dest({ last_run_at: ago(30), last_ok: true, last_ok_at: ago(30) }), now)).toMatchObject({ label: "Missed", tone: "fail" });
  });
});

describe("summary", () => {
  it("counts each state", () => {
    const ok = backupState(dest({ last_run_at: ago(11), last_ok: true, last_ok_at: ago(11) }), now);
    const failed = backupState(dest({ last_run_at: ago(11), last_ok: false }), now);
    expect(summary([ok, ok, failed])).toBe("2 backed up last night · 1 failing");
    expect(summary([backupState(null, now), backupState(null, now)])).toBe("Nothing is backed up yet");
    expect(summary([ok, failed, backupState(dest({ paused: true }), now)])).toBe("1 backed up last night · 1 failing · 1 not running");
    expect(summary([backupState(dest({ paused: true }), now)])).toBe("1 not running");
  });
});

describe("failureNote", () => {
  it("names the last success and the alert", () => {
    const d = dest({ last_ok_at: ago(35) });
    expect(failureNote(d, ago(11))).toBe(`Nothing was backed up. Last success ${formatDateTimeZone(ago(35))} · Backup failing is open since ${formatDateTimeZone(ago(11))}.`);
    expect(failureNote(dest({}), null)).toBe("Nothing was backed up. No backup has succeeded yet.");
  });
});
