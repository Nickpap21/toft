import type { PanelCertificate, PanelContainer, PanelSample, PanelStatus, PanelSystem, UpgradeRun, UpgradeStatus, Watchdog } from "@/lib/api/instance.service";
import { formatBytes } from "@/lib/api/sharedvolume.service";
import type { StatusTone } from "@/lib/status";
import { formatDateTime } from "@/lib/utils";
import type { BackupDestination, DiskReport, HostUpdates } from "@/lib/api/server.service";
import { backupState } from "@/app/settings/backups/backups-view";
import { attentionItems, unreadItem, type AttentionItem } from "@/app/servers/[id]/server-view";

/** formatBytes without a trailing ".0": "512 MiB", "92.4 MiB". */
export const size = (n: number) => formatBytes(n).replace(/\.0 /, " ");

const DAY = 86_400_000;

/** The header's health chip (board InstanceHead): "Healthy", or what is
 *  wrong in a few words. */
export function healthChip(st: PanelStatus): { text: string; tone: StatusTone } {
  if (st.healthy) return { text: "Healthy", tone: "running" };
  const bad = st.containers.filter((c) => !containerWell(c));
  if (bad.length === 1) return { text: `${bad[0].name} ${containerStatus(bad[0]).text.toLowerCase()}`, tone: "pending" };
  return { text: `${bad.length} containers not healthy`, tone: "pending" };
}

/** A container that runs and isn't failing or still starting its check. */
export function containerWell(c: PanelContainer): boolean {
  return c.state === "running" && c.health !== "unhealthy" && c.health !== "starting";
}

/** A container's status, as the table's dot and words. */
export function containerStatus(c: PanelContainer): { text: string; tone: StatusTone } {
  if (c.state === "missing") return { text: "Missing", tone: "failed" };
  if (c.state === "restarting") return { text: "Restarting", tone: "pending" };
  if (c.state !== "running") return { text: c.state.charAt(0).toUpperCase() + c.state.slice(1), tone: "failed" };
  if (c.health === "unhealthy") return { text: "Unhealthy", tone: "failed" };
  if (c.health === "starting") return { text: "Starting", tone: "building" };
  return { text: c.health === "healthy" ? "Healthy" : "Running", tone: "running" };
}

/** What each container is, beside its name. */
export const ROLE_LABEL: Record<PanelContainer["role"], string> = {
  api: "API",
  web: "Web app",
  database: "Database",
  proxy: "Proxy",
  git: "Git",
};

/** The image's tag, or the image when it has none: "v0.1.2", "16-alpine". */
export function imageTag(image: string): string {
  const name = image.split("@")[0];
  const i = name.lastIndexOf(":");
  return i > name.lastIndexOf("/") ? name.slice(i + 1) : name;
}

/** "21 d", "5 h", "12 min", "40 s": how long a container or the host has
 *  been up. */
export function upFor(seconds: number): string {
  if (seconds >= 86_400) return `${Math.floor(seconds / 86_400)} d`;
  if (seconds >= 3_600) return `${Math.floor(seconds / 3_600)} h`;
  if (seconds >= 60) return `${Math.floor(seconds / 60)} min`;
  return `${Math.max(0, Math.floor(seconds))} s`;
}

/** The host's uptime in words, for the header: "21 days", "5 hours". */
export function uptimeWords(seconds: number): string {
  const d = Math.floor(seconds / 86_400);
  if (d >= 1) return d === 1 ? "1 day" : `${d} days`;
  const h = Math.floor(seconds / 3_600);
  if (h >= 1) return h === 1 ? "1 hour" : `${h} hours`;
  const m = Math.max(1, Math.floor(seconds / 60));
  return m === 1 ? "1 minute" : `${m} minutes`;
}

/** Days until a certificate expires, rounded down; negative once expired. */
export function certDaysLeft(cert: PanelCertificate, now: number): number {
  return Math.floor((new Date(cert.not_after).getTime() - now) / DAY);
}

/** The Overview's certificate tile. Let's Encrypt renews at 30 days left, so
 *  under 14 something is wrong (D-68's alert); Traefik's own certificate
 *  means Let's Encrypt has none yet. */
