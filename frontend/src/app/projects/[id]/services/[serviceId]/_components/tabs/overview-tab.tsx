"use client";

import { DeployWork } from "@/components/issues/deploy-work";
import { safeHref } from "@/lib/routes";
import { useState } from "react";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { ConnectedWork } from "@/components/docs/connected-work";
import { AlertTriangle, Check, ExternalLink, GitCommitHorizontal, LoaderCircle, MoreHorizontal, RotateCcw, Server as ServerIcon } from "lucide-react";
import { LoadFailed, Read, readFailed } from "@/components/shell/unreachable";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { serviceService, type Service } from "@/lib/api/service.service";
import { connectTo } from "@/lib/network-alias";
import { allDeploymentsQuery, deploymentService, type Deployment } from "@/lib/api/deployment.service";
import { formatMiB, limitMB, resourcesService } from "@/lib/api/resources.service";
import { observabilityService, type ObservabilityConfig } from "@/lib/api/observability.service";
import { useDeployLog } from "@/lib/hooks/useDeployLog";
import { reachedIndex, stageDefs, type PipelineType } from "@/components/ui/deploy-stage-tracker";
import { deploymentHref } from "@/components/home/home";
import { isServing, TONE_CLASSES } from "@/lib/status";
import { formatDuration, timeAgo } from "@/lib/time";
import { cn } from "@/lib/utils";
import { RollbackDialog } from "../rollback-dialog";
import { useRedeploy } from "@/app/projects/_lib/redeploy";
import { AdminPasswordCard } from "../admin-password-card";
import { deploymentRef, deploymentSeconds, deploymentState, inProgress, latencyView, rollbackTarget, uptimeText } from "../service-view";
import { canRollBack, IMAGE_REMOVED_SHORT, rollbackRemoved, triggerWho } from "../deployment-view";
import { composeServiceOf } from "../log-view";
import { H, W, lines, nearest, niceTop, peak, SPAN, type Point, type Range } from "../metrics-view";

// Overview (board: private/design/canvas/ReworkService.dc.html, D-136): the
// live deployment, Usage and the recent deployments, with Connected work
// and the network beside them. Not on the board: the network, kept until
// Variables or the Environment page shows connections (D-157). The
// pipeline's steps carry no times: nothing records them per step yet.

interface OverviewTabProps {
  service: Service;
  serviceId: string;
  projectId: string;
  canDeploy?: boolean;
}

const card = "min-w-0 overflow-hidden rounded-[10px] border border-subtle bg-surface";
const cardHead = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3";
const h2 = "text-[15px] font-semibold leading-[22px] text-fg";
const smallButton =
  "inline-flex h-[30px] items-center gap-1.5 rounded-md border border-strong bg-raised px-2.5 text-xs font-medium text-fg hover:border-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:min-h-11";
/** The Deploying chip: the step it is on, so an image deploy says Pulling,
 *  not Building (the 2026-11-04 sweep). */
const STEP_WORD: Record<string, string> = { clone: "Cloning", pull: "Pulling", build: "Building", deploy: "Starting", health: "Health check" };

const ago = (t: string) => (t === "now" ? "just now" : `${t} ago`);

/** A read a card draws from: Read's states come from it. */
type Reading = { data: unknown; error: unknown; isError: boolean; errorUpdateCount: number; refetch: () => unknown };

const CONNECTION_STRING: Record<string, (alias: string, port: number) => string> = {
  postgres:  (a, p) => `postgres://user:pass@${a}:${p}/db`,
  mysql:     (a, p) => `mysql://user:pass@${a}:${p}/db`,
  redis:     (a, p) => `redis://${a}:${p}`,
  mongodb:   (a, p) => `mongodb://user:pass@${a}:${p}`,
  rabbitmq:  (a, p) => `amqp://user:pass@${a}:${p}`,
  minio:     (a, p) => `http://${a}:${p}`,
  mailhog:   (a) => `smtp://${a}:1025`,
  mailpit:   (a) => `smtp://${a}:1025`,
};

