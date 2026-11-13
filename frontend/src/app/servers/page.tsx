"use client";

import { useState } from "react";
import Link from "next/link";
import { AlertTriangle, ChevronRight, Circle, Info, Plus, Server as ServerIcon, XCircle } from "lucide-react";
import { useQuery, useQueries, type UseQueryResult } from "@tanstack/react-query";
import {
  backupPath,
  backupService,
  diskService,
  serverService,
  type CapacitySummary,
  type FitResult,
  type Server,
  type ServerShare,
} from "@/lib/api/server.service";
import { formatMiB, resourcesService } from "@/lib/api/resources.service";
import { inboxService } from "@/lib/api/inbox.service";
import { OpenInOwner } from "@/components/servers/open-in-owner";
import { TONE, agentChip, agentFrozen, agentStatus, clock, usesAgent, waitingForAgent } from "@/components/servers/agent";
import { PanelServerCard, PanelServerNote } from "@/components/servers/panel-server";
import { Chip, Meter, MeterHead, OTHER_COLOURS, OWN_COLOUR, SharedChip, online } from "@/components/servers/resource-bar";
import { cn } from "@/lib/utils";
import { useDocumentTitle } from "@/lib/use-document-title";
import { useAuth, useIsOperator, useOrgRole } from "@/context/AuthContext";
import { Read, readFailed } from "@/components/shell/unreachable";
import {
  DISK_WARN_PCT,
  cores,
  diskPct,
  diskUsedLabel,
  fleetTotals,
  gib,
  isOnline,
  liveRead,
  ownerParts,
  ownerSegments,
  parseCores,
  parseMemoryMB,
  runsLine,
  serverProblems,
  type Problem,
  type Segment,
} from "@/components/servers/capacity";
import { btnSm, card, field, h1, h2, help, label, primary, sum } from "../projects/_lib/ui";

// Servers (board: private/design/canvas/ReworkServers.dc.html, D-136). One
// block per app server: its state, three meters, what runs there and its
// problems in words; the capacity totals and Will it fit? on the right.
// Capacity counts what services reserve, their limits, not only what they
// use; whether something fits is the API's fit check, the deploy capacity
// gate's own arithmetic (D-15, D-39, D-41). Drain and Host shell live on the
// server's own page.

const cardHead = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3";
const link = "touch-target relative z-10 inline-flex items-center text-[13px] text-link underline-offset-4 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus";

/** A row of the Capacity card that can't be added up, and why; null once
 *  every server's figure is in. A failed refetch keeps its figure. */
const unreadNote = (reads: { data: unknown; isError: boolean; errorUpdateCount: number }[]) =>
  !reads.some((r) => r.data === undefined) ? null : reads.some(readFailed) ? "Couldn’t read every server" : "Reading…";

