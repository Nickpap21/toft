"use client";

import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useQuery, type UseQueryResult } from "@tanstack/react-query";
import {
  Box,
  Check,
  CircleDot,
  Folder,
  GitCommitHorizontal,
  GitMerge,
  Layers,
  Link2,
  LoaderCircle,
  MoreHorizontal,
  Pencil,
  Plus,
  Server as ServerIcon,
  X,
  type LucideIcon,
} from "lucide-react";
import { dashboardQuery, type DashboardData, type LiveDeploy, type RecentDeployment } from "@/lib/api/dashboard.service";
import { useDeployLog } from "@/lib/hooks/useDeployLog";
import { orgService } from "@/lib/api/org.service";
import { needsYouQuery, projectsQuery, serversQuery, workspaceServicesQuery } from "@/lib/queries";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import type { NeedsYouItem } from "@/lib/api/inbox.service";
import { HomeNeedsRows } from "@/components/inbox/needs-you";
import { useCommandPalette } from "@/components/shell/command-palette-provider";
import { PROJECT_LETTER } from "@/components/shell/sidebar-projects";
import { Read } from "@/components/shell/unreachable";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { useDocumentTitle } from "@/lib/use-document-title";
import { waitingForAgent } from "@/components/servers/agent";
import { isOnline } from "@/components/servers/capacity";
import { reachedIndex, stageDefs, type PipelineType } from "@/components/ui/deploy-stage-tracker";
import { TONE_CLASSES, type StatusTone } from "@/lib/status";
import { cn } from "@/lib/utils";
import {
  activity,
  deploymentHref,
  deployState,
  healthParts,
  homeNeeds,
  inboxItems,
  projectRows,
  serviceHref,
  serviceTitle,
  timeAgo,
  type ActivityIcon,
  type HealthPart,
} from "@/components/home/home";
import { Welcome, firstLive, useFinishSetup } from "./welcome";

// Home (board: private/design/canvas/Main.dc.html). Left off the board, since
// they don't exist yet: pull requests, issues and docs (their tiles, tabs,
// items and columns), Create and Deploy, cancelling a deploy, pruning
// images, per-stage timings, and who started a deploy.

const TONE_TEXT: Record<StatusTone, string> = {
  running: "text-status-running-fg",
  building: "text-status-building-fg",
  pending: "text-status-pending-fg",
  failed: "text-status-failed-fg",
  neutral: "text-muted",
};

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;
const ago = (t: string) => (t === "now" ? "just now" : `${t} ago`);

const card = "overflow-hidden rounded-[10px] border border-subtle bg-surface";
const cardHead = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3";
const countChip = "rounded-full bg-raised px-1.5 font-mono text-xs leading-[18px] text-fg";
const h2 = "text-[15px] font-semibold leading-[22px] text-fg";
const focusRing = "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus";
// Read's loading and failed rows sit in a card that has no padding of its own.
const reads = "*:data-read-state:m-4";
const primary = `touch-target inline-flex h-9 items-center gap-1.5 rounded-lg bg-accent px-3.5 text-[13px] font-medium text-on-accent hover:bg-accent/90 ${focusRing}`;
const outline = `touch-target inline-flex h-9 items-center gap-1.5 rounded-lg border border-strong bg-raised px-3 text-[13px] font-medium text-fg hover:border-muted ${focusRing}`;
const buttonLink =
  "touch-target inline-flex h-8 items-center gap-1.5 rounded-lg border border-strong bg-raised px-3 text-[13px] font-medium text-fg hover:border-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus";

