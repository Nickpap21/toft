import { cn, initials } from "@/lib/utils";
import { td } from "@/components/settings/settings-frame";

/** An account in a table: initials, name, "(you)", email. */
export function Person({ name, email, you, muted }: { name: string; email: string; you: boolean; muted?: boolean }) {
  return (
    <span className="flex items-center gap-2.5">
      <span
        aria-hidden="true"
        className={cn(
          "flex h-7 w-7 flex-none items-center justify-center rounded-full bg-raised text-xs font-semibold",
          muted ? "text-muted" : "text-fg",
        )}
      >
        {initials(name)}
      </span>
      <span className="flex min-w-0 flex-col">
        <span className={cn("font-medium", muted ? "text-fg/70" : "text-fg")}>
          {name}
          {you && <span className="font-normal text-muted"> (you)</span>}
        </span>
        <span className="font-mono text-xs leading-5 text-muted [overflow-wrap:anywhere]">{email}</span>
      </span>
    </span>
  );
}

export function LoadingRow({ cols }: { cols: number }) {
  return (
    <tr aria-busy="true">
      <td colSpan={cols} className={cn(td, "py-3.5")}>
        <span className="flex flex-col gap-2.5 motion-safe:animate-pulse" aria-label="Loading">
          <span className="block h-2.5 w-[40%] rounded bg-raised" />
          <span className="block h-2.5 w-[60%] rounded bg-raised" />
        </span>
      </td>
    </tr>
  );
}