export default function ServersPage() {
  const role = useOrgRole();
  const canManageServers = role === "owner";
  // Who acts on Maintenance and Settings, as on a server's own page.
  const isOperator = useIsOperator();
  const canAdmin = canManageServers || isOperator;
  const { user } = useAuth();
  const ownOrgId = user?.orgId ?? "";
  const ownName = user?.orgs?.find((o) => o.org_id === ownOrgId)?.name ?? "This workspace";
  useDocumentTitle(`Servers · ${user?.orgName ? `${user.orgName} · ` : ""}Toft`);

  const list = useQuery({
    queryKey: ["servers"],
    queryFn: () => serverService.list(),
  });
  const serversData = list.data;
  // Servers waiting for their first agent are one line under the list (D-153).
  const waiting = (serversData ?? []).filter(waitingForAgent);
  const servers = (serversData ?? []).filter((s) => !waitingForAgent(s));

  // Per server: the same keys as the server page, so the two share a cache.
  const summaries = useQueries({
    queries: servers.map((s) => ({
      queryKey: ["server-capacity", s.id],
      queryFn: () => serverService.getCapacitySummary(s.id),
      refetchInterval: 30_000,
    })),
  });
  const rooms = useQueries({
    queries: servers.map((s) => ({ queryKey: ["server-fit", s.id, 0, 0], queryFn: () => serverService.fit(s.id) })),
  });
  const shares = useQueries({
    queries: servers.map((s) => ({
      queryKey: ["server-shares", s.id],
      queryFn: () => serverService.listShares(s.id),
      enabled: !s.shared,
    })),
  });

  // Every other workspace on these servers gets a colour, in order of appearance.
  const others: { id: string; name: string }[] = [];
  const noteOther = (id: string, name: string) => {
    if (id !== ownOrgId && !others.some((o) => o.id === id)) others.push({ id, name });
  };
  servers.forEach((_, i) => {
    shares[i].data?.forEach((sh) => noteOther(sh.org_id, sh.org_name));
    summaries[i].data?.workspaces?.forEach((w) => noteOther(w.org_id, w.org_name));
  });
  const colourOf = (orgId: string) =>
    orgId === ownOrgId ? OWN_COLOUR : OTHER_COLOURS[Math.max(0, others.findIndex((o) => o.id === orgId)) % OTHER_COLOURS.length];

  const totals = fleetTotals(servers.map((s, i) => ({ name: s.name, summary: summaries[i].data, room: rooms[i].data })));
  // A share's unused part shows faded only where some of it is unused.
  const aside = others.filter((o) =>
    servers.some((_, i) =>
      (shares[i].data ?? []).some((sh) => {
        if (sh.org_id !== o.id) return false;
        const w = summaries[i].data?.workspaces?.find((x) => x.org_id === sh.org_id);
        return (w?.cpu_cores ?? 0) < sh.cpu_cores || (w?.memory_mb ?? 0) < sh.memory_mb;
      }),
    ),
  );
  const onlyShares = servers.length > 0 && servers.every((s) => s.shared);
  const has = servers.length > 0;

  return (
    <div className="flex w-full max-w-[1600px] flex-col gap-5 pb-14 max-sm:gap-4 md:-mt-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 flex-wrap items-center gap-x-3 max-sm:flex-col max-sm:items-start max-sm:gap-0.5">
          <h1 className={h1}>Servers</h1>
          {/* Nothing until loaded: "0 app servers" would be a false fact. */}
          <span className={sum}>{serversData ? `${servers.length} ${servers.length === 1 ? "app server" : "app servers"}` : " "}</span>
        </div>
        {canManageServers && (
          <Link href="/servers/new" className={cn(primary, "max-sm:w-full max-sm:justify-center")}>
            <Plus className="h-[15px] w-[15px]" aria-hidden="true" />
            Add server
          </Link>
        )}
      </div>

      <Read
        query={list}
        what="the servers"
        heading="h2"
        loading={
          <div role="status" className="h-64 rounded-[10px] bg-surface motion-safe:animate-pulse">
            <span className="sr-only">Loading servers…</span>
          </div>
        }
      >{() => (
        <div className="grid items-start gap-6 max-lg:grid-cols-1 max-sm:gap-4 lg:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)]">
          <section aria-labelledby="sv-h" className={card}>
            <div className={cardHead}>
              <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
                <h2 id="sv-h" className={h2}>App servers</h2>
                <span className={sum}>reserved is what services’ limits set aside</span>
              </div>
              {has && (
                <ul className="flex flex-wrap gap-x-4 gap-y-1.5 text-xs text-fg/70 max-sm:w-full" aria-label="Legend">
                  <Swatch swatch={OWN_COLOUR} label={ownName} />
                  {others.map((o) => <Swatch key={o.id} swatch={colourOf(o.id)} label={o.name} />)}
                  {aside.map((o) => <Swatch key={`aside-${o.id}`} swatch={cn(colourOf(o.id), "opacity-30")} label={`Set aside for ${o.name}`} />)}
                  <Swatch swatch="bg-subtle ring-1 ring-inset ring-strong" label="Free" />
                </ul>
              )}
            </div>
            {has ? (
              servers.map((s, i) => (
                <ServerBlock
                  key={s.id}
                  server={s}
                  capacity={summaries[i]}
                  room={rooms[i].data}
                  shares={shares[i].data ?? []}
                  ownOrgId={ownOrgId}
                  ownName={ownName}
                  colourOf={colourOf}
                  canAdmin={canAdmin}
                />
              ))
            ) : (
              <div className="flex flex-col items-center gap-2 px-6 py-14 text-center">
                <ServerIcon className="h-6 w-6 text-muted" aria-hidden="true" />
                <h3 className="text-[15px] font-semibold text-fg">No servers yet</h3>
                <p className="text-[13px] text-muted">Connect a server to deploy services to it.</p>
              </div>
            )}
            <PanelServerCard />
            <WaitingLine waiting={waiting} me={user?.id} />
            <PanelServerNote />
          </section>

          {has && (
            <aside className="flex min-w-0 flex-col gap-5 max-sm:gap-4">
              <CapacityCard totals={totals} onlyShares={onlyShares} capacityNote={unreadNote(summaries)} roomNote={unreadNote(rooms)} />
              <FitCard servers={servers} />
            </aside>
          )}
        </div>
      )}</Read>
    </div>
  );
}

