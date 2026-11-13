"use client";

import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, X } from "lucide-react";
import { toast } from "sonner";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  cancelButton, dialogContent, dialogFooter, dialogHeader, dialogText, dialogTitle, fieldInput, fieldLabel, help, primaryButton,
} from "@/components/settings/settings-frame";
import { reauthError } from "@/components/reauth";
import { ApiError, reauthProof } from "@/lib/api/client";
import { gvisorService, serverService, type ServerCapabilities } from "@/lib/api/server.service";
import { useAuth } from "@/context/AuthContext";
import { GVISOR_STEPS, gvisorLeft, gvisorSteps, type GvisorStepState } from "@/app/servers/[id]/server-view";
import { ProvisionLog, type ProvisionView } from "./provision-run";
import { cn, formatDateTime } from "@/lib/utils";

// Installing gVisor from the Isolation card (board: InstallGvisor, D-95):
// the confirm dialog with the password, then the six steps and the log in
// the Sandboxed row. The run is this page's stream, or else the one the
// panel recorded, polled while it runs elsewhere.

/** The release Toft installs; the backend pins it (server.GvisorRelease). */
export const GVISOR_RELEASE = "20261012";

/** Follows the server's gVisor install: start() streams a new one. */
export function useGvisorInstall(serverId: string | null) {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const ctrl = useRef<AbortController | null>(null);
  const [live, setLive] = useState<ProvisionView | null>(null);
  const recorded = useQuery({
    queryKey: ["gvisor-run", serverId],
    queryFn: () => gvisorService.last(serverId),
    refetchInterval: (q) => (q.state.data && !q.state.data.finished_at ? 3000 : false),
  });
  useEffect(() => () => ctrl.current?.abort(), []);

  async function start() {
    const c = new AbortController();
    ctrl.current = c;
    setLive({ lines: [], outcome: "running", startedAt: Date.now(), finishedAt: null, startedBy: user?.name ?? "", error: "" });
    try {
      await gvisorService.install(serverId, (line) => setLive((v) => (v ? { ...v, lines: [...v.lines, line] } : v)), c);
      setLive((v) => (v ? { ...v, outcome: "ok", finishedAt: Date.now() } : v));
      toast.success("gVisor installed");
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : (err as Error).message || "The install failed";
      if (msg === "Stopped watching.") setLive(null);
      else setLive((v) => (v ? { ...v, outcome: "failed", finishedAt: Date.now(), error: msg } : v));
    } finally {
      ctrl.current = null;
      queryClient.invalidateQueries({ queryKey: ["gvisor-run", serverId] });
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
        }
      : null);
  return { view, start, watching: !!live && live.outcome === "running", stopWatching: () => ctrl.current?.abort() };
}

/** Confirm, with the password unless this tab confirmed it lately. panel
 *  is the panel's own server (board PanelIsolation, D-97). */
export function InstallGvisorDialog({ open, onOpenChange, serverName, caps, onConfirmed, panel = false }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  serverName: string;
  caps: ServerCapabilities;
  onConfirmed: () => void;
  panel?: boolean;
}) {
  const [password, setPassword] = useState("");
  const needsPassword = open && !reauthProof();
  const confirm = useMutation({
    mutationFn: async () => {
      if (needsPassword) await serverService.confirmPassword(password);
    },
    onSuccess: () => {
      setPassword("");
      onOpenChange(false);
      onConfirmed();
    },
  });
  const bareMetal = caps.kvm && caps.virtualization === "none";
  // Over SSH there is no agent to answer names: step 6 is skipped (D-115).
  const strictHere = !panel && caps.levels.find((l) => l.id === "sandboxed_strict")?.state !== "unavailable";
  return (
    <Dialog open={open} onOpenChange={(o) => { if (!confirm.isPending) { onOpenChange(o); confirm.reset(); } }}>
      <DialogContent className={cn(dialogContent, "max-w-[560px]")}>
        <form onSubmit={(e) => { e.preventDefault(); confirm.mutate(); }}>
          <DialogHeader className={dialogHeader}>
            <DialogTitle className={dialogTitle}>Install gVisor on {serverName}?</DialogTitle>
            <DialogDescription className={dialogText}>
              {panel
                ? "Lets apps and previews here run Sandboxed: each container gets its own kernel in user space. The panel and everything running here keep running."
                : `Lets services here run Sandboxed${strictHere ? ", and Sandboxed with a strict network" : ""}: each container gets its own kernel in user space. Nothing that runs here now changes.`}
            </DialogDescription>
          </DialogHeader>
          <ol className="mx-5 mt-3.5 flex list-decimal flex-col gap-1.5 pl-5 text-[13px] leading-[19px] text-fg/80">
            <li>Adds gVisor’s signed apt repository, pinned to release <span className="font-mono text-xs">{GVISOR_RELEASE}</span>.</li>
            <li>Installs <span className="font-mono text-xs">runsc {GVISOR_RELEASE}.0</span> with apt, about 100 MB.</li>
            <li>
              Registers it with Docker twice, as <span className="font-mono text-xs">runsc</span> and <span className="font-mono text-xs">runsc-strict</span>, by a reload.{" "}
              {panel ? "The panel’s containers and your apps keep running." : "Running containers keep running."}
            </li>
            <li>Starts a sandboxed test container and checks it finds another container by name.</li>
            {strictHere && <li>Lets containers reach Toft’s agent for names, then checks a strict test container finds another by name and the health probe reaches it.</li>}
          </ol>
          <p className="mx-5 mt-3 rounded-lg border border-subtle bg-ground px-2.5 py-2 text-xs leading-[18px] text-fg/70">
            {panel && "Runs on the host through the panel’s host helper, as upgrades do. "}
            {bareMetal ? (
              <>This server has <span className="font-mono">/dev/kvm</span> and no hypervisor, so gVisor uses its KVM platform.</>
            ) : (
              <>This server {caps.kvm ? "is a virtual machine" : <>has no <span className="font-mono">/dev/kvm</span></>}, so gVisor uses systrap.</>
            )}
            {!panel && " Databases and small requests run slower under it."}
          </p>
          {needsPassword && (
            <div className="mx-5 mt-4 flex flex-col gap-1.5">
              <label htmlFor="gv-pw" className={fieldLabel}>Your password</label>
              <input id="gv-pw" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)}
                aria-describedby="gv-pw-help" className={fieldInput} required />
              <span id="gv-pw-help" className={help}>Good for 10 minutes in this tab.</span>
            </div>
          )}
          {confirm.error && (
            <p role="alert" className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">{reauthError(confirm.error)}</p>
          )}
          <div className={dialogFooter}>
            <button type="button" className={cancelButton} onClick={() => onOpenChange(false)} disabled={confirm.isPending}>Cancel</button>
            <button type="submit" className={primaryButton} disabled={confirm.isPending || (needsPassword && !password)}>
              {confirm.isPending ? "Confirming…" : "Install gVisor"}
            </button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

