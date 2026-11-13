"use client";

import { useEffect, useRef, useState } from "react";
import { streamEvents } from "@/lib/api/client";
import { panelLogsPath } from "@/lib/api/instance.service";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  dialogHeader,
  dialogText,
  dialogTitle,
} from "@/components/settings/settings-frame";

/** Lines kept on screen; older ones scroll away. */
const KEEP = 2000;

/** One of the panel's containers' logs: the last 200 lines, then live, until
 *  the dialog closes (D-68). */
export function PanelLogsDialog({
  container,
  onClose,
}: {
  container: string | null;
  onClose: () => void;
}) {
  return (
    <Dialog open={!!container} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-[min(1100px,calc(100vw-32px))] gap-0 rounded-xl p-0">
        <DialogHeader className={dialogHeader}>
          <DialogTitle className={dialogTitle}>
            <span className="font-mono text-[15px]">{container}</span>
          </DialogTitle>
          <DialogDescription className={dialogText}>
            The last 200 lines, then live. Times are UTC.
          </DialogDescription>
        </DialogHeader>
        {container && <Follow key={container} container={container} />}
      </DialogContent>
    </Dialog>
  );
}

function Follow({ container }: { container: string }) {
  const [lines, setLines] = useState<string[]>([]);
  const [state, setState] = useState<"following" | "ended" | "error">(
    "following",
  );
  const [error, setError] = useState("");
  const box = useRef<HTMLPreElement>(null);
  const stick = useRef(true);

  useEffect(() => {
    const ctrl = new AbortController();
    streamEvents(
      panelLogsPath(container),
      (type, data) => {
        if (type === "error") {
          setState("error");
          setError(data);
        } else if (type === "done") {
          setState((s) => (s === "error" ? s : "ended"));
        } else {
          setLines((l) =>
            l.length >= KEEP
              ? [...l.slice(l.length - KEEP + 1), data]
              : [...l, data],
          );
        }
      },
      ctrl.signal,
    ).catch((e: Error) => {
      if (ctrl.signal.aborted) return;
      setState("error");
      setError(e.message);
    });
    return () => ctrl.abort();
  }, [container]);

  // Follow the end, unless the reader scrolled up.
  useEffect(() => {
    const el = box.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [lines]);

  return (
    <div className="flex flex-col gap-2 px-5 pb-5 pt-3">
      <pre
        ref={box}
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
        }}
        role="group"
        tabIndex={0}
        aria-label={`${container} log`}
        className="h-[min(60vh,560px)] overflow-auto whitespace-pre-wrap break-words rounded-lg border border-subtle bg-ground px-3.5 py-3 font-mono text-xs leading-[19px] text-fg/85"
      >
        {lines.length === 0 && state === "following" ? (
          <span className="text-muted">Waiting for the log…</span>
        ) : (
          lines.join("\n")
        )}
      </pre>
      <p role="status" className="text-xs text-muted">
        {state === "following"
          ? `Following · ${lines.length} ${lines.length === 1 ? "line" : "lines"}`
          : state === "ended"
            ? "The container stopped writing: the stream ended."
            : `The log could not be followed: ${error}`}
      </p>
    </div>
  );
}
