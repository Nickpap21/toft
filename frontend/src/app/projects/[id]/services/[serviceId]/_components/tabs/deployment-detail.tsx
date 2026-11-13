"use client";

import { OpenFailureIssue } from "@/components/inbox/needs-you";
import { useEffect, useState } from "react";
import Link from "next/link";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertCircle, Check, ChevronLeft, Info, LoaderCircle, RotateCcw, X, XCircle } from "lucide-react";
import { deploymentHref, serviceHref } from "@/components/home/home";
import { DeployWork } from "@/components/issues/deploy-work";
import { LoadFailed, Read } from "@/components/shell/unreachable";
import { stageDefs, type PipelineType } from "@/components/ui/deploy-stage-tracker";
import { notFound } from "@/lib/api/client";
import { allDeploymentsQuery, deploymentService, type Deployment, type LogEntry } from "@/lib/api/deployment.service";
import type { Service } from "@/lib/api/service.service";
import { TONE_CLASSES } from "@/lib/status";
import { ago, formatDuration } from "@/lib/time";
import { cn } from "@/lib/utils";
import { RollbackDialog } from "../rollback-dialog";
import { deploymentRef, deploymentSeconds, inProgress } from "../service-view";
import {
  canRollBack,
  IMAGE_REMOVED_NOTE,
  rollbackRemoved,
  clock,
  deployAgain,
  entryLevel,
  groupLog,
  isoTime,
  lineText,
  previousKept,
  rowButton,
  rowState,
  startedBy,
  steps,
  triggerText,
  wasRefused,
  when,
  whileDeploying,
  type Level,
  type StepState,
} from "../deployment-view";

// A deployment's page (board: private/design/canvas/ReworkDeployment.dc.html,
// views failed, building and refused; D-44): what became of it, its pipeline,
// its log, details and the deploys around it. Not on it, since nothing
// records them: the health check's answer per attempt, the Grafana link,
// the deploy policy's rule and setting as rows, and Cancel deploy.

const card = "overflow-hidden rounded-[10px] border border-subtle bg-surface";
const h3 = "text-[15px] font-semibold leading-[22px] text-fg";
const button = cn(rowButton, "h-9 px-3 text-[13px] rounded-lg");

interface DeploymentDetailProps {
  service: Service;
  projectId: string;
  env?: { name: string; protected?: boolean };
  serverName?: string;
  deploymentId: string;
  canDeploy: boolean;
  onDeploy: (opts?: { commitSha?: string }) => void;
  deployBusy: boolean;
}