export function OverviewTab({ service, serviceId, projectId, canDeploy }: OverviewTabProps) {
  const [rollbackTo, setRollbackTo] = useState<Deployment | null>(null);
  const deploymentsRead = useQuery({ ...allDeploymentsQuery(serviceId), refetchInterval: 60_000 });
  const { data: deploymentsData, dataUpdatedAt } = deploymentsRead;
  const deployments = deploymentsData ?? [];
  const activeId = service.active_deployment_id;
  // Without an active deployment, the last one that went live.
  const liveId = activeId ?? deployments.find((d) => d.status === "running")?.id;
  // The live deployment, when the last few are newer than it.
  const activeMissing = !!activeId && !!deploymentsData && !deploymentsData.some((d) => d.id === activeId);
  const activeRead = useQuery({
    queryKey: ["deployments", serviceId, "active", activeId],
    queryFn: () => deploymentService.get(serviceId, activeId ?? ""),
    enabled: activeMissing,
  });
  const { data: activeFetched } = activeRead;
  const live = deployments.find((d) => d.id === liveId) ?? activeFetched ?? null;
  const running = deployments.find(inProgress) ?? null;
  // "Now" for relative times: the list's last refresh, at least every minute.
  const now = dataUpdatedAt;

  const envRead = useQuery({
    queryKey: ["env-services", service.environment_id ?? ""],
    queryFn: () => serviceService.listForEnvironment(service.environment_id ?? ""),
    enabled: typeof service.environment_id === "string" && service.environment_id.length > 0,
    staleTime: 30_000,
  });
  const { data: envServicesData } = envRead;

  return (
    <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)]">
      <div className="flex min-w-0 flex-col gap-5">
        <AdminPasswordCard service={service} serviceId={serviceId} />
        <LiveDeployment
          service={service}
          serviceId={serviceId}
          projectId={projectId}
          live={live}
          running={running}
          read={activeMissing && !running ? activeRead : deploymentsRead}
          liveKnown={!!activeId}
          target={rollbackTarget(deployments, liveId)}
          now={now}
          canDeploy={!!canDeploy}
          onRollback={setRollbackTo}
        />
        <Usage service={service} serviceId={serviceId} projectId={projectId} peers={envServicesData ?? []} />
        <RecentDeployments
          read={deploymentsRead}
          name={service.name}
          deployments={deployments.slice(0, 4)}
          total={deployments.length}
          live={live}
          liveId={liveId}
          liveKnown={!!activeId}
          now={now}
          projectId={projectId}
          serviceId={serviceId}
          canDeploy={!!canDeploy}
          onRollback={setRollbackTo}
        />
      </div>
      <aside className="flex min-w-0 flex-col gap-5">
        <ConnectedWork serviceId={serviceId} projectId={projectId} name={service.name} dir={service.base_directory} />
        <Network service={service} serviceId={serviceId} envServices={envServicesData} retryEnv={envRead.error && !envServicesData ? () => void envRead.refetch() : undefined} />
      </aside>

      <RollbackDialog service={service} target={rollbackTo} live={live} onClose={() => setRollbackTo(null)} />
    </div>
  );
}

// ─── Live deployment ──────────────────────────────────────────────────────────

function CommitLine({ d, projectId }: { d: Deployment; projectId: string }) {
  const ref = deploymentRef(d);
  const who = triggerWho(d);
  return (
    <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5 text-[13px]">
      <GitCommitHorizontal className="h-4 w-4 flex-none text-muted" aria-hidden="true" />
      {ref && <span className="font-mono text-[13px] text-fg">{ref}</span>}
      {d.commit_message && <span className="font-medium text-fg">{d.commit_message}</span>}
      {who && <span className="text-muted">by {who}</span>}
      {/* The pull request a merge deployed and the issues it ships (D-77, D-81). */}
      <DeployWork d={d} projectId={projectId} className="ml-auto" />
    </div>
  );
}

