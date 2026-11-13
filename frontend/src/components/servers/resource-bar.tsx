import type { Server } from "@/lib/api/server.service";
import { statusLabel } from "@/lib/status";
import { cn } from "@/lib/utils";
import { help } from "@/app/projects/_lib/ui";
import { isOnline, sharedWith, type Segment } from "./capacity";

// The capacity meters of the Servers and Server boards: one stretch per
// workspace's reservations, its share's unused part faded, then free.

/** The current workspace's colour, then the others' in the order they appear. */
export const OWN_COLOUR = "bg-link";
export const OTHER_COLOURS = ["bg-fg/75", "bg-muted"];

/** A server's state in a few words, with its dot and text colour. */
export function online(srv: Pick<Server, "status" | "provisioned_at">): { label: string; dot: string; text: string; chip: string } {
  if (isOnline(srv)) return { label: "Online", dot: "bg-status-running", text: "text-status-running-fg", chip: "bg-status-running-soft text-status-running-fg" };
  if (srv.status === "active") return { label: "Needs provisioning", dot: "bg-status-pending", text: "text-status-pending-fg", chip: "bg-status-pending-soft text-status-pending-fg" };
  const label = statusLabel(srv.status);
  return { label: label.charAt(0).toUpperCase() + label.slice(1), dot: "bg-status-failed", text: "text-status-failed-fg", chip: "bg-status-failed-soft text-status-failed-fg" };
}

export function LegendItem({ swatch, label }: { swatch: string; label: string }) {
  return (
    <li className="inline-flex items-center gap-1.5">
      <span aria-hidden="true" className={cn("h-2.5 w-2.5 rounded-xs", swatch)} />
      {label}
    </li>
  );
}

export function Chip({ children, title }: { children: React.ReactNode; title?: string }) {
  return <span title={title} className="max-w-full truncate rounded-full border border-strong px-2 text-xs leading-[22px] text-fg">{children}</span>;
}

/** "Shared with acme, beta", or "Shared with 3 workspaces" with the names in
 *  its title and for screen readers. */
export function SharedChip({ names }: { names: string[] }) {
  const { text, full } = sharedWith(names);
  return (
    <Chip title={full}>
      {text === full ? text : <><span aria-hidden="true">{text}</span><span className="sr-only">{full}</span></>}
    </Chip>
  );
}

export function MeterHead({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col items-start gap-0.5 max-sm:flex-row max-sm:items-center max-sm:justify-between min-[1100px]:flex-row min-[1100px]:items-center min-[1100px]:justify-between">
      <span className="text-[13px] text-fg">{label}</span>
      <span className="font-mono text-xs text-fg">{value}</span>
    </div>
  );
}

/** One capacity meter (boards ReworkServers, ReworkServer): its label and
 *  value, the bar (aria says it in words) and a line under it. */
export function Meter({
  label,
  value,
  aria,
  segments,
  colourOf,
  sub,
}: {
  label: string;
  value: string;
  aria: string;
  segments: Segment[];
  colourOf: (orgId: string) => string;
  sub: (string | false)[];
}) {
  const text = sub.filter(Boolean).join(" · ");
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <MeterHead label={label} value={value} />
      <span role="img" aria-label={aria} className="flex h-2.5 overflow-hidden rounded-xs bg-subtle">
        {segments.map((g, i) => (
          <span key={i} className={cn(colourOf(g.orgId), g.setAside && "opacity-30")} style={{ width: `${g.pct}%` }} />
        ))}
      </span>
      {text && <span className={help}>{text}</span>}
    </div>
  );
}
