"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Activity, Check, ChevronRight, RotateCw, Terminal, X } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { orgService } from "@/lib/api/org.service";
import { resourcesService } from "@/lib/api/resources.service";
import { inboxService } from "@/lib/api/inbox.service";
import { backupPath, backupService, diskService, serverService, updatesService, type ConnectionResult, type Server } from "@/lib/api/server.service";
import { cn, formatDateTime, possessive } from "@/lib/utils";
import { useAuth, useIsOperator, useOrgRole } from "@/context/AuthContext";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  cancelButton,
  card,
  dangerButton,
  dialogContent,
  dialogFooter,
  dialogHeader,
  dialogText,
  dialogTitle,
  fieldInput,
  fieldLabel,
  primaryButton,
  smallButton,
} from "@/components/settings/settings-frame";
import { btnSm, field, ghostSm, outline, primarySm, help as helpText, sum as sumText } from "@/app/projects/_lib/ui";
import { ServersIcon } from "@/components/shell/nav-icons";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import { ServerDrawer } from "@/components/ui/server-drawer";
import { BackupCard } from "@/components/servers/backup-card";
import { SharingCard } from "@/components/servers/sharing-card";
import { IsolationCard } from "@/components/servers/isolation-card";
import { UpdatesCard } from "@/components/servers/updates-card";
import { DiskCard } from "@/components/servers/disk-card";
import { duration } from "@/components/servers/updates-view";
import { DNSProviderCard } from "@/components/servers/dns-provider-card";
import { HostKeyDialog } from "@/components/servers/host-key-dialog";
import { useReauth } from "@/components/reauth";
import { cores, gib, isOnline, missingList, serverProblems } from "@/components/servers/capacity";
import { OTHER_COLOURS, OWN_COLOUR, SharedChip, online } from "@/components/servers/resource-bar";
import { ProvisionLog, Stages, stagesOf, useElapsed, useProvisioning } from "@/components/servers/provision-run";
import { AgentBanner, AgentConnection, AgentDialogs, AwayCard, MoveToAgentCard, type AgentDialogKind } from "@/components/servers/agent-card";
import { TONE, agentAway, agentChip, agentFrozen, agentStatus, usesAgent, waitingForAgent } from "@/components/servers/agent";
import { Connection, Firewall, Traefik, settingsHead, titleRow } from "./aside";
import { BackupGlance, CapacityCard, ConnectionGlance, NeedsAttention, RunningHere, TraefikGlance, cardHead } from "./overview";
import { SectionsLayout } from "./sections";
import { attentionItems, imageTag, maintenanceCount, removePlan, serverTab, uptimeLabel, type ServerTab } from "./server-view";

const mono = "font-mono text-xs";
export const sectionHead = "flex flex-col gap-0.5 border-b border-subtle px-4 py-3";
export const h2 = "text-[15px] font-semibold leading-[22px] text-fg";

// simple-icons (CC0, D-141): the board's marks beside Docker and Traefik.
const DOCKER_MARK =
  "M13.983 11.078h2.119a.186.186 0 00.186-.185V9.006a.186.186 0 00-.186-.186h-2.119a.185.185 0 00-.185.185v1.888c0 .102.083.185.185.185m-2.954-5.43h2.118a.186.186 0 00.186-.186V3.574a.186.186 0 00-.186-.185h-2.118a.185.185 0 00-.185.185v1.888c0 .102.082.185.185.185m0 2.716h2.118a.187.187 0 00.186-.186V6.29a.186.186 0 00-.186-.185h-2.118a.185.185 0 00-.185.185v1.887c0 .102.082.185.185.186m-2.93 0h2.12a.186.186 0 00.184-.186V6.29a.185.185 0 00-.185-.185H8.1a.185.185 0 00-.185.185v1.887c0 .102.083.185.185.186m-2.964 0h2.119a.186.186 0 00.185-.186V6.29a.185.185 0 00-.185-.185H5.136a.186.186 0 00-.186.185v1.887c0 .102.084.185.186.186m5.893 2.715h2.118a.186.186 0 00.186-.185V9.006a.186.186 0 00-.186-.186h-2.118a.185.185 0 00-.185.185v1.888c0 .102.082.185.185.185m-2.93 0h2.12a.185.185 0 00.184-.185V9.006a.185.185 0 00-.184-.186h-2.12a.185.185 0 00-.184.185v1.888c0 .102.083.185.185.185m-2.964 0h2.119a.185.185 0 00.185-.185V9.006a.185.185 0 00-.184-.186h-2.12a.186.186 0 00-.186.186v1.887c0 .102.084.185.186.185m-2.92 0h2.12a.185.185 0 00.184-.185V9.006a.185.185 0 00-.184-.186h-2.12a.185.185 0 00-.184.185v1.888c0 .102.082.185.185.185M23.763 9.89c-.065-.051-.672-.51-1.954-.51-.338.001-.676.03-1.01.087-.248-1.7-1.653-2.53-1.716-2.566l-.344-.199-.226.327c-.284.438-.49.922-.612 1.43-.23.97-.09 1.882.403 2.661-.595.332-1.55.413-1.744.42H.751a.751.751 0 00-.75.748 11.376 11.376 0 00.692 4.062c.545 1.428 1.355 2.48 2.41 3.124 1.18.723 3.1 1.137 5.275 1.137.983.003 1.963-.086 2.93-.266a12.248 12.248 0 003.823-1.389c.98-.567 1.86-1.288 2.61-2.136 1.252-1.418 1.998-2.997 2.553-4.4h.221c1.372 0 2.215-.549 2.68-1.009.309-.293.55-.65.707-1.046l.098-.288Z";