export default function HomePage() {
  const router = useRouter();
  const role = useOrgRole();
  const { user } = useAuth();
  useDocumentTitle(`Home · ${user?.orgName ? `${user.orgName} · ` : ""}Toft`);
  const dashboard = useQuery(dashboardQuery);
  const services = useQuery(workspaceServicesQuery);
  const projects = useQuery(projectsQuery);
  const servers = useQuery(serversQuery);
  // Needs you is the inbox's (D-88), for operators a release among it.
  const needs = useQuery(needsYouQuery());
  const audit = useQuery({
    queryKey: ["audit-log", "home"],
    queryFn: () => orgService.listAuditLog({ limit: 20 }),
    refetchInterval: 30_000,
  });

  // g p → Projects, g s → Servers (skipped while typing in a field)
  const pendingG = useRef(false);
  const pendingGTimeout = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      const tag = (e.target as HTMLElement)?.tagName;
      if (tag === "INPUT" || tag === "TEXTAREA" || (e.target as HTMLElement)?.isContentEditable) return;
      if (e.key === "g") {
        pendingG.current = true;
        if (pendingGTimeout.current) clearTimeout(pendingGTimeout.current);
        pendingGTimeout.current = setTimeout(() => { pendingG.current = false; }, 1000);
        return;
      }
      if (pendingG.current && (e.key === "p" || e.key === "s")) {
        pendingG.current = false;
        if (pendingGTimeout.current) clearTimeout(pendingGTimeout.current);
        router.push(e.key === "p" ? "/projects" : "/servers");
      }
    }
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      if (pendingGTimeout.current) clearTimeout(pendingGTimeout.current);
    };
  }, [router]);

  const data = dashboard.data;
  const serviceList = services.data ?? [];
  const serverList = servers.data ?? [];
  // "Now" for relative times: the last refresh, every 10 s.
  const now = dashboard.dataUpdatedAt;
  const live = firstLive(serviceList);
  const setup = useFinishSetup(live);
  const host = useSyncExternalStore(noSubscribe, () => window.location.host, () => "");
  const loading = dashboard.isLoading || services.isLoading || projects.isLoading || setup.loading;
  // The first load only: a failed read retrying keeps the page, and Retry's focus.
  const [settled, setSettled] = useState(false);
  if (!loading && !settled) setSettled(true);
  const canCreate = role === "owner" || role === "developer";
  // A server still waiting for its first agent isn't counted, as in the sidebar (D-153).
  const joined = serverList.filter((s) => !waitingForAgent(s));
  // Until Finish setting up is complete or an owner hides it (7g).
  const welcome = !!data && !!services.data && setup.known && !setup.hidden && !setup.complete;
  const skeleton = (
    <div className="flex flex-col gap-7 motion-safe:animate-pulse" aria-label="Loading Home">
      <div className="h-[104px] rounded-[10px] bg-surface" />
      <div className="flex flex-wrap gap-6">
        <div className="h-72 min-w-0 flex-[999_1_520px] rounded-[10px] bg-surface" />
        <div className="h-72 min-w-0 flex-[1_1_360px] rounded-[10px] bg-surface" />
      </div>
    </div>
  );

  return (
    <div className="flex flex-col">
      {welcome ? (
        <header className="mb-5 flex flex-wrap items-center gap-x-3 gap-y-1 md:-mt-3">
          <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">{live ? "Home" : "Welcome to Toft"}</h1>
          <p className="text-[13px] text-muted">
            {[user?.orgName, host, setup.appsHere ? "this server runs apps" : "apps run on the servers you add"].filter(Boolean).join(" · ")}
          </p>
        </header>
      ) : (
      <header className="mb-3 flex flex-wrap items-center justify-between gap-3 md:-mt-3">
        <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">Home</h1>
        {canCreate && <HomeActions />}
      </header>
      )}

      <div className={cn("flex w-full max-w-[1600px] flex-col pb-14", welcome ? "gap-7" : "gap-5 max-sm:gap-4")}>
        {/* The parts that lean on these have no row of their own: only a failure draws here. */}
        {data && <Read query={services} what="the services" loading={<></>}>{() => null}</Read>}
        {data && <Read query={servers} what="the servers" loading={<></>}>{() => null}</Read>}

        <Read query={dashboard} what="Home" heading="h2" loading={skeleton}>
        {() => loading && !settled ? skeleton : data && welcome ? (
          <>
            {/* What needs the person doesn't wait for setup to finish. */}
            {(needs.data ? needs.data.length > 0 : needs.isError || needs.errorUpdateCount > 0) && <HomeNeeds query={needs} now={now} />}
            <Welcome rows={setup.rows} services={serviceList} data={data} now={now} />
          </>
        ) : data ? (
          <>
            <HealthStrip parts={healthParts(data, services.data, servers.data && { online: joined.filter(isOnline).length, total: joined.length })} />
            {/* Two columns from 1024 px; below, one in the board's order: Needs you, Deploying now, Projects, Activity. */}
            <div className="grid items-start gap-6 max-lg:flex max-lg:flex-col max-lg:items-stretch max-lg:gap-5 max-sm:gap-4 lg:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)]">
              <div className="flex min-w-0 flex-col gap-5 max-lg:contents">
                <HomeNeeds query={needs} now={now} className="max-lg:order-1" />
                <Projects
                  query={projects}
                  rows={projectRows(projects.data ?? [], serviceList, data.project_deploys)}
                  services={services.data?.length}
                  canCreate={canCreate}
                  now={now}
                />
              </div>
              <aside className="flex min-w-0 flex-col gap-5 max-lg:contents">
                <DeployingNow deploying={data.deploying} last={data.recent_deployments[0]} now={now} />
                <Activity deploys={data.recent_deployments} audit={audit} work={data.activity} now={now} />
              </aside>
            </div>
          </>
        ) : null}
        </Read>
      </div>
    </div>
  );
}

