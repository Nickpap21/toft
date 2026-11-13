// Compact times, as the boards show them (Home, Service).
import { formatDateTime } from "./utils";

/** "now", "4m", "2h", "3d": the board's compact relative time. */
export function timeAgo(iso: string, now: number): string {
  const s = Math.max(0, Math.floor((now - new Date(iso).getTime()) / 1000));
  if (s < 60) return "now";
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

/** timeAgo in a sentence: "just now", "4m ago", "3d ago". */
export function ago(iso: string, now: number): string {
  const t = timeAgo(iso, now);
  return t === "now" ? "just now" : `${t} ago`;
}

const DAY = 86_400_000;

/** Settings and admin's rule (D-46): "just now", "18 min ago", "3 h ago"
 *  within a day, else "2026-10-12 03:12". */
export function sinceShort(iso: string, now: number): string {
  const ms = Math.max(0, now - new Date(iso).getTime());
  if (ms < 60_000) return "just now";
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)} min ago`;
  if (ms < DAY) return `${Math.floor(ms / 3_600_000)} h ago`;
  return formatDateTime(iso);
}

/** An expiry's hint beside its ISO time: "today", "tomorrow", "in 5 days",
 *  counted in calendar days of the viewer's time zone. */
export function untilHint(iso: string, now: number): string {
  const day = (t: number) => {
    const d = new Date(t);
    return Date.UTC(d.getFullYear(), d.getMonth(), d.getDate()) / DAY;
  };
  const days = day(new Date(iso).getTime()) - day(now);
  if (days <= 0) return "today";
  if (days === 1) return "tomorrow";
  return `in ${days} days`;
}

/** "48s", "1m 24s", "1h 5m". */
export function formatDuration(seconds: number): string {
  const s = Math.round(seconds);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}
