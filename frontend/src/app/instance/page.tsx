"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ArrowUp, XCircle } from "lucide-react";
import { useIsOperator } from "@/context/AuthContext";
import {
  panelHostQuery,
  panelStatusQuery,
  setupQuery,
  upgradeQuery,
  watchdogQuery,
  type PanelContainer,
  type PanelStatus,
  type Watchdog,
} from "@/lib/api/instance.service";
import { backupPath, backupService, diskService, updatesService, type BackupDestination } from "@/lib/api/server.service";
import { inboxService } from "@/lib/api/inbox.service";
import { TONE_CLASSES, type StatusTone } from "@/lib/status";
import { sinceShort } from "@/lib/time";
import { cn, formatDateTime } from "@/lib/utils";
import { btnSm, h2, sum } from "@/app/projects/_lib/ui";
import { card, phoneLabel, phoneNote, phoneRow, phoneTd, smallButton, th } from "@/components/settings/settings-frame";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import { InstanceFrame } from "@/components/instance/instance-frame";
import { PanelLogsDialog } from "@/components/instance/panel-logs-dialog";
import { StopIpDialog } from "@/components/instance/stop-ip-dialog";
import { UpgradeDialog } from "@/components/instance/upgrade";
import { watchdogState } from "@/components/instance/watchdog-card";
import { ROLE_LABEL, certTile, containerStatus, containerWell, imageTag, oldTabRoute, overviewAttention, panelMaintenance, size, upFor, type OverviewRow } from "@/components/instance/instance-view";
import { backupState } from "../settings/backups/backups-view";
import { PANEL_KEY } from "../settings/backups/panel-host";

const BACKUP_TONE: Record<string, StatusTone> = { ok: "running", fail: "failed", wait: "pending", off: "neutral" };

/** Instance → Overview (D-139, board ReworkInstance): what's wrong, not
 *  configuration. Needs attention and the panel's containers; Health and
 *  Setup beside them, Health right after Needs attention on phones.
 *  Operators only. */
