// Settings → Backups' wording (board: Backup), kept apart from the page so it
// is tested. The backend's rules: internal/server/backup.go and
// toft-backup.sh.
import type { BackupDestination } from "@/lib/api/server.service";
import { formatBytes } from "@/lib/api/sharedvolume.service";
import { sinceShort } from "@/lib/time";
import { formatDateTimeZone } from "@/lib/utils";

/** A run older than this with no success since raises Backup failing. */
const OVERDUE_MS = 26 * 3_600_000;

export type BackupTone = "ok" | "fail" | "wait" | "off";

export interface BackupState {
  label: string;
  tone: BackupTone;
  note: string;
  /** Show the last run's error under the row. */
  failing: boolean;
  /** Set up, but nothing runs: the host has no backup settings. */
  paused?: boolean;
}

/** How a destination's backups are going, from its last run. */
export function backupState(d: BackupDestination | null, now: number): BackupState {
  if (!d) return { label: "Not set up", tone: "off", note: "", failing: false };
  if (d.paused) {
    return { label: "Not running", tone: "fail", note: d.restored_at ? "since the restore" : "no settings on the host", failing: false, paused: true };
  }
  if (d.last_ok === false && d.last_run_at) {
    return { label: "Failed", tone: "fail", note: sinceShort(d.last_run_at, now), failing: true };
  }
  if (d.last_ok_at) {
    if (now - new Date(d.last_ok_at).getTime() > OVERDUE_MS) {
      return { label: "Missed", tone: "fail", note: `last success ${sinceShort(d.last_ok_at, now)}`, failing: true };
    }
    // Resumed after a restore: that success is the old server's, and this
    // one has run none. Failed and Missed above stay, as the alert's rule does.
    if (d.restored_at) return { label: "Waiting", tone: "wait", note: "no backup since the restore", failing: false };
    const size = d.last_bytes != null ? ` · ${formatBytes(d.last_bytes)}` : "";
    return { label: "Succeeded", tone: "ok", note: `${sinceShort(d.last_ok_at, now)}${size}`, failing: false };
  }
  return { label: "Waiting", tone: "wait", note: `first backup at ${d.schedule}, or within 15 minutes if that has passed today`, failing: false };
}

/** "2 backed up last night · 1 failing · 1 not running · 1 not set up". */
export function summary(states: BackupState[]): string {
  const count = (t: BackupTone) => states.filter((s) => s.tone === t).length;
  const parts: string[] = [];
  if (count("ok")) parts.push(`${count("ok")} backed up last night`);
  const paused = states.filter((s) => s.paused).length;
  const failing = count("fail") - paused;
  if (failing) parts.push(`${failing} failing`);
  if (paused) parts.push(`${paused} not running`);
  if (count("wait")) parts.push(`${count("wait")} waiting for the first`);
  const off = count("off");
  if (off) parts.push(off === states.length ? "nothing is backed up yet" : `${off} not set up`);
  const s = parts.join(" · ");
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** The line under a failing row: what was lost and since when it alerts. */
export function failureNote(d: BackupDestination, failingSince: string | null): string {
  const last = d.last_ok_at ? `Last success ${formatDateTimeZone(d.last_ok_at)}` : "No backup has succeeded yet";
  const alert = failingSince ? ` · Backup failing is open since ${formatDateTimeZone(failingSince)}.` : ".";
  return `Nothing was backed up. ${last}${alert}`;
}

/** "fsn1.your-objectstorage.com/founders-backups/toft/control-plane". */
export const destinationOf = (d: BackupDestination) => d.repository.replace(/^s3:https?:\/\//, "");
export const hostOf = (endpoint: string) => endpoint.replace(/^https?:\/\//, "");
