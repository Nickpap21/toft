"use client";

import { useEffect } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Share2 } from "lucide-react";
import { notFound } from "@/lib/api/client";
import { Read, ReadFailed } from "@/components/shell/unreachable";
import { serverService, type Server } from "@/lib/api/server.service";
import { TONE_CLASSES, statusLabel, statusTone } from "@/lib/status";
import { cn, possessive } from "@/lib/utils";
import { useAuth } from "@/context/AuthContext";
import { card, readInCard } from "@/components/settings/settings-frame";
import { ServersIcon } from "@/components/shell/nav-icons";
import { OpenInOwner } from "@/components/servers/open-in-owner";
import { NoBackupRow } from "@/components/servers/no-backup-row";
import { cores, gib } from "@/components/servers/capacity";
import { Chip, online } from "@/components/servers/resource-bar";
import { OwnerView } from "./owner-view";
import { memoryLabel } from "./server-view";

// One server (board: private/design/canvas/Server.dc.html, D-48): the owner's
// page; a shared server's page for a workspace it is shared with (D-39:
// name and share only, never the address); or Server not found.

export default function ServerPage() {
  const id = useParams().id as string;
  const { data: server, isLoading, error } = useQuery({
    queryKey: ["server", id],
    queryFn: () => serverService.get(id),
    retry: false,
    // An agent that isn't connected changes state by itself: follow it.
    refetchInterval: (q) => (q.state.data?.connection === "agent" && q.state.data.agent?.status !== "connected" ? 5_000 : 30_000),
  });
  // A server shared with this workspace is in its list, not readable here.
  const { data: list, isLoading: listLoading, error: listError } = useQuery({ queryKey: ["servers"], queryFn: () => serverService.list(), enabled: !!error });
  const queryClient = useQueryClient();
  const shared = list?.find((s) => s.id === id && s.shared);

  useEffect(() => {
    const name = server?.name ?? shared?.name;
    if (name) document.title = `${name} · Servers · Toft`;
  }, [server?.name, shared?.name]);

  if (isLoading || (error && listLoading)) {
    return (
      <div role="status" className="flex flex-col gap-6 motion-safe:animate-pulse" aria-label="Loading the server">
        <div className="h-20 rounded-[10px] bg-surface" />
        <div className="h-56 rounded-[10px] bg-surface" />
      </div>
    );
  }
  if (server) return <OwnerView server={server} />;
  if (shared) return <SharedView server={shared} />;
  const failed = !notFound(error) ? error : listError;
  if (failed) return <ReadFailed error={failed} onRetry={() => void queryClient.refetchQueries({ type: "active" })} />;
  return <MissingView id={id} servers={list ?? []} />;
}

const h2 = "text-[15px] font-semibold leading-[22px] text-fg";
const thCls = "border-b border-subtle px-4 py-2.5 text-left text-xs font-medium text-muted";
const tdCls = "border-t border-subtle px-4 py-2.5";