export function certTile(cert: PanelCertificate | null, now: number): { value: string; note: string; tone: StatusTone } {
  if (!cert) return { value: "None", note: "The panel answers on its IP address", tone: "neutral" };
  if (/traefik/i.test(cert.issuer)) return { value: "Self-signed", note: "Let’s Encrypt hasn’t issued one yet", tone: "pending" };
  const days = certDaysLeft(cert, now);
  if (days < 0) return { value: "Expired", note: `${cert.issuer} · renewal is failing`, tone: "failed" };
  const value = days === 1 ? "1 day left" : `${days} days left`;
  if (days < 14) return { value, note: `${cert.issuer} · renewal is failing`, tone: "pending" };
  return { value, note: `${cert.issuer} renews it at 30`, tone: "running" };
}

/** Why the panel's domain still serves Traefik's own certificate, and when
 *  Toft has Traefik ask Let's Encrypt again (the backend's certificate ask). */
export function certAskNote(cert: PanelCertificate, now: number): string {
  const a = cert.ask;
  if (!a) return "Let’s Encrypt hasn’t issued one yet. Traefik’s log says why.";
  if (!a.reaches_panel)
    return a.addresses.length
      ? `${cert.domain} points at ${a.addresses.join(", ")}, which doesn’t lead to this panel: another server, or a router that won’t send this server’s own requests back in. Toft has Traefik ask once it reaches here.`
      : `${cert.domain} has no DNS record yet. Toft has Traefik ask once it points at this server.`;
  const next = a.next_ask_at ? ` Toft has Traefik ask again at ${formatDateTime(a.next_ask_at)}.` : "";
  const e = a.error;
  if (!e) return a.last_try_at ? `Traefik asked Let’s Encrypt at ${formatDateTime(a.last_try_at)}.${next}` : `Let’s Encrypt hasn’t issued one yet.${next}`;
  switch (e.type) {
    case "connection":
      return `Port 80 doesn’t reach this server from the internet: check the server’s firewall, your provider’s firewall (such as Hetzner Cloud Firewall), or a router in front.${next}`;
    case "unauthorized":
      return `Something other than this panel answered Let’s Encrypt’s check on port 80: usually an IPv6 (AAAA) record pointing elsewhere, or port 80 forwarded to another machine.${next}`;
    case "dns":
      return `Let’s Encrypt couldn’t look ${cert.domain} up: ${e.detail}${next}`;
    case "caa":
      return `${cert.domain}’s CAA record doesn’t allow Let’s Encrypt.${next}`;
    case "rate_limited": {
      const why = e.detail.split(", retry after")[0];
      const limit = why ? ` (${why})` : "";
      if (e.retry_after && new Date(e.retry_after).getTime() > now)
        return `Let’s Encrypt has rate-limited ${cert.domain} until ${formatDateTime(e.retry_after)}${limit}. Toft asks again then: asking sooner would extend it.`;
      return `Let’s Encrypt rate-limited ${cert.domain}${limit}.${next}`;
    }
  }
  return `Let’s Encrypt refused it: ${e.detail}${next}`;
}

/** The busiest point of a range, for the chart's corner: its CPU peak and
 *  when. Null without samples. */
export function cpuPeak(samples: PanelSample[]): PanelSample | null {
  let best: PanelSample | null = null;
  for (const s of samples) if (!best || s.cpu_peak > best.cpu_peak) best = s;
  return best;
}

/** An SVG path for a chart of values between 0 and max, w by h: the line,
 *  and the area under it. Each point's x is where it falls in the range,
 *  0 at its start and 1 now, so a week with an hour of samples isn't drawn
 *  as a flat week. */
export function chartPaths(points: [x: number, v: number][], max: number, w = 336, h = 72): { line: string; area: string } {
  if (points.length === 0 || max <= 0) return { line: "", area: "" };
  const xy = points.map(([x, v]) => [Math.min(1, Math.max(0, x)) * w, h - Math.min(1, Math.max(0, v / max)) * h]);
  // One sample is a step to now, so it shows.
  if (xy.length === 1) xy.push([w, xy[0][1]]);
  const line = `M${xy.map(([x, y]) => `${x.toFixed(1)} ${y.toFixed(1)}`).join("L")}`;
  return { line, area: `${line}L${xy[xy.length - 1][0].toFixed(1)} ${h}L${xy[0][0].toFixed(1)} ${h}Z` };
}

// ── Upgrades from the panel (D-68, board InstanceUpgrade) ───────────────────

