"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, ExternalLink, Plus } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { deploymentService } from "@/lib/api/deployment.service";
import { observabilityService, type ObservabilityConfig } from "@/lib/api/observability.service";
import { serviceService, type WorkspaceService } from "@/lib/api/service.service";
import { projectsQuery, serversQuery, workspaceEnvironmentsQuery, workspaceServicesQuery } from "@/lib/queries";
import { safeHref } from "@/lib/routes";
import { isServing } from "@/lib/status";
import { cn, possessive } from "@/lib/utils";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import { ReadFailed } from "@/components/shell/unreachable";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  cancelButton, dialogContent, dialogHeader, dialogText, dialogTitle, fieldInput, primaryButton,
} from "@/components/settings/settings-frame";
import { inProse, serverName } from "../projects/_lib/projects-view";
import { StatusText } from "../projects/_lib/service-actions";
import { svcRow as row, svcTd, svcTh } from "../projects/_lib/service-table";
import { btnSm, card, h2, help, mono, sum } from "../projects/_lib/ui";
import {
  isContainerAlias, localLokiAddress, localLogsWarning, logsCell, lokiState, serverLogsLine, stackOf, type LogsTone, type StackKind,
} from "./observability-view";
import { runsOn } from "./shared-view";

// Logs and metrics (board: ReworkSharedServices, tab metrics; D-48, D-147,
// D-148, D-162): where the workspace's logs and metrics go. The stack is its
// catalog Loki, Prometheus and Grafana; the Logs table reads each container's
// log driver on its server (GET /observability/logs).

/** A card's head and foot, and a term and its value, as the board draws them. */
const head = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3";
const headLeft = "flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5";
const foot = "border-t border-subtle px-4 py-2.5 text-[13px] text-muted";
export const kv = "grid grid-cols-[110px_minmax(0,1fr)] gap-3 border-t border-subtle px-4 py-2 text-[13px] first:border-t-0 max-sm:grid-cols-1 max-sm:gap-0.5";
const link = "text-link hover:underline";
const svcRow = cn(row, "max-sm:gap-y-1");
const th = cn(svcTh, "leading-5");
const LOG_TONE: Record<LogsTone, string> = { running: "text-status-running-fg", pending: "text-status-pending-fg", muted: "text-muted" };

