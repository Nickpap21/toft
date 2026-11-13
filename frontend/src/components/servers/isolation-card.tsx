"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { useState } from "react";
import { AlertTriangle, Check, Loader2, Minus, RotateCw, X } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { capabilityService, type IsolationLevel, type ServerCapabilities } from "@/lib/api/server.service";
import { capabilityFacts, levelRow } from "@/app/servers/[id]/server-view";
import { ago, formatDuration } from "@/lib/time";
import { cn, formatDateTime } from "@/lib/utils";
import { card, smallButton } from "@/components/settings/settings-frame";
import { useElapsed, type ProvisionView } from "./provision-run";
import { GvisorRunDetail, InstallGvisorDialog, useGvisorInstall } from "./gvisor-install";

const TONE: Record<IsolationLevel["state"], { dot: string; state: string; Icon: typeof Check }> = {
  available: { dot: "bg-status-running-soft text-status-running", state: "text-status-running-fg", Icon: Check },
  not_installed: { dot: "bg-status-pending-soft text-status-pending", state: "text-status-pending-fg", Icon: Minus },
  failing: { dot: "bg-status-failed-soft text-status-failed", state: "text-status-failed-fg", Icon: AlertTriangle },
  unavailable: { dot: "bg-raised text-muted", state: "text-muted", Icon: X },
};

const RUNNING = { dot: "bg-status-building-soft text-status-building", state: "text-status-building-fg" };

/** What a server can run and how walled off its services are (board:
 *  Server, isolation; D-93). Owners and developers check again; instance
 *  operators install gVisor (board: InstallGvisor; D-95). serverId null is
 *  the panel's own server, on Instance → Server, read and installed
 *  through its host helper (board: PanelIsolation; D-97); controlPlaneOnly
 *  is its role when no app runs there. */