export const UPGRADE_STEPS = [
  { id: "preflight", label: "Preflight" },
  { id: "pull", label: "Pull" },
  { id: "save", label: "Save database" },
  { id: "migrate", label: "Migrate" },
  { id: "restart", label: "Restart" },
  { id: "health", label: "Health check" },
] as const;

export type StepState = "done" | "running" | "failed" | "rolled_back" | "waiting";

/** Each step's state in a run: done before the current one; the current one
 *  running, failed or rolled back; waiting after it. "starting" is before
 *  the first. */
export function stepStates(run: Pick<UpgradeRun, "step" | "outcome">): StepState[] {
  const at = UPGRADE_STEPS.findIndex((s) => s.id === run.step);
  return UPGRADE_STEPS.map((_, i) => {
    if (run.outcome === "succeeded") return "done";
    if (i < at) return "done";
    if (i > at || at < 0) return at < 0 && i === 0 && run.outcome === "running" ? "running" : "waiting";
    if (run.outcome === "running") return "running";
    return run.outcome === "rolled_back" ? "rolled_back" : "failed";
  });
}

export type UpgradeCardKind = "running" | "restarting" | "done" | "rolled_back" | "failed";

/** Which of the board's cards an upgrade shows: restarting while the panel
 *  doesn't answer (or toft is swapping it), then how it ended. */
export function upgradeCard(run: UpgradeRun, unreachable: boolean): { kind: UpgradeCardKind; title: string; text: string } {
  if (run.outcome === "succeeded") {
    const secs = run.finished_at ? (new Date(run.finished_at).getTime() - new Date(run.started_at).getTime()) / 1000 : 0;
    return {
      kind: "done",
      title: `Toft ${run.to} is running`,
      text: `Upgraded in ${secs >= 60 ? `${Math.floor(secs / 60)} min ${Math.round(secs % 60)} s` : `${Math.round(secs)} s`}. The database dump from before it is kept on the server.`,
    };
  }
  if (run.outcome === "rolled_back") {
    return {
      kind: "rolled_back",
      title: `The upgrade to ${run.to} failed. ${run.from} is running again`,
      text: `${run.to} wasn’t healthy after 2 minutes, so ${run.from} started again by itself. The full log says why.`,
    };
  }
  if (run.outcome === "failed") {
    return {
      kind: "failed",
      title: run.from ? `The upgrade stopped. ${run.from} kept running` : "The upgrade stopped",
      text: run.error || "The full log says why.",
    };
  }
  if (unreachable || run.step === "restart") {
    return { kind: "restarting", title: "Toft is restarting", text: "This page reconnects by itself, usually within 30 seconds. Apps keep running." };
  }
  const now: Record<string, string> = {
    starting: "Fetching the release’s own upgrader.",
    preflight: "Checking Docker and the free disk.",
    pull: `Pulling ${run.to}; ${run.from} still serves.`,
    save: "Saving the database first.",
    migrate: `Moving the database to ${run.to}; ${run.from} still serves.`,
    health: `Waiting for ${run.to} to report healthy, for up to 2 minutes.`,
  };
  return { kind: "running", title: `Upgrading to ${run.to}`, text: now[run.step] ?? "" };
}

// ── The host's units and the database's dumps (board InstanceServer) ────────

/** The Host card's Firewall row, from systemd's ActiveState (or missing);
 *  "—" before it is read. unattended-upgrades is the Updates card's (D-133). */
export function systemWords(sys: PanelSystem | null | undefined): { firewall: string } {
  const firewall: Record<string, string> = {
    active: "toft-firewall, loaded",
    inactive: "toft-firewall, not loaded",
    failed: "toft-firewall failed to load",
    missing: "Not installed",
  };
  return { firewall: firewall[sys?.firewall ?? ""] ?? "—" };
}

/** The Database card's "Dumps before upgrades": toft saves one before every
 *  upgrade and keeps the last 3. */
export function dumpWords(up: Pick<UpgradeStatus, "dumps" | "last_dump_at">): string {
  if (!up.dumps || !up.last_dump_at) return "None yet: the next upgrade saves one";
  return `${up.dumps} kept · last ${formatDateTime(up.last_dump_at).slice(0, 10)}`;
}