function Swatch({ swatch, label }: { swatch: string; label: string }) {
  return (
    <li className="inline-flex items-center gap-1.5">
      <span aria-hidden="true" className={cn("h-2.5 w-2.5 rounded-xs", swatch)} />
      {label}
    </li>
  );
}

/** "1 server waiting for its agent · server-4, made by you at 14:42 · Continue":
 *  Continue opens Add a server on the caller's own, or else the first. */
function WaitingLine({ waiting, me }: { waiting: Server[]; me?: string }) {
  if (!waiting.length) return null;
  const next = waiting.find((s) => s.user_id === me) ?? waiting[0];
  const n = waiting.length;
  return (
    <Link
      href={`/servers/new?server=${next.id}`}
      className="flex min-h-11 flex-wrap items-center gap-x-2.5 gap-y-1.5 border-t border-subtle px-4 py-2.5 text-[13px] leading-[19px] text-fg/70 first:border-t-0 hover:bg-raised focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus"
    >
      <Circle className="h-[15px] w-[15px] flex-none text-status-building" strokeWidth={2.2} aria-hidden="true" />
      <span className="min-w-0 [overflow-wrap:anywhere]">
        <span className="font-medium text-fg">{n === 1 ? "1 server waiting for its agent" : `${n} servers waiting for their agents`}</span>
        {" · "}
        {n === 1 ? `${next.name}, made ${next.user_id === me ? "by you " : ""}at ${clock(next.created_at)}` : waiting.map((s) => s.name).join(", ")}
      </span>
      <span className="flex-1" />
      <span>Continue</span>
    </Link>
  );
}