export default function InstanceOverviewPage() {
  const isOperator = useIsOperator();
  const router = useRouter();
  // An old /instance?tab=audit link opens its tab, not Overview.
  useEffect(() => {
    const to = oldTabRoute(window.location.search);
    if (to) router.replace(to + window.location.hash);
  }, [router]);
  const [now] = useState(() => Date.now());
  const [logs, setLogs] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [stopIp, setStopIp] = useState(false);
  const attentionHeading = useRef<HTMLHeadingElement>(null);
  const status = useQuery({ ...panelStatusQuery, enabled: isOperator, retry: false });
  const backup = useQuery({ queryKey: PANEL_KEY, queryFn: () => backupService.get(backupPath(null)), enabled: isOperator, retry: false });
  const upgrade = useQuery({ ...upgradeQuery, enabled: isOperator, retry: false });
  const watchdog = useQuery({ ...watchdogQuery, enabled: isOperator, retry: false });
  const setup = useQuery({ ...setupQuery, enabled: isOperator });
  // The keys Maintenance's cards read.
  const updatesRead = useQuery({ queryKey: ["updates", null], queryFn: () => updatesService.get(null), enabled: isOperator });
  const updates = updatesRead.data;
  const diskRead = useQuery({ queryKey: ["disk", null], queryFn: () => diskService.get(null), enabled: isOperator, retry: false });
  const disk = diskRead.data;
  // "any": a person's marks hide a row in the Inbox, never the panel's state here.
  const needs = useQuery({ queryKey: ["inbox", "needs-you", "any"], queryFn: () => inboxService.needsYou("any"), enabled: isOperator });
  const needsYou = needs.data;
  const st = status.data;
  const up = upgrade.data;
  const running = up?.run?.outcome === "running";
  const rows = overviewAttention({
    status: st,
    upgrade: up,
    maintenance: panelMaintenance(updates ?? undefined, disk),
    watchdog: watchdog.data,
    backup: backup.isLoading || backup.isError ? undefined : (backup.data ?? null),
    noBackup: needsYou?.find((it) => it.kind === "no_backup" && !it.server_id)?.detail,
    awaitingDomain: setup.data?.awaiting_domain ? setup.data.domain : undefined,
    kitOutOfDate: setup.data?.kit_out_of_date && setup.data.kit_acknowledged_at ? formatDateTime(setup.data.kit_acknowledged_at).slice(0, 10) : undefined,
    now,
    unread: [readFailed(updatesRead) ? "updates" : "", readFailed(diskRead) ? "the disk" : "", readFailed(backup) || readFailed(needs) ? "backups" : "", readFailed(watchdog) ? "the watchdog" : "", readFailed(upgrade) ? "upgrades" : "", readFailed(setup) ? "the recovery kit" : ""].filter(Boolean),
  });
  // A read still going can't back "nothing needs attention".
  const reading = [status, backup, upgrade, watchdog, setup, updatesRead, diskRead, needs].some((q) => q.data === undefined && q.fetchStatus !== "idle");

  return (
    <InstanceFrame title="Overview">
      {up && <UpgradeDialog status={up} open={confirm} onClose={() => setConfirm(false)} />}
      {up?.install === "deploy_sh" && <AdoptBanner />}
      {status.error && !running && (
        <div role="alert" className="flex items-center gap-2.5 rounded-[10px] border border-danger/35 bg-surface px-4 py-3.5 text-[13px] text-status-failed-fg">
          <AlertTriangle className="h-4 w-4 flex-none" aria-hidden="true" />
          Could not read the panel’s health: {status.error.message}
        </div>
      )}

      <div className="flex flex-col gap-6 min-[1200px]:grid min-[1200px]:grid-cols-[minmax(0,1fr)_340px] min-[1200px]:items-start">
        <div className="contents min-[1200px]:flex min-[1200px]:min-w-0 min-[1200px]:flex-col min-[1200px]:gap-6">
          {st && <Attention rows={rows} reading={reading} checked={status.dataUpdatedAt} now={now} heading={attentionHeading} onLogs={setLogs} onUpgrade={() => setConfirm(true)} onStopIp={() => setStopIp(true)} />}
          <Containers status={st} loading={status.isLoading} now={now} onLogs={setLogs} />
        </div>
        <div className="contents min-[1200px]:flex min-[1200px]:min-w-0 min-[1200px]:flex-col min-[1200px]:gap-6">
          {st && <Health status={st} checked={status.dataUpdatedAt} now={now} backup={backup.isLoading || backup.isError ? undefined : (backup.data ?? null)} watchdog={watchdog.data} />}
          <SetupCard watchdog={watchdog.data} />
        </div>
      </div>

      <PanelLogsDialog container={logs} onClose={() => setLogs(null)} />
      {/* Stays mounted after the stop, so focus has the heading to go to once its row is gone. */}
      {setup.data?.domain && <StopIpDialog domain={setup.data.domain} open={stopIp} onClose={() => setStopIp(false)} returnFocus={attentionHeading} />}
    </InstanceFrame>
  );
}

const ROW_ICON = {
  problem: { Icon: XCircle, tint: "text-status-failed-fg" },
  warning: { Icon: AlertTriangle, tint: "text-status-pending-fg" },
  release: { Icon: ArrowUp, tint: "text-link" },
};