/** Whether the panel needs an operator, for the sidebar's dot (board
 *  Sidebar): what Overview's Needs attention lists (a container not well, a
 *  certificate not renewed) and an upgrade that failed or rolled back in the
 *  last 24 hours. */
export function needsAttention(status: PanelStatus | undefined, upgrade: UpgradeStatus | undefined, now: number): boolean {
  if (status?.containers.some((c) => !containerWell(c))) return true;
  if (status?.certificate && ["pending", "failed"].includes(certTile(status.certificate, now).tone)) return true;
  const run = upgrade?.run;
  return !!run?.finished_at && (run.outcome === "failed" || run.outcome === "rolled_back") && now - Date.parse(run.finished_at) < 24 * 3_600_000;
}

/** What waits on an operator on Maintenance (D-139): the updates read's
 *  warnings and problems, and the disk past its alert. An available
 *  release is never one. maintenanceCount counts them; each item's anchor
 *  is its card (updates, disk). */
export function panelMaintenance(updates: HostUpdates | undefined, disk: DiskReport | undefined): AttentionItem[] {
  const pct = disk?.size_bytes ? Math.round((disk.used_bytes / disk.size_bytes) * 100) : null;
  const full = disk && pct !== null && pct >= disk.policy.effective.alert_percent;
  return attentionItems({ problems: full ? [{ kind: "disk", level: "warning", text: `Disk ${pct}% full` }] : [], updates, moveToAgent: false, canAdmin: true });
}

export interface OverviewRow {
  key: string;
  level: "problem" | "release" | "warning";
  title: string;
  sub: string;
  /** A link, a container's logs, the upgrade dialog, or the dialog that
   *  stops a restored panel answering on its IP. */
  action?: { label: string; href?: string; logs?: string; upgrade?: true; stopIp?: true };
}

const OVERVIEW_RANK = { problem: 0, release: 1, warning: 2 } as const;

/** Overview's Needs attention (D-139): what's wrong, not configuration.
 *  Problems first (containers, the certificate, a failed backup, failing
 *  pings), then a release that is out, then warnings (an out-of-date
 *  recovery kit, a restored panel still on its IP, Maintenance's items, no watchdog, no backups). noBackup is
 *  Needs you's no_backup detail. */