function ServerBlock({
  server,
  capacity,
  room,
  shares,
  ownOrgId,
  ownName,
  colourOf,
  canAdmin,
}: {
  server: Server;
  capacity: UseQueryResult<CapacitySummary>;
  room?: FitResult;
  shares: ServerShare[];
  ownOrgId: string;
  ownName: string;
  colourOf: (orgId: string) => string;
  canAdmin: boolean;
}) {
  const summary = capacity.data;
  const agent = agentStatus(server);
  const frozen = agentFrozen(server);
  const agentState = agent && (agent !== "connected" || frozen) ? agentChip(agent, frozen) : null;
  const state = agentState ? { label: agentState.label, text: TONE[agentState.tone].text, pulse: agentState.pulse } : { ...online(server), pulse: false };
  const known = !!summary?.capacity_discovered;
  const guest = !!server.shared;
  const freeCPU = room?.capacity_known ? Math.max(0, room.effective_cpu_cores - room.allocated_cpu_cores) : null;
  const freeMB = room?.capacity_known ? Math.max(0, room.effective_memory_mb - room.allocated_memory_mb) : null;
  const live = liveRead(summary);
  const disk = diskPct(summary);

  const resources = useQuery({
    queryKey: ["server-resources", server.id],
    queryFn: () => resourcesService.server(server.id),
    enabled: !guest,
  }).data;
  const backup = useQuery({
    queryKey: ["backup", backupPath(server.id)],
    queryFn: () => backupService.get(backupPath(server.id)),
    enabled: !guest,
    retry: false,
  });
  // The disk report is read from the server at most every 5 minutes.
  const diskReport = useQuery({
    meta: { quietFailure: "Only which of two facts sits under the disk bar: without the report it says the percentage it has." },
    queryKey: ["disk", server.id],
    queryFn: () => diskService.get(server.id),
    enabled: !guest && isOnline(server),
    retry: false,
  }).data;
  // The Inbox's "nothing backs up" item: the same keys as the server page's row.
  const needs = useQuery({ queryKey: ["inbox", "needs-you", "any"], queryFn: () => inboxService.needsYou("any"), enabled: !guest });
  const noBackup = needs.data?.find((it) => it.kind === "no_backup" && it.server_id === server.id);

  const problems = guest
    ? []
    : serverProblems({
        online: isOnline(server),
        summary,
        room,
        suggestions: resources?.services.length ?? 0,
        freesMB: resources?.frees_mb,
        cpuWait: resources?.cpu_wait,
        provisionWarnings: server.provision_warnings,
        backup: backup.isError ? undefined : backup.data,
        noBackup: noBackup?.detail,
        disk: diskReport,
        now: backup.dataUpdatedAt,
      });

  const segments = (total: number, r: "cpu" | "memory", allocated: number): Segment[] =>
    guest
      ? [{ orgId: ownOrgId, pct: total > 0 ? Math.min(100, (allocated / total) * 100) : 0, setAside: false }]
      : ownerSegments(total, ownOrgId, summary?.workspaces ?? [], shares, r);
  // The bar in words: each workspace's part when another one is there.
  const aria = (r: "cpu" | "memory", value: string) => {
    const parts = guest ? [] : ownerParts(ownOrgId, ownName, summary?.workspaces ?? [], shares, r);
    return `${r === "cpu" ? "CPU" : "Memory"}: ${parts.length ? `${parts.join(", ")}; ` : ""}${value} reserved`;
  };
  const meta = guest
    ? known && summary ? `${cores(summary.effective_cpu_cores)} vCPU · ${gib(summary.effective_memory_mb)} GiB share` : ""
    : [
        known && summary ? `${cores(summary.effective_cpu_cores)} vCPU` : null,
        known && summary ? `${gib(summary.effective_memory_mb)} GiB` : null,
        usesAgent(server) ? (server.agent?.version ? `agent ${server.agent.version}` : "agent") : "SSH",
      ].filter(Boolean).join(" · ");

  return (
    // The whole block opens the server (D-70): its name's link is stretched
    // over it, and what else in it is a link or a button sits above that.
    <article aria-labelledby={`srv-${server.id}`} className="relative flex flex-col gap-3 border-t border-subtle p-4 first-of-type:border-t-0 has-[a.card-link:hover]:bg-raised/40">
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <h3 id={`srv-${server.id}`} className="font-mono text-[15px] font-semibold leading-[22px] text-fg [overflow-wrap:anywhere]">
            {guest ? (
              server.name
            ) : (
              <Link
                href={`/servers/${server.id}`}
                className="card-link rounded-sm after:absolute after:inset-0 focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-inset focus-visible:after:ring-focus"
              >
                {server.name}
              </Link>
            )}
          </h3>
          <span className={cn("inline-flex items-center gap-1.5 text-[13px] font-medium", state.text)}>
            <span aria-hidden="true" className={cn("h-1.5 w-1.5 rounded-full bg-current", state.pulse && "motion-safe:animate-pulse")} />
            {state.label}
          </span>
          {guest ? (
            <Chip>Shared by {server.shared_by}</Chip>
          ) : shares.length > 0 ? (
            <SharedChip names={shares.map((s) => s.org_name)} />
          ) : null}
        </div>
        {guest ? (
          <OpenInOwner server={server} className="relative z-10" />
        ) : (
          <Link href={`/servers/${server.id}`} tabIndex={-1} className={cn(btnSm, "relative z-10 gap-1 pr-2 max-sm:hidden")}>
            Open<span className="sr-only"> {server.name}</span>
            <ChevronRight className="h-3.5 w-3.5" aria-hidden="true" />
          </Link>
        )}
      </div>
      {(meta || !guest) && (
        <p className="-mt-2 font-mono text-xs leading-[18px] text-muted">
          {!guest && server.ip_address && <span className="max-sm:hidden">{server.ip_address}{meta && " · "}</span>}
          {meta}
        </p>
      )}

      <Read query={capacity} what={`the capacity of ${server.name}`} loading={<p className="text-[13px] text-muted">Reading capacity…</p>}>{() => !known || !summary ? (
        <p className="text-[13px] text-muted">
          Capacity not scanned yet: open the server and Scan, so deploys can be checked against it.
        </p>
      ) : (
        <div className="grid grid-cols-3 gap-5 max-sm:grid-cols-1 max-sm:gap-3.5">
          <Meter
            label="CPU reserved"
            value={`${cores(summary.allocated_cpu_cores)} of ${cores(summary.effective_cpu_cores)} vCPU`}
            aria={aria("cpu", `${cores(summary.allocated_cpu_cores)} of ${cores(summary.effective_cpu_cores)} vCPU`)}
            segments={segments(summary.effective_cpu_cores, "cpu", summary.allocated_cpu_cores)}
            colourOf={colourOf}
            sub={[
              live && `${cores((summary.live_cpu_percent / 100) * summary.effective_cpu_cores)} vCPU in use`,
              !guest && !live && "Use not read yet",
              freeCPU !== null && `${cores(freeCPU)} free`,
            ]}
          />
          <Meter
            label="Memory reserved"
            value={`${gib(summary.allocated_memory_mb)} of ${gib(summary.effective_memory_mb)} GiB`}
            aria={aria("memory", `${gib(summary.allocated_memory_mb)} of ${gib(summary.effective_memory_mb)} GiB`)}
            segments={segments(summary.effective_memory_mb, "memory", summary.allocated_memory_mb)}
            colourOf={colourOf}
            sub={[
              live && `${gib(summary.live_memory_mb)} GiB in use`,
              !guest && !live && "Use not read yet",
              freeMB !== null && `${gib(freeMB)} free`,
            ]}
          />
          {!guest && disk !== null && summary.disk_total_mb ? (
            <div className="flex min-w-0 flex-col gap-2">
              <MeterHead label="Disk used" value={diskUsedLabel(summary.disk_used_mb ?? 0, summary.disk_total_mb)} />
              <span
                role="img"
                aria-label={`Disk: ${disk}% used${disk >= DISK_WARN_PCT ? ", warning" : ""}`}
                className="flex h-2.5 overflow-hidden rounded-xs bg-subtle"
              >
                <span className={disk >= DISK_WARN_PCT ? "bg-status-pending" : "bg-muted/60"} style={{ width: `${disk}%` }} />
              </span>
              <span className={cn(help, disk >= DISK_WARN_PCT && "text-status-pending-fg")}>
                {disk >= DISK_WARN_PCT || !diskReport
                  ? `${disk}% used`
                  : `${diskReport.volumes_in_use.count} ${diskReport.volumes_in_use.count === 1 ? "volume" : "volumes"}`}
              </span>
            </div>
          ) : null}
        </div>
      )}</Read>

      {!guest && summary && <p className="text-[13px] leading-[18px] text-fg/70 [overflow-wrap:anywhere]">{runsLine(ownOrgId, summary.workspaces ?? [])}</p>}

      {problems.map((p) => (
        <ProblemRow key={p.kind} problem={p} server={server} canAdmin={canAdmin} />
      ))}
    </article>
  );
}