export function LogsTab() {
  const { user } = useAuth();
  const role = useOrgRole();
  const canEdit = role === "owner" || role === "developer";
  // Where the workspace's logs go is the owners'.
  const canSet = role === "owner";
  const ws = user?.orgName ?? "this workspace";
  const [editing, setEditing] = useState<"loki" | "grafana" | null>(null);

  const { data: config, isLoading, error: configError, refetch: refetchConfig } = useQuery({ queryKey: ["observability"], queryFn: () => observabilityService.getForOrg() });
  const { data: services, error: servicesError, refetch: refetchServices } = useQuery(workspaceServicesQuery);
  const { data: servers } = useQuery(serversQuery);
  const { data: envs } = useQuery(workspaceEnvironmentsQuery);
  const { data: projects } = useQuery(projectsQuery);
  const { data: logs, isLoading: logsLoading, error: logsError, refetch: refetchLogs } = useQuery({ queryKey: ["observability-logs"], queryFn: () => observabilityService.logs(), staleTime: 30_000 });
  const { data: byProject, error: byProjectError, refetch: refetchByProject } = useQuery({ queryKey: ["observability-projects"], queryFn: () => observabilityService.projects() });

  const cfg = (config && "org_id" in config ? config : {}) as Partial<ObservabilityConfig>;
  const svcs = services ?? [];
  const stack = stackOf(cfg, svcs);
  const hasStack = stack.some((s) => s.svc);
  // No stack is only known with both reads in: a project's service registered
  // as the workspace's is found through the config.
  const stackKnown = !!services && (hasStack || !!config);
  // Where logs go is read against Loki's address, so it needs both as well.
  const placed = config ? logs : undefined;
  const placedError = !placed && (configError || logsError);
  const svcOf = (kind: StackKind) => stack.find((s) => s.kind === kind)?.svc;
  const lokiSvc = svcOf("loki");
  const prometheus = svcOf("prometheus");
  const loki = cfg.loki_url;
  const state = lokiState(loki);
  const grafanaUrl = safeHref(cfg.grafana_url);
  const name = (id: string) => serverName(id, servers) || "a server";
  const attention = stack.flatMap((s) => (s.svc && s.svc.scope !== "org" ? [{ kind: s.kind, svc: s.svc }] : []));

  const projectName = (id: string) => projects?.find((p) => p.id === id)?.name ?? "";
  const envName = (id: string) => envs?.find((e) => e.id === id)?.name ?? "";
  // Where a project's service runs, in a sentence.
  const place = (s: WorkspaceService) => {
    const project = projectName(s.project_id);
    if (!project) return "a project";
    const env = s.scope === "project" ? "" : envName(s.environment_id);
    return env ? `${possessive(project)} ${env} environment` : `the ${project} project`;
  };
  // Where focus goes when a Stack row's dialog closes with its row gone.
  const stackHeading = useRef<HTMLHeadingElement>(null);
  const href = (s: WorkspaceService) => (s.scope === "org" || !s.project_id ? `/shared/${s.id}` : `/projects/${s.project_id}/services/${s.id}`);

  // Each server with services, and where each running one's logs go.
  const groups = (placed ?? [])
    .map((g) => {
      const rows = svcs
        .filter((s) => (s.server_id || "") === g.server_id)
        .map((s) => {
          const containers = g.services.find((x) => x.service_id === s.id)?.containers;
          const here = !lokiSvc || state === "ok" ? undefined : s.id === lokiSvc.id ? "loki" : (s.server_id || "") === (lokiSvc.server_id || "") ? "app" : "elsewhere";
          const agent = servers?.find((x) => x.id === s.server_id)?.connection === "agent";
          return { s, cell: logsCell(containers, isServing(s.runtime_status), s.source_type === "docker-compose", loki, here, agent) };
        });
      return { g, rows, running: rows.filter((r) => isServing(r.s.runtime_status)) };
    })
    .filter((x) => x.rows.length > 0);
  const at = (serverId: string) => (state === "ok" ? "address" : !lokiSvc ? "none" : serverId === (lokiSvc.server_id || "") ? "here" : "elsewhere") as "address" | "here" | "elsewhere" | "none";
  const running = groups.reduce((n, x) => n + x.running.length, 0);
  // Loki itself counts: its server's row says its services go to Loki.
  const shipping = groups.reduce((n, x) => n + x.running.filter((r) => r.cell.ships || r.s.id === lokiSvc?.id).length, 0);
  const elsewhere = lokiSvc && state !== "ok" ? groups.filter((x) => at(x.g.server_id) === "elsewhere" && x.running.length > 0) : [];
  const stuck = groups.filter((x) => x.rows.some((r) => !r.cell.ships && r.cell.tone === "pending")).map((x) => name(x.g.server_id));

  const firstProject = byProject?.[0];

  return (
    <div className="grid items-start gap-6 max-sm:gap-4 min-[1200px]:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)]">
      <div className="flex min-w-0 flex-col gap-5 max-sm:gap-4">
        {attention.length > 0 && (
          <section aria-labelledby="att-h" className={card}>
            <div className={head}>
              <div className={headLeft}>
                <h2 id="att-h" className={h2}>Needs attention</h2>
                <span className={sum}>Registered as the workspace’s, but running in a project</span>
              </div>
            </div>
            <table aria-label="Services that need attention" className="w-full border-collapse text-[13px] leading-5 max-sm:block sm:table-fixed">
              <thead className="max-sm:hidden">
                <tr>
                  <th scope="col" className={cn(th, "w-[26%]")}>Service</th>
                  <th scope="col" className={cn(th, "w-[17%]")}>Status</th>
                  <th scope="col" className={th}>Why</th>
                  <th scope="col" className={cn(th, "w-[172px]")}><span className="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody className="max-sm:block">
                {attention.map(({ kind, svc }) => (
                  <AttentionRow
                    key={kind}
                    kind={kind}
                    label={LABEL[kind]}
                    svc={svc}
                    server={name(svc.server_id)}
                    where={place(svc)}
                    href={href(svc)}
                    returnFocus={stackHeading}
                    canEdit={canEdit}
                    canSet={canSet}
                  />
                ))}
              </tbody>
            </table>
          </section>
        )}

        {!stackKnown && (servicesError || configError) ? (
          <ReadFailed heading="h2" error={servicesError ?? configError} onRetry={() => { void refetchServices(); void refetchConfig(); }} />
        ) : isLoading || !stackKnown ? (
          <div role="status" className="h-48 rounded-[10px] bg-surface motion-safe:animate-pulse" aria-label="Loading logs and metrics" />
        ) : hasStack ? (
          <>
            {lokiSvc && elsewhere.length > 0 ? (
              <Banner
                {...localLogsWarning(inProse(name(lokiSvc.server_id)), elsewhere.map((x) => name(x.g.server_id)), elsewhere.reduce((n, x) => n + x.running.length, 0))}
                action={canSet ? () => setEditing("loki") : undefined}
              />
            ) : stuck.length > 0 ? (
              <Banner
                title={`Some services on ${new Intl.ListFormat("en").format(stuck)} don’t ship their logs yet`}
                text="The log driver is set when a container starts: they ship from their next deploy."
              />
            ) : null}

            <section aria-labelledby="logs-h" className={card}>
              <div className={head}>
                <div className={headLeft}>
                  <h2 id="logs-h" className={h2}>Logs</h2>
                  {placed && <span className={sum}>{shipping} of {running} {running === 1 ? "service ships" : "services ship"} to Loki</span>}
                </div>
                {grafanaUrl && (
                  <a href={grafanaUrl} target="_blank" rel="noopener noreferrer" className={cn(link, "touch-target inline-flex items-center gap-1 text-[13px]")}>
                    Logs in Grafana
                    <ExternalLink className="h-3 w-3" aria-hidden="true" />
                    <span className="sr-only">, opens in a new tab</span>
                  </a>
                )}
              </div>
              <table aria-label="Where each server’s logs go" className="w-full border-collapse text-[13px] leading-5 max-sm:block sm:table-fixed">
                <thead className="max-sm:hidden">
                  <tr>
                    <th scope="col" className={cn(th, "w-[26%]")}>Server</th>
                    <th scope="col" className={th}>Runs</th>
                    <th scope="col" className={cn(th, "w-[30%]")}>Logs go to</th>
                  </tr>
                </thead>
                <tbody className="max-sm:block">
                  {placedError ? (
                    <tr className={svcRow}>
                      <td colSpan={3} className={svcTd}>
                        <div role="alert" className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1.5">
                          Couldn’t read where the logs go.
                          <button type="button" className={btnSm} onClick={() => { void refetchLogs(); void refetchConfig(); }}>Retry</button>
                        </div>
                      </td>
                    </tr>
                  ) : logsLoading ? (
                    <tr className={svcRow}>
                      <td colSpan={3} className={cn(svcTd, "text-muted motion-safe:animate-pulse")}>Reading each server…</td>
                    </tr>
                  ) : (
                    groups.map(({ g, rows, running: here }) => {
                      const line = g.error ? { label: "Couldn’t read this server", tone: "muted" as const, why: g.error } : serverLogsLine(rows.map((r) => r.cell), at(g.server_id));
                      return (
                        <tr key={g.server_id || "panel"} className={svcRow}>
                          <td className={cn(svcTd, "max-sm:col-start-1 max-sm:row-start-1")}>
                            {g.server_id ? (
                              <Link href={`/servers/${g.server_id}`} className={cn(mono, "touch-target inline-flex items-center text-fg [overflow-wrap:anywhere] hover:underline")}>{name(g.server_id)}</Link>
                            ) : (
                              <span className={cn(mono, "text-fg")}>{name("")}</span>
                            )}
                          </td>
                          <td className={cn(svcTd, "max-sm:col-span-2 max-sm:row-start-2")}>
                            <span>{here.length} {here.length === 1 ? "service" : "services"}</span>
                            <div className={help}>{runsOn(g.server_id, svcs, envs ?? [], projects ?? [])}</div>
                          </td>
                          <td className={cn(svcTd, "max-sm:col-start-2 max-sm:row-start-1 max-sm:text-right")}>
                            <span className={cn("inline-flex items-center gap-1.5 font-medium", LOG_TONE[line.tone])}>
                              <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-current" />
                              {line.label}
                            </span>
                            <div className={help}>{line.why}</div>
                          </td>
                        </tr>
                      );
                    })
                  )}
                </tbody>
              </table>
              <p className={foot}>Where logs go is set at each deploy, so a new address reaches a service at its next one.</p>
            </section>
          </>
        ) : (
          <section aria-labelledby="empty-h" className={cn(card, "flex flex-wrap items-start gap-4 p-6")}>
            <div className="flex min-w-0 flex-[1_1_360px] flex-col gap-1.5">
              <h2 id="empty-h" className={h2}>No logs or metrics stack in {ws}</h2>
              <p className="max-w-[720px] text-[13px] text-fg/70">
                Loki keeps container logs, Prometheus scrapes metrics and Grafana shows both. Add them from the catalog as workspace services. Once Loki has
                an address every server reaches, Toft points every container it starts at it. Until then, logs stay on each server and show on each
                service’s Logs tab.
              </p>
              {canEdit && (
                <div className="mt-2 flex flex-wrap gap-2">
                  <Link href="/shared/new?template=loki" className={primaryButton}>
                    <Plus className="h-3.5 w-3.5" aria-hidden="true" />
                    Add Loki
                  </Link>
                  <Link href="/shared/new?template=grafana" className={cancelButton}>Add Grafana</Link>
                  <Link href="/shared/new?template=prometheus" className={cancelButton}>Add Prometheus</Link>
                </div>
              )}
            </div>
          </section>
        )}

        <section aria-labelledby="met-h" className={card}>
          <div className={head}>
            <div className={headLeft}>
              <h2 id="met-h" className={h2}>Metrics</h2>
              <span className={sum}>Toft measures usage itself; Prometheus is separate</span>
            </div>
          </div>
          <dl>
            <div className={kv}><dt className="text-muted">Measured</dt><dd className="text-fg">CPU and memory of every running service every 60 seconds, memory kills, and each server’s CPU waiting.</dd></div>
            <div className={kv}><dt className="text-muted">Kept</dt><dd className="text-fg">Samples for 48 hours; daily peaks, memory kills and CPU waiting for 30 days.</dd></div>
            <div className={kv}><dt className="text-muted">Shown</dt><dd className="text-fg">On each service’s Overview, each server’s page, and in memory limit suggestions.</dd></div>
            <div className={kv}>
              <dt className="text-muted">Prometheus</dt>
              <dd className="text-fg">
                {prometheus ? (
                  <>
                    Scrapes what its own <span className={mono}>prometheus.yml</span> lists; Toft adds nothing to it.{" "}
                    <Link href={`${href(prometheus)}?tab=config-files`} className="inline-link">Its config files</Link>
                  </>
                ) : stackKnown ? (
                  "Not deployed."
                ) : null}
              </dd>
            </div>
          </dl>
        </section>
      </div>

      <aside aria-label="The stack and each project" className="flex min-w-0 flex-col gap-5 max-sm:gap-4">
        {hasStack && (
          <section aria-labelledby="sk-h" className={card}>
            <div className={head}>
              <h2 id="sk-h" ref={stackHeading} tabIndex={-1} className={h2}>The stack</h2>
            </div>
            <dl>
              {stack.map(({ kind, svc }) => (
                <div key={kind} className={kv}>
                  <dt className="text-muted">{kind}</dt>
                  <dd className="text-fg [overflow-wrap:anywhere]">
                    <StackText
                      kind={kind}
                      svc={svc}
                      server={svc ? inProse(name(svc.server_id)) : ""}
                      where={svc ? place(svc) : ""}
                      address={kind === "grafana" ? cfg.grafana_url ?? "" : loki ?? ""}
                      local={localLokiAddress(logs)}
                      canEdit={canEdit}
                      configRead={!!config}
                    />
                    {canSet && config && svc?.scope === "org" && kind !== "prometheus" && (
                      <button type="button" className={cn(link, "touch-target mt-1 block text-xs")} onClick={() => setEditing(kind)}>
                        {(kind === "loki" ? loki : cfg.grafana_url) ? "Change address…" : kind === "loki" ? "Set an external address…" : "Set an address…"}
                      </button>
                    )}
                  </dd>
                </div>
              ))}
            </dl>
          </section>
        )}

        <section aria-labelledby="proj-h" className={card}>
          <div className={head}>
            <h2 id="proj-h" className={h2}>By project</h2>
          </div>
          {!byProject && byProjectError && (
            <div role="alert" className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1.5 px-4 py-2.5 text-[13px]">
              Couldn’t read each project’s logs and metrics.
              <button type="button" className={btnSm} onClick={() => void refetchByProject()}>Retry</button>
            </div>
          )}
          <dl>
            {(byProject ?? []).map((p) => {
              const own = p.loki_url || p.grafana_url;
              const note = !stackKnown
                ? ""
                : !svcs.some((s) => s.project_id === p.project_id)
                ? "No services"
                : own
                  ? `Its own ${[p.loki_url && "Loki", p.grafana_url && "Grafana"].filter(Boolean).join(" and ")}`
                  : hasStack
                    ? "The workspace’s Loki and Grafana"
                    : "No Loki or Grafana";
              return (
                <div key={p.project_id} className={kv}>
                  <dt className="min-w-0">
                    <Link href={`/projects/${p.project_id}/settings#s-obs`} aria-label={`Settings of ${p.name}`} className="touch-target inline-flex items-center text-muted [overflow-wrap:anywhere] hover:text-fg hover:underline">
                      {p.name}
                    </Link>
                  </dt>
                  <dd className={note === "No services" ? "text-muted" : "text-fg"}>{note}</dd>
                </div>
              );
            })}
            {byProject && byProject.length === 0 && <div className={kv}><dd className="text-muted">No projects yet.</dd></div>}
          </dl>
          {firstProject && (
            <p className={foot}>
              A project can use its own Loki and Grafana instead, in its <Link href={`/projects/${firstProject.project_id}/settings#s-obs`} className="inline-link">Settings</Link>.
            </p>
          )}
        </section>
      </aside>

      {editing && svcOf(editing) && (
        <AddressDialog kind={editing} svc={svcOf(editing)!} current={(editing === "loki" ? loki : cfg.grafana_url) ?? ""} onClose={() => setEditing(null)} />
      )}
    </div>
  );
}

