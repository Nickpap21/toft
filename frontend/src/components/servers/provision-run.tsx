"use client";

import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check } from "lucide-react";
import { toast } from "sonner";
import { serverService } from "@/lib/api/server.service";
import { useAuth } from "@/context/AuthContext";
import { cn } from "@/lib/utils";
import { lineTone, stagesOf, type LineTone, type Stage } from "./provisioning";

// A server's provisioning as the Server and AddServer boards draw it: five
// stages with what each found, and the numbered log. The run is the one
// this page streams, or else the last one the panel recorded (D-48), which
// is polled while it runs elsewhere.

export type Outcome = "running" | "ok" | "failed";

export interface ProvisionView {
  lines: string[];
  outcome: Outcome;
  startedAt: number;
  finishedAt: number | null;
  startedBy: string;
  error: string;
  /** What a provisioning run that ended ok left out (ProvisionRun.warnings). */
  warnings?: string[];
}

/** Follows provisioning of serverId: start() streams a new run; otherwise
 *  the recorded one. */
export function useProvisioning(serverId: string, enabled = true) {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const ctrl = useRef<AbortController | null>(null);
  const [live, setLive] = useState<ProvisionView | null>(null);

  const recorded = useQuery({
    queryKey: ["provision-run", serverId],
    queryFn: () => serverService.lastProvision(serverId),
    enabled: enabled && !!serverId,
    refetchInterval: (q) => (q.state.data && !q.state.data.finished_at ? 3000 : false),
  });

  useEffect(() => () => ctrl.current?.abort(), []);

  async function start() {
    const c = new AbortController();
    ctrl.current = c;
    const begun = Date.now();
    setLive({ lines: [], outcome: "running", startedAt: begun, finishedAt: null, startedBy: user?.name ?? "", error: "", warnings: [] });
    try {
      await serverService.provision(serverId, (line) => setLive((v) => (v ? { ...v, lines: [...v.lines, line] } : v)), c);
      // The stream ends the same way with or without warnings: the run
      // the panel recorded says which.
      const warnings = (await serverService.lastProvision(serverId).catch(() => null))?.warnings ?? [];
      setLive((v) => (v ? { ...v, outcome: "ok", finishedAt: Date.now(), warnings } : v));
      if (warnings.length) toast.warning("Server provisioned with warnings");
      else toast.success("Server provisioned");
    } catch (err) {
      const msg = (err as Error).message || "Provisioning failed";
      if (msg === "Provisioning cancelled.") {
        // Stopped watching: the run carries on; the recorded one shows it.
        setLive(null);
      } else {
        setLive((v) => (v ? { ...v, outcome: "failed", finishedAt: Date.now(), error: msg } : v));
      }
    } finally {
      ctrl.current = null;
      queryClient.invalidateQueries({ queryKey: ["provision-run", serverId] });
      queryClient.invalidateQueries({ queryKey: ["server", serverId] });
      queryClient.invalidateQueries({ queryKey: ["servers"] });
      queryClient.invalidateQueries({ queryKey: ["server-capacity", serverId] });
      // Provisioning probes again: the Firewall card's address pool.
      queryClient.invalidateQueries({ queryKey: ["capabilities", serverId] });
    }
  }

  const r = recorded.data;
  const view: ProvisionView | null =
    live ??
    (r
      ? {
          lines: r.log ? r.log.replace(/\n$/, "").split("\n") : [],
          outcome: !r.finished_at ? "running" : r.ok ? "ok" : "failed",
          startedAt: new Date(r.started_at).getTime(),
          finishedAt: r.finished_at ? new Date(r.finished_at).getTime() : null,
          startedBy: r.started_by,
          error: r.error,
          warnings: r.warnings ?? [],
        }
      : null);

  return {
    view,
    loading: recorded.isLoading,
    /** This page streams the run (Stop watching applies). */
    watching: !!live && live.outcome === "running",
    start,
    stopWatching: () => ctrl.current?.abort(),
  };
}