// ─── Header ───────────────────────────────────────────────────────────────────

/** Create and Deploy… (board ReworkHome): both open ⌘K on the matching
 *  commands, where a protected environment's deploy confirms; under ⋯ on phones. */
function HomeActions() {
  const { openPalette } = useCommandPalette();
  const router = useRouter();
  const create = [
    { label: "New project", run: () => router.push("/projects?new") },
    { label: "New service…", run: () => openPalette("New service in ") },
    { label: "New issue…", run: () => openPalette("New issue in ") },
  ];
  return (
    <div className="flex items-center gap-2">
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button" className={cn(outline, "max-sm:hidden")}>
            <Plus className="h-[15px] w-[15px]" aria-hidden="true" />
            Create
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {create.map((c) => <DropdownMenuItem key={c.label} onSelect={c.run}>{c.label}</DropdownMenuItem>)}
        </DropdownMenuContent>
      </DropdownMenu>
      <button type="button" onClick={() => openPalette("Deploy ")} className={cn(primary, "max-sm:hidden")}>
        <Layers className="h-[15px] w-[15px]" aria-hidden="true" />
        Deploy…
      </button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label="More actions: deploy, create a project, service or issue"
            className="inline-flex h-11 w-11 flex-none items-center justify-center rounded-lg border border-subtle bg-raised text-muted hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus sm:hidden"
          >
            <MoreHorizontal className="h-[18px] w-[18px]" aria-hidden="true" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={() => openPalette("Deploy ")}>Deploy…</DropdownMenuItem>
          {create.map((c) => <DropdownMenuItem key={c.label} onSelect={c.run}>{c.label}</DropdownMenuItem>)}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

// ─── Health strip ─────────────────────────────────────────────────────────────

const DOT: Record<StatusTone, string> = {
  running: "bg-status-running",
  building: "bg-status-building",
  pending: "bg-status-pending",
  failed: "bg-status-failed",
  neutral: "bg-muted",
};

/** State only (D-140): servers, services, backups, disks, deploys. */
function HealthStrip({ parts }: { parts: HealthPart[] }) {
  return (
    <ul aria-label="System health" className="flex flex-wrap gap-px overflow-hidden rounded-[10px] border border-subtle bg-subtle max-md:flex-col">
      {parts.map((p, i) => {
        const body = (
          <>
            <span aria-hidden="true" className={cn("h-1.5 w-1.5 flex-none rounded-full", DOT[p.tone])} />
            <span className="font-medium text-fg">{p.value}</span>
            {p.sub && <span className="min-w-0 text-xs text-muted [overflow-wrap:anywhere]">{p.sub}</span>}
          </>
        );
        const cls = "flex min-h-11 w-full min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 px-4 py-2 text-[13px]";
        return (
          <li key={i} className="flex min-w-0 flex-auto bg-surface">
            {p.href ? (
              <Link href={p.href} className={cn(cls, "hover:bg-raised/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus")}>{body}</Link>
            ) : (
              <span className={cls}>{body}</span>
            )}
          </li>
        );
      })}
    </ul>
  );
}

// ─── Needs you ────────────────────────────────────────────────────────────────

/** Capped at 4 (D-140); the footer names the rest and links the Inbox. */
function HomeNeeds({ query, now, className }: { query: UseQueryResult<NeedsYouItem[]>; now: number; className?: string }) {
  const { shown, total, more } = homeNeeds(inboxItems(query.data ?? [], now));
  return (
    <section aria-labelledby="needs-h" className={cn(card, className)}>
      <div className={cardHead}>
        <div className="flex items-center gap-2">
          <h2 id="needs-h" className={h2}>Needs you</h2>
          <span className={countChip}>{query.data ? total : "—"}</span>
        </div>
        <Link href="/inbox" className="text-[13px] text-link hover:underline">Open Inbox</Link>
      </div>
      <div className={reads}>
        <Read
          query={query}
          what="what needs you"
          isEmpty={() => total === 0}
          empty={
            <p className="flex items-center gap-2 px-4 py-3.5 text-[13px] text-muted">
              <Check className="h-4 w-4 text-status-running" aria-hidden="true" />
              Nothing needs you.
            </p>
          }
        >
          {() => <HomeNeedsRows items={shown} now={now} />}
        </Read>
      </div>
      {more && (
        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-muted max-sm:flex-col max-sm:items-start">
          <span>{more}</span>
          <Link href="/inbox" className="text-link hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center">All {total} in Inbox</Link>
        </div>
      )}
    </section>
  );
}

// ─── Deploying now ────────────────────────────────────────────────────────────

function DeployingNow({ deploying, last, now }: { deploying: LiveDeploy[]; last?: RecentDeployment; now: number }) {
  const live = deploying[0];
  const lines = useDeployLog(live?.service_id ?? "", live?.id ?? "");
  const queued = live?.status !== "building";
  return (
    <section aria-labelledby="live-h" className={cn(card, "max-lg:order-2")}>
      <div className={cardHead}>
        <h2 id="live-h" className={h2}>Deploying now</h2>
        {live && (
          <span className={cn("inline-flex h-[22px] items-center gap-1.5 rounded-full px-2 text-xs font-medium", TONE_CLASSES[queued ? "pending" : "building"].chip)}>
            {!queued && <LoaderCircle className="h-3 w-3 motion-safe:animate-spin" aria-hidden="true" />}
            {queued ? "Queued" : "Building"}
          </span>
        )}
      </div>
      {live ? (
        <LiveDeployBody live={live} lines={lines} more={deploying.slice(1)} />
      ) : (
        <div className="flex flex-col gap-1 px-4 py-3.5 text-[13px] text-muted">
          <span>Nothing is deploying.</span>
          {last && (
            <span>
              Last:{" "}
              <Link href={serviceHref(last.project_id, last.service_id)} className="inline-link [overflow-wrap:anywhere]">
                {serviceTitle(last.project_name, last.service_name)}
              </Link>{" "}
              <span className={TONE_TEXT[deployState(last.status).tone]}>{deployState(last.status).label}</span>
              {" · "}
              {timeAgo(last.finished_at ?? last.created_at, now)}
            </span>
          )}
        </div>
      )}
    </section>
  );
}

function LiveDeployBody({ live, lines, more }: { live: LiveDeploy; lines: string[]; more: LiveDeploy[] }) {
  const type = (live.source_type || "git") as PipelineType;
  const stages = stageDefs(type, live.has_compose_repo);
  const current = live.status === "building" ? reachedIndex(lines, stages) : -1;
  const href = deploymentHref(live.project_id, live.service_id, live.id);
  return (
    <>
      <div className="flex flex-col gap-3.5 p-4">
      <div className="flex flex-col gap-1">
        <Link href={serviceHref(live.project_id, live.service_id)} className="text-[15px] font-semibold text-fg [overflow-wrap:anywhere] hover:underline">
          <span className="text-muted">{live.project_name} / </span>
          {live.service_name}
        </Link>
        {live.commit_sha && (
          <span className="flex items-center gap-1.5 text-[13px] text-muted">
            <GitCommitHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
            <span className="font-mono text-xs text-fg">{live.commit_sha.slice(0, 7)}</span>
          </span>
        )}
        <span className="text-xs text-muted [overflow-wrap:anywhere]">
          {[live.environment_name, live.server_name || "the panel host"].filter(Boolean).join(" · ")}
        </span>
      </div>
      {/* A row per stage on phones, as the board draws it. */}
      <ol
        aria-label="Pipeline"
        className="grid grid-cols-[repeat(var(--stages),minmax(0,1fr))] gap-1.5 max-sm:grid-cols-1 max-sm:gap-0"
        style={{ "--stages": stages.length } as React.CSSProperties}
      >
        {stages.map((s, i) => (
          <li key={s.key} className="flex flex-col gap-1.5 max-sm:min-h-9 max-sm:justify-center max-sm:border-t max-sm:border-subtle max-sm:first:border-t-0">
            <span className={cn("h-[3px] rounded-sm max-sm:hidden", i < current ? "bg-status-running" : i === current ? "bg-status-building" : "bg-strong")} />
            <span className={cn("text-xs", i < current ? "text-fg" : i === current ? "font-medium text-status-building-fg" : "text-muted")}>
              {s.label}
            </span>
          </li>
        ))}
      </ol>
      {lines.length > 0 && (
        <pre className="overflow-hidden rounded-md border border-subtle bg-ground px-3 py-2.5 font-mono text-xs leading-[18px] text-muted">
          {lines.slice(-3).map((line, i) => <span key={i} className="block truncate">{line}</span>)}
        </pre>
      )}
      {more.length > 0 && (
        <p className="text-xs text-muted">
          Also deploying:{" "}
          {more.map((d, i) => (
            <span key={d.id}>
              {i > 0 && ", "}
              <Link href={serviceHref(d.project_id, d.service_id)} className="inline-link [overflow-wrap:anywhere]">
                {serviceTitle(d.project_name, d.service_name)}
              </Link>
            </span>
          ))}
        </p>
      )}
      </div>
      <div className="flex flex-wrap items-center justify-between gap-2 border-t border-subtle px-4 py-2.5">
        <Link href={href} className={buttonLink}>Follow logs</Link>
      </div>
    </>
  );
}

// ─── Activity ─────────────────────────────────────────────────────────────────

const ACTIVITY_ICON: Record<ActivityIcon, { Icon: LucideIcon; tint: string }> = {
  live: { Icon: Check, tint: "text-status-running" },
  failed: { Icon: X, tint: "text-status-failed" },
  deploy: { Icon: Layers, tint: "text-link" },
  server: { Icon: ServerIcon, tint: "text-muted" },
  service: { Icon: Box, tint: "text-muted" },
  project: { Icon: Folder, tint: "text-muted" },
  invite: { Icon: Link2, tint: "text-muted" },
  other: { Icon: Pencil, tint: "text-muted" },
  merge: { Icon: GitMerge, tint: "text-link" },
  issue: { Icon: CircleDot, tint: "text-muted" },
  doc: { Icon: Pencil, tint: "text-muted" },
};

function Activity({ deploys, audit, work, now }: {
  deploys: RecentDeployment[];
  audit: UseQueryResult<Parameters<typeof activity>[1]>;
  work: DashboardData["activity"];
  now: number;
}) {
  const items = activity(deploys, audit.data ?? [], 8, work);
  return (
    <section aria-labelledby="act-h" className={cn(card, "max-lg:order-4")}>
      <div className={cardHead}>
        <h2 id="act-h" className={h2}>Activity</h2>
        <Link href="/audit-log" className="text-[13px] text-link hover:underline">All activity</Link>
      </div>
      <div className={reads}>
        <Read
          query={audit}
          what="the activity"
          isEmpty={() => items.length === 0}
          empty={<p className="px-4 py-3.5 text-[13px] text-fg/70">No activity yet. Deploys, invites and changes to projects and services show here as they happen.</p>}
        >
          {() => (
            <ul className="pb-2 pt-1.5">
              {items.map((a) => {
                const { Icon, tint } = ACTIVITY_ICON[a.icon];
                return (
                  <li key={a.key} className="flex items-start gap-2.5 px-4 py-[7px] leading-[19px] pointer-coarse:min-h-11">
                    <Icon className={cn("mt-0.5 h-4 w-4 flex-none", tint)} aria-hidden="true" />
                    <span className="min-w-0 flex-1 text-[13px] text-muted">
                      {a.who && <span className="font-medium text-fg">{a.who} </span>}
                      {a.text} <span className="text-fg [overflow-wrap:anywhere]">{a.target}</span>
                    </span>
                    <time dateTime={a.at} className="flex-none font-mono text-xs text-muted">{timeAgo(a.at, now)}</time>
                  </li>
                );
              })}
            </ul>
          )}
        </Read>
      </div>
    </section>
  );
}

// ─── Projects ─────────────────────────────────────────────────────────────────

function Projects({ query, rows, services, canCreate, now }: { query: UseQueryResult<unknown>; rows: ReturnType<typeof projectRows>; services: number | undefined; canCreate: boolean; now: number }) {
  const td = "px-4 py-2.5 align-middle sm:py-3 max-sm:block max-sm:p-0";
  // The divider on each cell, so it runs under every column.
  const divided = "sm:[&>td]:border-t sm:[&>td]:border-subtle max-sm:border-t max-sm:border-subtle";
  return (
    <section aria-labelledby="proj-h" className={cn(card, "max-lg:order-3")}>
      <div className={cardHead}>
        <div className="flex flex-wrap items-baseline gap-x-2">
          <h2 id="proj-h" className={h2}>Projects</h2>
          {query.data !== undefined && <span className="text-xs text-muted">{plural(rows.length, "project")} · {services === undefined ? "—" : plural(services, "service")}</span>}
        </div>
        {canCreate && (
          <Link href="/projects?new" className={cn(buttonLink, "font-medium")}>
            <Plus className="h-3.5 w-3.5" aria-hidden="true" />
            New project
          </Link>
        )}
      </div>
      <div className={reads}>
      <Read query={query} what="the projects">
      {() => (
      <table className="w-full border-collapse text-[13px]">
        <thead className="max-sm:sr-only">
          <tr className="text-left text-xs text-muted">
            <th scope="col" className="border-b border-subtle px-4 py-2 font-medium">Project</th>
            <th scope="col" className="border-b border-subtle px-4 py-2 font-medium">Services</th>
            <th scope="col" className="border-b border-subtle px-4 py-2 text-right font-medium">Last deploy</th>
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 && (
            <tr><td colSpan={3} className="px-4 py-3.5 text-muted">No projects yet.</td></tr>
          )}
          {rows.map((p, i) => {
            const state = p.last && deployState(p.last.status);
            const total = p.bar.reduce((n, b) => n + b.n, 0);
            return (
              <tr key={p.id} className={cn("max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1.5 max-sm:px-4 max-sm:py-3", i > 0 && divided)}>
                <td className={td}>
                  <Link href={`/projects/${p.id}`} className="inline-flex items-center gap-2.5 font-medium text-fg [overflow-wrap:anywhere] hover:underline">
                    <span aria-hidden="true" className={cn("flex h-[18px] w-[18px] flex-none items-center justify-center rounded text-[11px] font-bold", PROJECT_LETTER[p.health])}>
                      {p.name.trim().charAt(0).toUpperCase()}
                    </span>
                    {p.name}
                  </Link>
                  {p.work && <div className="mt-0.5 pl-7 text-xs text-muted">{p.work}</div>}
                </td>
                <td className={cn(td, "max-sm:col-span-2 max-sm:row-start-2 max-sm:pl-7")}>
                  {total > 0 && (
                    <span className="mb-1.5 flex h-1.5 w-[120px] gap-0.5 overflow-hidden rounded-sm" role="img" aria-label={p.servicesText}>
                      {p.bar.map((b) => <span key={b.tone} className={DOT[b.tone]} style={{ flex: b.n }} />)}
                    </span>
                  )}
                  <span className={total === 0 ? "text-muted" : "text-fg/80"}>
                    {/* Only what failed is red, as on the board. */}
                    {services === undefined ? "—" : p.servicesText.split(" · ").map((part, j) => (
                      <span key={part}>
                        {j > 0 && " · "}
                        <span className={part.endsWith(" failed") ? TONE_TEXT.failed : undefined}>{part}</span>
                      </span>
                    ))}
                  </span>
                </td>
                <td className={cn(td, "text-right max-sm:col-start-2 max-sm:row-start-1")}>
                  {p.last && state ? (
                    <>
                      <span className={cn("font-medium", TONE_TEXT[state.tone])}>{state.label}</span>
                      <div className="text-xs text-muted">{ago(timeAgo(p.last.created_at, now))}</div>
                    </>
                  ) : <span className="text-muted">—</span>}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      )}
      </Read>
      </div>
    </section>
  );
}

const noSubscribe = () => () => {};