function Banner({ title, text, action }: { title: string; text: string; action?: () => void }) {
  return (
    <div role="status" className="flex gap-2.5 rounded-[10px] border border-status-pending/40 bg-status-pending-soft/40 p-3">
      <AlertTriangle className="mt-0.5 h-4 w-4 flex-none text-status-pending-fg" aria-hidden="true" />
      <div className="flex min-w-0 flex-col gap-2">
        <p className="text-[13px] font-semibold leading-[19px] text-fg">{title}</p>
        <p className="text-[13px] leading-[19px] text-fg/85">{text}</p>
        {action && (
          <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
            <button type="button" className={btnSm} onClick={action}>Set an external Loki address…</button>
            <span className={help}>then each service ships from its next deploy</span>
          </div>
        )}
      </div>
    </div>
  );
}

const LABEL: Record<StackKind, string> = { loki: "Loki", prometheus: "Prometheus", grafana: "Grafana" };

function StackText({ kind, svc, server, where, address, local, canEdit, configRead }: {
  kind: StackKind;
  svc?: WorkspaceService;
  server: string;
  where: string;
  address: string;
  local: string;
  canEdit: boolean;
  configRead: boolean;
}) {
  if (!svc) {
    return (
      <>
        Not deployed.{" "}
        {canEdit && <Link href={`/shared/new?template=${kind}`} className="inline-link">Add</Link>}
      </>
    );
  }
  if (svc.scope !== "org") return <>Runs in {where}: it needs attention, above.</>;
  if (kind === "prometheus") return <>Scrapes its own targets.</>;
  if (kind === "grafana") {
    return address ? (
      <>Shows both, at <span className={mono}>{hostOf(address)}</span>.</>
    ) : (
      <>Shows both.{configRead && " No address yet."}</>
    );
  }
  const alias = !!address && isContainerAlias(address);
  return (
    <>
      Receives the logs of the containers Toft starts{" "}
      {address && !alias ? (
        <>at <span className={mono}>{hostOf(address)}</span></>
      ) : (
        <>on {server}{local && <>, at <span className={mono}>{local}</span> there</>}</>
      )}
      , labelled with container and service.
      {alias && <span className="text-status-pending-fg"> Its address, {address}, is an alias, which reaches no log driver.</span>}
    </>
  );
}

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

