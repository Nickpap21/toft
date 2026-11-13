import { cn } from "@/lib/utils";
import { TONE_CLASSES } from "@/lib/status";
import { HEALTH_TONE, healthLabel, type HealthSummary } from "./health";

/** A project's health: a dot (a hollow ring when nothing runs), with the
 *  words for screen readers. Failing projects also say so in text. */
export function HealthDot({ summary, showFailing = true }: { summary: HealthSummary | null; showFailing?: boolean }) {
  if (!summary) return null;
  const label = healthLabel(summary);
  if (summary.health === "failed" && showFailing) {
    return (
      <span className="shrink-0 text-xs text-status-failed-fg" title={label}>
        failing
        <span className="sr-only"> ({label})</span>
      </span>
    );
  }
  return (
    <span className="inline-flex shrink-0 items-center" title={label}>
      {/* Each state has its own shape, not only its own colour: running is a
          solid dot, deploying an open arc (spinning when motion is allowed),
          unhealthy a diamond, nothing running a thin ring. */}
      <span
        aria-hidden="true"
        className={cn(
          "h-1.5 w-1.5 rounded-full",
          summary.health === "idle"
            ? "border border-muted"
            : summary.health === "building"
              ? "h-2.5 w-2.5 border-2 border-status-building border-t-transparent motion-safe:animate-spin"
              : summary.health === "unhealthy"
                ? "rotate-45 rounded-xs bg-status-pending"
                : TONE_CLASSES[HEALTH_TONE[summary.health]].dot,
        )}
      />
      <span className="sr-only">{label}</span>
    </span>
  );
}