/** Seconds since start, ticking while it runs. */
export function useElapsed(view: ProvisionView | null): number {
  const [now, setNow] = useState(() => Date.now());
  const running = view?.outcome === "running";
  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [running]);
  if (!view) return 0;
  return Math.max(0, ((view.finishedAt ?? now) - view.startedAt) / 1000);
}

const BAR: Record<Stage["state"], string> = {
  done: "bg-status-running",
  now: "bg-[linear-gradient(90deg,var(--status-building)_55%,var(--border)_55%)]",
  todo: "bg-subtle",
  failed: "bg-status-failed",
};
const LABEL: Record<Stage["state"], string> = {
  done: "text-fg",
  now: "font-medium text-status-building-fg",
  todo: "text-muted",
  failed: "font-medium text-status-failed-fg",
};

export function Stages({ stages }: { stages: Stage[] }) {
  return (
    <ol aria-label="Provisioning stages" className="grid grid-cols-[repeat(auto-fit,minmax(88px,1fr))] gap-x-2 gap-y-3 max-sm:grid-cols-1 max-sm:gap-0">
      {stages.map((st) => (
        <li
          key={st.id}
          aria-current={st.state === "now" ? "step" : undefined}
          className="flex flex-col gap-1.5 max-sm:min-h-9 max-sm:flex-row max-sm:items-center max-sm:justify-between max-sm:gap-3 max-sm:border-t max-sm:border-subtle max-sm:first:border-t-0"
        >
          <span className={cn("h-[3px] rounded-sm max-sm:hidden", BAR[st.state])} />
          <span className={cn("flex items-center gap-1.5 text-[13px]", LABEL[st.state])}>
            {st.state === "done" && <Check className="h-3.5 w-3.5 text-status-running" aria-hidden="true" />}
            {st.label}
            <span className="sr-only">
              {st.state === "done" ? ": done" : st.state === "now" ? ": running" : st.state === "failed" ? ": failed" : ": to do"}
            </span>
          </span>
          <span className="font-mono text-[11px] text-muted">{st.sub}</span>
        </li>
      ))}
    </ol>
  );
}

const TONE: Record<LineTone, string> = {
  text: "text-fg/85",
  muted: "text-muted",
  ok: "text-status-running-fg",
  warn: "text-status-pending-fg",
  error: "text-status-failed-fg",
};

/** The numbered log; it follows new lines while live, unless the reader
 *  scrolled up. */
export function ProvisionLog({ lines, live, className }: { lines: string[]; live: boolean; className?: string }) {
  const box = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  useEffect(() => {
    const el = box.current;
    if (el && live && pinned.current) el.scrollTop = el.scrollHeight;
  }, [lines.length, live]);
  return (
    <div
      ref={box}
      role="log"
      aria-label="Provisioning log"
      aria-live={live ? "polite" : undefined}
      tabIndex={0}
      onScroll={(e) => {
        const el = e.currentTarget;
        pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
      }}
      className={cn("max-h-[420px] overflow-y-auto rounded-lg border border-subtle bg-ground py-2", className)}
    >
      {lines.map((l, i) => (
        <div key={i} className="flex gap-3.5 px-3.5 py-px font-mono text-xs leading-5">
          <span aria-hidden="true" className="flex-none text-muted">{String(i + 1).padStart(2, "0")}</span>
          <span className={cn("min-w-0 whitespace-pre-wrap break-words", TONE[lineTone(l)])}>{l}</span>
        </div>
      ))}
      {live && (
        <div className="flex gap-3.5 px-3.5 py-px font-mono text-xs leading-5" aria-hidden="true">
          <span className="flex-none text-muted">{String(lines.length + 1).padStart(2, "0")}</span>
          <span className="my-[3px] w-2 bg-status-building motion-safe:animate-pulse" />
        </div>
      )}
    </div>
  );
}

export { stagesOf };
