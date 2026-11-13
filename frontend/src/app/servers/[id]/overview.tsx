"use client";

import { useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, CheckCircle2, ChevronRight, Info, RotateCw, XCircle } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { LoadFailed, Read } from "@/components/shell/unreachable";
import { formatMiB, type ServerResources } from "@/lib/api/resources.service";
import {
  dnsProviderService,
  serverService,
  type BackupDestination,
  type CapacitySummary,
  type DiskReport,
  type FitResult,
  type Server,
  type ServerShare,
  type ServiceOnServer,
} from "@/lib/api/server.service";
import { TONE_CLASSES, statusLabel, statusTone } from "@/lib/status";
import { ago } from "@/lib/time";
import { cn, formatDateTime, possessive } from "@/lib/utils";
import { btnSm, card, h2, help, sum } from "@/app/projects/_lib/ui";
import { ServiceMark } from "@/app/projects/_lib/service-table";
import { DISK_WARN_PCT, cores, diskGB, diskPct, diskUsedLabel, gib, liveRead, ownerParts, ownerSegments } from "@/components/servers/capacity";
import { gb, safeRows } from "@/components/servers/disk-view";
import { LegendItem, Meter, MeterHead } from "@/components/servers/resource-bar";
import { agentChip, agentFrozen, agentStatus, usesAgent } from "@/components/servers/agent";
import { traefikState } from "./aside";
import { attentionSummary, dnsProviderName, memoryLabel, type AttentionItem } from "./server-view";

// The server's Overview tab (board: private/design/canvas/ReworkServer.dc.html):
// Needs attention, Capacity and Running here, and four cards at a glance on
// the right, each opening its part of Settings.

export const cardHead = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3";
const tabHref = (id: string, tab: string, anchor: string) => `/servers/${id}?tab=${tab}#${anchor}`;

const LEVEL = {
  problem: { Icon: XCircle, tint: "text-status-failed", name: "Problem" },
  warning: { Icon: AlertTriangle, tint: "text-status-pending", name: "Warning" },
  info: { Icon: Info, tint: "text-muted", name: "Suggestion" },
} as const;

/** What needs someone on this server, worst first, each row opening where
 *  it is acted on. */
export function NeedsAttention({ serverId, items, reading, checkedAt, now }: { serverId: string; items: AttentionItem[]; reading: boolean; checkedAt?: string; now: number }) {
  const checked = checkedAt ? `checked ${ago(checkedAt, now)}` : "";
  return (
    <section aria-labelledby="att-h" className={card}>
      <div className={cardHead}>
        <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <h2 id="att-h" className={h2}>Needs attention</h2>
          <span className={sum}>{[items.length ? attentionSummary(items) : "", checked].filter(Boolean).join(" · ")}</span>
        </div>
      </div>
      {items.length === 0 && reading ? (
        <p className="px-4 py-3 text-[13px] text-muted">Checking…</p>
      ) : items.length === 0 ? (
        <div className="flex items-center gap-3 px-4 py-3 text-sm">
          <CheckCircle2 className="h-4 w-4 flex-none text-status-running" strokeWidth={2} aria-hidden="true" />
          <span className="font-medium">Nothing needs attention</span>
        </div>
      ) : (
        <div>
        {items.map((it) => {
          const { Icon, tint, name } = LEVEL[it.level];
          const body = (
            <>
              <Icon role="img" aria-label={name} className={cn("mt-0.5 h-4 w-4 flex-none", tint)} strokeWidth={2} />
              <span data-read-state={it.key === "unread" ? "failed" : undefined} className="min-w-0 flex-[1_1_260px]">
                <span className="block font-medium">{it.title}</span>
                {it.sub && <span className="block text-[13px] leading-[19px] text-muted [overflow-wrap:anywhere]">{it.sub}</span>}
              </span>
            </>
          );
          const row = "flex items-start gap-x-3 gap-y-2 border-t border-subtle px-4 py-3 text-sm text-fg first:border-t-0 sm:flex-wrap";
          return it.action && it.tab ? (
            <Link
              key={it.key}
              href={tabHref(serverId, it.tab, it.anchor ?? "")}
              className={cn(row, "max-sm:items-center hover:bg-raised/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus")}
            >
              {body}
              <span className={cn(btnSm, "max-sm:hidden")}>{it.action}</span>
              <ChevronRight className="h-4 w-4 flex-none text-muted sm:hidden" aria-hidden="true" />
            </Link>
          ) : (
            <div key={it.key} className={row}>{body}</div>
          );
        })}
        </div>
      )}
    </section>
  );
}