const TRAEFIK_MARK =
  "M12 1.19c1.088 0 2.056.768 2.056 1.714 0 .947-.921 1.715-2.056 1.715-.13 0-.3-.022-.509-.064a.685.685 0 0 0-.475.076l-7.37 4.195a.344.344 0 0 0 .001.597l7.99 4.49c.208.116.461.116.669 0l8.034-4.468a.343.343 0 0 0 .003-.598l-2.507-1.424a.683.683 0 0 0-.67-.003l-2.647 1.468a.234.234 0 0 0-.119.18l-.001.025c0 .946-.921 1.714-2.056 1.714s-2.056-.768-2.056-1.714c0-.947.921-1.715 2.056-1.715.042 0 .09.002.145.007l.087.008.096.013a.685.685 0 0 0 .425-.08l3.913-2.173c.3-.166.662-.171.965-.017l.04.023 5.465 3.104c.686.39.693 1.368.03 1.773l-.037.021-3.656 2.033a.343.343 0 0 0 .007.604l3.62 1.906c.72.378.736 1.402.03 1.804l-10.995 6.272a1.03 1.03 0 0 1-1.019 0L.526 16.43a1.03 1.03 0 0 1 .034-1.806l3.66-1.911a.343.343 0 0 0 .01-.603L.524 10.029a1.03 1.03 0 0 1-.041-1.77l.036-.021L9.618 3.06a.688.688 0 0 0 .308-.369l.011-.036c.32-.952 1.046-1.466 2.063-1.466Zm5.076 12.63-4.492 2.586-.041.022c-.306.158-.671.152-.973-.018l-4.478-2.527a.682.682 0 0 0-.65-.01L3.86 15.224a.343.343 0 0 0-.012.602l7.887 4.515c.21.12.467.121.677 0l7.956-4.547a.343.343 0 0 0-.01-.602l-2.623-1.384a.683.683 0 0 0-.659.012z";

const TABS: { id: ServerTab; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "maintenance", label: "Maintenance" },
  { id: "settings", label: "Settings" },
];

/** A server this workspace owns (board: ReworkServer, D-136): the header and
 *  three tabs, ?tab=overview (the default), maintenance and settings. A link
 *  from before the tabs, /servers/<id>#backup, opens the tab its card is on. */
