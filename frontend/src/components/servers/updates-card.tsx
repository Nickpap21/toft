"use client";

import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Check, CheckCircle2, Info, Loader2, RotateCw, X, XCircle } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  cancelButton, card, dialogContent, dialogFooter, dialogHeader, dialogText, dialogTitle, fieldInput, fieldLabel, help, primaryButton, smallButton,
} from "@/components/settings/settings-frame";
import { btnSm, h2, primarySm, sum } from "@/app/projects/_lib/ui";
import { reauthError } from "@/components/reauth";
import { ApiError, reauthProof } from "@/lib/api/client";
import { serverService, updatesService, type DockerAdvisory, type HostUpdates, type UpdateAction, type UpdateRun } from "@/lib/api/server.service";
import { sinceShort } from "@/lib/time";
import { cn, formatDateTime } from "@/lib/utils";
import { advisoriesSummary, confirmCopy, dockerUpdatable, installCount, listsFrom, packagesSummary, runView, updateRows, type Tone } from "./updates-view";

// The board's rows: an icon in the status colour, named for screen readers.
const TONE: Record<Tone, { tint: string; name: string; Icon: typeof Check }> = {
  ok: { tint: "text-status-running", name: "Fine", Icon: CheckCircle2 },
  warn: { tint: "text-status-pending", name: "Warning", Icon: AlertTriangle },
  bad: { tint: "text-status-failed", name: "Problem", Icon: XCircle },
  info: { tint: "text-muted", name: "Note", Icon: Info },
};

// A table at 640 px and up, a card per row below (the board's .lt).
const th = "border-b border-subtle px-4 py-2 text-left text-xs font-medium text-muted";
const tr = "max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1 max-sm:border-t max-sm:border-subtle max-sm:px-4 max-sm:py-3 max-sm:first:border-t-0";
const td = "border-t border-subtle px-4 py-2.5 align-middle [tr:first-child>&]:border-t-0 max-sm:block max-sm:border-0 max-sm:p-0";
const subhead = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 pb-2 pt-3";
const chip = "inline-flex rounded px-1.5 text-[11px] font-semibold leading-[18px]";
/** Pending packages shown before Show all. */
const FIRST = 6;

const RUN_TONE = {
  running: { dot: "bg-status-building-soft text-status-building", state: "text-status-building-fg", row: "bg-status-building-soft/25" },
  ok: { dot: "bg-status-running-soft text-status-running", state: "text-status-running-fg", row: "" },
  bad: { dot: "bg-status-failed-soft text-status-failed", state: "text-status-failed-fg", row: "" },
};

const SEVERITY: Record<DockerAdvisory["severity"], string> = {
  critical: "bg-status-failed-soft text-status-failed-fg",
  high: "bg-status-failed-soft text-status-failed-fg",
  medium: "bg-status-pending-soft text-status-pending-fg",
  low: "bg-raised text-fg/70",
};

const active = (r: UpdateRun | null | undefined) => !!r && (r.outcome === "running" || r.outcome === "rebooting");

/** A server's pending updates, reboot, Docker and unattended-upgrades, read
 *  every 6 hours (board: ServerUpdates; D-133), and the runs that install
 *  updates, update Docker or reboot (board: ServerUpdateActions; D-134).
 *  serverId null is the panel's own server, on Instance → Server, reached
 *  through its host helper; agent: a server reached through its agent;
 *  canCheck reads again now, canRun runs (owners
 *  and operators on a server, operators on the panel's); services are what
 *  runs there, named in the confirmations. */