// ─── Capacity ────────────────────────────────────────────────────────────────

export function CapacityCard({
  server,
  isLocal,
  canManage,
  ownOrgId,
  ownName,
  colourOf,
  nameOf,
  otherIds,
  shares,
  retryShares,
  capacity,
  room,
  disk,
}: {
  server: Server;
  isLocal: boolean;
  canManage: boolean;
  ownOrgId: string;
  ownName: string;
  colourOf: (orgId: string) => string;
  nameOf: (orgId: string) => string;
  otherIds: string[];
  shares: ServerShare[];
  /** Set when the shares couldn't be read. */
  retryShares?: () => void;
  capacity: UseQueryResult<CapacitySummary>;
  room: FitResult | undefined;
  disk: DiskReport | undefined;
}) {
  const queryClient = useQueryClient();
  const summary = capacity.data;
  const known = !!summary?.capacity_discovered;
  const live = liveRead(summary);
  const diskUsed = diskPct(summary);
  const scan = useMutation({
    mutationFn: () => serverService.discoverCapacity(server.id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["server-capacity", server.id] });
      queryClient.invalidateQueries({ queryKey: ["server", server.id] });
      toast.success("Capacity scanned");
    },
    onError: (err: Error) => toast.error(err.message || "The scan failed: check the connection"),
  });

  const freeCPU = room?.capacity_known ? Math.max(0, room.effective_cpu_cores - room.allocated_cpu_cores) : null;
  const freeMB = room?.capacity_known ? Math.max(0, room.effective_memory_mb - room.allocated_memory_mb) : null;
  const segments = (total: number, r: "cpu" | "memory") => ownerSegments(total, ownOrgId, summary?.workspaces ?? [], shares, r);
  const aria = (r: "cpu" | "memory", value: string) => {
    const parts = ownerParts(ownOrgId, ownName, summary?.workspaces ?? [], shares, r);
    return `${r === "cpu" ? "CPU" : "Memory"}: ${parts.length ? `${parts.join(", ")}; ` : ""}${value} reserved`;
  };
  const safe = disk ? safeRows(disk).reduce((a, r) => a + r.bytes, 0) : 0;
  const meta =
    known && summary
      ? [`${cores(summary.effective_cpu_cores)} vCPU`, `${gib(summary.effective_memory_mb)} GiB`, summary.disk_total_mb ? diskGB(summary.disk_total_mb) : null]
          .filter(Boolean)
          .join(" · ") + (server.cpu_cores_override != null || server.memory_mb_override != null ? ", with overrides" : "")
      : null;

  return (
    <section aria-labelledby="cap-h" className={card}>
      <div className={cardHead}>
        <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <h2 id="cap-h" className={h2}>Capacity</h2>
          {meta && <span className="font-mono text-xs text-muted">{meta}</span>}
        </div>
        {canManage && !isLocal && (
          <button type="button" className={btnSm} disabled={scan.isPending} onClick={() => scan.mutate()}>
            <RotateCw className="h-[13px] w-[13px]" aria-hidden="true" />
            {scan.isPending ? "Scanning…" : known ? "Scan again" : "Scan"}
          </button>
        )}
      </div>
      <div className="flex flex-col gap-3.5 p-4">
        <Read query={capacity} what="capacity" loading={<p className="text-[13px] text-muted">Reading capacity…</p>}>{() => !known || !summary ? (
          <p className="text-[13px] text-muted">
            {isLocal
                ? "The panel host's capacity is not scanned."
                : "Not scanned yet: Scan reads its CPU and memory over SSH, so deploys can be checked against them."}
          </p>
        ) : (
          <>
            <ul className="flex flex-wrap gap-x-4 gap-y-1.5 text-xs text-fg/70" aria-label="Legend">
              <LegendItem swatch={colourOf(ownOrgId)} label={ownName} />
              {otherIds.map((o) => <LegendItem key={o} swatch={colourOf(o)} label={nameOf(o)} />)}
              {shares.map((s) => <LegendItem key={`aside-${s.org_id}`} swatch={cn(colourOf(s.org_id), "opacity-30")} label={`Set aside for ${s.org_name}`} />)}
              <LegendItem swatch="bg-subtle ring-1 ring-inset ring-strong" label="Free" />
            </ul>
            <div className="grid grid-cols-3 gap-5 max-sm:grid-cols-1 max-sm:gap-3.5">
              <Meter
                label="CPU reserved"
                value={`${cores(summary.allocated_cpu_cores)} of ${cores(summary.effective_cpu_cores)} vCPU`}
                aria={aria("cpu", `${cores(summary.allocated_cpu_cores)} of ${cores(summary.effective_cpu_cores)} vCPU`)}
                segments={segments(summary.effective_cpu_cores, "cpu")}
                colourOf={colourOf}
                sub={[
                  live ? `${cores((summary.live_cpu_percent / 100) * summary.effective_cpu_cores)} vCPU in use` : "Use not read yet",
                  freeCPU !== null && `${cores(freeCPU)} vCPU free`,
                ]}
              />
              <Meter
                label="Memory reserved"
                value={`${gib(summary.allocated_memory_mb)} of ${gib(summary.effective_memory_mb)} GiB`}
                aria={aria("memory", `${gib(summary.allocated_memory_mb)} of ${gib(summary.effective_memory_mb)} GiB`)}
                segments={segments(summary.effective_memory_mb, "memory")}
                colourOf={colourOf}
                sub={[live ? `${gib(summary.live_memory_mb)} GiB in use` : "Use not read yet", freeMB !== null && `${gib(freeMB)} GiB free`]}
              />
              {diskUsed !== null && summary.disk_total_mb ? (
                <div className="flex min-w-0 flex-col gap-2">
                  <MeterHead label="Disk used" value={diskUsedLabel(summary.disk_used_mb ?? 0, summary.disk_total_mb)} />
                  <span
                    role="img"
                    aria-label={`Disk: ${diskUsed}% used${diskUsed >= DISK_WARN_PCT ? ", warning" : ""}`}
                    className="flex h-2.5 overflow-hidden rounded-xs bg-subtle"
                  >
                    <span className={diskUsed >= DISK_WARN_PCT ? "bg-status-pending" : "bg-muted/60"} style={{ width: `${diskUsed}%` }} />
                  </span>
                  <span className={cn(help, diskUsed >= DISK_WARN_PCT && "text-status-pending-fg")}>
                    {disk
                      ? [
                          `${disk.volumes_in_use.count} ${disk.volumes_in_use.count === 1 ? "volume" : "volumes"}`,
                          safe > 0 && `${gb(safe)} safe to delete`,
                        ]
                          .filter(Boolean)
                          .join(" · ")
                      : `${diskUsed}% used`}
                  </span>
                </div>
              ) : null}
            </div>
            {retryShares && <LoadFailed what="what’s set aside for shares" onRetry={retryShares} />}
          </>
        )}</Read>
      </div>
    </section>
  );
}