const LEVEL = {
  problem: { Icon: XCircle, tint: "text-status-failed", name: "Problem" },
  warning: { Icon: AlertTriangle, tint: "text-status-pending", name: "Warning" },
  info: { Icon: Info, tint: "text-muted", name: "Suggestion" },
} as const;

/** One problem in words, with where to fix it. Without canAdmin the link is
 *  Review: the card it opens doesn't offer the act. */
function ProblemRow({ problem, server, canAdmin }: { problem: Problem; server: Server; canAdmin: boolean }) {
  const { Icon, tint, name } = LEVEL[problem.level];
  const where: Record<Problem["kind"], [string, string] | null> = {
    backup: ["Backup settings", "#backup"],
    no_backup: ["Set up backups", "#backup"],
    disk: ["Free space…", "#disk"],
    provision: ["Re-provision", "#provisioning"],
    cpu_wait: null,
    room: null,
    suggestions: ["Review limits", ""],
  };
  const to = where[problem.kind];
  return (
    <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 rounded-lg border border-subtle px-3 py-2 text-[13px] leading-[19px] text-fg/80">
      <Icon role="img" aria-label={name} className={cn("h-[15px] w-[15px] flex-none", tint)} strokeWidth={2} />
      <span className="min-w-0 flex-[1_1_240px] [overflow-wrap:anywhere]">{problem.text}</span>
      {to && (
        <Link href={`/servers/${server.id}${to[1]}`} className={link}>
          {canAdmin ? to[0] : "Review"}
          <span className="sr-only"> of {server.name}</span>
        </Link>
      )}
    </div>
  );
}

