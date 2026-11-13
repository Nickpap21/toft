import { cn } from "@/lib/utils";
import { statusLabel, statusTone, TONE_CLASSES } from "@/lib/status";

interface StatusBadgeProps {
  /** A runtime, deployment, domain or server status. Anything unknown is
   *  shown as given, in the neutral tone. */
  status: string;
  className?: string;
}

export function StatusBadge({ status, className }: StatusBadgeProps) {
  const tone = TONE_CLASSES[statusTone(status)];
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium",
        tone.chip,
        className
      )}
    >
      <span aria-hidden="true" className={cn("h-2 w-2 rounded-full shrink-0", tone.dot)} />
      {statusLabel(status)}
    </span>
  );
}