export function IsolationCard({ serverId, serverName, canProbe, isOperator, controlPlaneOnly = false }: {
  serverId: string | null;
  serverName: string;
  canProbe: boolean;
  isOperator: boolean;
  controlPlaneOnly?: boolean;
}) {
  const panel = serverId === null;
  const queryClient = useQueryClient();
  const install = useGvisorInstall(serverId);
  const [confirming, setConfirming] = useState(false);
  const [fullLog, setFullLog] = useState(false);
  const queryKey = ["capabilities", serverId];
  const { data, error, isLoading, dataUpdatedAt } = useQuery({ queryKey, queryFn: () => capabilityService.get(serverId) });
  const probe = useMutation({
    mutationFn: () => capabilityService.probe(serverId),
    onSuccess: (c) => queryClient.setQueryData(queryKey, c),
    onError: (err) => toast.error(err instanceof ApiError && err.status < 500 ? err.message : "Failed to check the server"),
  });

  return (
    <section aria-labelledby="iso-h" className={card}>
      <div className={cn("flex flex-wrap justify-between gap-3 px-4", panel ? "items-start pb-3 pt-3.5" : "items-center py-3")}>
        {panel ? (
          <div className="flex flex-col gap-0.5">
            <h2 id="iso-h" className="text-[15px] font-semibold leading-[22px] text-fg">Isolation</h2>
            <p className="text-[13px] text-fg/70">
              {controlPlaneOnly
                ? "Apps deploy to your other servers; this server runs the panel only."
                : "How walled off the apps here are from the panel, the server and each other."}
            </p>
          </div>
        ) : (
          // The server page's Settings (board: ReworkServerSettings): the summary beside the title.
          <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
            <h2 id="iso-h" className="text-[15px] font-semibold leading-[22px] text-fg">Isolation</h2>
            <span className="text-xs text-muted">what this server can offer services</span>
          </div>
        )}
        <span className="inline-flex items-center gap-2 text-xs text-muted">
          {data && `${panel ? "Checked" : "checked"} ${ago(data.probed_at, dataUpdatedAt)}`}
          {canProbe && (
            <button type="button" className={cn(smallButton, "text-[13px] font-medium")} disabled={probe.isPending} onClick={() => probe.mutate()}>
              <RotateCw className={cn("h-[13px] w-[13px]", probe.isPending && "motion-safe:animate-spin")} aria-hidden="true" />
              {probe.isPending ? "Checking…" : data ? "Check again" : "Check now"}
            </button>
          )}
        </span>
      </div>

      {isLoading ? (
        <p className="border-t border-subtle px-4 py-3 text-[13px] text-muted">Loading…</p>
      ) : error ? (
        <p role="alert" className="border-t border-subtle px-4 py-3 text-[13px] text-status-failed-fg">What this server can run could not be loaded.</p>
      ) : !data ? (
        <p className="border-t border-subtle px-4 py-3 text-[13px] text-fg/70">
          Not checked yet. The check reads the kernel, Docker’s runtimes and its security, and runs a smoke test of each runtime.
        </p>
      ) : (
        <>
          {data.error && (
            <p role="alert" className="mx-4 mb-3 mt-3 flex items-start gap-2 rounded-[10px] bg-status-failed-soft px-3 py-2.5 text-[13px] text-status-failed-fg">
              <AlertTriangle className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
              <span className="min-w-0 break-words">{data.error}</span>
            </p>
          )}
          {data.levels.length > 0 && (
          <ul className="border-t border-subtle">
            {/* Dedicated is a server of a workspace's own; the panel's never is. */}
            {data.levels.filter((l) => !(panel && l.id === "dedicated")).map((l) => {
              if (l.id === "sandboxed_strict") {
                return controlPlaneOnly ? (
                  <LevelRow key={l.id} level={l} tone={TONE.unavailable} Icon={Minus} state="not needed here"
                    detail="Apps and previews run on your other servers, where it needs Toft’s agent. Change role to run apps here." />
                ) : (
                  <StrictRow key={l.id} level={l} sandboxed={data.levels.find((x) => x.id === "sandboxed")} panel={panel}
                    canInstall={isOperator && install.view?.outcome !== "running"} onInstall={() => setConfirming(true)} />
                );
              }
              if (l.id === "sandboxed") {
                return controlPlaneOnly ? (
                  <LevelRow key={l.id} level={l} tone={TONE.unavailable} Icon={Minus} state="not needed here"
                    detail="Apps and previews run on your other servers, where their own Server page installs gVisor. Change role to run apps here." />
                ) : (
                  <SandboxedRow key={l.id} level={l} caps={data} run={install.view} isOperator={isOperator} panel={panel}
                    onInstall={() => setConfirming(true)} fullLog={fullLog} onFullLog={setFullLog} />
                );
              }
              return (
                <LevelRow key={l.id} level={l} tone={TONE[l.state]} Icon={TONE[l.state].Icon} state={levelRow(l).state}
                  detail={
                    l.id !== "standard" || !panel
                      ? l.reason
                      : controlPlaneOnly
                        ? "The panel’s own containers run as they are; Standard applies to apps."
                        : l.reason.replace(/^Every service:/, "Every app here:")
                  } />
              );
            })}
          </ul>
          )}
          {data.kernel && (
            <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5 px-4 pb-3.5 pt-3 text-[13px]">
              {capabilityFacts(data).map((f) => (
                <div key={f.name} className="contents">
                  <dt className="text-muted">{f.name}</dt>
                  <dd className="flex flex-wrap items-center gap-1.5">
                    <span className={cn(f.mono && "font-mono")}>{f.value}</span>
                    <span className="text-muted">{f.note}</span>
                  </dd>
                </div>
              ))}
            </dl>
          )}
          {panel && !controlPlaneOnly && install.view?.outcome !== "running" && (
            <p className="border-t border-subtle px-4 py-3 text-xs leading-[17px] text-muted">
              {data.levels.some((l) => l.id === "sandboxed" && l.state === "available")
                ? "Services on the panel’s server can choose Sandboxed in Settings → Isolation, and a workspace can make it the default."
                : "Read through the host helper every day, after an upgrade, and on Check again. Servers apps run on have the same card on their own page."}
            </p>
          )}
          <InstallGvisorDialog open={confirming} onOpenChange={setConfirming} serverName={serverName} caps={data} panel={panel}
            onConfirmed={() => void install.start()} />
        </>
      )}
    </section>
  );
}

