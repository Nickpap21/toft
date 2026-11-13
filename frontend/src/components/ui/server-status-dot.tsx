import { statusTone, TONE_CLASSES } from "@/lib/status";

export function ServerStatusDot({ status, provisionedAt }: { status: string; provisionedAt?: string | null }) {
  // "active" is set by both a bare SSH test-connection and a completed Provision —
  // distinguish "reachable" from "actually set up" using provisionedAt when it's passed.
  const needsProvisioning = status === "active" && provisionedAt === null;
  const active = status === "active" && !needsProvisioning;
  const label = needsProvisioning ? "Needs provisioning" : (status ?? "unknown");
  // Any other status ("error", "pending", …) takes its usual tone.
  const tone = TONE_CLASSES[active ? "running" : needsProvisioning ? "pending" : statusTone(status)];

  return (
    <span className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium ${tone.chip}`}>
      <span aria-hidden="true" className={`h-2 w-2 rounded-full shrink-0 ${tone.dot}`} />
      {label}
    </span>
  );
}
