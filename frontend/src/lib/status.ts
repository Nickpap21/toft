/**
 * Status tones (DESIGN.md → Color): green running, blue building, amber
 * pending or warning, red failed. Every badge, dot and health signal maps
 * its status through here, so a new status string needs one line.
 * Unknown statuses are neutral, never alarming.
 */

export type StatusTone = "running" | "building" | "pending" | "failed" | "neutral";

const TONES: Record<string, StatusTone> = {
  running: "running",
  unhealthy: "pending",
  active: "running",
  verified: "running",
  deploying: "building",
  building: "building",
  pending: "pending",
  queued: "pending",
  stopped: "pending",
  error: "failed",
  failed: "failed",
  idle: "neutral",
  cancelled: "neutral",
};

const LABELS: Record<string, string> = {
  idle: "Idle",
  deploying: "Deploying",
  running: "Running",
  unhealthy: "Unhealthy",
  stopped: "Stopped",
  error: "Error",
  active: "Active",
  verified: "Verified",
  pending: "Pending",
  queued: "Queued",
  building: "Building",
  failed: "Failed",
  cancelled: "Cancelled",
};

/** A service whose container serves: running, or running but failing its
 *  health check. Its logs, usage and domains are live. */
export function isServing(status: string | null | undefined): boolean {
  return status === "running" || status === "unhealthy";
}

/** A service that has containers to stop: serving, deploying, or shown as
 *  failing while one crash-loops. */
export function isStoppable(status: string | null | undefined): boolean {
  return !!status && status !== "idle" && status !== "stopped";
}

export function statusTone(status: string | null | undefined): StatusTone {
  return (status && TONES[status]) || "neutral";
}

/** The label for a known status; anything else is shown as given. */
export function statusLabel(status: string | null | undefined): string {
  if (!status) return "Unknown";
  return LABELS[status] ?? status;
}

/** Classes per tone: a chip (soft background, readable text), a solid dot,
 *  and text alone (a dot-and-label status in a table). */
export const TONE_CLASSES: Record<StatusTone, { chip: string; dot: string; text: string }> = {
  running: { chip: "bg-status-running-soft text-status-running-fg", dot: "bg-status-running", text: "text-status-running-fg" },
  building: { chip: "bg-status-building-soft text-status-building-fg", dot: "bg-status-building motion-safe:animate-pulse", text: "text-status-building-fg" },
  pending: { chip: "bg-status-pending-soft text-status-pending-fg", dot: "bg-status-pending", text: "text-status-pending-fg" },
  failed: { chip: "bg-status-failed-soft text-status-failed-fg", dot: "bg-status-failed", text: "text-status-failed-fg" },
  neutral: { chip: "bg-raised text-muted", dot: "bg-muted", text: "text-muted" },
};