export function OwnerView({ server }: { server: Server }) {
  const id = server.id;
  const router = useRouter();
  const searchParams = useSearchParams();
  const param = searchParams.get("tab");
  // The page renders in the browser only once the server is read, so the
  // anchor is there to read.
  const [anchorTab] = useState(() => (typeof window === "undefined" ? null : serverTab(null, window.location.hash)));
  const tab = param ? serverTab(param, "") : (anchorTab ?? "overview");
  // An old link's anchor: the address says which tab it is on now.
  useEffect(() => {
    if (!param && anchorTab && anchorTab !== "overview") router.replace(`?tab=${anchorTab}${window.location.hash}`, { scroll: false });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  // A link to a card on another tab lands on the card once the tab is drawn,
  // and the card takes focus: Tab goes on from there.
  useEffect(() => {
    const anchor = window.location.hash.slice(1);
    if (!anchor) return;
    requestAnimationFrame(() => {
      const el = document.getElementById(anchor);
      el?.scrollIntoView({ block: "start" });
      el?.focus({ preventScroll: true });
    });
  }, [tab]);

  const { user } = useAuth();
  const isOperator = useIsOperator();
  const role = useOrgRole();
  const canManage = role === "owner";
  const ownOrgId = user?.orgId ?? "";
  const ownName = user?.orgs?.find((o) => o.org_id === ownOrgId)?.name ?? "This workspace";
  const isLocal = ["localhost", "127.0.0.1", "::1", "0.0.0.0"].includes(server.ip_address);

  const [shell, setShell] = useState(false);
  const [hostKey, setHostKey] = useState(false);
  const [test, setTest] = useState<{ ok: ConnectionResult } | { error: string } | null>(null);
  const [agentDialog, setAgentDialog] = useState<AgentDialogKind | null>(null);
  // Moving, joining again and revoking: owners and instance operators (D-106).
  const canAdmin = canManage || isOperator;
  const agent = agentStatus(server);
  const away = agentAway(server);

  const capacity = useQuery({
    queryKey: ["server-capacity", id],
    queryFn: () => serverService.getCapacitySummary(id),
    refetchInterval: 30_000,
  });
  const summary = capacity.data;
  const fit = useQuery({ queryKey: ["server-fit", id, 0, 0], queryFn: () => serverService.fit(id), enabled: !waitingForAgent(server) });
  const room = fit.data;
  const sharesRead = useQuery({
    queryKey: ["server-shares", id],
    queryFn: () => serverService.listShares(id),
    enabled: canManage,
  });
  const shares = sharesRead.data ?? [];
  // Set only when the read failed: the Capacity bar and the Remove dialog
  // would otherwise say nothing is set aside and no share ends.
  const retryShares = readFailed(sharesRead) ? () => void sharesRead.refetch() : undefined;
  const servicesRead = useQuery({
    queryKey: ["server-services", id],
    queryFn: () => serverService.listServices(id),
    refetchInterval: 30_000,
  });
  const services = servicesRead.data;
  const servicesError = readFailed(servicesRead);
  const { data: resources } = useQuery({ queryKey: ["server-resources", id], queryFn: () => resourcesService.server(id) });
  const { data: members } = useQuery({ queryKey: ["org-members"], queryFn: () => orgService.listMembers() });
  const addedBy = members?.find((m) => m.user_id === server.user_id)?.user_name;
  // Needs attention reads what the Servers page reads for a server's
  // problems (the same keys, one cache), with its updates and last run.
  const backup = useQuery({ queryKey: ["backup", backupPath(id)], queryFn: () => backupService.get(backupPath(id)), retry: false });
  const { data: disk } = useQuery({ queryKey: ["disk", id], queryFn: () => diskService.get(id), enabled: !isLocal && isOnline(server), retry: false });
  const updatesRead = useQuery({ queryKey: ["updates", id], queryFn: () => updatesService.get(id), enabled: !isLocal });
  const updates = updatesRead.data ?? undefined;
  const { data: run } = useQuery({ queryKey: ["provision-run", id], queryFn: () => serverService.lastProvision(id), enabled: !isLocal });
  const needs = useQuery({ queryKey: ["inbox", "needs-you", "any"], queryFn: () => inboxService.needsYou("any") });
  const noBackup = needs.data?.find((it) => it.kind === "no_backup" && it.server_id === id);
  const now = backup.dataUpdatedAt;
  // What Needs attention couldn't check. A failed refetch keeps its figures.
  const items = attentionItems({
    unread: [readFailed(capacity) || readFailed(fit) ? "capacity" : "", readFailed(updatesRead) ? "updates" : "", !isLocal && (readFailed(backup) || readFailed(needs)) ? "backups" : ""].filter(Boolean),
    problems: serverProblems({
      online: isOnline(server),
      summary,
      room,
      suggestions: resources?.services.length ?? 0,
      freesMB: resources?.frees_mb,
      cpuWait: resources?.cpu_wait,
      provisionWarnings: server.provision_warnings,
      backup: backup.isError ? undefined : backup.data,
      noBackup: noBackup?.detail,
      disk,
      now,
    }),
    updates,
    poolFix: run?.ok && run.warnings.includes("the app network address pool") ? run.address_pool_fix || undefined : undefined,
    moveToAgent: !isLocal && !usesAgent(server) && canAdmin,
    canAdmin,
  });
  // A read still going can't back "nothing needs attention".
  const reading = [capacity, fit, updatesRead, backup, needs].some((q) => q.data === undefined && q.fetchStatus !== "idle");
  const due = maintenanceCount(items);

  const testMutation = useMutation({
    mutationFn: () => serverService.testConnection(id),
    onMutate: () => setTest(null),
    onSuccess: (r) => {
      setTest({ ok: r });
      if (r.host_key_newly_pinned) toast.info("SSH host key pinned on this first connection", { description: `${r.host_key_type} ${r.host_key_fingerprint}` });
    },
    onError: (err: Error) => setTest({ error: err.message }),
  });

  const others = (summary?.workspaces ?? []).filter((w) => w.org_id !== ownOrgId);
  const otherIds = [...new Set([...shares.map((s) => s.org_id), ...others.map((w) => w.org_id)])];
  const colourOf = (orgId: string) =>
    orgId === ownOrgId ? OWN_COLOUR : OTHER_COLOURS[Math.max(0, otherIds.indexOf(orgId)) % OTHER_COLOURS.length];
  const nameOf = (orgId: string) => shares.find((s) => s.org_id === orgId)?.org_name ?? others.find((w) => w.org_id === orgId)?.org_name ?? "";
  const frozen = agentFrozen(server);
  const chip = agent && (agent !== "connected" || frozen) ? agentChip(agent, frozen) : null;
  const state = chip ? { label: chip.label, chip: TONE[chip.tone].chip } : online(server);
  const traefik = summary?.traefik_image ? imageTag(summary.traefik_image) : null;

  return (
    <div className="flex w-full max-w-[1600px] flex-col gap-5 pb-14 max-sm:gap-4 md:-mt-3">
      <div className="flex flex-col gap-3">
        <p className="text-[13px] text-muted">
          <Link href="/servers" className="hover:text-fg hover:underline">Servers</Link>
          <span aria-hidden="true" className="mx-1.5">/</span>
          {isLocal ? "Panel host" : "App server"}
        </p>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 flex-wrap items-center gap-3">
            <span className="flex h-8 w-8 flex-none items-center justify-center rounded-lg border border-strong bg-raised text-muted">
              <ServersIcon className="h-4 w-4" aria-hidden="true" />
            </span>
            <h1 className="break-all font-mono text-xl font-medium leading-7 tracking-[-0.01em] text-fg">{server.name}</h1>
            <span className={cn("inline-flex h-[22px] items-center gap-1.5 rounded-full px-2 text-xs font-medium", state.chip)}>
              <span className={cn("h-1.5 w-1.5 rounded-full bg-current", chip?.pulse && "motion-safe:animate-pulse")} aria-hidden="true" />
              {state.label}
            </span>
            {shares.length > 0 && <SharedChip names={shares.map((s) => s.org_name)} />}
          </div>
          <div className="flex flex-wrap items-center gap-2 max-md:w-full">
            {isOperator && (
              <button type="button" className={headerButton} disabled={away} onClick={() => setShell(true)}>
                <Terminal className="h-[15px] w-[15px]" aria-hidden="true" />
                Host shell
              </button>
            )}
            {canManage && (
              <button type="button" className={cn(headerButton, "max-md:flex-1 max-md:justify-center")} disabled={testMutation.isPending} onClick={() => testMutation.mutate()}>
                <Activity className="h-[15px] w-[15px]" aria-hidden="true" />
                {testMutation.isPending ? "Testing…" : "Test connection"}
              </button>
            )}
          </div>
        </div>
        <dl className="flex flex-wrap gap-x-6 gap-y-1.5 text-[13px] max-md:flex-col">
          <Meta k="Address"><span className={mono}>{server.ip_address}</span></Meta>
          {usesAgent(server) ? (
            <Meta k="Agent">
              <span className={cn(mono, away && "text-status-failed-fg")}>
                {agent === "waiting" ? "not joined yet" : agent === "revoked" ? "revoked" : `${server.agent?.version ?? "—"} · ${away ? "offline" : "outbound 443"}`}
              </span>
            </Meta>
          ) : (
            <Meta k="SSH"><span className={mono}>{server.user} · port {server.port}</span></Meta>
          )}
          <Meta k="Docker" mark={DOCKER_MARK}><span className={mono}>{summary?.docker_version || "—"}</span></Meta>
          <Meta k="Traefik" mark={TRAEFIK_MARK}><span className={mono}>{traefik ?? "—"}</span></Meta>
          {summary?.uptime_seconds ? <Meta k="Uptime"><span className={mono}>{uptimeLabel(summary.uptime_seconds)}</span></Meta> : null}
        </dl>
        {test && (
          <p
            role={"error" in test ? "alert" : "status"}
            className={cn(
              "flex flex-wrap items-center gap-2 rounded-lg px-3 py-2 text-[13px]",
              "error" in test ? "bg-status-failed-soft text-status-failed-fg" : "bg-status-running-soft text-status-running-fg",
            )}
          >
            {"error" in test ? (
              <>
                <X className="h-4 w-4 flex-none" aria-hidden="true" />
                <span className="min-w-0 flex-1 break-words">{test.error}</span>
                {isOperator && /host key/i.test(test.error) && (
                  <button type="button" className={smallButton} onClick={() => setHostKey(true)}>Verify or replace</button>
                )}
              </>
            ) : (
              <>
                <Check className="h-4 w-4 flex-none" aria-hidden="true" />
                Connected in {test.ok.latency_ms} ms, host key {test.ok.host_key_newly_pinned ? "pinned on this first connection" : "matched"}.
              </>
            )}
            <button type="button" aria-label="Dismiss" className="touch-target ml-auto inline-flex items-center justify-center rounded p-0.5 hover:bg-raised pointer-coarse:min-w-11" onClick={() => setTest(null)}>
              <X className="h-3.5 w-3.5" aria-hidden="true" />
            </button>
          </p>
        )}
        <nav aria-label="Server" className="flex max-w-full gap-1 overflow-x-auto whitespace-nowrap border-b border-subtle">
          {TABS.map((t) => (
            <Link
              key={t.id}
              href={t.id === "overview" ? `/servers/${id}` : `/servers/${id}?tab=${t.id}`}
              replace
              scroll={false}
              aria-current={tab === t.id ? "page" : undefined}
              className={cn(
                "-mb-px inline-flex h-[38px] items-center gap-1.5 border-b-2 px-3 text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus pointer-coarse:min-h-11",
                tab === t.id ? "border-link text-fg" : "border-transparent text-fg/70 hover:text-fg",
              )}
            >
              {t.label}
              {t.id === "maintenance" && due.n > 0 && (
                <span
                  className={cn(
                    "rounded-full px-1.5 font-mono text-xs font-semibold leading-[18px]",
                    due.tone === "bad" ? "bg-status-failed-soft text-status-failed-fg" : "bg-status-pending-soft text-status-pending-fg",
                  )}
                >
                  {due.n}
                  <span className="sr-only"> {due.n === 1 ? "item needs" : "items need"} action</span>
                </span>
              )}
            </Link>
          ))}
        </nav>
      </div>

      <AgentBanner server={server} canAdmin={canAdmin} onDialog={setAgentDialog} />

      {tab === "overview" && (
        <div className="grid items-start gap-6 max-lg:grid-cols-1 max-sm:gap-4 lg:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)]">
          <div className="flex min-w-0 flex-col gap-5 max-sm:gap-4">
            {away && <AwayCard />}
            <NeedsAttention serverId={id} items={items} reading={reading} checkedAt={updates?.checked_at} now={updatesRead.dataUpdatedAt} />
            <CapacityCard
              server={server}
              isLocal={isLocal}
              canManage={canManage}
              ownOrgId={ownOrgId}
              ownName={ownName}
              colourOf={colourOf}
              nameOf={nameOf}
              otherIds={otherIds}
              shares={shares}
              retryShares={retryShares}
              capacity={capacity}
              room={room}
              disk={disk}
            />
            <RunningHere
              ownOrgId={ownOrgId}
              ownName={ownName}
              services={services}
              servicesError={servicesError}
              summary={summary}
              shares={shares}
              resources={resources}
            />
          </div>
          <aside aria-label="At a glance" className="flex min-w-0 flex-col gap-5 max-sm:gap-4">
            <ConnectionGlance server={server} />
            <TraefikGlance server={server} summary={summary} />
            {!isLocal && <BackupGlance serverId={id} backup={backup.isError ? undefined : backup.data} now={now} />}
          </aside>
        </div>
      )}

      {tab === "maintenance" && (
        <SectionsLayout
          sections={[
            ...(!isLocal
              ? [
                  { id: "updates", label: "Updates", count: maintenanceCount(items.filter((i) => i.anchor === "updates")) },
                  { id: "disk", label: "Disk", count: maintenanceCount(items.filter((i) => i.anchor === "disk")) },
                ]
              : []),
            { id: "provisioning", label: "Provisioning", count: maintenanceCount(items.filter((i) => i.anchor === "provisioning")) },
          ]}
        >
          {!isLocal && (
            <div id="updates" tabIndex={-1}>
              <UpdatesCard serverId={id} serverName={server.name} agent={server.connection === "agent"} canCheck={!!role && role !== "viewer"} canRun={canAdmin}
                services={(services ?? []).map((s) => s.name)} />
            </div>
          )}
          {!isLocal && <DiskCard serverId={id} serverName={server.name} canWrite={canAdmin} />}
          <div id="provisioning" tabIndex={-1}>
            {isLocal ? (
              <section aria-labelledby="prov-h" className={card}>
                <div className={sectionHead}>
                  <h2 id="prov-h" className={h2}>Provisioning</h2>
                  <span className="text-xs text-muted">
                    The panel host runs Docker and Traefik through the panel&apos;s Docker Compose, so it isn&apos;t provisioned from here.
                  </span>
                </div>
              </section>
            ) : (
              <ProvisioningCard server={server} canManage={canManage} />
            )}
          </div>
        </SectionsLayout>
      )}

      {tab === "settings" && (
        <SectionsLayout
          label="Jump to a settings section"
          sections={[
            { id: "connection", label: "Connection" },
            ...(!isLocal && !usesAgent(server) && canAdmin ? [{ id: "agent", label: "Agent" }] : []),
            ...(!isLocal ? [{ id: "isolation", label: "Isolation" }] : []),
            { id: "traefik", label: "Traefik" },
            { id: "dns", label: "DNS provider" },
            ...(!isLocal ? [{ id: "firewall", label: "Firewall and ports" }, { id: "backup", label: "Backups" }] : []),
            ...(canManage && !isLocal ? [{ id: "overrides", label: "Capacity overrides" }] : []),
            ...(canManage ? [{ id: "sharing", label: "Sharing", count: { n: shares.length } }, { id: "remove", label: "Remove server", danger: true }] : []),
          ]}
        >
          <div id="connection" tabIndex={-1}>
            {usesAgent(server) ? (
              <AgentConnection server={server} canAdmin={canAdmin} isOwner={canManage} onDialog={setAgentDialog} />
            ) : (
              <Connection server={server} isOperator={isOperator} onHostKey={() => setHostKey(true)} addedBy={addedBy} />
            )}
          </div>
          {!isLocal && !usesAgent(server) && canAdmin && (
            <div id="agent" tabIndex={-1}>
              <MoveToAgentCard onMove={() => setAgentDialog("move-agent")} />
            </div>
          )}
          {!isLocal && (
            <div id="isolation" tabIndex={-1}>
              <IsolationCard serverId={id} serverName={server.name} canProbe={!!role && role !== "viewer"} isOperator={isOperator} />
            </div>
          )}
          <div id="traefik" tabIndex={-1}>
            <Traefik server={server} summary={summary} />
          </div>
          <div id="dns" tabIndex={-1}>
            <DNSProviderCard serverId={id} serverName={server.name} canManage={canManage} provisionedAt={server.provisioned_at} />
          </div>
          {!isLocal && (
            <div id="firewall" tabIndex={-1}>
              <Firewall server={server} />
            </div>
          )}
          {!isLocal && <BackupCard serverId={id} serverName={server.name} canManage={canManage} />}
          {canManage && !isLocal && (
            <div id="overrides" tabIndex={-1}>
              <CapacityOverrides server={server} />
            </div>
          )}
          {canManage && (
            <div id="sharing" tabIndex={-1}>
              <SharingCard serverId={id} canManage={canManage} colourOf={colourOf} summary={summary} />
            </div>
          )}
          {canManage && (
            <div id="remove" tabIndex={-1}>
              <RemoveCard server={server} ownServices={services} servicesFailed={servicesError} retryServices={() => void servicesRead.refetch()} others={others} shares={shares} retryShares={retryShares} />
            </div>
          )}
        </SectionsLayout>
      )}

      {isOperator && <ServerDrawer serverId={id} serverName={server.name} open={shell} onClose={() => setShell(false)} />}
      {/* Mounted only while open: it scans the server when it mounts. */}
      {isOperator && hostKey && <HostKeyDialog server={server} onOpenChange={setHostKey} />}
      {canAdmin && <AgentDialogs server={server} kind={agentDialog} onClose={() => setAgentDialog(null)} />}
    </div>
  );
}

const headerButton =
  "touch-target inline-flex h-9 items-center gap-1.5 rounded-lg border border-strong bg-raised px-3 text-[13px] font-medium text-fg hover:bg-raised/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-50";

function Meta({ k, mark, children }: { k: string; mark?: string; children: React.ReactNode }) {
  return (
    <div className="flex min-w-0 items-baseline gap-2">
      <dt className="inline-flex items-center gap-1.5 self-center text-muted">
        {mark && (
          <svg viewBox="0 0 24 24" className="h-3.5 w-3.5 flex-none" fill="currentColor" aria-hidden="true">
            <path d={mark} />
          </svg>
        )}
        {k}
      </dt>
      <dd className="text-fg [overflow-wrap:anywhere]">{children}</dd>
    </div>
  );
}

// ─── Capacity overrides ──────────────────────────────────────────────────────

/** The overrides that used to sit under Capacity, as their own card in
 *  Settings (board: ReworkServerSettings). */
function CapacityOverrides({ server }: { server: Server }) {
  const queryClient = useQueryClient();
  const { data: summary } = useQuery({ queryKey: ["server-capacity", server.id], queryFn: () => serverService.getCapacitySummary(server.id) });
  const known = !!summary?.capacity_discovered;
  const [cpu, setCpu] = useState(server.cpu_cores_override != null ? String(server.cpu_cores_override) : "");
  const [mem, setMem] = useState(server.memory_mb_override != null ? String(Math.round((server.memory_mb_override / 1024) * 10) / 10) : "");
  const cpuValue = cpu.trim() === "" ? null : Number(cpu);
  const memValue = mem.trim() === "" ? null : Number(mem);
  const overridesValid = (cpuValue === null || cpuValue > 0) && (memValue === null || memValue > 0);
  const save = useMutation({
    mutationFn: () =>
      serverService.updateCapacity(server.id, {
        cpu_cores_override: cpuValue,
        memory_mb_override: memValue === null ? null : Math.round(memValue * 1024),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["server-capacity", server.id] });
      queryClient.invalidateQueries({ queryKey: ["server", server.id] });
      toast.success("Overrides saved");
    },
    onError: (err: Error) => toast.error(err instanceof ApiError && err.status < 500 ? err.message : "The overrides were not saved"),
  });

  return (
    <section aria-labelledby="ovr-h" className={card}>
      <div className={settingsHead}>
        <div className={titleRow}>
          <h2 id="ovr-h" className={h2}>Capacity overrides</h2>
          <span className={sumText}>empty uses what the scan found</span>
        </div>
      </div>
      <form
        className="flex flex-col gap-2.5 p-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (overridesValid) save.mutate();
        }}
      >
        <div className="grid items-end gap-3.5 sm:grid-cols-3">
          <div className="flex min-w-0 flex-col gap-1.5">
            <label htmlFor="ovr-cpu" className="text-[13px] font-medium text-fg">CPU, vCPU</label>
            <input
              id="ovr-cpu"
              inputMode="decimal"
              value={cpu}
              onChange={(e) => setCpu(e.target.value)}
              aria-invalid={cpuValue !== null && !(cpuValue > 0)}
              placeholder={server.cpu_cores_override == null && known && summary ? `${cores(summary.effective_cpu_cores)} found` : ""}
              className={cn(field, "font-mono text-[13px]")}
            />
          </div>
          <div className="flex min-w-0 flex-col gap-1.5">
            <label htmlFor="ovr-mem" className="text-[13px] font-medium text-fg">Memory, GiB</label>
            <input
              id="ovr-mem"
              inputMode="decimal"
              value={mem}
              onChange={(e) => setMem(e.target.value)}
              aria-invalid={memValue !== null && !(memValue > 0)}
              placeholder={server.memory_mb_override == null && known && summary ? `${gib(summary.effective_memory_mb)} found` : ""}
              className={cn(field, "font-mono text-[13px]")}
            />
          </div>
          <button type="submit" className={cn(outline, "self-end justify-self-start")} disabled={save.isPending || !overridesValid}>
            {save.isPending ? "Saving…" : "Save overrides"}
          </button>
        </div>
        {!overridesValid && (
          <span role="alert" className="text-xs text-status-failed-fg">Enter numbers above 0, or leave a field empty.</span>
        )}
      </form>
    </section>
  );
}