function CapacityCard({ totals, onlyShares, capacityNote, roomNote }: { totals: ReturnType<typeof fleetTotals>; onlyShares: boolean; capacityNote: string | null; roomNote: string | null }) {
  // A share's guest never sees the server's own use (D-39).
  const noLive = onlyShares ? "Shares: use is measured per server" : "Use right now could not be read";
  const rows: [string, string, string][] = [
    ["CPU reserved", capacityNote ? "—" : `${cores(totals.cpuReserved)} of ${cores(totals.cpuTotal)} vCPU`, capacityNote ?? (totals.liveRead ? `${cores(totals.cpuLive)} vCPU in use right now` : noLive)],
    ["Memory reserved", capacityNote ? "—" : `${gib(totals.memReservedMB)} of ${gib(totals.memTotalMB)} GiB`, capacityNote ?? (totals.liveRead ? `${gib(totals.memLiveMB)} GiB in use right now` : noLive)],
    [
      "Largest free slot",
      !roomNote && totals.largest ? `${cores(totals.largest.cpu)} vCPU · ${gib(totals.largest.memMB)} GiB` : "—",
      roomNote ?? (totals.largest ? `on ${totals.largest.server}` : "No server’s capacity is known"),
    ],
  ];
  return (
    <section aria-labelledby="cp-h" className={card}>
      <div className={cardHead}>
        <h2 id="cp-h" className={h2}>Capacity</h2>
        <span className={sum}>all app servers</span>
      </div>
      <dl>
        {rows.map(([k, v, note], i) => (
          <div key={k} className={cn("grid grid-cols-[120px_minmax(0,1fr)] gap-3 px-4 py-2.5 text-[13px] max-sm:grid-cols-1 max-sm:gap-0.5", i > 0 && "border-t border-subtle")}>
            <dt className="text-muted">{k}</dt>
            <dd className="min-w-0">
              <span className="font-mono text-xs text-fg">{v}</span>
              <span className={cn(help, "block")}>{note}</span>
            </dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

function FitCard({ servers }: { servers: Server[] }) {
  const [cpu, setCpu] = useState("1 vCPU");
  const [memory, setMemory] = useState("2 GiB");
  const cpuValue = cpu.trim() === "" ? 0 : parseCores(cpu);
  const memValue = parseMemoryMB(memory);
  const invalid = cpuValue === null || memValue === null || memValue === 0;
  // Checked as typed: the last valid ask stays while a value is half typed.
  const [ask, setAsk] = useState({ cpu: 1, memMB: 2048 });
  if (!invalid && (ask.cpu !== cpuValue || ask.memMB !== memValue)) setAsk({ cpu: cpuValue!, memMB: memValue! });

  const results = useQueries({
    queries: servers.map((s) => ({
      queryKey: ["server-fit", s.id, ask.cpu, ask.memMB],
      queryFn: () => serverService.fit(s.id, ask.cpu, ask.memMB),
    })),
  });

  return (
    <section aria-labelledby="wf-h" className={card}>
      <div className={cardHead}>
        <h2 id="wf-h" className={h2}>Will it fit?</h2>
        <span className={sum}>the check every deploy runs</span>
      </div>
      <div className="flex flex-col gap-3.5 p-4">
        <div className="grid grid-cols-2 gap-2">
          <div className="flex min-w-0 flex-col gap-1.5">
            <label htmlFor="wf-cpu" className={label}>CPU</label>
            <input id="wf-cpu" value={cpu} onChange={(e) => setCpu(e.target.value)} aria-invalid={cpuValue === null} className={cn(field, "px-2 font-mono text-[13px]")} />
          </div>
          <div className="flex min-w-0 flex-col gap-1.5">
            <label htmlFor="wf-mem" className={label}>Memory</label>
            <input id="wf-mem" value={memory} onChange={(e) => setMemory(e.target.value)} aria-invalid={memValue === null || memValue === 0} className={cn(field, "px-2 font-mono text-[13px]")} />
          </div>
        </div>
        {invalid && (
          <p className="text-xs text-status-failed-fg" role="alert">
            CPU in cores (0.5, 2) and memory as 512m, 2g or 1.5 GiB.
          </p>
        )}
        <ul className="flex flex-col rounded-lg border border-subtle" aria-live="polite" aria-label="Will it fit, per server">
          {servers.map((s, i) => (
            <FitRow key={s.id} server={s} ask={ask} result={results[i].data} failed={readFailed(results[i])} />
          ))}
        </ul>
        <details>
          <summary className="cursor-pointer py-1 text-[13px] text-link pointer-coarse:py-3">How capacity is checked</summary>
          <ul className="mt-1.5 flex list-disc flex-col gap-1 pl-[18px] text-[13px] leading-5 text-fg/80">
            <li>Reserved is what services&apos; limits set aside. A service without a limit reserves nothing.</li>
            <li>A deploy that would reserve more than is free stops before it starts, and says what is short.</li>
            <li>Members the workspace allows can deploy anyway; past a share, only owners of the server&apos;s workspace.</li>
            <li>Every deploy needs a memory limit once its service&apos;s grace period has ended.</li>
          </ul>
        </details>
      </div>
    </section>
  );
}

function FitRow({ server, ask, result, failed }: { server: Server; ask: { cpu: number; memMB: number }; result?: FitResult; failed: boolean }) {
  let verdict = "…";
  let tone = "text-muted";
  let detail = "";
  if (failed) {
    verdict = "?";
    detail = "could not be checked";
  } else if (result && !result.capacity_known) {
    verdict = "Unknown";
    detail = "capacity not scanned, so a deploy here is not checked";
  } else if (result?.fits) {
    verdict = "Fits";
    tone = "text-status-running-fg";
    detail = `after: ${cores(result.allocated_cpu_cores + ask.cpu)} of ${cores(result.effective_cpu_cores)} vCPU · ${gib(result.allocated_memory_mb + ask.memMB)} of ${gib(result.effective_memory_mb)} GiB${result.share ? " (this workspace's share)" : ""}`;
  } else if (result) {
    verdict = "No";
    tone = "text-status-failed-fg";
    const short: string[] = [];
    if (!result.memory_ok) short.push(`needs ${formatMiB(ask.memMB)}, ${formatMiB(Math.max(0, Math.floor(result.effective_memory_mb - result.allocated_memory_mb)))} free`);
    if (!result.cpu_ok) short.push(`needs ${cores(ask.cpu)} vCPU, ${cores(Math.max(0, result.effective_cpu_cores - result.allocated_cpu_cores))} free`);
    detail = short.join(" · ");
  }
  return (
    <li className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 border-t border-subtle px-3 py-2 text-[13px] first:border-t-0">
      <span className={cn("min-w-10 font-semibold", tone)}>{verdict}</span>
      <span className="font-mono text-xs text-fg [overflow-wrap:anywhere]">{server.name}</span>
      {detail && <span className={cn(help, "flex-[1_1_100%] pl-12")}>{detail}</span>}
    </li>
  );
}