function LiveDeployment({ service, serviceId, projectId, live, liveKnown, running, read, target, now, canDeploy, onRollback }: {
  service: Service;
  serviceId: string;
  projectId: string;
  live: Deployment | null;
  liveKnown: boolean;
  running: Deployment | null;
  /** What "not deployed" rests on: the list, or the live one's own read. */
  read: Reading;
  target: Deployment | null;
  now: number;
  canDeploy: boolean;
  onRollback: (d: Deployment) => void;
}) {
  const type = (service.source_type || "git") as PipelineType;
  const stages = stageDefs(type, !!service.compose_repo_url, (service.health_check_retries ?? 0) > 0);
  const lines = useDeployLog(serviceId, running?.id ?? "");
  const current = running?.status === "building" ? reachedIndex(lines, stages) : -1;
  const shown = running ?? live;
  const took = live && deploymentSeconds(live);
  return (
    <section aria-labelledby="live-h" className={card}>
      <div className={cardHead}>
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <h2 id="live-h" className={h2}>{running ? "Deploying" : live ? (liveKnown ? "Live deployment" : "Last live deployment") : read.data === undefined ? "Live deployment" : "Not deployed yet"}</h2>
          {running ? (
            <span className={cn("inline-flex h-[22px] items-center gap-1.5 rounded-full px-2 text-xs font-medium", TONE_CLASSES[running.status === "building" ? "building" : "pending"].chip)}>
              {running.status === "building" ? (STEP_WORD[stages[current]?.key ?? ""] ?? "Starting") : "Queued"}
            </span>
          ) : live && (
            <span className="text-xs text-muted">
              {ago(timeAgo(live.finished_at ?? live.created_at, now))}
              {took ? ` · took ${formatDuration(took)}` : ""}
            </span>
          )}
        </div>
        {canDeploy && !running && target && (
          <button type="button" className={smallButton} onClick={() => onRollback(target)}>
            <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
            Roll back to {deploymentRef(target)}
          </button>
        )}
      </div>
      <div className="p-4">
        <Read query={read} what={`the deployments of ${service.name}`}>
          {() => shown ? (
            <div className="flex flex-col gap-3.5">
              <CommitLine d={shown} projectId={projectId} />
              <ol
                aria-label={running ? "Pipeline" : "Pipeline, every step passed"}
                className="grid grid-cols-1 sm:grid-cols-[repeat(auto-fit,minmax(88px,1fr))] sm:gap-x-2 sm:gap-y-3"
              >
                {stages.map((s, i) => {
                  const done = !running || i < current;
                  const active = !!running && i === current;
                  return (
                    <li key={s.key} className="flex min-h-9 items-center border-t border-subtle first:border-t-0 sm:min-h-0 sm:flex-col sm:items-stretch sm:gap-2 sm:border-t-0">
                      <span className={cn("hidden h-[3px] rounded-sm sm:block", done ? "bg-status-running" : active ? "bg-status-building" : "bg-strong")} />
                      <span className={cn("flex items-center gap-1.5 text-[13px]", done ? "text-fg" : active ? "font-medium text-status-building-fg" : "text-muted")}>
                        {done && <Check className="h-3.5 w-3.5 text-status-running" aria-hidden="true" />}
                        {active && <LoaderCircle className="h-3.5 w-3.5 motion-safe:animate-spin" aria-hidden="true" />}
                        {s.label}
                      </span>
                    </li>
                  );
                })}
              </ol>
              {running && lines.length > 0 && (
                <pre className="overflow-hidden rounded-md border border-subtle bg-ground px-3 py-2.5 font-mono text-xs leading-[18px] text-muted">
                  {lines.slice(-4).map((line, i) => <span key={i} className="block truncate">{line}</span>)}
                </pre>
              )}
              {running && (
                <div>
                  <Link href={deploymentHref(projectId, serviceId, running.id)} className={smallButton}>Follow logs</Link>
                </div>
              )}
            </div>
          ) : (
            <p className="text-[13px] text-muted">{canDeploy ? "Deploy starts its first version." : "Nothing has been deployed."}</p>
          )}
        </Read>
      </div>
    </section>
  );
}

// ─── Usage ────────────────────────────────────────────────────────────────────

const RANGES: { id: Range; label: string; long: string }[] = [
  { id: "1h", label: "1h", long: "the last hour" },
  { id: "24h", label: "24h", long: "the last 24 hours" },
  { id: "7d", label: "7d", long: "the last 7 days, a point a day" },
];

const mb = (n: number) => formatMiB(Math.round(n));
const pct = (n: number) => `${Math.round(n)}%`;