// ─── Provisioning ────────────────────────────────────────────────────────────

function ProvisioningCard({ server, canManage }: { server: Server; canManage: boolean }) {
  const { view, loading, watching, start, stopWatching } = useProvisioning(server.id);
  const elapsed = useElapsed(view);
  const [confirm, setConfirm] = useState(false);
  const [showLog, setShowLog] = useState(false);
  const running = view?.outcome === "running";
  const stages = view ? stagesOf(view.lines, view.outcome) : [];
  const provisioned = !!server.provisioned_at;
  const warned = view?.outcome === "ok" && !!view.warnings?.length;
  // A run that is going or failed shows its log; a finished one on request.
  const logOpen = !!view && (running || view.outcome === "failed" || showLog);
  const what = "Installs or checks Docker, the networks, Traefik and the firewall; changes only what is missing.";

  return (
    <section aria-labelledby="prov-h" className={card}>
      <div className={cardHead}>
        <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <h2 id="prov-h" className={h2}>Provisioning</h2>
          {view && (
            <>
              <span
                className={cn(
                  "inline-flex items-center gap-1.5 text-xs font-medium",
                  running
                    ? "text-status-building-fg"
                    : warned
                      ? "text-status-pending-fg"
                      : view.outcome === "ok"
                        ? "text-status-running-fg"
                        : "text-status-failed-fg",
                )}
              >
                <span aria-hidden="true" className={cn("h-1.5 w-1.5 rounded-full bg-current", running && "motion-safe:animate-pulse")} />
                {running ? "Running" : warned ? "Finished with warnings" : view.outcome === "ok" ? "Succeeded" : "Failed"}
              </span>
              <span className={sumText}>
                {formatDateTime(new Date(view.startedAt).toISOString())} · {duration(elapsed * 1000)}
                {view.startedBy && ` · by ${view.startedBy}`}
              </span>
            </>
          )}
        </div>
        {canManage && !running && (
          <button type="button" className={provisioned ? btnSm : primarySm} onClick={() => (provisioned ? setConfirm(true) : start())}>
            <RotateCw className="h-[13px] w-[13px]" aria-hidden="true" />
            {provisioned ? "Re-provision" : "Provision"}
          </button>
        )}
      </div>

      <div className="flex flex-col gap-3.5 p-4">
        {!view ? (
          <p className="text-[13px] text-fg/70">
            {loading
              ? "Loading…"
              : provisioned
                ? `Provisioned ${formatDateTime(server.provisioned_at!)}. That run’s log wasn’t kept; runs are recorded from now on.`
                : "Not provisioned yet. Provisioning installs Docker if it’s missing, then sets up the app networks, Traefik with Let’s Encrypt, the firewall and security updates. Safe to run again."}
          </p>
        ) : (
          <>
            <Stages stages={stages} />
            {view.lines.length > 0 && !running && view.outcome !== "failed" && (
              <div className="flex flex-wrap items-center gap-x-2.5 gap-y-2">
                <button type="button" className={ghostSm} aria-expanded={showLog} aria-controls="prov-log" onClick={() => setShowLog(!showLog)}>
                  <ChevronRight className={cn("h-[13px] w-[13px]", showLog && "rotate-90")} aria-hidden="true" />
                  {showLog ? "Hide the log" : "Show the log"} · {view.lines.length} {view.lines.length === 1 ? "line" : "lines"}
                </button>
                <span className={helpText}>{what}</span>
              </div>
            )}
            {logOpen && view.lines.length > 0 && (
              <div id="prov-log">
                <ProvisionLog lines={view.lines} live={running} />
              </div>
            )}
            {view.outcome === "failed" && view.error && (
              <p role="alert" className="flex items-start gap-2 rounded-lg bg-status-failed-soft px-3 py-2.5 text-[13px] text-status-failed-fg">
                <AlertTriangle className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
                <span className="min-w-0 break-words">{view.error}</span>
              </p>
            )}
            {warned && (
              <p role="status" className="flex items-start gap-2 rounded-lg bg-status-pending-soft px-3 py-2.5 text-[13px] text-status-pending-fg">
                <AlertTriangle className="mt-0.5 h-4 w-4 flex-none" aria-hidden="true" />
                <span className="min-w-0 break-words">
                  The server is usable, but without {missingList(view.warnings!)}. The log’s warnings say why; fix that, then Re-provision.
                </span>
              </p>
            )}
            {running && (
              <div className="flex flex-wrap items-center gap-3 border-t border-subtle pt-3 text-xs text-muted">
                <span className="min-w-0 flex-[1_1_320px]">
                  {watching
                    ? "Stop watching only stops this page from following along. Provisioning keeps running on the server."
                    : "Started elsewhere; this page checks on it every few seconds."}
                </span>
                {watching && (
                  <button type="button" className={cancelButton} onClick={stopWatching}>Stop watching</button>
                )}
              </div>
            )}
            {(running || view.outcome === "failed" || view.lines.length === 0) && <span className={helpText}>{what}</span>}
          </>
        )}
      </div>

      <Dialog open={confirm} onOpenChange={setConfirm}>
        <DialogContent className={dialogContent}>
          <DialogHeader className={dialogHeader}>
            <DialogTitle className={dialogTitle}>
              Re-provision <span className="font-mono font-medium">{server.name}</span>?
            </DialogTitle>
            <DialogDescription className={dialogText}>
              Checks Docker, the networks, Traefik and the firewall, and changes only what is missing or out of date. Running services are not redeployed.
            </DialogDescription>
          </DialogHeader>
          <ul className="mx-5 mt-1 flex list-disc flex-col gap-1.5 pl-[18px] text-[13px] leading-[19px] text-fg/85">
            <li>If Traefik’s configuration changed, as it does after a DNS provider change, Traefik restarts and this server’s routes pause for a few seconds. Certificates are kept.</li>
            <li>Docker is never restarted while containers run on it.</li>
            <li>Firewall rules you added yourself are left as they are.</li>
          </ul>
          <div className={dialogFooter}>
            <button type="button" className={cancelButton} onClick={() => setConfirm(false)}>Cancel</button>
            <button
              type="button"
              className={primaryButton}
              onClick={() => {
                setConfirm(false);
                start();
              }}
            >
              Re-provision
            </button>
          </div>
        </DialogContent>
      </Dialog>
    </section>
  );
}