function SharedView({ server }: { server: Server }) {
  const { user } = useAuth();
  const me = user?.orgName ?? "This workspace";
  const owner = server.shared_by ?? "its owner";
  const isOwnerMember = (user?.orgs ?? []).some((o) => o.org_id === server.shared_by_org_id);
  const capacity = useQuery({ queryKey: ["server-capacity", server.id], queryFn: () => serverService.getCapacitySummary(server.id) });
  const summary = capacity.data;
  const servicesRead = useQuery({ queryKey: ["server-services", server.id], queryFn: () => serverService.listServices(server.id) });
  const state = online(server);
  const shareCPU = server.total_cpu_cores ?? 0;
  const shareMB = server.total_memory_mb ?? 0;
  const usedCPU = summary?.allocated_cpu_cores ?? 0;
  const usedMB = summary?.allocated_memory_mb ?? 0;

  return (
    <div className="flex flex-col">
      <header className="-mx-4 -mt-4 mb-6 flex flex-wrap items-center gap-3 border-b border-subtle px-4 py-4 md:-mx-8 md:-mt-8 md:px-8">
        <span className="flex h-9 w-9 items-center justify-center rounded-lg border border-subtle bg-raised text-fg/70">
          <ServersIcon className="h-[18px] w-[18px]" aria-hidden="true" />
        </span>
        <div className="flex min-w-0 flex-col">
          <span className="text-xs text-muted">
            <Link href="/servers" className="hover:text-fg">Servers</Link> / Shared with {me}
          </span>
          <h1 className="break-all font-mono text-xl font-medium leading-7 tracking-[-0.01em] text-fg">{server.name}</h1>
        </div>
        <span className={cn("inline-flex h-6 items-center gap-1.5 rounded-full px-[9px] text-xs font-medium", state.chip)}>
          <span className="h-1.5 w-1.5 rounded-full bg-current" aria-hidden="true" />
          {state.label}
        </span>
        <Chip>Shared by {owner}</Chip>
      </header>

      <div className="flex w-full max-w-[1320px] flex-col gap-6">
        <NoBackupRow serverId={server.id} />
        <section aria-labelledby="who-h" className={cn(card, "flex flex-wrap items-center gap-3.5 px-4 py-3.5")}>
          <span className="flex h-8 w-8 flex-none items-center justify-center rounded-lg bg-accent-soft text-link">
            <Share2 className="h-4 w-4" aria-hidden="true" />
          </span>
          <div className="flex min-w-0 flex-[1_1_420px] flex-col gap-0.5">
            <h2 id="who-h" className="text-sm font-semibold text-fg">
              {owner} owns this server and shares {cores(shareCPU)} vCPU and {gib(shareMB)} GiB with {me}
            </h2>
            <p className="text-[13px] text-fg/70">
              {me} sees the server’s name, its share and its own services here. The address, the settings and other workspaces’ services stay with{" "}
              {owner}.
            </p>
          </div>
          {isOwnerMember ? (
            <OpenInOwner
              server={server}
              className="h-8 gap-1.5 border border-subtle bg-raised px-3 text-[13px] font-medium text-fg hover:bg-raised/70"
              label={
                <>
                  You own {owner}: open it there
                  <ArrowRight className="h-3.5 w-3.5" aria-hidden="true" />
                </>
              }
            />
          ) : (
            <span className="text-xs text-muted">Ask {possessive(owner)} owners for anything about the server itself.</span>
          )}
        </section>

        <section aria-labelledby="share-h" className={cn(card, "flex flex-col gap-4 p-4")}>
          <div className="flex flex-wrap items-center gap-2.5">
            <h2 id="share-h" className={h2}>{possessive(me)} share</h2>
            <span className="font-mono text-xs text-muted">{cores(shareCPU)} vCPU · {gib(shareMB)} GiB</span>
            <span className="flex-1" />
            <span className="text-xs text-muted">Use isn’t measured on a shared server, only what limits reserve</span>
          </div>
          <Read query={capacity} what="what this share has reserved">{() => (
          <div className="grid grid-cols-[repeat(auto-fit,minmax(240px,1fr))] gap-5">
            <ShareBar label="CPU" summary={`${cores(usedCPU)} of ${cores(shareCPU)} vCPU reserved`} pct={shareCPU ? usedCPU / shareCPU : 0} note={`${cores(Math.max(0, shareCPU - usedCPU))} vCPU free in the share`} />
            <ShareBar label="Memory" summary={`${gib(usedMB)} of ${gib(shareMB)} GiB reserved`} pct={shareMB ? usedMB / shareMB : 0} note={`${gib(Math.max(0, shareMB - usedMB))} GiB free in the share`} />
          </div>
          )}</Read>
          <p className="border-t border-subtle pt-3 text-[13px] text-fg/70">
            A deploy that needs more than the share has free stops before it starts. {possessive(me)} owners (or developers, if {me} allows it) can override
            within the share; past it, only {possessive(owner)} owners can.
          </p>
        </section>

        <section aria-labelledby="mine-h" className="flex flex-col gap-2.5">
          <h2 id="mine-h" className={h2}>{possessive(me)} services here</h2>
          <div className={cn(card, readInCard, "overflow-x-auto")}>
            <Read
              query={servicesRead}
              what="the services here"
              loading={<p className="px-4 py-3 text-[13px] text-muted">Loading…</p>}
              isEmpty={(s) => s.length === 0}
              empty={<p className="px-4 py-3 text-[13px] text-muted">None yet. Choose {server.name} as a service’s server to deploy it here.</p>}
            >{(services) => (
              <table className="w-full min-w-[560px] border-collapse text-[13px]">
                <thead>
                  <tr>
                    <th scope="col" className={thCls}>Service</th>
                    <th scope="col" className={thCls}>CPU</th>
                    <th scope="col" className={thCls}>Memory</th>
                    <th scope="col" className={thCls}>Status</th>
                  </tr>
                </thead>
                <tbody>
                  {services.map((s) => (
                    <tr key={s.id}>
                      <td className={tdCls}>
                        <Link
                          href={s.scope === "org" || !s.project_id ? `/shared/${s.id}` : `/projects/${s.project_id}/services/${s.id}`}
                          className="text-fg hover:underline"
                        >
                          <span className="text-muted">{s.project_name || "Workspace"} /</span> <span className="font-medium">{s.name}</span>
                        </Link>
                      </td>
                      <td className="border-t border-subtle px-4 py-2.5 font-mono text-xs">{s.cpu_limit || <span className="text-muted">none</span>}</td>
                      <td className="border-t border-subtle px-4 py-2.5 font-mono text-xs">{s.memory_limit ? memoryLabel(s.memory_limit) : <span className="text-muted">none</span>}</td>
                      <td className={tdCls}>
                        <span className={cn("inline-flex items-center gap-1.5 font-medium", TONE_CLASSES[statusTone(s.runtime_status)].text)}>
                          <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-current" />
                          {statusLabel(s.runtime_status)}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}</Read>
          </div>
        </section>
      </div>
    </div>
  );
}

function ShareBar({ label, summary, pct, note }: { label: string; summary: string; pct: number; note: string }) {
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex justify-between gap-2 text-xs">
        <span className="text-fg/70">{label}</span>
        <span className="font-mono text-fg">{summary}</span>
      </div>
      <div className="flex h-2.5 overflow-hidden rounded-xs bg-subtle" aria-hidden="true">
        <span className="bg-fg/75" style={{ width: `${Math.min(100, pct * 100)}%` }} />
      </div>
      <span className="text-xs text-muted">{note}</span>
    </div>
  );
}

function MissingView({ id, servers }: { id: string; servers: Server[] }) {
  const { user } = useAuth();
  const ws = user?.orgName ?? "this workspace";
  const own = servers.filter((s) => !s.shared);
  return (
    <div className="flex flex-col">
      <header className="-mx-4 -mt-4 mb-6 flex flex-col gap-0.5 border-b border-subtle px-4 py-4 md:-mx-8 md:-mt-8 md:px-8">
        <span className="text-xs text-muted">
          <Link href="/servers" className="hover:text-fg">Servers</Link> /
        </span>
        <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">Server not found</h1>
      </header>
      <section aria-labelledby="nf-h" className={cn(card, "flex max-w-[720px] flex-col gap-3.5 p-4")}>
        <div className="flex flex-col gap-1">
          <h2 id="nf-h" className={h2}>No server with this ID in {ws}</h2>
          <p className="text-[13px] text-fg/70">
            It may have been removed, or it belongs to another workspace. If it is shared with you, its share is on the Servers page of the workspace it is
            shared with.
          </p>
          <span className="mt-1 break-all font-mono text-xs text-muted">{id}</span>
        </div>
        <div className="flex flex-wrap items-center gap-2 border-t border-subtle pt-3 text-[13px]">
          {own.length > 0 && <span className="text-muted">Servers in {ws}:</span>}
          {own.map((s) => (
            <Link key={s.id} href={`/servers/${s.id}`} className="inline-flex h-7 items-center rounded-md border border-subtle px-2 font-mono text-xs text-fg hover:bg-raised">
              {s.name}
            </Link>
          ))}
          <span className="flex-1" />
          <Link href="/servers" className="inline-flex h-8 items-center rounded-lg border border-subtle bg-raised px-3 text-[13px] font-medium text-fg hover:bg-raised/70">
            Back to Servers
          </Link>
        </div>
      </section>
    </div>
  );
}