export function UpdatesCard({ serverId, serverName, agent = false, canCheck, canRun, services = [], accent = true }: {
  serverId: string | null;
  serverName?: string;
  agent?: boolean;
  canCheck: boolean;
  canRun: boolean;
  services?: string[];
  /** Install as the accent button; false where the page has its own (Instance's Upgrade, D-139). */
  accent?: boolean;
}) {
  const queryClient = useQueryClient();
  const queryKey = ["updates", serverId];
  const { data, error, isLoading, dataUpdatedAt } = useQuery({ queryKey, queryFn: () => updatesService.get(serverId) });
  const check = useMutation({
    mutationFn: () => updatesService.check(serverId),
    onSuccess: (u) => queryClient.setQueryData(queryKey, u),
    onError: (err) => toast.error(err instanceof ApiError && err.status < 500 ? err.message : "Failed to read the updates"),
  });
  const runKey = ["update-run", serverId];
  const run = useQuery({
    queryKey: runKey,
    queryFn: () => updatesService.run(serverId),
    // The host restarts under a Docker update or a reboot: keep asking.
    refetchInterval: (q) => (active(q.state.data) ? 2000 : false),
    retry: (n) => n < 2 || active(queryClient.getQueryData<UpdateRun | null>(runKey)),
    retryDelay: 3000,
  });
  // When a run ends the backend reads the updates again in a few seconds.
  const wasActive = useRef(false);
  const runData = run.data;
  useEffect(() => {
    const now = active(runData);
    if (wasActive.current && !now) {
      const refresh = () => queryClient.invalidateQueries({ queryKey: ["updates", serverId] });
      refresh();
      const t = setTimeout(refresh, 15000);
      wasActive.current = now;
      return () => clearTimeout(t);
    }
    wasActive.current = now;
  }, [runData, queryClient, serverId]);
  const [asking, setAsking] = useState<UpdateAction | null>(null);
  const busy = active(runData);
  // On the panel's own server, Docker's restart or the reboot takes Toft
  // down with it: the run can't be read until the panel is back.
  // Retries go on while a run lasts, so the query never errors: count them.
  const panelAway = serverId === null && busy && runData?.action !== "install" && run.failureCount > 0;

  return (
    <section aria-labelledby="upd-h" className={card}>
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <h2 id="upd-h" className={h2}>Updates</h2>
          {data && <span className={sum}>{[`checked ${sinceShort(data.checked_at, dataUpdatedAt)}`, listsFrom(data)].filter(Boolean).join(" · ")}</span>}
        </div>
        {canCheck && (
          <button type="button" className={btnSm} disabled={check.isPending || busy} onClick={() => check.mutate()}>
            <RotateCw className={cn("h-[13px] w-[13px]", check.isPending && "motion-safe:animate-spin")} aria-hidden="true" />
            {check.isPending ? "Checking…" : data ? "Check again" : "Check now"}
          </button>
        )}
      </div>

      {panelAway && (
        <div role="status" className="mx-4 my-3 flex items-start gap-3 rounded-[10px] border border-status-building/35 bg-status-building-soft px-4 py-3.5">
          <Loader2 className="mt-0.5 h-4 w-4 flex-none text-status-building motion-safe:animate-spin" aria-hidden="true" />
          <div className="flex flex-col gap-0.5">
            <span className="text-[13px] font-semibold">The panel is restarting with its server</span>
            <span className="text-[13px] text-fg/70">
              {runData?.action === "reboot" ? "The server is rebooting" : "Docker restarts the panel’s containers"}, so Toft is out of reach for a moment. This page reconnects by itself and shows how the run ended. Nothing to do here.
            </span>
          </div>
        </div>
      )}

      {isLoading ? (
        <p className="px-4 py-3 text-[13px] text-muted">Loading…</p>
      ) : error && !data ? (
        <p role="alert" className="px-4 py-3 text-[13px] text-status-failed-fg">
          The updates could not be loaded{error instanceof ApiError && error.status < 500 ? `: ${error.message}` : "."}
        </p>
      ) : !data ? (
        <p className="px-4 py-3 text-[13px] text-fg/70">
          Not read yet. The read lists what apt would install, whether a reboot waits and the Docker engine running, every 6 hours.
        </p>
      ) : (
        <Report data={data} run={runData ?? null} canRun={canRun && !busy} onRun={setAsking} accent={accent} />
      )}
      {data && asking && (
        <UpdateRunDialog
          action={asking}
          focusAfter="upd-run"
          updates={data}
          where={serverId === null ? null : (serverName ?? "this server")}
          services={services}
          agent={agent}
          onClose={() => setAsking(null)}
          onStarted={(r) => {
            setAsking(null);
            queryClient.setQueryData(runKey, r);
          }}
          serverId={serverId}
        />
      )}
    </section>
  );
}