export function overviewAttention(f: {
  status?: PanelStatus;
  upgrade?: UpgradeStatus;
  maintenance: AttentionItem[];
  watchdog?: Watchdog;
  backup?: BackupDestination | null;
  noBackup?: string;
  /** The day the kit was stored, while it is out of date (D-163). */
  kitOutOfDate?: string;
  /** The panel's domain, while a restored panel also answers on its IP (D-164). */
  awaitingDomain?: string;
  now: number;
  /** The reads that failed: "nothing needs attention" would be a claim about them. */
  unread?: string[];
}): OverviewRow[] {
  const out: OverviewRow[] = f.unread?.length ? [unreadItem(f.unread)] : [];
  for (const c of f.status?.containers ?? []) {
    if (containerWell(c)) continue;
    out.push({
      key: `c-${c.name}`,
      level: "problem",
      title: c.state === "missing" ? `${c.name} is missing` : c.state === "running" ? `${c.name} isn’t healthy` : `${c.name} has stopped`,
      sub:
        c.state === "missing"
          ? "Docker has no container by that name. Run the panel’s compose file again on its server."
          : c.restarts > 0
            ? `Restarted ${c.restarts} ${c.restarts === 1 ? "time" : "times"} since it was created. Its log usually says why.`
            : "Its log usually says why.",
      action: c.state === "missing" ? undefined : { label: "Logs", logs: c.name },
    });
  }
  const cert = f.status?.certificate;
  // A restored panel whose domain points elsewhere: the IP row says where.
  const elsewhere = f.awaitingDomain && cert?.ask && !cert.ask.reaches_panel ? cert.ask.addresses : undefined;
  if (cert && !elsewhere) {
    const t = certTile(cert, f.now);
    if (t.tone === "pending" || t.tone === "failed")
      out.push({
        key: "cert",
        level: "problem",
        title: `${cert.domain}: ${t.value.toLowerCase()}`,
        sub: /traefik/i.test(cert.issuer)
          ? certAskNote(cert, f.now)
          : "Let’s Encrypt renews at 30 days left; this one wasn’t. Traefik’s log says why, usually that port 80 doesn’t reach this server.",
        action: { label: "Traefik logs", logs: "paas-traefik" },
      });
  }
  if (f.kitOutOfDate)
    out.push({
      key: "kit",
      level: "warning",
      title: "The stored recovery kit is out of date",
      sub: `The encryption key or the panel host’s backup settings changed after it was stored on ${f.kitOutOfDate}. A restore with that kit can’t read the panel’s secrets or open its backups.`,
      action: { label: "Show kit", href: "/instance/kit" },
    });
  if (f.awaitingDomain)
    out.push({
      key: "ip",
      level: "warning",
      title: "This panel also answers on its server’s IP address",
      sub: `It was restored from a backup, so it answers on the IP, with a certificate browsers don’t trust, until ${f.awaitingDomain} points at this server and has a trusted certificate. Toft checks every minute and stops by itself then.${elsewhere ? (elsewhere.length ? ` It points at ${elsewhere.join(", ")} now.` : " It has no DNS record now.") : ""}`,
      action: { label: "Stop answering on the IP…", stopIp: true },
    });
  if (f.backup) {
    const b = backupState(f.backup, f.now);
    if (b.failing) out.push({ key: "backup", level: "problem", title: `Toft’s database backup ${b.label.toLowerCase()}`, sub: b.note, action: { label: "Backup settings", href: "/settings/backups#panel-host" } });
    else if (b.paused)
      out.push({
        key: "backup",
        level: "problem",
        title: "Toft’s database backups are not running",
        sub: f.backup.restored_at ? "Paused since this panel was restored from a backup. Resume them on the server you keep." : "The panel host has no backup settings. Resume backups to write them again.",
        action: { label: "Backup settings", href: "/settings/backups#panel-host" },
      });
  } else if (f.backup === null && f.noBackup) {
    out.push({ key: "backup", level: "warning", title: f.noBackup, sub: "If this server’s disk is lost, the data goes with it.", action: { label: "Set up backups", href: "/settings/backups#panel-host" } });
  }
  const w = f.watchdog;
  if (w && !w.url) out.push({ key: "watchdog", level: "warning", title: "No watchdog", sub: "Nobody hears if this server goes down.", action: { label: "Set up", href: "/instance/server#wd-h" } });
  else if (w?.last_error) out.push({ key: "watchdog", level: "problem", title: "The watchdog’s pings fail", sub: w.last_error, action: { label: "Review", href: "/instance/server#wd-h" } });
  const u = f.upgrade;
  if (u?.install === "installer" && u.available && u.run?.outcome !== "running" && !retryUpgrade(u))
    out.push({
      key: "release",
      level: "release",
      title: `Toft ${u.latest} is available`,
      sub: `You run ${u.current}. The upgrade saves the database first, takes about 2 minutes, and goes back by itself if ${u.latest} isn’t healthy. Apps keep running.`,
      action: { label: "Upgrade…", upgrade: true },
    });
  for (const m of f.maintenance) {
    if (m.level === "info") continue;
    out.push({ key: `m-${m.key}`, level: m.level, title: m.title, sub: m.sub, action: { label: "Review", href: `/instance/maintenance#${m.anchor ?? "updates"}` } });
  }
  return out.sort((a, b) => OVERVIEW_RANK[a.level] - OVERVIEW_RANK[b.level]);
}

/** The last run to the latest release failed or rolled back: its card's Try
 *  again… (and Maintenance → Toft) is the way to retry, so the header's
 *  Upgrade and Overview's release row step aside (board, rolled back). */
export function retryUpgrade(u: Pick<UpgradeStatus, "latest" | "run">): boolean {
  return !!u.run && u.run.to === u.latest && (u.run.outcome === "failed" || u.run.outcome === "rolled_back");
}

/** Where an old `/instance?tab=<id>` link (Instance's tabs before they were
 *  routes) goes now; "" for none. */
export function oldTabRoute(search: string): string {
  const tab = new URLSearchParams(search).get("tab") ?? "";
  const to: Record<string, string> = {
    server: "/instance/server",
    maintenance: "/instance/maintenance",
    accounts: "/instance/accounts",
    operators: "/instance/accounts",
    policy: "/instance/deploy-policy",
    "deploy-policy": "/instance/deploy-policy",
    audit: "/instance/audit-log",
    "audit-log": "/instance/audit-log",
  };
  return to[tab] ?? "";
}
