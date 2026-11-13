"use client";

import { useEffect, useState } from "react";
import { X } from "lucide-react";
import { panelPtyWsUrl } from "@/lib/api/instance.service";
import { PtyTerminal, type PtyState } from "@/components/ui/pty-terminal";
import { ToftMark } from "@/components/shell/toft-mark";

/** A root shell on the panel's own server (D-68), in a drawer along the
 *  bottom as a server's host shell opens. Escape or × closes it, which ends
 *  the shell. */
export function PanelShellDrawer({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const [pty, setPty] = useState<PtyState>({ connected: false, error: null });

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      // xterm keeps Escape for the shell; only close from outside it.
      if (
        e.key === "Escape" &&
        !(e.target as HTMLElement | null)?.closest(".xterm")
      )
        onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!open) return null;
  return (
    <>
      <div
        className="fixed inset-0 z-40 bg-scrim/20 backdrop-blur-[1px]"
        onClick={onClose}
        aria-hidden="true"
      />
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Shell on the panel’s server"
        className="fixed inset-x-0 bottom-0 z-50 flex h-[60vh] flex-col border-t border-subtle bg-surface shadow-dialog"
      >
        <div className="flex shrink-0 items-center gap-3 border-b border-subtle px-4 py-2.5">
          <ToftMark className="h-4 w-4 text-fg" />
          <span className="text-sm font-medium text-fg">
            The panel’s server
          </span>
          <span className="hidden text-xs text-muted sm:inline">
            root · written to the audit log
          </span>
          <span
            className={`ml-auto flex items-center gap-1.5 text-xs ${pty.connected ? "text-status-running-fg" : "text-muted"}`}
          >
            <span
              aria-hidden="true"
              className={`h-1.5 w-1.5 rounded-full ${pty.connected ? "bg-status-running" : "bg-strong"}`}
            />
            {pty.connected
              ? "connected"
              : pty.error
                ? "not connected"
                : pty.closed
                  ? "disconnected"
                  : "connecting…"}
          </span>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close the shell"
            className="rounded p-1 text-muted hover:text-fg"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
        <PtyTerminal
          url={panelPtyWsUrl}
          retryHint="Open Host shell again to confirm it."
          onState={setPty}
        />
      </div>
    </>
  );
}