/** What is wrong right now, worst first, each with where to act on it. */
function Attention({ rows, reading, checked, now, heading, onLogs, onUpgrade, onStopIp }: { rows: OverviewRow[]; reading: boolean; checked: number; now: number; heading: React.RefObject<HTMLHeadingElement | null>; onLogs: (name: string) => void; onUpgrade: () => void; onStopIp: () => void }) {
  return (
    <section aria-labelledby="na-h" className={cn(card, "order-1")}>
      <div className="flex flex-wrap items-baseline gap-x-2.5 border-b border-subtle px-4 py-3">
        <h2 id="na-h" ref={heading} tabIndex={-1} className={h2}>
          Needs attention
        </h2>
        <span className={sum}>
          {rows.length ? `${rows.length} ${rows.length === 1 ? "item" : "items"} · ` : ""}checked {sinceShort(new Date(checked).toISOString(), Math.max(now, checked))}
        </span>
      </div>
      {rows.length === 0 ? (
        <p className="px-4 py-3.5 text-[13px] text-muted">{reading ? "Checking…" : "Nothing needs attention."}</p>
      ) : (
        <ul>
          {rows.map((r, i) => {
            const { Icon, tint } = ROW_ICON[r.level];
            const a = r.action;
            return (
              <li key={r.key} data-read-state={r.key === "unread" ? "failed" : undefined} className={cn("flex items-start gap-3 px-4 py-3", i > 0 && "border-t border-subtle")}>
                <Icon className={cn("mt-0.5 h-4 w-4 flex-none", tint)} aria-hidden="true" />
                <div className="flex min-w-0 flex-1 flex-wrap items-start justify-between gap-x-3 gap-y-2">
                  <div className="flex min-w-0 flex-[1_1_300px] flex-col gap-0.5">
                    <span className="text-[13px] font-medium leading-[19px] text-fg">
                      <span className="sr-only">{r.level === "problem" ? "Problem: " : r.level === "warning" ? "Warning: " : ""}</span>
                      {r.title}
                    </span>
                    {r.sub && <span className="text-[13px] leading-[19px] text-muted">{r.sub}</span>}
                  </div>
                  {a?.href ? (
                    <Link href={a.href} className={btnSm}>
                      {a.label}
                    </Link>
                  ) : a ? (
                    <button type="button" onClick={() => (a.upgrade ? onUpgrade() : a.stopIp ? onStopIp() : onLogs(a.logs!))} className={btnSm} aria-label={a.logs ? `Logs of ${a.logs}` : undefined}>
                      {a.label}
                    </button>
                  ) : null}
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function Containers({ status, loading, now, onLogs }: { status: PanelStatus | undefined; loading: boolean; now: number; onLogs: (name: string) => void }) {
  const n = status?.containers.length ?? 0;
  const well = status?.containers.filter(containerWell).length ?? 0;
  return (
    <section aria-labelledby="ct-h" className={cn(card, "order-3")}>
      <div className="flex flex-wrap items-baseline gap-x-2.5 border-b border-subtle px-4 py-3">
        <h2 id="ct-h" className={h2}>
          The panel’s containers
        </h2>
        <span className={sum}>{status ? `${well} of ${n} healthy · live, every 5 seconds` : "live, every 5 seconds"}</span>
      </div>
      <div role="group" tabIndex={0} aria-label="The panel’s containers" className="overflow-x-auto">
        <table className="w-full border-collapse text-[13px] max-sm:block sm:min-w-[560px]">
          <thead className="max-sm:hidden">
            <tr>
              <th scope="col" className={cn(th, "border-t-0")}>Container</th>
              <th scope="col" className={cn(th, "border-t-0")}>Status</th>
              <th scope="col" className={cn(th, "border-t-0")}>Memory</th>
              <th scope="col" className={cn(th, "border-t-0")}>
                <span className="sr-only">Logs</span>
              </th>
            </tr>
          </thead>
          <tbody className="max-sm:block">
            {!status ? (
              <tr aria-busy="true" className="max-sm:block">
                <td colSpan={4} className={cn(phoneNote, "py-3.5 text-muted")}>
                  {loading ? "Reading the panel’s containers…" : "—"}
                </td>
              </tr>
            ) : (
              status.containers.map((c) => <ContainerRow key={c.name} c={c} now={now} onLogs={() => onLogs(c.name)} />)
            )}
          </tbody>
        </table>
      </div>
      <p className="border-t border-subtle px-4 py-3 text-xs leading-[17px] text-muted">
        Memory limits come from the panel’s compose file. An alert fires when a container stays unhealthy for 2 minutes or restarts 3 times in 10.
      </p>
    </section>
  );
}

function ContainerRow({ c, now, onLogs }: { c: PanelContainer; now: number; onLogs: () => void }) {
  const s = containerStatus(c);
  const pct = c.memory_bytes != null && c.memory_limit_bytes ? Math.min(100, (c.memory_bytes / c.memory_limit_bytes) * 100) : null;
  const facts = [
    c.started_at && c.state === "running" ? `up ${upFor((now - new Date(c.started_at).getTime()) / 1000)}` : "",
    `${c.restarts} ${c.restarts === 1 ? "restart" : "restarts"}`,
    c.cpu_percent != null ? `CPU ${c.cpu_percent.toFixed(1)}%` : "",
  ].filter(Boolean);
  return (
    <tr className={phoneRow}>
      <td className={cn(phoneTd, "max-sm:col-start-1 max-sm:row-start-1")}>
        <span className="flex flex-col">
          <span className="font-mono text-xs font-medium text-fg">{c.name}</span>
          <span className="text-xs text-muted">
            {ROLE_LABEL[c.role]}
            {c.image && ` · ${imageTag(c.image)}`}
          </span>
        </span>
      </td>
      <td className={cn(phoneTd, "max-sm:col-span-2")}>
        <span className="flex flex-col">
          <span className={cn("inline-flex items-center gap-1.5", TONE_CLASSES[s.tone].text)}>
            <span aria-hidden="true" className={cn("h-1.5 w-1.5 rounded-full", TONE_CLASSES[s.tone].dot)} />
            {s.text}
          </span>
          <span className={cn("text-xs", c.restarts > 0 ? "text-status-pending-fg" : "text-muted")}>{facts.join(" · ")}</span>
        </span>
      </td>
      <td className={cn(phoneTd, "max-sm:col-span-2")}>
        {c.memory_bytes != null ? (
          <span className="flex min-w-[140px] max-w-[180px] flex-col gap-1">
            <span className="font-mono text-xs text-fg/85">
              <span className={cn(phoneLabel, "font-sans")}>Memory </span>
              {size(c.memory_bytes)}
              {c.memory_limit_bytes ? <span className="text-muted"> of {size(c.memory_limit_bytes)}</span> : null}
            </span>
            {pct != null && (
              <span aria-hidden="true" className="h-[3px] rounded-sm bg-raised">
                <span className={cn("block h-[3px] rounded-sm", pct > 90 ? "bg-status-pending" : "bg-accent")} style={{ width: `${pct}%` }} />
              </span>
            )}
          </span>
        ) : (
          <span className="font-mono text-xs text-muted">
            <span className={cn(phoneLabel, "font-sans")}>Memory </span>—
          </span>
        )}
      </td>
      <td className={cn(phoneTd, "py-1.5 text-right max-sm:col-start-2 max-sm:row-start-1")}>
        {c.state !== "missing" && (
          <button type="button" onClick={onLogs} aria-label={`Logs of ${c.name}`} className={smallButton}>
            Logs
          </button>
        )}
      </td>
    </tr>
  );
}

/** Panel, certificate, backups and watchdog, one row each (replaces the four tiles). */
function Health({ status, checked, now, backup, watchdog }: { status: PanelStatus; checked: number; now: number; backup: BackupDestination | null | undefined; watchdog: Watchdog | undefined }) {
  const bad = status.containers.filter((c) => !containerWell(c)).length;
  const n = status.containers.length;
  const rows: { label: string; value: string; note: string; tone: StatusTone }[] = [
    status.healthy ? { label: "Panel", value: "Healthy", note: `${n} of ${n} containers`, tone: "running" } : { label: "Panel", value: "Degraded", note: `${bad} of ${n} containers not healthy`, tone: "pending" },
    { label: "Certificate", ...certTile(status.certificate, now) },
  ];
  if (backup !== undefined) {
    const b = backupState(backup, now);
    rows.push({ label: "Backups", value: b.label, note: b.note, tone: BACKUP_TONE[b.tone] });
  }
  if (watchdog) {
    const w = watchdogState(watchdog, now);
    rows.push({ label: "Watchdog", value: w.value, note: w.note, tone: w.tone });
  }
  return (
    <section aria-labelledby="hl-h" className={cn(card, "order-2")}>
      <div className="flex flex-wrap items-baseline justify-between gap-x-2.5 border-b border-subtle px-4 py-3">
        <h2 id="hl-h" className={h2}>
          Health
        </h2>
        <span className={sum}>checked {sinceShort(new Date(checked).toISOString(), Math.max(now, checked))}</span>
      </div>
      <dl className="flex flex-col text-[13px]">
        {rows.map((r, i) => (
          <div key={r.label} className={cn("grid grid-cols-[120px_minmax(0,1fr)] gap-x-3 px-4 py-3", i > 0 && "border-t border-subtle")}>
            <dt className="text-muted">{r.label}</dt>
            <dd className="flex flex-col gap-0.5">
              <span className={cn("inline-flex items-center gap-1.5 font-medium", TONE_CLASSES[r.tone].text)}>
                <span aria-hidden="true" className={cn("h-1.5 w-1.5 rounded-full", TONE_CLASSES[r.tone].dot)} />
                {r.value}
              </span>
              {r.note && <span className="text-xs leading-[17px] text-muted">{r.note}</span>}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

/** The panel's address, server role, watchdog and recovery kit, set in the
 *  setup wizard; each row opens its step again (D-62). */
function SetupCard({ watchdog }: { watchdog: Watchdog | undefined }) {
  const hostRead = useQuery(panelHostQuery);
  const setupRead = useQuery(setupQuery);
  const host = hostRead.data;
  const setup = setupRead.data;
  const head = (
    <div className="border-b border-subtle px-4 py-3">
      <h2 id="set-h" className={h2}>
        Setup
      </h2>
    </div>
  );
  if (readFailed(hostRead) || readFailed(setupRead)) {
    return (
      <section aria-labelledby="set-h" className={cn(card, "order-4")}>
        {head}
        <div className="p-4">
          <LoadFailed
            what="the panel’s setup"
            onRetry={() => {
              void hostRead.refetch();
              void setupRead.refetch();
            }}
          />
        </div>
      </section>
    );
  }
  if (!host || !setup) return null;
  const kit = setup.kit_acknowledged_at ? formatDateTime(setup.kit_acknowledged_at).slice(0, 10) : "";
  let pinged = "";
  try {
    pinged = watchdog?.url ? new URL(watchdog.url).host : "";
  } catch {}
  const rows: [string, React.ReactNode, string, string, string][] = [
    ["Address", setup.domain || "The server’s IP address", setup.awaiting_domain ? "Also on the server’s IP address until the domain reaches this panel" : setup.domain ? "Let’s Encrypt certificate" : "With a certificate browsers don’t trust", "/setup/address", "Change address"],
    setup.role === "control_plane"
      ? ["Server role", "Only Toft", "No apps deploy here", "/setup/role", "Change role"]
      : ["Server role", "Run apps here too", `Apps deploy here too; the panel keeps ${host.reservation.panel_cpu} vCPU and ${host.reservation.panel_memory_bytes / 2 ** 30} GiB`, "/setup/role", "Change role"],
    ["Watchdog", pinged || "Not set up", pinged ? "pinged every 5 minutes while healthy" : "Nobody hears if this server goes down", "/instance/server#wd-h", "Configure watchdog"],
    !kit
      ? ["Recovery kit", "Not stored yet", "What install.sh --restore needs", "/instance/kit", "Show kit"]
      : setup.kit_out_of_date
        ? ["Recovery kit", "Out of date", `Stored ${kit}, before the key or the backup settings changed`, "/instance/kit", "Show kit"]
        : ["Recovery kit", `Stored ${kit}`, "What install.sh --restore needs", "/instance/kit", "Show kit"],
  ];
  return (
    <section aria-labelledby="set-h" className={cn(card, "order-4")}>
      {head}
      <dl className="flex flex-col text-[13px]">
        {rows.map(([dt, dd, note, href, action], i) => (
          <div key={dt} className={cn("grid grid-cols-[120px_minmax(0,1fr)] gap-x-3 px-4 py-3", i > 0 && "border-t border-subtle")}>
            <dt className="text-muted">{dt}</dt>
            <dd className="flex min-w-0 flex-col items-start gap-0.5">
              <span className="break-words text-fg">{dd}</span>
              <span className="text-xs leading-[17px] text-muted">{note}</span>
              <Link href={href} className="touch-target mt-0.5 inline-flex items-center font-medium text-link hover:underline">
                {action}
              </Link>
            </dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

const ADOPT = "sudo sh install.sh --adopt --version vX.Y.Z --registry-token <token>";

/** A panel deploy.sh set up (board InstanceOverview, deploysh): it
 *  upgrades with deploy.sh until it moves to the installer, once (D-66). */
function AdoptBanner() {
  const [copied, setCopied] = useState(false);
  return (
    <section aria-labelledby="ds-h" className={cn(card, "flex flex-col gap-2.5 p-4")}>
      <h2 id="ds-h" className="text-[15px] font-semibold leading-[22px] text-fg">
        This panel upgrades with deploy.sh
      </h2>
      <p className="max-w-[860px] text-[13px] leading-[19px] text-fg/85">
        It was set up with deploy.sh, so upgrades from here are off. Moving it to the installer keeps every app, volume, setting and
        account, and turns them on; deploy.sh refuses the server after that. Run this once on the panel’s server, as root, with the
        release’s install.sh and a token that can read Toft’s images:
      </p>
      <div className="flex max-w-[860px] items-center gap-2.5 rounded-lg border border-subtle bg-ground px-3 py-2.5">
        <code className="min-w-0 flex-1 break-all font-mono text-xs text-fg">{ADOPT}</code>
        <button
          type="button"
          onClick={() => void navigator.clipboard?.writeText(ADOPT).then(() => setCopied(true))}
          className={smallButton}
        >
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
    </section>
  );
}