/** A level's state and why, with an action when it has one. */
function LevelRow({ level, tone, Icon, state, detail, action }: {
  level: IsolationLevel;
  tone: { dot: string; state: string };
  Icon: typeof Check;
  state: string;
  detail: string;
  action?: React.ReactNode;
}) {
  const row = levelRow(level);
  return (
    <li className="flex flex-wrap items-start gap-3 border-b border-subtle px-4 py-3">
      <span aria-hidden="true" className={cn("mt-px flex h-[18px] w-[18px] flex-none items-center justify-center rounded-full", tone.dot)}>
        <Icon className="h-[11px] w-[11px]" strokeWidth={3} />
      </span>
      <div className="flex min-w-0 flex-[1_1_240px] flex-col gap-0.5">
        <span className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
          <span className="text-[13px] font-semibold">{row.name}</span>
          <span className="font-mono text-xs text-muted">{row.runtime}</span>
          <span className={cn("text-xs", tone.state)}>{state}</span>
        </span>
        <span className="text-[13px] text-fg/70">{detail}</span>
      </div>
      {action}
    </li>
  );
}

/** Sandboxed with a strict network (board: ServerIsolation, D-115): it
 *  needs the agent, and a gVisor installed since it came. */
function StrictRow({ level, sandboxed, panel, canInstall, onInstall }: {
  level: IsolationLevel;
  sandboxed: IsolationLevel | undefined;
  panel: boolean;
  canInstall: boolean;
  onInstall: () => void;
}) {
  const gvisor = sandboxed && sandboxed.state !== "not_installed" && sandboxed.state !== "unavailable";
  // Unavailable with gVisor possible here: it is the agent that's missing,
  // which the panel's own server never runs (D-26).
  const needsAgent = !panel && level.state === "unavailable" && sandboxed?.state !== "unavailable";
  const reinstall = level.state === "not_installed" && gvisor;
  const tone = level.state === "not_installed" && !reinstall ? TONE.unavailable : TONE[level.state];
  const Icon = needsAgent || level.state === "not_installed" ? Minus : TONE[level.state].Icon;
  const action = reinstall || level.state === "failing" ? (
    canInstall ? (
      <span className="ml-[30px] flex flex-col items-start gap-1 sm:ml-0 sm:items-end">
        <button type="button" className={cn(smallButton, "text-[13px] font-medium")} onClick={onInstall}>Install gVisor again…</button>
        <span className="text-[11px] text-muted">Instance operators, with your password</span>
      </span>
    ) : undefined
  ) : undefined;
  return (
    <LevelRow level={level} tone={tone} Icon={Icon} action={action}
      state={needsAgent ? "needs the agent" : levelRow(level).state}
      detail={needsAgent
        ? `Toft’s agent answers the name lookups of gVisor’s own network stack, and this server connects over SSH. Move it to the agent under Connection, then install gVisor${gvisor ? " again" : ""}.`
        : panel && level.state === "unavailable" && sandboxed?.state !== "unavailable"
          ? "Needs Toft’s agent, and the panel’s own server runs without one."
          : level.reason} />
  );
}