export function DeploymentDetail({ service, projectId, env, serverName, deploymentId, canDeploy, onDeploy, deployBusy }: DeploymentDetailProps) {
  const serviceId = service.id;
  const queryClient = useQueryClient();
  const allRead = useQuery({ ...allDeploymentsQuery(serviceId), refetchInterval: 60_000 });
  const { data: all, dataUpdatedAt } = allRead;
  const listed = all?.find((d) => d.id === deploymentId);
  // Not in the list: newer than its last load, or not this service's.
  const fetchedRead = useQuery({
    queryKey: ["deployments", serviceId, deploymentId],
    queryFn: () => deploymentService.get(serviceId, deploymentId),
    enabled: !!all && !listed,
    retry: false,
  });
  const { data: fetched, dataUpdatedAt: fetchedAt } = fetchedRead;
  const d = listed ?? fetched;
  const running = !!d && inProgress(d);
  // "Now", for times: the data's last load, every second while it runs.
  const [tick, setTick] = useState(0);
  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => setTick(Date.now()), 1000);
    return () => clearInterval(t);
  }, [running]);

  // The log: followed live while it runs, read once after.
  const [streamed, setStreamed] = useState<{ id: string; entries: LogEntry[] }>({ id: "", entries: [] });
  // The deployment whose stream failed, and Retry's count, which reopens it.
  const [streamLost, setStreamLost] = useState("");
  const [streamTry, setStreamTry] = useState(0);
  useEffect(() => {
    if (!running) return;
    return deploymentService.streamLogs(
      serviceId,
      deploymentId,
      // The stream starts from the first line, again after a reconnect.
      (e) => setStreamed((prev) => {
        const entries = prev.id === deploymentId ? prev.entries : [];
        return entries.length > 0 && e.id <= entries[entries.length - 1].id ? prev : { id: deploymentId, entries: [...entries, e] };
      }),
      () => {
        void queryClient.invalidateQueries({ queryKey: ["deployments", serviceId] });
        void queryClient.invalidateQueries({ queryKey: ["service", serviceId] });
      },
      () => setStreamLost(deploymentId),
    );
  }, [serviceId, deploymentId, running, queryClient, streamTry]);
  const streamFailed = running && streamLost === deploymentId;
  const storedRead = useQuery({
    queryKey: ["deployment-logs", deploymentId],
    queryFn: () => deploymentService.getLogs(serviceId, deploymentId),
    enabled: !!d && !running,
  });
  const { data: stored } = storedRead;
  const entries = (running || !stored) && streamed.id === deploymentId ? streamed.entries : stored ?? [];
  // Nothing to show yet: the stored log not read, and nothing streamed here.
  const logUnread = !running && !stored && streamed.id !== deploymentId;

  // The board opens a failed deploy's log on its warnings and errors.
  const [errorsOnly, setErrorsOnly] = useState<boolean | null>(null);
  const [rollbackTo, setRollbackTo] = useState<Deployment | null>(null);

  if (!d) {
    // A 404 is the answer; any other failure says nothing about the deployment.
    const reading: { data: unknown; error: unknown; refetch: () => unknown } = all ? fetchedRead : allRead;
    return (
      <div className="flex flex-col gap-3">
        <BackLink projectId={projectId} serviceId={serviceId} />
        {notFound(fetchedRead.error) ? (
          <p className="rounded-[10px] border border-subtle bg-surface px-4 py-3.5 text-[13px] text-fg">
            {service.name} has no such deployment. The panel keeps the last 200 of each service.
          </p>
        ) : (
          <Read
            query={reading}
            what="the deployment"
            heading="h2"
            loading={<div role="status" className="h-96 rounded-[10px] border border-subtle bg-surface motion-safe:animate-pulse"><span className="sr-only">Loading the deployment</span></div>}
          >
            {() => null}
          </Read>
        )}
      </div>
    );
  }

  const list = all ?? [];
  const now = Math.max(dataUpdatedAt, fetchedAt, tick);
  const activeId = service.active_deployment_id;
  const liveId = activeId ?? list.find((x) => x.status === "running")?.id;
  const liveKnown = !!activeId;
  const live = list.find((x) => x.id === liveId) ?? null;
  const state = rowState(d, liveId, liveKnown);
  const ref = deploymentRef(d) || d.id.slice(0, 8);
  const liveRef = live ? deploymentRef(live) || live.id.slice(0, 8) : "";
  const refused = wasRefused(d);
  const failed = d.status === "failed" && !refused;

  const type = (service.source_type || "git") as PipelineType;
  const stages = stageDefs(type, !!service.compose_repo_url, (service.health_check_retries ?? 0) > 0);
  const pipeline = refused
    ? [{ key: "failed", label: "Policy", state: "failed" as StepState, time: "refused" }, ...stages.map((s) => ({ key: s.key, label: s.label, state: "todo" as StepState, time: "" }))]
    : steps(d, groupLog(entries, stages), now).map((s) => (logUnread ? { ...s, time: "" } : s));
  // Who replaced it: the next deployment that went live after it.
  const index = list.findIndex((x) => x.id === d.id);
  const replacedBy = index > 0 ? list.slice(0, index).reverse().find((x) => x.status === "running") : undefined;
  const liveStep = pipeline.find((s) => s.key === "done");
  if (liveStep && d.status === "running") {
    liveStep.time = state.label === "Live" ? "live now" : replacedBy?.finished_at ? `until ${when(replacedBy.finished_at, now).text}` : "";
  }
  const stopped = pipeline.find((s) => s.state === "failed");

  const took = deploymentSeconds(d);
  const ranFor = d.started_at ? formatDuration(((d.finished_at ? new Date(d.finished_at).getTime() : now) - new Date(d.started_at).getTime()) / 1000) : "";
  const sent = now - new Date(d.created_at).getTime() >= 86_400_000 ? `${ago(d.created_at, now)}, ${isoTime(d.created_at)}` : ago(d.created_at, now);
  const byline = [startedBy(d), running ? (d.started_at ? `started ${ranFor} ago` : "queued") : sent].filter(Boolean).join(" · ");

  const again = failed && canDeploy ? deployAgain(d, service) : null;
  const rollBack = canDeploy && canRollBack(d, liveId, live, liveKnown);
  const removed = canDeploy && rollbackRemoved(d, liveId, live, liveKnown);

  const onlyErrors = errorsOnly ?? failed;
  const lines = onlyErrors ? entries.filter((e) => entryLevel(e) !== "info") : entries;
  const logNote = streamFailed || logUnread ? "" : running ? "following live" : onlyErrors ? `showing warnings and errors · ${lines.length} of ${entries.length} lines` : `${entries.length} lines`;
  const showErrors = () => {
    setErrorsOnly(true);
    document.getElementById("deploy-log")?.scrollIntoView({ behavior: "smooth", block: "start" });
  };

  const image = d.image_tag ? d.image_tag.slice(d.image_tag.lastIndexOf("/") + 1) : "";
  const refusedIn = d.finished_at ? (new Date(d.finished_at).getTime() - new Date(d.created_at).getTime()) / 1000 : null;
  const details: [string, string][] = [
    ["Deployment", d.id.slice(0, 8)],
    ["Trigger", triggerText(d)],
    ...(image ? [["Image", running ? `${image}, building` : image] as [string, string]] : []),
    ...(serverName ? [["Server", serverName] as [string, string]] : []),
    ...(env ? [["Environment", env.name] as [string, string]] : []),
    running
      ? ["Started", d.started_at ? clock(d.started_at) : "queued"]
      : ["Took", refused ? (refusedIn !== null && refusedIn < 1 ? "under a second" : refusedIn !== null ? formatDuration(refusedIn) : "—") : took !== null ? formatDuration(took) : "—"],
  ];
  // Three around it, this one among them, also at either end of the list.
  const from = Math.max(0, Math.min(index - 1, list.length - 3));
  const around = index < 0 ? [d] : list.slice(from, from + 3);
  const tone = TONE_CLASSES[state.tone];

  return (
    <div className="flex flex-col gap-5">
      <BackLink projectId={projectId} serviceId={serviceId} />

      <div className="flex flex-wrap items-start gap-6">
        <div className="flex min-w-0 flex-[999_1_560px] flex-col gap-6">
          <section aria-labelledby="dep-h" className={card}>
            <div className="flex flex-col gap-4 p-4">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="flex min-w-0 flex-[1_1_320px] flex-col gap-1">
                  <span role="status" className={cn("inline-flex items-center gap-1.5 text-[13px] font-medium", tone.text)}>
                    <span className={cn("h-1.5 w-1.5 rounded-full", tone.dot)} aria-hidden="true" />
                    {state.label}
                  </span>
                  <h2 id="dep-h" className="text-[17px] font-semibold leading-[26px] tracking-[-0.01em] text-fg [overflow-wrap:anywhere]">
                    {d.commit_message || `Deployment ${d.id.slice(0, 8)}`}
                  </h2>
                  <p className="flex flex-wrap items-center gap-x-1.5 text-xs text-muted">
                    <span className="font-mono text-fg/85">{ref}</span>
                    <span>· {byline}</span>
                    <DeployWork d={d} projectId={projectId} className="ml-1" />
                  </p>
                </div>
                {(again || (failed && canDeploy && projectId) || rollBack || removed) && (
                  <div className="flex flex-wrap items-center gap-2">
                    {again && (
                      <button type="button" disabled={deployBusy} onClick={() => onDeploy(again.opts)} className={cn(button, "disabled:opacity-50")}>
                        Deploy <span className="font-mono text-xs">{again.ref}</span> again
                      </button>
                    )}
                    {failed && canDeploy && projectId && (
                      <OpenFailureIssue projectId={projectId} serviceId={serviceId} deploymentId={d.id} label="Open an issue" className={button} />
                    )}
                    {rollBack && (
                      <button type="button" onClick={() => setRollbackTo(d)} className={cn(button, "gap-1.5")}>
                        <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
                        Roll back to {ref}
                      </button>
                    )}
                    {removed && (
                      <button type="button" aria-disabled="true" aria-describedby="rb-why" className={cn(button, "cursor-not-allowed gap-1.5 text-muted opacity-60")}>
                        <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
                        Roll back to {ref}
                      </button>
                    )}
                  </div>
                )}
              </div>
              {removed && <p id="rb-why" className="-mt-2 text-xs leading-[17px] text-muted">{IMAGE_REMOVED_NOTE}</p>}

              <ol aria-label="Pipeline" className="grid grid-cols-[repeat(auto-fit,minmax(104px,1fr))] gap-x-2 gap-y-3 max-sm:flex max-sm:flex-col max-sm:gap-0">
                {pipeline.map((s) => (
                  <li key={s.key} className="flex min-w-0 flex-col gap-1.5 max-sm:min-h-[34px] max-sm:flex-row max-sm:items-center max-sm:justify-between max-sm:gap-3">
                    {/* The policy's refusal is amber, as the Refused chip. */}
                    <span className={cn("h-[3px] rounded-sm max-sm:hidden", refused && s.state === "failed" ? "bg-status-pending" : BAR[s.state])} />
                    <span className={cn("flex items-center gap-1.5 text-[13px]", LABEL[s.state])}>
                      <StepIcon state={s.state} refused={refused} />
                      {s.label}
                      <span className="sr-only">, {refused && s.state === "failed" ? "refused" : SR[s.state]}</span>
                    </span>
                    <span className="font-mono text-[11px] text-muted">{s.state === "running" && s.time ? `${s.time} so far` : s.time}</span>
                  </li>
                ))}
              </ol>

              {failed && (
                <div role="alert" className="flex gap-3 rounded-lg border border-subtle bg-ground/40 px-4 py-3.5">
                  <XCircle className="mt-px h-[18px] w-[18px] flex-none text-status-failed" aria-hidden="true" />
                  <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                    <p className="text-[13px] font-semibold leading-[19px] text-fg [overflow-wrap:anywhere]">
                      {stopped ? `${stopped.label} failed` : "Failed"}{d.error_msg ? `: ${d.error_msg}` : ""}
                    </p>
                    {previousKept(entries) && (
                      <p className="text-[13px] leading-[19px] text-fg/70">
                        {stopped?.key === "health" ? "The new container started but never passed the check, so it was removed. " : "The new container was removed. "}
                        {liveRef ? `${liveRef} kept serving` : "The previous version kept serving"}: nothing changed for users.
                      </p>
                    )}
                    <div className="flex flex-wrap gap-x-4 gap-y-1 pt-0.5 text-[13px]">
                      {entries.some((e) => entryLevel(e) !== "info") && (
                        <button type="button" onClick={showErrors} className="text-link hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus">
                          The error lines
                        </button>
                      )}
                      {stopped?.key === "health" && (
                        <Link href={`${serviceHref(projectId, serviceId)}?tab=settings#set-health`} className="text-link hover:underline">Health check settings</Link>
                      )}
                    </div>
                  </div>
                </div>
              )}

              {refused && (
                <div role="alert" className="flex gap-3 rounded-lg border border-subtle bg-ground/40 px-4 py-3.5">
                  <AlertCircle className="mt-px h-[18px] w-[18px] flex-none text-status-pending" aria-hidden="true" />
                  <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                    <p className="text-[13px] font-semibold leading-[19px] text-fg">Refused by the deploy policy before anything ran</p>
                    <p className="text-[13px] leading-[19px] text-fg/70 [overflow-wrap:anywhere]">
                      {d.error_msg && <>{d.error_msg} </>}
                      Nothing stopped or started{liveRef ? <>; <span className="font-mono text-xs text-fg">{liveRef}</span> is still live</> : null}.
                    </p>
                    <div className="flex flex-wrap gap-x-4 gap-y-1 pt-0.5 text-[13px]">
                      <Link href={`${serviceHref(projectId, serviceId)}?tab=settings#set-hooks`} className="text-link hover:underline">Lifecycle hooks</Link>
                      <Link href={`${serviceHref(projectId, serviceId)}?tab=settings#set-policy`} className="text-link hover:underline">The policy</Link>
                    </div>
                  </div>
                </div>
              )}

              {running && liveRef && liveId !== d.id && (
                <p className="flex items-center gap-2 text-[13px] text-muted">
                  <Info className="h-3.5 w-3.5 flex-none" aria-hidden="true" />
                  <span>{whileDeploying(service, liveRef, ref)}</span>
                </p>
              )}

              {state.label === "Replaced" && d.finished_at && replacedBy && (
                <p className="flex flex-wrap items-center gap-1.5 text-[13px] text-muted">
                  Live from <span className="font-mono text-xs">{isoTime(d.finished_at)}</span>
                  {replacedBy.finished_at && <> until {when(replacedBy.finished_at, now).text},</>} when
                  <Link href={deploymentHref(projectId, serviceId, replacedBy.id)} className="font-mono text-xs inline-link">
                    {deploymentRef(replacedBy) || replacedBy.id.slice(0, 8)}
                  </Link>
                  replaced it.
                </p>
              )}
            </div>
          </section>

          {!refused && (
            <section id="deploy-log" aria-labelledby="log-h" className={card}>
              <div className="flex flex-wrap items-center gap-x-2.5 gap-y-2 border-b border-subtle px-4 py-2.5">
                <h3 id="log-h" className={h3}>Deploy log</h3>
                <span className="text-xs text-muted">{logNote}</span>
                <span className="flex-1" />
                <span role="group" aria-label="Lines" className="max-sm:basis-full max-sm:self-start inline-flex gap-0.5 rounded-lg border border-strong p-0.5">
                  {[false, true].map((v) => (
                    <button
                      key={String(v)}
                      type="button"
                      aria-pressed={onlyErrors === v}
                      onClick={() => setErrorsOnly(v)}
                      className={cn(
                        "h-[30px] rounded-md px-2.5 text-[13px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:h-11",
                        onlyErrors === v ? "bg-raised font-medium text-fg" : "text-muted hover:text-fg",
                      )}
                    >
                      {v ? "Warnings and errors" : "All"}
                    </button>
                  ))}
                </span>
              </div>
              <div className="overflow-x-auto bg-ground py-2.5">
                {lines.map((e) => {
                  const l = LINE[entryLevel(e)];
                  return (
                    <div key={e.id} className="flex gap-3.5 px-4 py-px font-mono text-xs leading-5">
                      <span className="flex-none text-muted max-sm:hidden">{clock(e.created_at)}</span>
                      <span className={cn("w-10 flex-none", l.tint)}>{l.label}</span>
                      <span className="min-w-0 flex-1 whitespace-pre-wrap text-fg/85 [overflow-wrap:anywhere]">{lineText(e.message).replace(/^\s*[✗⚠]\s?/, "")}</span>
                    </div>
                  );
                })}
                {running && !onlyErrors && !streamFailed && (
                  <div className="flex gap-3.5 px-4 py-px font-mono text-xs leading-5">
                    <span className="w-[58px] flex-none max-sm:hidden" />
                    <span className="w-10 flex-none" />
                    <span aria-hidden="true" className="mt-[3px] inline-block h-3.5 w-[7px] bg-muted motion-safe:animate-pulse" />
                  </div>
                )}
                {streamFailed && (
                  <div className="px-4 py-1">
                    <LoadFailed what="the live log" onRetry={() => { setStreamLost(""); setStreamTry((n) => n + 1); }} />
                  </div>
                )}
                {logUnread && (
                  <div className="px-4 py-1">
                    <Read query={storedRead} what="the log">{() => null}</Read>
                  </div>
                )}
                {lines.length === 0 && !running && !logUnread && (
                  <p className="px-4 py-0.5 text-[13px] text-muted">{onlyErrors ? "No warnings or errors." : "No lines."}</p>
                )}
              </div>
            </section>
          )}
        </div>

        <aside className="flex min-w-0 flex-[1_1_320px] flex-col gap-6">
          <section aria-labelledby="det-h" className={card}>
            <div className="flex items-center justify-between gap-3 border-b border-subtle px-4 py-3">
              <h3 id="det-h" className={h3}>Details</h3>
              <button
                type="button"
                className={cn(rowButton, "pointer-coarse:h-11")}
                onClick={() => navigator.clipboard?.writeText(window.location.href).then(
                  () => toast.success("Link copied"),
                  () => toast.error("Couldn’t copy: the browser refused"),
                ) ?? toast.error("Couldn’t copy: this page isn’t on HTTPS")}
              >
                Copy link
              </button>
            </div>
            <dl className="text-[13px]">
              {details.map(([k, v], i) => (
                <div key={k} className={cn("grid grid-cols-[110px_minmax(0,1fr)] gap-3 px-4 py-2.5 max-sm:grid-cols-1 max-sm:gap-1", i > 0 && "border-t border-subtle")}>
                  <dt className="text-muted">{k}</dt>
                  <dd className="font-mono text-xs leading-5 text-fg [overflow-wrap:anywhere]">{v}</dd>
                </div>
              ))}
            </dl>
          </section>

          <nav aria-labelledby="around-h" className={card}>
            <div className="flex items-center justify-between border-b border-subtle px-4 py-3">
              <h3 id="around-h" className={h3}>Around it</h3>
              <Link href={deploymentHref(projectId, serviceId)} className="text-[13px] text-link hover:underline">All{all && ` ${all.length}`}</Link>
            </div>
            <ul>
              {around.map((x) => {
                const st = rowState(x, liveId, liveKnown);
                const current = x.id === d.id;
                return (
                  <li key={x.id} className="border-t border-subtle first:border-t-0">
                    <Link
                      href={deploymentHref(projectId, serviceId, x.id)}
                      aria-current={current ? "page" : undefined}
                      className={cn(
                        "grid grid-cols-[76px_56px_minmax(0,1fr)] items-center gap-x-2 px-4 py-2.5 text-[13px] text-fg hover:bg-raised focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus",
                        current && "bg-selected hover:bg-selected",
                      )}
                    >
                      <span className={cn("inline-flex items-center gap-1.5 text-xs font-medium", current ? "text-fg" : TONE_CLASSES[st.tone].text)}>
                        <span className={cn("h-1.5 w-1.5 rounded-full", TONE_CLASSES[st.tone].dot)} aria-hidden="true" />
                        {st.label}
                      </span>
                      <span className="truncate font-mono text-xs text-muted">{deploymentRef(x) || x.id.slice(0, 8)}</span>
                      <span title={x.commit_message} className="line-clamp-2 min-w-0 [overflow-wrap:anywhere]">{x.commit_message || "—"}</span>
                    </Link>
                  </li>
                );
              })}
            </ul>
          </nav>
        </aside>
      </div>

      <RollbackDialog service={service} target={rollbackTo} live={live} onClose={() => setRollbackTo(null)} />
    </div>
  );
}