// ─── Remove ──────────────────────────────────────────────────────────────────

function RemoveCard({
  server,
  ownServices,
  servicesFailed,
  retryServices,
  others,
  shares,
  retryShares,
}: {
  server: Server;
  /** Undefined until they're read: the plan, and so Remove, wait for it. */
  ownServices: { id: string; name: string; project_name: string }[] | undefined;
  servicesFailed: boolean;
  retryServices: () => void;
  others: { org_id: string; org_name: string; services: number }[];
  shares: { org_id: string; org_name: string }[];
  /** Set when the shares couldn't be read. */
  retryShares?: () => void;
}) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  const plan = removePlan(ownServices?.length ?? 0, others, shares);
  const n = plan.services;
  const { data: backup } = useQuery({
    queryKey: ["backup", backupPath(server.id)],
    queryFn: () => backupService.get(backupPath(server.id)),
    retry: false,
  });

  const confirmPassword = useReauth();
  const remove = useMutation({
    mutationFn: async () => {
      if (n > 0 && !(await confirmPassword(`Removing ${server.name} takes its services out of Toft, so Toft asks for your password first.`))) {
        throw new Error("The server stays without your password.");
      }
      return serverService.delete(server.id, n > 0);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["servers"] });
      queryClient.invalidateQueries({ queryKey: ["workspace-services"] });
      toast.success(`${server.name} removed`);
      router.push("/servers");
    },
  });
  const close = (o: boolean) => {
    if (remove.isPending) return;
    setOpen(o);
    if (!o) {
      setTyped("");
      remove.reset();
    }
  };
  const blocked = plan.blockedBy.length > 0;
  const one = plan.blockedBy.length === 1;
  const blockedServices = plan.blockedBy.reduce((a, b) => a + b.services, 0);
  const ok = !!ownServices && !blocked && (!plan.typeName || typed.trim() === server.name);

  return (
    <section aria-labelledby="rm-h" className={cn(card, "flex flex-wrap items-center gap-x-4 gap-y-3 border-danger/40 p-4")}>
      <div className="flex min-w-0 flex-[1_1_320px] flex-col">
        <h2 id="rm-h" className={h2}>Remove server</h2>
        <p className="text-[13px] leading-[19px] text-muted">
          Removes {server.name} from Toft; nothing on the machine is stopped or deleted.
          {n > 0 && ` ${n === 1 ? "Its service is" : `Its ${n} services are`} removed from Toft with it.`}
        </p>
      </div>
      <button type="button" className={dangerButton} onClick={() => setOpen(true)}>
        Remove server…
      </button>

      <Dialog open={open} onOpenChange={close}>
        <DialogContent className={cn(dialogContent, "max-w-[540px]")}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              if (ok) remove.mutate();
            }}
          >
            <DialogHeader className={dialogHeader}>
              {blocked ? (
                <>
                  <DialogTitle className={dialogTitle}>
                    Move {plan.blockedBy.map((b) => possessive(b.name)).join(" and ")} {blockedServices} {blockedServices === 1 ? "service" : "services"} before
                    removing <span className="font-mono font-medium">{server.name}</span>
                  </DialogTitle>
                  <DialogDescription className={dialogText}>
                    {plan.blockedBy.map((b) => `${b.name} has ${b.services} ${b.services === 1 ? "service" : "services"}`).join(" and ")} here, inside{" "}
                    {one ? "its share" : "their shares"}. Only {one ? possessive(plan.blockedBy[0].name) : "those workspaces’"} owners can move them to another
                    server; once they have, this one can be removed.
                  </DialogDescription>
                </>
              ) : (
                <>
                  <DialogTitle className={dialogTitle}>
                    Remove <span className="font-mono font-medium">{server.name}</span>
                    {n > 0 ? " and its services?" : "?"}
                  </DialogTitle>
                  <DialogDescription className={dialogText}>
                    The server leaves Toft. Nothing on the machine is stopped or deleted: containers, volumes and data stay where they are.
                  </DialogDescription>
                </>
              )}
            </DialogHeader>
            <div className="mx-5 mt-3.5 flex flex-col gap-3.5">
              {!blocked && (
                <>
                  {n > 0 && (
                    <div className="flex flex-col gap-2 rounded-lg border border-danger/40 px-3 py-2.5 text-[13px] leading-[19px] text-fg/85">
                      <span>
                        {n} {n === 1 ? "service still runs" : "services still run"} here. Removing the server also removes{" "}
                        {n === 1 ? "it" : "them"} from Toft, with {n === 1 ? "its" : "their"} deployments, variables and domains
                        {plan.shares.length > 0 && `, and ends ${possessive(plan.shares.join(" and "))} share`}.
                      </span>
                      <span className="flex flex-wrap gap-1.5">
                        {ownServices?.map((s) => (
                          <span key={s.id} className="rounded border border-subtle px-1.5 font-mono text-xs leading-5 text-fg/85">
                            {s.project_name ? `${s.project_name}/` : ""}{s.name}
                          </span>
                        ))}
                      </span>
                    </div>
                  )}
                  {n === 0 && plan.shares.length > 0 && (
                    <p className="text-[13px] text-fg/85">{possessive(plan.shares.join(" and "))} share ends with it.</p>
                  )}
                  {servicesFailed && <LoadFailed what="the services on this server" onRetry={retryServices} />}
                  {retryShares && <LoadFailed what="who shares this server" onRetry={retryShares} />}
                  {backup && (
                    <p className="text-[13px] leading-[19px] text-fg/85">
                      It keeps backing up to <span className="font-mono text-xs">{backup.bucket}</span> every night: removing it from Toft doesn’t stop
                      that. <a href="#backup" className="inline-link" onClick={() => close(false)}>Stop backups</a> first if
                      you don’t want it to.
                    </p>
                  )}
                  {plan.typeName && (
                    <div className="flex flex-col gap-1.5">
                      <label htmlFor="rm-name" className={fieldLabel}>
                        Type <span className="font-mono text-fg">{server.name}</span> to confirm
                      </label>
                      <input id="rm-name" autoComplete="off" spellCheck={false} value={typed} onChange={(e) => setTyped(e.target.value)} className={cn(fieldInput, "font-mono")} />
                    </div>
                  )}
                </>
              )}
              {remove.error && (
                <p role="alert" className="rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
                  {remove.error instanceof ApiError && remove.error.status < 500 ? remove.error.message : "The server was not removed."}
                </p>
              )}
            </div>
            <div className={dialogFooter}>
              <button type="button" className={cancelButton} onClick={() => close(false)}>
                {blocked ? "Close" : "Cancel"}
              </button>
              {!blocked && (
                <button type="submit" className={dangerButton} disabled={!ok || remove.isPending}>
                  {remove.isPending ? "Removing…" : n > 0 ? `Remove server and ${n} ${n === 1 ? "service" : "services"}` : "Remove server"}
                </button>
              )}
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </section>
  );
}