// ─── Running here ────────────────────────────────────────────────────────────

function serviceHref(s: { id: string; project_id: string; scope: string }): string {
  return s.scope === "org" || !s.project_id ? `/shared/${s.id}` : `/projects/${s.project_id}/services/${s.id}`;
}

// A table at 640 px and up, a card per row below (the board's .lt).
const th = "border-b border-subtle px-4 py-2 text-left text-xs font-medium text-muted";
const tr = "max-sm:relative max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1 max-sm:border-t max-sm:border-subtle max-sm:px-4 max-sm:py-3 max-sm:first:border-t-0";
const td = "border-t border-subtle px-4 py-2.5 align-middle [tr:first-child>&]:border-t-0 max-sm:block max-sm:border-0 max-sm:p-0";
const tdName = cn(td, "max-sm:col-start-1 max-sm:row-start-1");
const tdRes = cn(td, "font-mono text-xs max-sm:col-span-2 max-sm:row-start-2");
const tdState = cn(td, "whitespace-nowrap max-sm:col-start-2 max-sm:row-start-1 max-sm:text-right");

/** Running here's rows before Show all. */
const FIRST_ROWS = 10;

function suggestionText(s: { direction: string; value: string }): string {
  return `${s.direction === "down" ? "↓" : s.direction === "up" ? "↑" : "Set"} ${memoryLabel(s.value)} suggested`;
}