function BackLink({ projectId, serviceId }: { projectId: string; serviceId: string }) {
  return (
    <Link href={deploymentHref(projectId, serviceId)} className="inline-flex h-6 items-center gap-1 self-start text-[13px] text-muted hover:text-fg pointer-coarse:h-11">
      <ChevronLeft className="h-3.5 w-3.5" aria-hidden="true" />
      All deploys
    </Link>
  );
}

const BAR: Record<StepState, string> = {
  done: "bg-status-running",
  failed: "bg-status-failed",
  running: "bg-status-building",
  todo: "bg-strong",
};
const LABEL: Record<StepState, string> = {
  done: "text-fg",
  failed: "font-medium text-fg",
  running: "font-medium text-fg",
  todo: "text-muted",
};
const SR: Record<StepState, string> = { done: "passed", failed: "failed", running: "running", todo: "not run" };

function StepIcon({ state, refused }: { state: StepState; refused?: boolean }) {
  const c = "h-[13px] w-[13px] flex-none";
  if (state === "done") return <Check className={cn(c, "text-status-running")} strokeWidth={2.4} aria-hidden="true" />;
  if (state === "failed") return <X className={cn(c, refused ? "text-status-pending" : "text-status-failed")} strokeWidth={2.4} aria-hidden="true" />;
  if (state === "running") return <LoaderCircle className={cn(c, "text-status-building motion-safe:animate-spin")} strokeWidth={2.4} aria-hidden="true" />;
  return <span className={cn(c, "inline-flex items-center justify-center")} aria-hidden="true"><span className="h-1 w-1 rounded-full bg-strong" /></span>;
}

const LINE: Record<Level, { label: string; tint: string }> = {
  info: { label: "INFO", tint: "text-muted" },
  warn: { label: "WARN", tint: "text-status-pending-fg" },
  error: { label: "ERR", tint: "text-status-failed-fg" },
};