const BAR: Record<GvisorStepState, string> = {
  done: "bg-status-running",
  now: "bg-status-building",
  failed: "bg-status-failed",
  todo: "bg-subtle",
};

/** The six steps, as the board draws them. */
export function GvisorSteps({ states, notes }: { states: GvisorStepState[]; notes: Partial<Record<number, string>> }) {
  return (
    <ol aria-label="Install steps" className="grid grid-cols-[repeat(auto-fit,minmax(88px,1fr))] gap-2.5">
      {GVISOR_STEPS.map((s, i) => {
        const st = states[i];
        return (
          <li key={s.label} aria-current={st === "now" ? "step" : undefined} className="flex flex-col gap-1.5">
            <span className={cn("h-[3px] rounded-sm", BAR[st])} />
            <span className={cn("flex items-center gap-1.5 text-[13px]", st === "todo" ? "text-muted" : "text-fg")}>
              {st === "done" ? <Check className="h-[13px] w-[13px]" strokeWidth={2.4} aria-hidden="true" />
                : st === "failed" ? <X className="h-[13px] w-[13px]" strokeWidth={2.4} aria-hidden="true" />
                : <span aria-hidden="true" className="w-[13px] text-center">·</span>}
              {s.label}
              <span className="sr-only">{st === "done" ? ": done" : st === "now" ? ": running" : st === "failed" ? ": failed" : ": to do"}</span>
            </span>
            <span className="font-mono text-[11px] text-muted">{notes[i] ?? s.note}</span>
          </li>
        );
      })}
    </ol>
  );
}

/** The steps, the error and the log of a run, under the Sandboxed row. */
export function GvisorRunDetail({ view, caps, fullLog, onFullLog }: {
  view: ProvisionView;
  caps: ServerCapabilities;
  fullLog: boolean;
  onFullLog: (open: boolean) => void;
}) {
  const states = gvisorSteps(view.lines, view.outcome);
  const notes: Partial<Record<number, string>> = {
    0: [caps.arch === "aarch64" ? "arm64" : caps.arch === "x86_64" ? "amd64" : caps.arch, "apt"].filter(Boolean).join(" · "),
    1: GVISOR_RELEASE,
    2: `${GVISOR_RELEASE}.0`,
  };
  // Over SSH, and on the panel's own server, there is no agent to test.
  if (view.lines.some((l) => l.startsWith("  · Skipped"))) notes[5] = "skipped";
  const failed = states.indexOf("failed");
  if (failed >= 0) notes[failed] = "failed";
  // "Install runsc failed: apt-get install …: exit status 100."
  const step = failed === 5 ? "The strict network’s test" : failed >= 0 ? GVISOR_STEPS[failed].label : "The install";
  const error = view.error ? `${step} failed: ${view.error}${view.error.endsWith(".") ? "" : "."}` : "";
  const log = useRef<HTMLPreElement>(null);
  useEffect(() => {
    if (log.current) log.current.scrollTop = log.current.scrollHeight;
  }, [view.lines.length]);
  return (
    <div className="ml-[30px] flex flex-col gap-3">
      <GvisorSteps states={states} notes={notes} />
      {view.outcome === "failed" && (
        <div role="alert" className="rounded-lg border border-status-failed/35 bg-status-failed-soft px-3 py-2.5 text-[13px] leading-[19px] text-fg">
          {error} {gvisorLeft(states)}
        </div>
      )}
      {view.lines.length > 0 && (
        <pre ref={log} role="log" aria-label="Install log" tabIndex={0} aria-live={view.outcome === "running" ? "polite" : undefined}
          className="max-h-[180px] overflow-auto whitespace-pre-wrap rounded-lg border border-subtle bg-ground px-3 py-2.5 font-mono text-xs leading-[19px] text-fg/80">
          {view.lines.join("\n")}
        </pre>
      )}
      <Dialog open={fullLog} onOpenChange={onFullLog}>
        <DialogContent className={cn(dialogContent, "max-w-[960px]")}>
          <DialogHeader className={dialogHeader}>
            <DialogTitle className={dialogTitle}>The gVisor install’s log</DialogTitle>
            <DialogDescription className={dialogText}>Run by {view.startedBy || "someone"}, {formatDateTime(view.startedAt)}.</DialogDescription>
          </DialogHeader>
          <div className="p-5 pt-3"><ProvisionLog lines={view.lines} live={false} /></div>
        </DialogContent>
      </Dialog>
    </div>
  );
}