export function RunningHere({
  ownOrgId,
  ownName,
  services,
  servicesError,
  summary,
  shares,
  resources,
}: {
  ownOrgId: string;
  ownName: string;
  services: ServiceOnServer[] | undefined;
  servicesError: boolean;
  summary: CapacitySummary | undefined;
  shares: ServerShare[];
  resources: ServerResources | undefined;
}) {
  const list = services ?? [];
  const [all, setAll] = useState(false);
  const shown = all ? list : list.slice(0, FIRST_ROWS);
  const others = (summary?.workspaces ?? []).filter((w) => w.org_id !== ownOrgId);
  // A project's environment is named only when it has more than one here.
  const envsOf = (pid: string) => new Set(list.filter((s) => s.project_id === pid && s.environment_name).map((s) => s.environment_name)).size;
  const total = list.length + others.reduce((a, w) => a + w.services, 0);
  const shareOf = (orgId: string) => shares.find((s) => s.org_id === orgId);
  const head = services
    ? [`${total} ${total === 1 ? "service" : "services"}`, ...others.map((w) => (shareOf(w.org_id) ? `${possessive(w.org_name)} share` : w.org_name))].join(" · ")
    : "";

  return (
    <section aria-labelledby="run-h" className={card}>
      <div className={cardHead}>
        <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <h2 id="run-h" className={h2}>Running here</h2>
          {head && <span className={sum}>{head}</span>}
        </div>
        <span className={sum}>Reserved is what each service’s limits set aside</span>
      </div>
      {servicesError ? (
        <p role="alert" className="px-4 py-3 text-[13px] text-status-failed-fg">The services could not be loaded.</p>
      ) : list.length === 0 && others.length === 0 ? (
        <p className="px-4 py-3 text-[13px] text-muted">{services ? `No ${ownName} services run here yet. Pick this server when you create a service to run it here.` : "Loading…"}</p>
      ) : (
        <table className="w-full border-collapse text-[13px] max-sm:block">
          <thead className="max-sm:hidden">
            <tr>
              <th scope="col" className={th}>Service</th>
              <th scope="col" className={th}>Reserved</th>
              <th scope="col" className={th}>Status</th>
            </tr>
          </thead>
          <tbody className="max-sm:block">
            {shown.map((s) => {
              const sugs = (resources?.services ?? []).filter((r) => r.service_id === s.id);
              return (
                <tr key={s.id} className={tr}>
                  <td className={tdName}>
                    <Link href={serviceHref(s)} className="inline-flex min-w-0 items-center gap-2 text-fg hover:underline max-sm:after:absolute max-sm:after:inset-0">
                      <ServiceMark className="mt-0 self-center" />
                      <span className="text-muted max-[1099px]:hidden">{s.project_name || "Workspace"} /</span>
                      <span className="font-medium">{s.name}</span>
                      {s.project_id && envsOf(s.project_id) > 1 && s.environment_name !== s.name && <span className="text-xs text-muted">{s.environment_name}</span>}
                    </Link>
                  </td>
                  <td className={tdRes}>
                    <span className="sr-only sm:hidden">Reserved: </span>
                    {s.cpu_limit ? `${s.cpu_limit} vCPU` : "no CPU limit"}
                    {" · "}
                    <span className={cn(!s.memory_limit && "text-status-pending-fg")}>{s.memory_limit ? memoryLabel(s.memory_limit) : "no memory limit"}</span>
                    {sugs.map((r) => (
                      <span
                        key={r.compose_service}
                        title={r.suggestion.reason}
                        className="ml-2 inline-flex rounded-sm border border-subtle bg-raised px-1.5 font-sans text-[11px] leading-[18px] text-fg/80"
                      >
                        {r.compose_service && `${r.compose_service} `}
                        {suggestionText(r.suggestion)}
                        <span className="sr-only">: {r.suggestion.reason}</span>
                      </span>
                    ))}
                  </td>
                  <td className={tdState}>
                    <span className="sr-only sm:hidden">Status: </span>
                    <span className={cn("inline-flex items-center gap-1.5 font-medium", TONE_CLASSES[statusTone(s.runtime_status)].text)}>
                      <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-current" />
                      {statusLabel(s.runtime_status)}
                    </span>
                  </td>
                </tr>
              );
            })}
            {others.map((w) => {
              const share = shareOf(w.org_id);
              return (
                <tr key={w.org_id} className={tr}>
                  <td className={tdName}>
                    <span className="inline-flex min-w-0 items-center gap-2">
                      <ServiceMark className="mt-0 self-center" />
                      <span className="text-muted max-[1099px]:hidden">{w.org_name} /</span>
                      <span className="font-medium text-fg">
                        {w.projects.length > 0 && `${w.projects.join(", ")}, `}
                        {w.services} {w.services === 1 ? "service" : "services"}
                      </span>
                    </span>
                  </td>
                  <td className={tdRes}>
                    <span className="sr-only sm:hidden">Reserved: </span>
                    {cores(w.cpu_cores)} {share && `of ${cores(share.cpu_cores)} `}vCPU · {gib(w.memory_mb)} {share && `of ${gib(share.memory_mb)} `}GiB
                  </td>
                  <td className={tdState}>
                    <span className="sr-only sm:hidden">Status: </span>
                    <span className="inline-flex items-center gap-1.5 font-medium text-muted">
                      <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-current" />
                      {share ? `${possessive(w.org_name)} share` : "Share removed"}
                    </span>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      {list.length > FIRST_ROWS && (
        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
          <span>{shown.length} of {list.length} shown</span>
          <button type="button" className="touch-target text-link hover:underline" aria-expanded={all} onClick={() => setAll(!all)}>
            {all ? "Show fewer" : `Show all ${list.length}`}
          </button>
        </div>
      )}
      {summary?.capacity_discovered && (
        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
          <span className="font-mono text-xs text-fg/80">
            {cores(summary.allocated_cpu_cores)} of {cores(summary.effective_cpu_cores)} vCPU · {gib(summary.allocated_memory_mb)} of {gib(summary.effective_memory_mb)} GiB reserved
          </span>
          {resources && resources.frees_mb > 0 && <span>Following the suggestions frees {formatMiB(resources.frees_mb)}</span>}
        </div>
      )}
    </section>
  );
}

// ─── At a glance ─────────────────────────────────────────────────────────────

const STATE_TEXT = {
  running: "text-status-running-fg",
  pending: "text-status-pending-fg",
  failed: "text-status-failed-fg",
  building: "text-status-building-fg",
  muted: "text-muted",
} as const;

function Glance({
  title,
  state,
  rows,
  link,
}: {
  title: string;
  state: { label: string; tone: keyof typeof STATE_TEXT };
  rows: [string, React.ReactNode][];
  link: { label: string; href: string };
}) {
  return (
    <section aria-label={title} className={card}>
      <div className={cardHead}>
        <h2 className={h2}>{title}</h2>
        <span className={cn("inline-flex items-center gap-1.5 text-xs font-medium", STATE_TEXT[state.tone])}>
          <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-current" />
          {state.label}
        </span>
      </div>
      <dl>
        {rows.map(([k, v], i) => (
          <div key={k} className={cn("grid grid-cols-[120px_minmax(0,1fr)] gap-3 px-4 py-2.5 text-[13px] max-sm:grid-cols-1 max-sm:gap-0.5", i > 0 && "border-t border-subtle")}>
            <dt className="text-muted">{k}</dt>
            <dd className="min-w-0 [overflow-wrap:anywhere]">{v}</dd>
          </div>
        ))}
      </dl>
      <div className="flex justify-end border-t border-subtle px-4 py-2.5">
        <Link href={link.href} className="text-[13px] text-link underline-offset-4 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus">
          {link.label}
        </Link>
      </div>
    </section>
  );
}

export function ConnectionGlance({ server }: { server: Server }) {
  const agent = agentStatus(server);
  const href = tabHref(server.id, "settings", "connection");
  if (usesAgent(server) && agent) {
    const chip = agentChip(agent, agentFrozen(server));
    const tone = agent === "connected" && !agentFrozen(server) ? "running" : chip.tone;
    return (
      <Glance
        title="Connection"
        state={{ label: agent === "connected" ? "Agent" : chip.label, tone }}
        rows={[
          ["Agent", <span key="a" className="font-mono text-xs">{server.agent?.version ? `${server.agent.version} · outbound 443` : agent === "waiting" ? "not joined yet" : "—"}</span>],
          ["Address", <span key="b" className="font-mono text-xs">{server.ip_address || "—"}</span>],
        ]}
        link={{ label: "Connection settings", href }}
      />
    );
  }
  const pinned = server.host_key_pinned_at;
  return (
    <Glance
      title="Connection"
      state={{ label: "SSH", tone: server.status === "active" ? "running" : "failed" }}
      rows={[
        ["Host key", pinned ? `Pinned ${formatDateTime(pinned).slice(0, 10)}` : <span key="h" className="text-status-pending-fg">Not pinned yet</span>],
        ["Address", <span key="a"><span className="font-mono text-xs">{server.ip_address}</span> · port {server.port}</span>],
      ]}
      link={{ label: "Connection settings", href }}
    />
  );
}

export function TraefikGlance({ server, summary }: { server: Server; summary: CapacitySummary | undefined }) {
  const { data: dns, error } = useQuery({ queryKey: ["dns-provider", server.id], queryFn: () => dnsProviderService.get(server.id), retry: false });
  const noDNS = error instanceof ApiError && error.status === 404;
  return (
    <Glance
      title="Traefik"
      state={traefikState(summary)}
      rows={[
        ["Listens on", <span key="l" className="font-mono text-xs">80, 443</span>],
        ["Certificates", dns ? <>HTTP-01, and DNS-01 via {dnsProviderName(dns.provider) ?? <span className="font-mono text-xs">{dns.provider}</span>}</> : noDNS ? "HTTP-01; no DNS provider for wildcards" : "HTTP-01"],
      ]}
      link={{ label: "Traefik and DNS", href: tabHref(server.id, "settings", "traefik") }}
    />
  );
}

export function BackupGlance({ serverId, backup, now }: { serverId: string; backup: BackupDestination | null | undefined; now: number }) {
  const href = tabHref(serverId, "settings", "backup");
  if (backup === undefined) return null;
  if (backup === null) {
    return (
      <Glance
        title="Backups"
        state={{ label: "Not set up", tone: "muted" }}
        rows={[["Volumes", "Nothing backs them up: if the disk is lost, the data goes with it."]]}
        link={{ label: "Set up backups", href }}
      />
    );
  }
  const state =
    backup.last_ok === true && backup.last_ok_at
      ? { label: `Succeeded ${ago(backup.last_ok_at, now)}`, tone: "running" as const }
      : backup.last_ok === false && backup.last_run_at
        ? { label: `Failed ${ago(backup.last_run_at, now)}`, tone: "failed" as const }
        : { label: "Not run yet", tone: "muted" as const };
  return (
    <Glance
      title="Backups"
      state={state}
      rows={[
        ["Next run", <><span className="font-mono text-xs">{backup.schedule}</span> daily</>],
        ["Keeps", "7 daily · 4 weekly · 6 monthly"],
      ]}
      link={{ label: "Backup settings", href }}
    />
  );
}