function AddressDialog({ kind, svc, current, onClose }: { kind: "loki" | "grafana"; svc: WorkspaceService; current: string; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [value, setValue] = useState(current);
  const save = useMutation({
    mutationFn: () => (kind === "loki" ? observabilityService.registerLoki(svc.id, value.trim()) : observabilityService.registerGrafana(svc.id, value.trim())),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["observability"] });
      queryClient.invalidateQueries({ queryKey: ["observability-logs"] });
      toast.success(kind === "loki" ? "Loki’s address saved: services ship to it from their next deploy" : "Grafana’s address saved");
      onClose();
    },
  });
  const label = kind === "loki" ? "Loki" : "Grafana";
  return (
    <Dialog open onOpenChange={(o) => !o && !save.isPending && onClose()}>
      <DialogContent showClose={false} className={dialogContent}>
        <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
          <DialogTitle className={dialogTitle}>{kind === "loki" ? "Set an external Loki address" : "Grafana’s address"}</DialogTitle>
          <DialogDescription className={cn(dialogText, "whitespace-normal text-left")}>
            {kind === "loki"
              ? "Every server’s Docker sends logs here from the host, so it has to resolve and answer from each server: a domain of the loki service, with its IP allowlist limited to your servers. Services pick it up at their next deploy. Toft adds /loki/api/v1/push."
              : "Where Open Grafana and the Logs in Grafana links go. Usually a domain of the grafana service."}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3 px-5 pb-5 pt-3"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <div className="flex flex-col gap-1.5">
            <label htmlFor={`f-${kind}`} className="text-[13px] font-medium text-fg">{possessive(label)} address</label>
            <input
              id={`f-${kind}`}
              type="url"
              required={kind === "loki"}
              value={value}
              onChange={(e) => setValue(e.target.value)}
              spellCheck={false}
              placeholder={kind === "loki" ? "https://loki.example.com" : "https://grafana.example.com"}
              className={cn(fieldInput, "h-[34px] font-mono")}
            />
          </div>
          {save.error && (
            <p role="alert" className="rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
              {save.error instanceof ApiError && save.error.status < 500 ? save.error.message : "The address was not saved."}
            </p>
          )}
          <div className="flex flex-wrap justify-end gap-2">
            <button type="button" className={cancelButton} disabled={save.isPending} onClick={onClose}>Cancel</button>
            <button type="submit" className={primaryButton} disabled={save.isPending}>{save.isPending ? "Saving…" : "Save"}</button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// A Loki, Grafana or Prometheus registered before only workspace services
// could be (D-162): it runs in a project, and Toft never moves it on its
// own. Moving keeps its data and redeploys it on the workspace's network;
// unregistering leaves it running as the project's.
function AttentionRow({ kind, label, svc, server, where, href, returnFocus, canEdit, canSet }: {
  kind: StackKind;
  label: string;
  svc: WorkspaceService;
  server: string;
  where: string;
  href: string;
  returnFocus: React.RefObject<HTMLHeadingElement | null>;
  canEdit: boolean;
  canSet: boolean;
}) {
  const queryClient = useQueryClient();
  const [ask, setAsk] = useState<"move" | "unregister" | null>(null);
  const done = () => {
    setAsk(null);
    for (const key of ["observability", "observability-logs", "workspace-services", "org-scoped-services"]) queryClient.invalidateQueries({ queryKey: [key] });
  };
  const move = useMutation({
    mutationFn: async () => {
      await serviceService.moveToWorkspace(svc.id);
      try {
        await deploymentService.trigger(svc.id, {});
        return true;
      } catch {
        return false;
      }
    },
    onSuccess: (deployed) => {
      done();
      if (deployed) toast.success(`${svc.name} is a workspace service: deploying it on the workspace’s network`);
      else toast.warning(`${svc.name} is a workspace service. Its deploy didn’t start: deploy it from its page to move it onto the workspace’s network.`);
    },
  });
  const unregister = useMutation({
    mutationFn: () => observabilityService.unregister(kind),
    onSuccess: () => {
      done();
      toast.success(`${svc.name} is no longer the workspace’s ${label}`);
    },
  });
  const m = ask === "move" ? move : unregister;
  return (
    <tr className={svcRow}>
      <td className={cn(svcTd, "max-sm:col-start-1 max-sm:row-start-1")}>
        <span className="flex min-w-0 flex-col">
          <Link href={href} className="font-medium text-fg [overflow-wrap:anywhere] hover:underline">{svc.name}</Link>
          <span className={cn(mono, "leading-[18px] text-muted [overflow-wrap:anywhere]")}>{svc.docker_image}</span>
        </span>
      </td>
      <td className={cn(svcTd, "whitespace-nowrap max-sm:col-start-2 max-sm:row-start-1 max-sm:text-right")}>
        <span className="flex flex-col gap-0.5 max-sm:items-end">
          <StatusText status={svc.runtime_status} />
          <span className={cn(mono, "leading-[18px] text-muted")}>{server}</span>
        </span>
      </td>
      <td className={cn(svcTd, "leading-[19px] text-fg/85 max-sm:col-span-2 max-sm:row-start-2")}>
        Registered as the workspace’s {label}, but it runs in {where}. Only a workspace service can be the workspace’s:{" "}
        {canSet ? "move it to the workspace, or unregister it." : canEdit ? "move it to the workspace; an owner can unregister it." : "a developer or owner can move it; an owner can unregister it."}
      </td>
      <td className={cn(svcTd, "whitespace-nowrap py-1.5 text-right max-sm:col-span-2 max-sm:row-start-3 max-sm:text-left")}>
        <span className="inline-flex flex-col items-stretch gap-2 max-sm:flex-row max-sm:flex-wrap">
          {canEdit && (
            <button type="button" aria-label={`Move to workspace: ${svc.name}`} className={cn(btnSm, "justify-center")} onClick={() => { move.reset(); setAsk("move"); }}>
              Move to workspace…
            </button>
          )}
          {canSet && (
            <button type="button" aria-label={`Unregister ${svc.name}`} className={cn(btnSm, "justify-center")} onClick={() => { unregister.reset(); setAsk("unregister"); }}>
              Unregister…
            </button>
          )}
        </span>
        <Dialog open={ask !== null} onOpenChange={(o) => !o && !m.isPending && setAsk(null)}>
          <DialogContent showClose={false} className={dialogContent} returnFocus={returnFocus}>
            <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
              <DialogTitle className={dialogTitle}>
                {ask === "move" ? `Move ${svc.name} to the workspace?` : `Unregister ${svc.name}?`}
              </DialogTitle>
              <DialogDescription className={cn(dialogText, "whitespace-normal text-left")}>
                {ask === "move"
                  ? `It keeps its data, domains and settings, and gets its own copy of the variables and secrets its environment shares with it. Toft then redeploys it on the workspace’s network, where every project’s services reach it by its alias.`
                  : `It keeps running in ${where}, as the project’s. The workspace has no ${label} until you add one as a workspace service.${kind === "loki" ? " Services that send their logs to it stop at their next deploy." : ""}`}
              </DialogDescription>
            </DialogHeader>
            {m.error && (
              <p role="alert" className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
                {m.error instanceof ApiError && m.error.status < 500 ? m.error.message : ask === "move" ? "It wasn’t moved. Try again." : "It wasn’t unregistered. Try again."}
              </p>
            )}
            <div className="flex flex-wrap justify-end gap-2 px-5 pb-5 pt-[18px]">
              <button type="button" className={cancelButton} disabled={m.isPending} onClick={() => setAsk(null)}>Cancel</button>
              <button type="button" className={primaryButton} disabled={m.isPending} onClick={() => m.mutate()}>
                {ask === "move" ? (move.isPending ? "Moving…" : "Move and redeploy") : unregister.isPending ? "Unregistering…" : "Unregister"}
              </button>
            </div>
          </DialogContent>
        </Dialog>
      </td>
    </tr>
  );
}