/** The Sandboxed level, with gVisor's install when one runs or failed. */
function SandboxedRow({ level, caps, run, isOperator, panel, onInstall, fullLog, onFullLog }: {
  level: IsolationLevel;
  caps: ServerCapabilities;
  run: ProvisionView | null;
  isOperator: boolean;
  panel: boolean;
  onInstall: () => void;
  fullLog: boolean;
  onFullLog: (open: boolean) => void;
}) {
  const elapsed = useElapsed(run);
  const row = levelRow(level);
  const running = run?.outcome === "running";
  const failed = run?.outcome === "failed" && level.state !== "available";
  // A run that failed at the strict network's step leaves Sandboxed working:
  // its log stays here, and the strict row offers the install (D-115).
  const strict = caps.levels.find((l) => l.id === "sandboxed_strict")?.state;
  const strictFailed = run?.outcome === "failed" && level.state === "available" && strict !== "available" && strict !== "unavailable";
  const version = caps.runtimes.find((r) => r.name === "runsc")?.version;
  const tone = running ? RUNNING : failed ? TONE.failing : TONE[level.state];
  const Icon = failed ? X : TONE[level.state].Icon;
  const state = running ? "installing" : failed ? "install failed" : row.state;
  const started = running ? `Started by ${run.startedBy || "an operator"} at ${formatDateTime(run.startedAt).slice(11)}` : "";
  const detail = running
    ? panel
      ? "Through the host helper. The panel and the apps here aren’t touched."
      : `${started}. Running services aren’t touched.`
    : panel && level.state === "not_installed"
      ? "Each container gets its own kernel in user space. Previews opened with API keys run Sandboxed, so they can’t start here until it is installed."
      : level.reason;
  const meta = running
    ? panel ? `${started} · ${formatDuration(elapsed)}` : formatDuration(elapsed)
    : failed
      ? `Run by ${run.startedBy || "an operator"} · ${formatDateTime(run.startedAt)} · ${formatDuration(elapsed)}`
      : level.state === "available" && version
        ? `gVisor ${version}${run?.outcome === "ok" ? ` · installed by ${run.startedBy || "an operator"} · ${formatDateTime(run.startedAt)}` : ""}`
        : "";
  const canInstall = isOperator && !running && (failed || level.state === "not_installed" || level.state === "failing");
  return (
    <li className={cn("flex flex-col gap-3 border-b border-subtle px-4 py-3", running && "bg-status-building-soft/30")}>
      <div className="flex flex-wrap items-start gap-3">
        <span aria-hidden="true" className={cn("mt-px flex h-[18px] w-[18px] flex-none items-center justify-center rounded-full", tone.dot)}>
          {running ? <Loader2 className="h-[11px] w-[11px] motion-safe:animate-spin" strokeWidth={3} /> : <Icon className="h-[11px] w-[11px]" strokeWidth={3} />}
        </span>
        <div className="flex min-w-0 flex-[1_1_240px] flex-col gap-0.5">
          <span className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
            <span className="text-[13px] font-semibold">{row.name}</span>
            <span className="font-mono text-xs text-muted">{row.runtime}</span>
            <span className={cn("text-xs", tone.state)}>{state}</span>
          </span>
          <span className="text-[13px] text-fg/70">{detail}</span>
          {meta && <span className="font-mono text-[11px] text-muted">{meta}</span>}
          {!isOperator && level.state === "not_installed" && !running && (
            <span className="text-xs text-muted">An instance operator installs gVisor here.</span>
          )}
        </div>
        {(failed || strictFailed || canInstall) && (
          <span className="ml-[30px] flex flex-col items-start gap-1 sm:ml-0 sm:items-end">
            <span className="flex flex-wrap justify-end gap-1.5">
              {(failed || strictFailed) && <button type="button" className={cn(smallButton, "text-[13px] font-medium")} onClick={() => onFullLog(true)}>Full log</button>}
              {canInstall && (
                <button type="button" className={cn(smallButton, "text-[13px] font-medium")} onClick={onInstall}>
                  {failed ? "Try again…" : level.state === "failing" ? "Install again…" : "Install gVisor…"}
                </button>
              )}
            </span>
            {canInstall && !failed && <span className="text-[11px] text-muted">Instance operators, with your password</span>}
          </span>
        )}
      </div>
      {(running || failed || strictFailed) && run && <GvisorRunDetail view={run} caps={caps} fullLog={fullLog} onFullLog={onFullLog} />}
    </li>
  );
}