function Report({ data, run, canRun, onRun, accent }: {
  data: HostUpdates;
  run: UpdateRun | null;
  canRun: boolean;
  onRun: (a: UpdateAction) => void;
  accent: boolean;
}) {
  const [all, setAll] = useState(false);
  // unknown until a read works: then only its error shows.
  const read = data.unattended.state !== "unknown";
  const pending = data.security + data.other;
  const installs = installCount(data);
  const advisories = data.docker.advisories;
  const packages = all ? data.packages : data.packages.slice(0, FIRST);
  const button = (key: string): { label: string; action: UpdateAction } | null => {
    if (!canRun) return null;
    if (key === "reboot" && data.reboot.required) return { label: "Reboot…", action: "reboot" };
    if (key === "docker" && dockerUpdatable(data)) return { label: "Update Docker…", action: "docker" };
    return null;
  };
  return (
    <>
      {data.error && (
        <p className="mx-4 my-3 flex items-start gap-2 rounded-[10px] bg-status-failed-soft px-3 py-2.5 text-[13px] text-status-failed-fg">
          <AlertTriangle className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
          <span className="min-w-0 break-words">
            {/[.!?]$/.test(data.error) ? data.error : `${data.error}.`}
            {read && data.read_at && ` What follows is from the last read that worked, ${formatDateTime(data.read_at)}.`}
          </span>
        </p>
      )}
      {(read || run) && (
        <ul>
          {run && <RunRow run={run} canRetry={canRun} onRetry={() => onRun(run.action)} />}
          {read &&
            updateRows(data).map((r) => {
              const tone = TONE[r.tone];
              const b = button(r.key);
              return (
                <li key={r.key} className="flex flex-wrap items-start gap-x-3 gap-y-2 border-t border-subtle px-4 py-3 first:border-t-0">
                  <tone.Icon role="img" aria-label={tone.name} className={cn("mt-0.5 h-4 w-4 flex-none", tone.tint)} strokeWidth={2} />
                  <div className="flex min-w-0 flex-[1_1_260px] flex-col">
                    <span className="text-sm font-medium">{r.title}</span>
                    <span className="text-[13px] leading-[19px] text-muted">{r.detail}</span>
                  </div>
                  {b && (
                    <button type="button" className={cn(btnSm, "max-sm:ml-7")} onClick={() => onRun(b.action)}>
                      {b.label}
                    </button>
                  )}
                </li>
              );
            })}
        </ul>
      )}
      {read && advisories.length > 0 && (
        <>
          <div className={subhead}>
            <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
              <h3 className="text-[13px] font-semibold">Docker advisories</h3>
              <span className={sum}>{advisoriesSummary(data)}</span>
            </div>
          </div>
          <table aria-label="Advisories affecting this engine" className="w-full border-collapse text-[13px] max-sm:block">
            <thead className="max-sm:hidden">
              <tr>
                <th scope="col" className={th}>Severity</th>
                <th scope="col" className={th}>Advisory</th>
                <th scope="col" className={cn(th, "whitespace-nowrap")}>Fixed in</th>
              </tr>
            </thead>
            <tbody className="max-sm:block">
              {advisories.map((a) => (
                <tr key={a.id} className={tr}>
                  <td className={cn(td, "whitespace-nowrap sm:w-px max-sm:col-start-1 max-sm:row-start-1")}>
                    <span className={cn(chip, SEVERITY[a.severity])}>{a.severity}</span>
                  </td>
                  <td className={cn(td, "max-sm:col-span-2 max-sm:row-start-2")}>
                    <a href={a.url} target="_blank" rel="noopener noreferrer" className="text-link hover:underline">
                      {a.summary}
                      <span className="sr-only"> (opens in a new tab)</span>
                    </a>{" "}
                    <span className="ml-1 font-mono text-[11px] text-muted [overflow-wrap:anywhere]">{a.id}</span>
                  </td>
                  <td className={cn(td, "whitespace-nowrap font-mono text-xs sm:w-px max-sm:col-start-2 max-sm:row-start-1 max-sm:text-right")}>
                    <span className="sr-only">Fixed in </span>
                    {a.fixed_in}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
      {read && pending > 0 && (
        <>
          <div className={subhead}>
            <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
              <h3 className="text-[13px] font-semibold">Pending packages</h3>
              <span className={sum}>{packagesSummary(data)}</span>
            </div>
            {canRun && (installs > 0 || data.held > 0) && (
              <div className="flex flex-wrap items-center gap-x-2.5 gap-y-2">
                {data.held > 0 && (
                  <span className={help}>{installs > 0 ? "Docker is held back; it updates separately." : "Docker’s updates install with Update Docker."}</span>
                )}
                {installs > 0 && (
                  <button type="button" className={accent ? primarySm : btnSm} onClick={() => onRun("install")}>
                    Install {installs} {installs === 1 ? "update" : "updates"}…
                  </button>
                )}
              </div>
            )}
          </div>
          <table id="upd-packages" aria-label="Pending updates" className="w-full border-collapse text-[13px] max-sm:block">
            <thead className="max-sm:hidden">
              <tr>
                <th scope="col" className={th}>Package</th>
                <th scope="col" className={th}>From → to</th>
              </tr>
            </thead>
            <tbody className="max-sm:block">
              {packages.map((p) => (
                <tr key={p.name} className={tr}>
                  <td className={cn(td, "font-mono text-xs [overflow-wrap:anywhere] max-sm:col-span-2")}>
                    <span className="sr-only sm:hidden">Package: </span>
                    {p.name}
                    {/* The engine's row: the frontend knows only how many of Docker's packages are held. */}
                    {data.held > 0 && p.name === data.docker.package && <span className="ml-1.5 font-sans text-muted">(held back)</span>}
                    {p.security && <span className={cn(chip, "ml-2 bg-status-pending-soft font-sans text-status-pending-fg")}>security</span>}
                  </td>
                  <td className={cn(td, "font-mono text-xs text-fg/80 [overflow-wrap:anywhere] max-sm:col-span-2")}>
                    <span className="sr-only">installed </span>
                    {p.current || "new"} → <span className="sr-only">available </span>
                    {p.candidate}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {(pending > packages.length || all) && (
            <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
              <span>
                {packages.length} of {pending} shown
                {all && data.truncated && <>; <span className="font-mono text-xs">apt list --upgradable</span> on the server lists them all</>}
              </span>
              {data.packages.length > FIRST && (
                <button type="button" aria-expanded={all} aria-controls="upd-packages" onClick={() => setAll(!all)} className="touch-target text-link hover:underline">
                  {all ? "Show fewer" : data.truncated ? `Show the first ${data.packages.length}` : `Show all ${pending}`}
                </button>
              )}
            </div>
          )}
        </>
      )}
    </>
  );
}

/** The run in progress, or the last, at the top of the list. */
function RunRow({ run, canRetry, onRetry }: { run: UpdateRun; canRetry: boolean; onRetry: () => void }) {
  const [now, setNow] = useState(() => Date.now());
  const going = active(run);
  useEffect(() => {
    if (!going) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [going]);
  const [showOutput, setShowOutput] = useState(false);
  const v = runView(run, now);
  const tone = RUN_TONE[v.tone];
  const failed = run.outcome === "failed";
  const output = going || failed || showOutput ? run.output : [];
  return (
    <li className={cn("flex flex-col gap-2.5 border-t border-subtle px-4 py-3 first:border-t-0", tone.row)}>
      <div className="flex items-start gap-3">
        <span aria-hidden="true" className={cn("mt-px flex h-[18px] w-[18px] flex-none items-center justify-center rounded-full", tone.dot)}>
          {going ? (
            <Loader2 className="h-[11px] w-[11px] motion-safe:animate-spin" strokeWidth={3} />
          ) : failed ? (
            <X className="h-[11px] w-[11px]" strokeWidth={3} />
          ) : (
            <Check className="h-[11px] w-[11px]" strokeWidth={3} />
          )}
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span id="upd-run" tabIndex={-1} className="flex flex-wrap items-baseline gap-x-2 gap-y-1 outline-none">
            <span className="text-[13px] font-semibold">{v.label}</span>
            {v.state && <span className={cn("text-xs", tone.state)}>{v.state}</span>}
          </span>
          <span role="status" className="sr-only">{v.announce}</span>
          <span className="text-[13px] text-fg/70">{v.detail}</span>
          <span className={cn("text-xs text-muted", !going && "font-mono")}>{v.meta}</span>
        </div>
        {!going && (
          <span className="flex flex-none flex-wrap justify-end gap-1.5">
            {!failed && run.output.length > 0 && (
              <button type="button" className={cn(smallButton, "text-[13px] font-medium")} aria-expanded={showOutput} onClick={() => setShowOutput(!showOutput)}>
                {showOutput ? "Hide output" : "Show output"}
              </button>
            )}
            {failed && canRetry && (
              <button type="button" className={cn(smallButton, "text-[13px] font-medium")} onClick={onRetry}>
                Try again…
              </button>
            )}
          </span>
        )}
      </div>
      {failed && run.error && (
        <div role="alert" className="ml-[30px] rounded-lg border border-status-failed/35 bg-status-failed-soft px-3 py-2.5 text-[13px] leading-[19px] text-fg">
          {/[.!?]$/.test(run.error) ? run.error : `${run.error}.`}
        </div>
      )}
      {output.length > 0 && <RunOutput lines={output} live={going} />}
    </li>
  );
}

function RunOutput({ lines, live }: { lines: string[]; live: boolean }) {
  const ref = useRef<HTMLPreElement>(null);
  useEffect(() => {
    if (live && ref.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [lines, live]);
  return (
    <pre ref={ref} role="group" aria-label="Output" tabIndex={0}
      className="ml-[30px] max-h-[180px] overflow-auto whitespace-pre-wrap rounded-lg [overflow-wrap:anywhere] border border-subtle bg-ground px-3 py-2.5 font-mono text-xs leading-[19px] text-fg/80">
      {lines.join("\n")}
    </pre>
  );
}

/** Confirm a run, with the password unless this tab confirmed it lately. */
function UpdateRunDialog({ action, updates, where, services, agent, serverId, focusAfter, onClose, onStarted }: {
  focusAfter: string;
  action: UpdateAction;
  updates: HostUpdates;
  where: string | null;
  services: string[];
  agent: boolean;
  serverId: string | null;
  onClose: () => void;
  onStarted: (r: UpdateRun) => void;
}) {
  const [password, setPassword] = useState("");
  const [needsPassword] = useState(() => !reauthProof());
  const started = useRef(false);
  const start = useMutation({
    mutationFn: async () => {
      if (needsPassword) await serverService.confirmPassword(password);
      return updatesService.start(serverId, action);
    },
    onSuccess: (r) => {
      started.current = true;
      onStarted(r);
    },
  });
  const c = confirmCopy(action, updates, where, services, agent);
  const code = start.error instanceof ApiError ? (start.error.body as { data?: { code?: unknown } } | null)?.data?.code : undefined;
  const blocked = code === "deploy_running" || code === "update_running";
  return (
    <Dialog open onOpenChange={(o) => { if (!o && !start.isPending) onClose(); }}>
      <DialogContent
        className={cn(dialogContent, "max-w-[560px]")}
        // The button that opened it goes while the run lasts: focus the run.
        onCloseAutoFocus={(e) => {
          if (!started.current) return;
          e.preventDefault();
          requestAnimationFrame(() => document.getElementById(focusAfter)?.focus());
        }}
      >
        <form onSubmit={(e) => { e.preventDefault(); start.mutate(); }}>
          <DialogHeader className={dialogHeader}>
            <DialogTitle className={dialogTitle}>{c.title}</DialogTitle>
            <DialogDescription className={dialogText}>{c.lead}</DialogDescription>
          </DialogHeader>
          <ol className="mx-5 mt-3.5 flex list-decimal flex-col gap-1.5 pl-5 text-[13px] leading-[19px] text-fg/80">
            {c.steps.map((s) => <li key={s}>{s}</li>)}
          </ol>
          <div className={cn("mx-5 mt-3.5 flex flex-col gap-1 rounded-lg border px-3 py-2.5",
            c.loud ? "border-status-pending/35 bg-status-pending-soft" : "border-subtle bg-ground")}>
            <span className={cn("text-xs font-semibold", c.loud ? "text-status-pending-fg" : "text-status-running-fg")}>{c.downTitle}</span>
            <span className="text-[13px] leading-[19px] text-fg/80">{c.down}</span>
          </div>
          {c.note && <p className="mx-5 mt-3 rounded-lg border border-subtle bg-ground px-2.5 py-2 text-xs leading-[18px] text-fg/70">{c.note}</p>}
          {needsPassword && !blocked && (
            <div className="mx-5 mt-4 flex flex-col gap-1.5">
              <label htmlFor="upd-pw" className={fieldLabel}>Your password</label>
              <input id="upd-pw" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)}
                aria-describedby="upd-pw-help" className={fieldInput} required />
              <span id="upd-pw-help" className={help}>Good for 10 minutes in this tab.</span>
            </div>
          )}
          {start.error && (
            <p role="alert" className={cn("mx-5 mt-3 rounded-lg px-3 py-2.5 text-[13px] leading-[19px]",
              blocked ? "border border-status-pending/35 bg-status-pending-soft text-fg" : "bg-status-failed-soft text-status-failed-fg")}>
              {reauthError(start.error)}
            </p>
          )}
          <div className={dialogFooter}>
            <button type="button" className={cancelButton} onClick={onClose} disabled={start.isPending}>
              {blocked ? "Close" : "Cancel"}
            </button>
            {!blocked && (
              <button type="submit" className={primaryButton} disabled={start.isPending || (needsPassword && !password)}>
                {start.isPending ? "Starting…" : c.confirm}
              </button>
            )}
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