function Usage({ service, serviceId, projectId, peers }: { service: Service; serviceId: string; projectId: string; peers: Service[] }) {
  const isCompose = service.source_type === "docker-compose";
  // During a redeploy the previous container still serves.
  const running = isServing(service.runtime_status) || service.runtime_status === "deploying";
  const [range, setRange] = useState<Range>("24h");
  const [composeService, setComposeService] = useState("");
  const r = RANGES.find((x) => x.id === range)!;

  const metricsRead = useQuery({
    queryKey: ["metrics", serviceId, range, composeService],
    queryFn: () => serviceService.getMetrics(serviceId, range, composeService),
    refetchInterval: running ? 60_000 : false,
    retry: false,
  });
  const { data: points, dataUpdatedAt } = metricsRead;
  const { data: resources, isError: resourcesFailed } = useQuery({
    queryKey: ["service-resources", serviceId],
    queryFn: () => resourcesService.service(serviceId),
    staleTime: 60_000,
  });
  const rows = resources?.rows ?? [];
  const { data: containers, isLoading: containersLoading } = useQuery({
    queryKey: ["containers", serviceId],
    queryFn: () => serviceService.listContainers(serviceId),
    enabled: isCompose && running,
    staleTime: 15_000,
    retry: false,
  });
  // A stack's uptime is its picked compose service's, else its first.
  const container = isCompose
    ? (composeService && containers?.find((c) => composeServiceOf(c, serviceId) === composeService)) || containers?.[0] || ""
    : "";
  const { data: stats, dataUpdatedAt: statsAt, isLoading: statsLoading } = useQuery({
    queryKey: ["stats", serviceId, container],
    queryFn: () => serviceService.getStats(serviceId, container || undefined),
    enabled: running && (!isCompose || container !== ""),
    refetchInterval: 10_000,
    retry: false,
  });

  // "Grafana" only where the project or workspace has one (D-45).
  const { data: projectObs } = useQuery({
    queryKey: ["observability-project", projectId],
    queryFn: () => observabilityService.getForProject(projectId),
    enabled: !!projectId, // a workspace service has no project
    staleTime: 60_000,
  });
  const { data: orgObs } = useQuery({
    queryKey: ["observability-org"],
    queryFn: () => observabilityService.getForOrg(),
    staleTime: 60_000,
  });
  const grafanaUrl = safeHref((projectObs as ObservabilityConfig | undefined)?.grafana_url || (orgObs as ObservabilityConfig | undefined)?.grafana_url);

  // The scale of each chart: the limit when there is one.
  const picked = isCompose && composeService ? rows.find((x) => x.compose_service === composeService) : undefined;
  const stackLimits = rows.map((x) => limitMB(x.memory.limit));
  const memLimit = isCompose
    ? picked
      ? limitMB(picked.memory.limit)
      : rows.length > 0 && stackLimits.every((x) => x > 0) ? stackLimits.reduce((a, b) => a + b, 0) : 0
    : limitMB(service.memory_limit ?? "");
  const cpuCores = isCompose ? parseFloat(picked?.cpu.limit ?? "") || 0 : parseFloat(service.cpu_limit ?? "") || 0;

  const series: Point[] = points ?? [];
  // 7d days summarised before the summed peaks were kept (0070).
  const olderUntil = series.filter((p) => p.largest_container).at(-1)?.timestamp;
  // The lines end at the last fetch: "now" on the axis.
  const end = dataUpdatedAt;
  const memPeak = peak(series, (p) => p.memory_mb);
  const cpuPeak = peak(series, (p) => p.cpu_percent);
  // A stack's limit is in its resources: "no limit" needs them read.
  const limitUnknown = isCompose && !resources;
  const memTop = memLimit || niceTop(memPeak ?? 0);
  const cpuTop = cpuCores > 0 ? cpuCores * 100 : Math.max(100, niceTop(cpuPeak ?? 0));
  const last = series.at(-1);
  const who = isCompose ? composeService || `${service.name}'s compose services together` : service.name;

  const charts = [
    {
      title: "CPU",
      value: (p: Point) => pct(p.cpu_percent / (cpuCores || 1)),
      now: last ? <>{pct(last.cpu_percent / (cpuCores || 1))} <span className="text-muted">{cpuCores ? `of ${+cpuCores.toFixed(2)} vCPU` : "of one core"}</span></> : null,
      line: lines(series, range, end, cpuTop, (p) => p.cpu_percent),
      limit: false,
      peak: cpuPeak !== null ? `peak ${pct(cpuPeak / (cpuCores || 1))}` : "",
      aria: last
        ? `${who} CPU over ${r.long}: ${pct(last.cpu_percent / (cpuCores || 1))} ${cpuCores ? `of ${+cpuCores.toFixed(2)} vCPU` : "of one core"} now, peak ${pct((cpuPeak ?? 0) / (cpuCores || 1))}`
        : `${who} CPU: no samples over ${r.long}`,
    },
    {
      title: "Memory",
      value: (p: Point) => mb(p.memory_mb),
      now: last ? <>{mb(last.memory_mb)} <span className="text-muted">{memLimit ? `of ${mb(memLimit)} limit` : limitUnknown ? "" : "no limit"}</span></> : null,
      line: lines(series, range, end, memTop, (p) => p.memory_mb),
      limit: memLimit > 0,
      peak: memPeak !== null ? `peak ${mb(memPeak)}` : "",
      aria: last
        ? `${who} memory over ${r.long}: ${mb(last.memory_mb)}${memLimit ? ` of the ${mb(memLimit)} limit` : ""} now, peak ${mb(memPeak ?? 0)}`
        : `${who} memory: no samples over ${r.long}`,
    },
  ];

  // Restarts over 24 hours: the picked compose service's, else every row's
  // summed, counted since the latest row's counting started.
  const counted = (picked ? [picked] : rows).filter((x) => x.restarts_24h !== null);
  const restarts = counted.length > 0 ? counted.reduce((n, x) => n + (x.restarts_24h ?? 0), 0) : null;
  const restartsSince = counted.map((x) => x.restarts_since).filter((x): x is string => !!x).sort().at(-1);

  // p95 latency (D-156): hidden while no minute is recorded (no domain).
  // A stack's is its routed compose service's, named in the title.
  const latency = resources?.latency_24h ? latencyView(resources.latency_24h) : null;
  const routedCompose = isCompose ? service.compose_route_service || (rows.length === 1 ? rows[0].compose_service : "") : "";
  const latencyTitle = latency && [routedCompose && `Requests to ${routedCompose}, the compose service its domains route to`, latency.title].filter(Boolean).join(". ");

  const check = service.last_health_check;
  const unmet =peers.filter((p) => (service.dependencies ?? []).includes(p.id) && p.runtime_status !== "running");
  const dd = "font-mono text-xs text-fg";

  return (
    <section aria-labelledby="use-h" className={card}>
      <div className={cardHead}>
        <h2 id="use-h" className={h2}>Usage</h2>
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-2">
          {isCompose && rows.length > 1 && (
            <label className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-strong pl-2.5 pr-1 text-xs text-muted focus-within:ring-2 focus-within:ring-focus pointer-coarse:min-h-11">
              <span className="sr-only">Compose service</span>
              <select
                value={composeService}
                onChange={(e) => setComposeService(e.target.value)}
                className="h-7 cursor-pointer bg-transparent text-[13px] text-fg focus:outline-none"
              >
                <option value="">All together</option>
                {rows.map((x) => <option key={x.compose_service} value={x.compose_service}>{x.compose_service}</option>)}
              </select>
            </label>
          )}
          <span role="group" aria-label="Time range" className="inline-flex gap-0.5 rounded-lg border border-strong p-0.5">
            {RANGES.map((x) => (
              <button
                key={x.id}
                type="button"
                aria-pressed={range === x.id}
                onClick={() => setRange(x.id)}
                className={cn(
                  "h-[26px] rounded-md px-2.5 text-[13px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:min-h-11",
                  range === x.id ? "bg-raised text-fg" : "text-muted hover:text-fg",
                )}
              >
                {x.label}
              </button>
            ))}
          </span>
          {grafanaUrl && (
            <a
              href={grafanaUrl}
              target="_blank"
              rel="noreferrer"
              aria-label={`${service.name} in Grafana, opens in a new tab`}
              className="inline-flex items-center gap-1 text-[13px] text-link hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
            >
              Grafana
              <ExternalLink className="h-3 w-3" aria-hidden="true" />
            </a>
          )}
        </div>
      </div>
      <div className="flex flex-col gap-3.5 p-4">
        <Read
          query={metricsRead}
          what={`the usage of ${service.name}`}
          loading={<p className="text-[13px] leading-[19px] text-muted">Reading usage…</p>}
          isEmpty={(samples) => samples.length === 0}
          empty={
            <p className="text-[13px] leading-[19px] text-muted">
              {!running
                ? `${service.name} isn't running, so nothing is measured. Samples are kept for 48 hours.`
                : range === "7d"
                  ? "No days measured yet: a day's peak is kept once it has samples."
                  : `Collecting the first samples: ${service.name} is sampled every minute.`}
            </p>
          }
        >
          {() => (
            <div className="grid gap-5 sm:grid-cols-2">
              {charts.map((ch) => (
                <Chart key={ch.title} {...ch} series={series} range={range} end={end} />
              ))}
            </div>
          )}
        </Read>
        {range === "7d" && series.length > 0 && (
          <p className="text-xs text-muted">
            A point a day (UTC): the peak memory and CPU&apos;s 95th percentile of the day&apos;s minutes.
            {olderUntil && (
              <> Up to {new Date(olderUntil).toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" })}, memory is the peak of the largest container{isCompose ? " of each compose service" : ""}, as those days were kept.</>
            )}
          </p>
        )}
        <dl className={cn("grid grid-cols-2 gap-3 border-t border-subtle pt-3.5", latency || resourcesFailed ? "sm:grid-cols-4" : "sm:grid-cols-3")}>
          <div className="flex flex-col gap-1">
            <dt className="text-xs text-muted">Health check</dt>
            <dd className={dd} title={check ? `Answered in ${check.ms} ms at ${new Date(check.at).toLocaleTimeString()}` : undefined}>
              {(service.health_check_retries ?? 0) === 0 ? (
                <span className="font-sans text-muted">off</span>
              ) : (
                <>
                  {check && (
                    <span className={cn("mr-1.5", check.healthy ? "text-status-running-fg" : "text-status-failed-fg")}>
                      {check.status || "no answer"}
                    </span>
                  )}
                  {service.health_check_path || "/"}
                  {!check && <span className="ml-1.5 font-sans text-muted">not checked yet</span>}
                </>
              )}
            </dd>
          </div>
          <div className="flex flex-col gap-1">
            <dt className="text-xs text-muted">Restarts, 24h</dt>
            {/* Counted from the minute samples, so a redeploy adds nothing (D-156). */}
            <dd className={cn(dd, restarts ? "text-status-pending-fg" : "")}>
              {restarts !== null ? (
                <>
                  {restarts}
                  {restartsSince && (
                    <span className="ml-1.5 font-sans text-muted">
                      since {new Date(restartsSince).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}
                    </span>
                  )}
                </>
              ) : resources ? (
                <>— <span className="font-sans text-muted">not counted yet</span></>
              ) : resourcesFailed ? "—" : "…"}
            </dd>
          </div>
          <div className={cn("flex flex-col gap-1", !latency && !resourcesFailed && "max-sm:col-span-2")}>
            <dt className="text-xs text-muted">Uptime</dt>
            <dd className={dd} title={stats?.started_at ? `Started ${new Date(stats.started_at).toLocaleString()}` : undefined}>
              {running && stats?.started_at ? uptimeText(statsAt - new Date(stats.started_at).getTime()) : running && (statsLoading || containersLoading) ? "…" : "—"}
            </dd>
          </div>
          {(latency || resourcesFailed) && (
            <div className="flex flex-col gap-1">
              <dt className="text-xs text-muted">p95 latency, 24h</dt>
              <dd className={dd}>
                {/* The bucket's bounds open on a click, a tap or Enter, not on hover alone. */}
                {!latency ? "—" : latencyTitle ? (
                  <details>
                    <summary className="w-fit cursor-pointer list-none rounded-sm underline decoration-strong decoration-dotted underline-offset-[3px] hover:decoration-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:min-h-11 [&::-webkit-details-marker]:hidden">
                      {latency.value}
                      <span className={cn("font-sans text-muted", latency.value && "ml-1.5")}>{latency.note}</span>
                    </summary>
                    <p className="mt-1 font-sans text-muted">{latencyTitle}.</p>
                  </details>
                ) : (
                  <span className="font-sans text-muted">{latency.note}</span>
                )}
              </dd>
            </div>
          )}
        </dl>
        {unmet.length > 0 && (
          <p className="flex items-center gap-1.5 text-xs text-status-pending-fg">
            <AlertTriangle className="h-3.5 w-3.5 flex-none" aria-hidden="true" />
            {unmet.map((s) => s.name).join(", ")} not running
          </p>
        )}
      </div>
    </section>
  );
}

/** One Usage chart: the line against its scale, and on hover the sample
 *  under the pointer with its time. */
function Chart({ title, now, aria, line, limit, peak, value, series, range, end }: {
  title: string;
  now: React.ReactNode;
  aria: string;
  line: string[];
  limit: boolean;
  peak: string;
  value: (p: Point) => string;
  series: Point[];
  range: Range;
  end: number;
}) {
  const [hover, setHover] = useState<{ frac: number; p: Point } | null>(null);
  const when = (p: Point) => {
    const d = new Date(p.timestamp);
    return range === "7d"
      ? d.toLocaleDateString(undefined, { day: "numeric", month: "short" })
      : d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  };
  return (
    <figure className="m-0 flex min-w-0 flex-col gap-2">
      <figcaption className="flex flex-wrap justify-between gap-x-3 gap-y-1 text-[13px] text-fg">
        <span>{title}</span>
        {now && <span className="font-mono text-xs">{now}</span>}
      </figcaption>
      <div
        className="relative"
        onPointerMove={(e) => {
          const box = e.currentTarget.getBoundingClientRect();
          const frac = Math.min(1, Math.max(0, (e.clientX - box.left) / box.width));
          const p = nearest(series, range, end, frac);
          setHover(p ? { frac: (new Date(p.timestamp).getTime() - (end - SPAN[range])) / SPAN[range], p } : null);
        }}
        onPointerLeave={() => setHover(null)}
      >
        <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" role="img" aria-label={aria} className="block h-[84px] w-full">
          <line x1="0" y1="1" x2={W} y2="1" vectorEffect="non-scaling-stroke" className={limit ? "stroke-muted" : "stroke-subtle"} strokeDasharray={limit ? "4 4" : undefined} />
          <line x1="0" y1={H / 2} x2={W} y2={H / 2} vectorEffect="non-scaling-stroke" className="stroke-subtle" />
          <line x1="0" y1={H - 1} x2={W} y2={H - 1} vectorEffect="non-scaling-stroke" className="stroke-subtle" />
          {line.map((pts, i) => (
            <polyline key={i} points={pts} fill="none" strokeWidth="2" strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" className="stroke-link" />
          ))}
          {hover && (
            <line x1={hover.frac * W} y1="0" x2={hover.frac * W} y2={H} vectorEffect="non-scaling-stroke" className="stroke-muted" />
          )}
        </svg>
        {hover && (
          <span
            aria-hidden="true"
            className="pointer-events-none absolute -top-1 whitespace-nowrap rounded-md border border-strong bg-raised px-1.5 py-0.5 font-mono text-[11px] text-fg shadow-menu"
            style={hover.frac > 0.5 ? { right: `${(1 - hover.frac) * 100}%`, marginRight: 6 } : { left: `${hover.frac * 100}%`, marginLeft: 6 }}
          >
            {value(hover.p)} <span className="text-muted">{when(hover.p)}</span>
          </span>
        )}
      </div>
      <div aria-hidden="true" className="flex justify-between font-mono text-[11px] text-muted">
        <span>{range} ago</span>
        <span>{peak}</span>
        <span>{range === "7d" ? "today" : "now"}</span>
      </div>
    </figure>
  );
}

// ─── Recent deployments ───────────────────────────────────────────────────────

function RecentDeployments({ read, name, deployments, total, live, liveId, liveKnown, now, projectId, serviceId, canDeploy, onRollback }: {
  read: Reading;
  name: string;
  deployments: Deployment[];
  total: number;
  live: Deployment | null;
  liveId?: string;
  liveKnown: boolean;
  now: number;
  projectId: string;
  serviceId: string;
  canDeploy: boolean;
  onRollback: (d: Deployment) => void;
}) {
  const th = "px-4 py-2 text-xs font-medium";
  // On phones each row is a card: status, commit and when, and a ⋯ menu.
  const td = "border-t border-subtle px-4 py-2.5 max-sm:block max-sm:border-0 max-sm:p-0";
  const { redeploy, dialog: redeployDialog } = useRedeploy();
  return (
    <section aria-labelledby="recent-h" className={card}>
      <div className={cardHead}>
        <h2 id="recent-h" className={h2}>Recent deployments</h2>
        {total > 0 && (
          <Link href={deploymentHref(projectId, serviceId)} className="text-[13px] text-link hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center">All {total} in Deploys</Link>
        )}
      </div>
      <div className={cn(read.data === undefined && "p-4")}>
        {/* The live card draws this read's failure: the row would say it twice. */}
        {readFailed(read) ? <p className="text-[13px] text-muted">—</p> : (
        <Read
          query={read}
          what={`the deployments of ${name}`}
          isEmpty={() => total === 0}
          empty={<p className="px-4 py-3 text-[13px] text-muted">No deployments yet.</p>}
        >
          {() => (
            <table className="w-full border-collapse text-[13px] max-sm:block">
              <thead className="max-sm:hidden">
                <tr className="text-left text-muted">
                  <th scope="col" className={cn(th, "w-28")}>Status</th>
                  <th scope="col" className={th}>Commit</th>
                  <th scope="col" className={cn(th, "w-px whitespace-nowrap")}>When</th>
                  <th scope="col" className={cn(th, "w-px")}><span className="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody className="max-sm:block">
                {deployments.map((d) => {
                  const state = deploymentState(d, liveId, liveKnown);
                  const ref = deploymentRef(d);
                  const who = triggerWho(d);
                  const href = deploymentHref(projectId, serviceId, d.id);
                  const rollback = canDeploy && canRollBack(d, liveId, live, liveKnown);
                  const removed = canDeploy && rollbackRemoved(d, liveId, live, liveKnown);
                  // The live one runs again with today's variables (7g step 8).
                  const redeployable = canDeploy && state.label === "Live" && !!d.image_tag && !d.image_removed;
                  return (
                    <tr key={d.id} className="max-sm:relative max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1 max-sm:border-t max-sm:border-subtle max-sm:px-4 max-sm:py-3 max-sm:first:border-t-0">
                      <td className={cn(td, "whitespace-nowrap max-sm:col-start-1 max-sm:row-start-1")}>
                        <span className={cn("inline-flex items-center gap-1.5 font-medium", TONE_CLASSES[state.tone].text)}>
                          <span className={cn("h-1.5 w-1.5 rounded-full", TONE_CLASSES[state.tone].dot)} aria-hidden="true" />
                          {state.label}
                        </span>
                      </td>
                      <td className={cn(td, "max-sm:col-start-1 max-sm:row-start-2")}>
                        {/* On phones the commit's link covers the card, under the ⋯. */}
                        <Link href={href} className="hover:underline max-sm:after:absolute max-sm:after:inset-0 max-sm:after:content-['']">
                          <span className="mr-2 font-mono text-xs text-fg">{ref || "—"}</span>
                          {d.commit_message && <span className="text-fg/85">{d.commit_message}</span>}
                        </Link>
                        {removed && <span id={`rb-why-${d.id}`} className="block text-xs leading-[18px] text-muted">{IMAGE_REMOVED_SHORT}</span>}
                      </td>
                      <td className={cn(td, "whitespace-nowrap text-muted max-sm:col-start-1 max-sm:row-start-3")}>
                        {ago(timeAgo(d.created_at, now))}
                        {who && ` · ${who}`}
                      </td>
                      <td className={cn(td, "whitespace-nowrap text-right max-sm:col-start-2 max-sm:row-span-3 max-sm:row-start-1 max-sm:self-center")}>
                        <span className="max-sm:hidden">
                          {d.status === "failed" ? (
                            <Link href={href} className={smallButton} aria-label={`View logs of deployment ${ref}`}>View logs</Link>
                          ) : rollback ? (
                            <button type="button" className={smallButton} onClick={() => onRollback(d)}>Roll back</button>
                          ) : removed ? (
                            <button type="button" aria-disabled="true" aria-label={`Roll back to ${ref}`} aria-describedby={`rb-why-${d.id}`}
                              className={cn(smallButton, "cursor-not-allowed text-muted opacity-60")}>Roll back</button>
                          ) : null}
                        </span>
                        {rollback && (
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <button
                              type="button"
                              aria-label={`Actions for deployment ${ref || state.label}`}
                              className="relative z-[1] inline-flex h-11 w-11 items-center justify-center rounded-lg text-muted hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus sm:hidden"
                            >
                              <MoreHorizontal className="h-[18px] w-[18px]" aria-hidden="true" />
                            </button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem asChild><Link href={href}>{d.status === "failed" ? "View logs" : "Open deployment"}</Link></DropdownMenuItem>
                            <DropdownMenuItem onSelect={() => onRollback(d)}>Roll back to {ref}</DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                        )}
                        {redeployable && (
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <button
                              type="button"
                              aria-label={`Actions for deployment ${ref || state.label}`}
                              className="relative z-[1] inline-flex h-8 w-8 items-center justify-center rounded-lg text-muted hover:bg-raised hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:h-11 pointer-coarse:w-11"
                            >
                              <MoreHorizontal className="h-[18px] w-[18px]" aria-hidden="true" />
                            </button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem asChild><Link href={href}>Open deployment</Link></DropdownMenuItem>
                            <DropdownMenuItem onSelect={() => void redeploy(serviceId, d.id)}>Redeploy {ref || "it"}</DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </Read>
        )}
      </div>
      {redeployDialog}
    </section>
  );
}

// ─── Network ──────────────────────────────────────────────────────────────────

export function Network({ service, serviceId, envServices, retryEnv }: { service: Service; serviceId: string; envServices?: Service[]; /** The environment's services failed to load. */ retryEnv?: () => void }) {
  const peers = (envServices ?? []).filter((s) => s.id !== serviceId);
  const own = connectTo(service);
  if ((envServices ?? []).length === 0 && !own && !retryEnv) return null;
  const copy = (value: string) => {
    navigator.clipboard.writeText(value);
    toast.success("Copied");
  };
  return (
    <section aria-labelledby="net-h" className={card}>
      <div className="flex flex-col gap-0.5 border-b border-subtle px-4 py-3">
        <h2 id="net-h" className={cn(h2, "flex items-center gap-2")}>
          <ServerIcon className="h-4 w-4 text-muted" aria-hidden="true" />
          Network
        </h2>
        <p className="text-xs text-muted">
          {service.scope === "org"
            ? "Every project's services on the same server reach it by alias."
            : service.scope === "project"
              ? "Every environment of the project reaches it by alias, from services on the same server."
              : "Reachable by alias from this service: this environment's network, plus shared services on the same server."}
        </p>
      </div>
      <div className="flex flex-col gap-2 p-4">
        {own && (
          <div className="flex items-center gap-2 rounded-md border border-strong bg-raised px-3 py-2">
            <span className="min-w-0 flex-1">
              <span className="flex items-baseline gap-2">
                <span className="truncate text-[13px] font-medium text-fg">{service.name}</span>
                <span className="shrink-0 text-xs text-muted">this service</span>
              </span>
              <span className="block truncate font-mono text-xs text-link" title={own}>
                {own}
              </span>
            </span>
            <Button variant="ghost" size="sm" className="h-7 shrink-0 px-2 text-xs" onClick={() => copy(own)}>
              Copy
            </Button>
          </div>
        )}
        {retryEnv && <LoadFailed what="the other services in this environment" onRetry={retryEnv} />}
        {envServices && peers.length === 0 && own && service.environment_id && (
          <p className="text-[13px] text-muted">No other services in this environment yet.</p>
        )}
        {peers.map((peer) => {
          const at = connectTo(peer);
          if (!at || !peer.connect_to) return null;
          const connStr = peer.addon_slug ? CONNECTION_STRING[peer.addon_slug]?.(peer.connect_to.host, peer.connect_to.port) : undefined;
          const isDependency = (service.dependencies ?? []).includes(peer.id);
          return (
            <div key={peer.id} className="flex items-center gap-2 rounded-md bg-raised px-3 py-2">
              <span className="min-w-0 flex-1">
                <span className="flex items-center gap-2">
                  <span className="truncate text-[13px] font-medium text-fg">{peer.name}</span>
                  {isDependency && <span className="shrink-0 font-mono text-xs text-status-pending-fg">required</span>}
                </span>
                <span className="block truncate font-mono text-xs text-muted" title={at}>{at}</span>
                {connStr && <span className="block truncate font-mono text-xs text-muted">{connStr}</span>}
              </span>
              <Button variant="ghost" size="sm" className="h-7 shrink-0 px-2 text-xs" onClick={() => copy(connStr ?? at)}>
                Copy
              </Button>
            </div>
          );
        })}
      </div>
    </section>
  );
}
