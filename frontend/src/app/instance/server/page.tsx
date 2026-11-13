"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";
import { useIsOperator } from "@/context/AuthContext";
import {
  panelHostQuery,
  panelServerQuery,
  panelStatusQuery,
  setupQuery,
  upgradeQuery,
  watchdogQuery,
  type PanelRange,
  type PanelSample,
  type PanelServer,
} from "@/lib/api/instance.service";
import { gitInfoQuery } from "@/lib/api/code.service";
import { backupPath, backupService, diskService } from "@/lib/api/server.service";
import { PANEL_KEY } from "../../settings/backups/panel-host";
import { backupState } from "../../settings/backups/backups-view";
import { DISK_ALERT_PERCENT } from "../../settings/alerts/alerts-view";
import { cn, formatDateTime } from "@/lib/utils";
import { Segmented, card } from "@/components/settings/settings-frame";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import { InstanceFrame } from "@/components/instance/instance-frame";
import { IsolationCard } from "@/components/servers/isolation-card";
import { WatchdogCard } from "@/components/instance/watchdog-card";
import { gb } from "@/components/servers/disk-view";
import {
  chartPaths,
  cpuPeak,
  dumpWords,
  size,
  systemWords,
  uptimeWords,
} from "@/components/instance/instance-view";

const RANGES: [PanelRange, string, string, number][] = [
  ["live", "Live", "5 min ago", 5 * 60_000],
  ["24h", "24 h", "24 h ago", 24 * 3_600_000],
  ["7d", "7 days", "7 days ago", 7 * 24 * 3_600_000],
];

const pct = (n: number) => `${n < 10 ? n.toFixed(1) : Math.round(n)}%`;

/** Instance → Server (D-68, board InstanceServer): the panel's own server,
 *  live and over 7 days, what the panel keeps of it, what fills its disk,
 *  its database and its host. Operators only. */
export default function InstanceServerPage() {
  const isOperator = useIsOperator();
  const router = useRouter();
  // Updates and Disk moved to Maintenance (D-139): old links follow them.
  useEffect(() => {
    const hash = window.location.hash;
    if (hash === "#updates" || hash === "#disk") router.replace(`/instance/maintenance${hash}`);
  }, [router]);
  const [range, setRange] = useState<PanelRange>("7d");
  const server = useQuery({
    ...panelServerQuery(range),
    enabled: isOperator,
    retry: false,
    placeholderData: (prev) => prev,
  });
  const live = useQuery({
    ...panelServerQuery("live"),
    enabled: isOperator,
    retry: false,
  });
  const status = useQuery({
    ...panelStatusQuery,
    enabled: isOperator,
    retry: false,
  });
  // The disk card's read: its policy holds the alert line.
  const disk = useQuery({
    queryKey: ["disk", null],
    queryFn: () => diskService.get(null),
    enabled: isOperator,
    retry: false,
  });
  const backup = useQuery({
    queryKey: PANEL_KEY,
    queryFn: () => backupService.get(backupPath(null)),
    enabled: isOperator,
    retry: false,
  });
  const watchdog = useQuery({ ...watchdogQuery, enabled: isOperator, retry: false });
  const [mounted] = useState(() => Date.now());
  const s = server.data;
  const now = live.data?.now ?? s?.now ?? null;
  // The cards take the freshest of the two answers: live polls every 5
  // seconds, so Docker's disk use shows as soon as it is read.
  const fresh = s && {
    ...s,
    now,
    disk: live.data?.disk ?? s.disk,
    database: live.data?.database ?? s.database,
  };

  // Where each sample falls in the range, 0 at its start and 1 when it was
  // fetched.
  const [, , from, span] = RANGES.find((r) => r[0] === range)!;
  const end = server.dataUpdatedAt;
  const points = (v: (x: PanelSample) => number): [number, number][] =>
    (s?.samples ?? []).map((x) => [1 - (end - Date.parse(x.at)) / span, v(x)]);

  const panelCPU = (status.data?.containers ?? []).reduce(
    (a, c) => a + (c.cpu_percent ?? 0),
    0,
  );
  const panelMem = (status.data?.containers ?? []).reduce(
    (a, c) => a + (c.memory_bytes ?? 0),
    0,
  );

  return (
    <InstanceFrame title="Server">
      {server.error && !s ? (
        <div
          role="alert"
          className="flex items-center gap-2.5 rounded-[10px] border border-danger/35 bg-surface px-4 py-3.5 text-[13px] text-status-failed-fg"
        >
          <AlertTriangle className="h-4 w-4 flex-none" aria-hidden="true" />
          Could not read the panel’s server: {server.error.message}
        </div>
      ) : null}

      <div className="grid items-start gap-6 min-[1200px]:grid-cols-[minmax(0,1fr)_340px]">
        <div className="flex min-w-0 flex-col gap-6">
          <section aria-labelledby="use-h" className={card}>
            <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
              <div className="flex flex-wrap items-baseline gap-2.5">
                <h2
                  id="use-h"
                  className="text-[15px] font-semibold leading-[22px] text-fg"
                >
                  Usage
                </h2>
                {s && (
                  <span className="font-mono text-xs text-muted">
                    {s.cpus} vCPU · {size(s.memory_total_bytes)} ·{" "}
                    {gb(s.disk_total_bytes)}
                  </span>
                )}
              </div>
              <Segmented
                label="Time range"
                options={RANGES.map(([value, label]) => ({ value, label }))}
                value={range}
                onChange={setRange}
              />
            </div>
            <div className="grid divide-subtle max-[899px]:divide-y min-[900px]:grid-cols-3 min-[900px]:divide-x">
              <Metric
                label="CPU"
                value={now ? pct(now.cpu_percent) : "—"}
                of={s ? `of ${s.cpus} vCPU` : ""}
                corner={peakNote(s, range)}
                points={points((x) => x.cpu_percent)}
                max={100}
                from={from}
                aria="CPU, percent of all cores"
                facts={[
                  [
                    "Load 1 · 5 · 15",
                    now
                      ? `${now.load_1.toFixed(2)} · ${now.load_5.toFixed(2)} · ${now.load_15.toFixed(2)}`
                      : "—",
                  ],
                  [
                    "The panel’s containers",
                    s && status.data ? pct(panelCPU / s.cpus) : "—",
                  ],
                  [
                    "Nightly backup",
                    backup.isSuccess
                      ? backup.data
                        ? backup.data.schedule
                        : "Not set up"
                      : "—",
                  ],
                ]}
              />
              <Metric
                label="Memory"
                value={now ? size(now.memory_used_bytes) : "—"}
                of={s ? `of ${size(s.memory_total_bytes)}` : ""}
                corner={memPeak(s, range)}
                points={points((x) => x.memory_used_bytes)}
                max={s?.memory_total_bytes ?? 0}
                from={from}
                aria="Memory used"
                facts={[
                  ["The panel’s containers", status.data ? size(panelMem) : "—"],
                  ["Page cache", now ? size(now.memory_cache_bytes) : "—"],
                  [
                    "Swap",
                    now
                      ? now.swap_total_bytes
                        ? `${size(now.swap_used_bytes)} of ${size(now.swap_total_bytes)}`
                        : "none"
                      : "—",
                  ],
                ]}
              />
              <Metric
                label="Disk"
                value={now ? gb(now.disk_used_bytes) : "—"}
                of={s ? `of ${gb(s.disk_total_bytes)}` : ""}
                corner={diskGrowth(s, range, end - span)}
                points={points((x) => x.disk_used_bytes)}
                max={s?.disk_total_bytes ?? 0}
                from={from}
                aria="Disk used"
                facts={[
                  ["Free", now ? gb(now.disk_free_bytes) : "—"],
                  ["Alert at", `${disk.data?.policy.effective.alert_percent ?? DISK_ALERT_PERCENT}%`],
                  ["Filesystem", s?.system?.filesystem || "—"],
                ]}
              />
            </div>
            <p className="border-t border-subtle px-4 py-3 text-xs text-muted">
              One sample a minute, kept 7 days. Live refreshes every 5 seconds.
            </p>
          </section>

          <Reservation server={fresh} />

          <PanelIsolation />

          <WatchdogCard watchdog={watchdog.data} now={mounted} />
        </div>

        <div className="flex min-w-0 flex-col gap-6">
          <DatabaseCard server={fresh} />
          <HostCard server={fresh} />
          <NetworksCard />
        </div>
      </div>
    </InstanceFrame>
  );
}

function peakNote(s: PanelServer | undefined, range: PanelRange): string {
  if (!s || range === "live") return "Last 5 minutes";
  const p = cpuPeak(s.samples);
  if (!p) return "No samples yet";
  const at = formatDateTime(p.at);
  return `Peak ${pct(p.cpu_peak)}, ${range === "24h" ? at.slice(11) : at.slice(5, 16)}`;
}

function memPeak(s: PanelServer | undefined, range: PanelRange): string {
  if (!s || range === "live" || s.samples.length === 0)
    return range === "live" ? "Last 5 minutes" : "";
  return `Peak ${size(Math.max(...s.samples.map((x) => x.memory_used_bytes)))}`;
}

function diskGrowth(
  s: PanelServer | undefined,
  range: PanelRange,
  start: number,
): string {
  if (!s || range === "live" || s.samples.length < 2) return "";
  const first = s.samples[0];
  const d =
    s.samples[s.samples.length - 1].disk_used_bytes - first.disk_used_bytes;
  // Samples that start well into the range say since when.
  const slack = (range === "24h" ? 1 : 6) * 3_600_000;
  const whole = Date.parse(first.at) - start < slack;
  const words = whole
    ? range === "24h"
      ? "today"
      : "this week"
    : `since ${formatDateTime(first.at).slice(range === "24h" ? 11 : 5, 16)}`;
  return Math.abs(d) < 2 ** 20
    ? `Flat ${words}`
    : `${d > 0 ? "+" : "−"}${gb(Math.abs(d))} ${words}`;
}

function Metric(props: {
  label: string;
  value: string;
  of: string;
  corner: string;
  points: [number, number][];
  max: number;
  from: string;
  aria: string;
  facts: [string, string][];
}) {
  const { line, area } = chartPaths(props.points, props.max);
  return (
    <div className="flex min-w-0 flex-col gap-2.5 p-4">
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-xs text-fg/70">{props.label}</span>
        <span className="text-xs text-muted">{props.corner}</span>
      </div>
      <span className="flex items-baseline gap-1.5">
        <span className="font-mono text-[15px] font-semibold leading-[22px] tabular-nums text-fg">
          {props.value}
        </span>
        <span className="text-[13px] text-muted">{props.of}</span>
      </span>
      <svg
        viewBox="0 0 336 72"
        preserveAspectRatio="none"
        role="img"
        aria-label={`${props.aria}, since ${props.from}`}
        className="block h-[72px] w-full"
      >
        {[18, 36, 54].map((y) => (
          <line
            key={y}
            x1="0"
            y1={y}
            x2="336"
            y2={y}
            className="stroke-subtle"
            strokeWidth="1"
            vectorEffect="non-scaling-stroke"
          />
        ))}
        {area && <path d={area} className="fill-accent/15" />}
        {line && (
          <path
            d={line}
            fill="none"
            className="stroke-accent"
            strokeWidth="1.5"
            vectorEffect="non-scaling-stroke"
          />
        )}
      </svg>
      <div className="flex justify-between font-mono text-[11px] text-muted">
        <span>{props.from}</span>
        <span>now</span>
      </div>
      <dl className="flex flex-col gap-1 border-t border-subtle pt-2.5 text-xs">
        {props.facts.map(([k, v]) => (
          <div key={k} className="flex justify-between gap-2">
            <dt className="text-muted">{k}</dt>
            <dd className="font-mono text-fg/85">{v}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

/** What the panel's server can run, read and installed through its host
 *  helper (board PanelIsolation, D-97). Every reader here is an operator. */
function PanelIsolation() {
  const setupRead = useQuery(setupQuery);
  const setup = setupRead.data;
  if (readFailed(setupRead)) {
    return (
      <section aria-labelledby="piso-h" className={card}>
        <CardHead id="piso-h" title="Isolation" />
        <div className="p-4">
          <LoadFailed what="the panel’s isolation" onRetry={() => void setupRead.refetch()} />
        </div>
      </section>
    );
  }
  if (!setup) return null;
  return <IsolationCard serverId={null} serverName="the panel’s server" canProbe isOperator controlPlaneOnly={setup.role === "control_plane"} />;
}

/** What the panel keeps of its server (D-62's reservation) and what apps
 *  get, when apps run here; one line under Only Toft. */
function Reservation({ server }: { server: PanelServer | undefined }) {
  const hostRead = useQuery(panelHostQuery);
  const setupRead = useQuery(setupQuery);
  const host = hostRead.data;
  const setup = setupRead.data;
  if (readFailed(hostRead) || readFailed(setupRead)) {
    return (
      <section aria-labelledby="res-h" className={cn(card, "flex flex-col gap-3.5 p-4")}>
        <h2 id="res-h" className="text-[15px] font-semibold leading-[22px] text-fg">
          What the panel keeps
        </h2>
        <LoadFailed
          what="what the panel keeps"
          onRetry={() => {
            void hostRead.refetch();
            void setupRead.refetch();
          }}
        />
      </section>
    );
  }
  if (!host || !setup) return null;
  const apps = setup.role !== "control_plane";
  const r = host.reservation;
  const cpus = host.cpu_cores || 1;
  const mem = host.memory_bytes || 1;
  const rows = [
    {
      label: "CPU",
      total: `${host.cpu_cores} vCPU`,
      sys: r.system_cpu / cpus,
      panel: r.panel_cpu / cpus,
      used: (server?.now?.cpu_percent ?? 0) / 100,
      note: `System ${r.system_cpu} · the panel ${r.panel_cpu} · apps ${r.apps_cpu} vCPU`,
    },
    {
      label: "Memory",
      total: size(host.memory_bytes),
      sys: r.system_memory_bytes / mem,
      panel: r.panel_memory_bytes / mem,
      used: (server?.now?.memory_used_bytes ?? 0) / mem,
      note: `System ${size(r.system_memory_bytes)} · the panel ${size(r.panel_memory_bytes)} · apps ${size(r.apps_memory_bytes)}`,
    },
  ];
  return (
    <section
      aria-labelledby="res-h"
      className={cn(card, "flex flex-col gap-3.5 p-4")}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2
          id="res-h"
          className="text-[15px] font-semibold leading-[22px] text-fg"
        >
          What the panel keeps
        </h2>
        <span className="text-xs text-muted">
          {apps
            ? "Run apps here too · the panel’s share is fixed"
            : "Only Toft"}
        </span>
      </div>
      {apps ? (
        <>
          <div className="flex flex-wrap items-center gap-4 text-xs text-fg/70">
            <Legend className="bg-muted/60">System</Legend>
            <Legend className="bg-fg/60">The panel</Legend>
            <Legend className="bg-accent">Apps</Legend>
            <span className="inline-flex items-center gap-1.5">
              <span
                aria-hidden="true"
                className="h-[3px] w-3.5 rounded-sm bg-fg"
              />
              In use now
            </span>
          </div>
          <div className="grid grid-cols-[repeat(auto-fit,minmax(min(300px,100%),1fr))] gap-5">
            {rows.map((row) => (
              <div key={row.label} className="flex flex-col gap-1.5">
                <div className="flex justify-between gap-2 text-xs">
                  <span className="text-fg/70">{row.label}</span>
                  <span className="font-mono text-fg">{row.total}</span>
                </div>
                <div
                  aria-hidden="true"
                  className="flex h-2.5 gap-0.5 overflow-hidden rounded-xs bg-raised"
                >
                  <span
                    className="bg-muted/60"
                    style={{ width: `${row.sys * 100}%` }}
                  />
                  <span
                    className="bg-fg/60"
                    style={{ width: `${row.panel * 100}%` }}
                  />
                  <span className="flex-1 bg-accent" />
                </div>
                <div
                  aria-hidden="true"
                  className="h-[3px] rounded-sm bg-raised"
                >
                  <div
                    className="h-[3px] rounded-sm bg-fg"
                    style={{ width: `${Math.min(1, row.used) * 100}%` }}
                  />
                </div>
                <span className="text-xs text-muted">{row.note}</span>
              </div>
            ))}
          </div>
          <p className="text-xs leading-[17px] text-muted">
            {r.enforced ? (
              <>
                Apps run in{" "}
                <span className="font-mono text-fg/85">toft-apps.slice</span>,
                which holds them to their share, so a busy app can’t starve the
                panel.{" "}
              </>
            ) : (
              "Apps aren’t held to their share on this install: an install with install.sh runs them in toft-apps.slice. "
            )}
            They also show in{" "}
            <Link href="/servers" className="inline-link">
              Servers
            </Link>
            , as this server’s row.
          </p>
        </>
      ) : (
        <p className="text-[13px] leading-[19px] text-fg/85">
          Everything here is the panel’s: this server’s role is Only Toft,
          and apps deploy to your other servers.{" "}
          <Link
            href="/setup/role"
            className="inline-link"
          >
            Change role
          </Link>
        </p>
      )}
    </section>
  );
}

function Legend({
  className,
  children,
}: {
  className: string;
  children: React.ReactNode;
}) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span
        aria-hidden="true"
        className={cn("h-2.5 w-2.5 rounded-sm", className)}
      />
      {children}
    </span>
  );
}

function CardHead({
  id,
  title,
  right,
}: {
  id: string;
  title: string;
  right?: React.ReactNode;
}) {
  return (
    <div className="flex items-center justify-between gap-2 border-b border-subtle px-4 py-3">
      <h2 id={id} className="text-[15px] font-semibold leading-[22px] text-fg">
        {title}
      </h2>
      {right}
    </div>
  );
}

function Rows({
  rows,
  mono,
}: {
  rows: [string, React.ReactNode][];
  mono?: boolean;
}) {
  return (
    <dl className="flex flex-col text-[13px]">
      {rows.map(([k, v], i) => (
        <div
          key={k}
          className={cn(
            "flex flex-wrap justify-between gap-x-3 gap-y-0.5 px-4 py-2.5",
            i > 0 && "border-t border-subtle",
          )}
        >
          <dt className="text-muted">{k}</dt>
          <dd
            className={cn("text-right text-fg/85", mono && "font-mono text-xs")}
          >
            {v}
          </dd>
        </div>
      ))}
    </dl>
  );
}

function DatabaseCard({ server }: { server: PanelServer | undefined }) {
  const db = server?.database;
  const [now] = useState(() => Date.now());
  const backup = useQuery({
    queryKey: PANEL_KEY,
    queryFn: () => backupService.get(backupPath(null)),
    retry: false,
  });
  const up = useQuery(upgradeQuery).data;
  const b = backup.isSuccess ? backupState(backup.data, now) : null;
  const backedUp: React.ReactNode = !b ? (
    "—"
  ) : b.tone === "off" ? (
    <Link
      href="/settings/backups#panel-host"
      className="text-link hover:underline"
    >
      Not set up
    </Link>
  ) : b.tone === "ok" ? (
    `Nightly, with the server · ${b.note}`
  ) : (
    `${b.label}${b.note ? ` · ${b.note}` : ""}`
  );
  return (
    <section aria-labelledby="pg-h" className={card}>
      <CardHead
        id="pg-h"
        title="Database"
        right={
          db ? (
            <span className="inline-flex items-center gap-1.5 text-xs text-status-running-fg">
              <span
                aria-hidden="true"
                className="h-1.5 w-1.5 rounded-full bg-status-running"
              />
              Accepting connections
            </span>
          ) : null
        }
      />
      {db ? (
        <Rows
          rows={[
            ["Version", `Postgres ${db.version.split(" ")[0]}`],
            ["Size", size(db.size_bytes)],
            ["Connections", `${db.connections} of ${db.max_connections}`],
            ["Backed up", backedUp],
            ...(up?.install === "installer"
              ? ([["Dumps before upgrades", dumpWords(up)]] as [
                  string,
                  string,
                ][])
              : []),
          ]}
        />
      ) : (
        <p className="px-4 py-3 text-[13px] text-muted">
          {server ? "The database didn’t say." : "Reading…"}
        </p>
      )}
    </section>
  );
}

function HostCard({ server }: { server: PanelServer | undefined }) {
  const host = useQuery(panelHostQuery).data;
  const shell =
    useQuery({ ...panelStatusQuery, retry: false }).data?.shell ?? false;
  return (
    <section aria-labelledby="host-h" className={card}>
      <CardHead
        id="host-h"
        title="Host"
        right={
          server?.uptime_seconds ? (
            <span className="text-xs text-muted">
              Up {uptimeWords(server.uptime_seconds)}
            </span>
          ) : null
        }
      />
      {server ? (
        <>
          <Rows
            mono
            rows={[
              ...(host?.address
                ? ([["Address", host.address]] as [string, string][])
                : []),
              ["System", server.os || "—"],
              ["Kernel", server.kernel || "—"],
              ["Docker", server.docker_version || "—"],
              ["Firewall", systemWords(server.system).firewall],
            ]}
          />
          <p className="border-t border-subtle px-4 py-2.5 text-xs leading-[17px] text-muted">
            {shell
              ? "Host shell, top right, opens a root shell here. It asks for your password and is written to the audit log; TOFT_PANEL_HOST_SHELL=off in the panel’s settings file turns it off."
              : "The host shell is off on this panel: TOFT_PANEL_HOST_SHELL=off in its settings file."}
          </p>
        </>
      ) : (
        <p className="px-4 py-3 text-[13px] text-muted">Reading…</p>
      )}
    </section>
  );
}

/** The ranges the installer picked on the panel's server and the host ports
 *  it opens (D-152, board ReworkInstanceServer). */
function NetworksCard() {
  const hostRead = useQuery(panelHostQuery);
  const host = hostRead.data;
  // Git over SSH's port, where this install hosts git (D-71).
  const git = useQuery(gitInfoQuery).data;
  if (readFailed(hostRead)) {
    return (
      <section aria-labelledby="net-h" className={card}>
        <CardHead id="net-h" title="Networks" />
        <div className="p-4">
          <LoadFailed what="the networks" onRetry={() => void hostRead.refetch()} />
        </div>
      </section>
    );
  }
  if (!host) return null;
  // Docker's own pools here: [] when it has none, null when it didn't say.
  const pools = host.app_networks === "isolated" ? host.app_address_pools : undefined;
  // The last flag draws the value as words (Geist), not as an infrastructure value (mono).
  const rows: [string, string, string, boolean?, boolean?][] = [
    ["paas-network", host.control_plane_subnet || "—", "The panel and Traefik. Apps never join it; Postgres has its own internal network."],
    pools === undefined
      ? ["Apps", "paas-network", "Legacy app networks: apps share the control plane’s network until isolated networks are on."]
      : pools?.length
        ? ["paas-env-…", pools.map((p) => `${p.base} in /${p.size}s`).join(", "), "One network per environment, from this pool. No routes between them."]
        : pools
          ? [
              "paas-env-…",
              "not set",
              "Docker here has no default-address-pools, so apps take its own ranges, about 30 networks. Add them to /etc/docker/daemon.json and restart Docker, which restarts every container here, the panel too.",
              true,
              true,
            ]
          : ["paas-env-…", "not known yet", "Docker here didn’t say which pools it has. Reload this page to read them again.", false, true],
    git?.enabled
      ? ["Host ports", `80, 443, 22, ${git.ssh_port}`, `Traefik, SSH, and git over SSH on ${git.ssh_port}. The host firewall keeps apps off everything else here.`]
      : ["Host ports", "80, 443, 22", "Traefik and SSH. The host firewall keeps apps off everything else here."],
  ];
  return (
    <section aria-labelledby="net-h" className={card}>
      <CardHead id="net-h" title="Networks" />
      <dl className="flex flex-col text-[13px]">
        {rows.map(([name, range, what, warn, words], i) => (
          <div
            key={name}
            className={cn(
              "grid grid-cols-[120px_minmax(0,1fr)] gap-3 px-4 py-2.5",
              i > 0 && "border-t border-subtle",
            )}
          >
            <dt className="font-mono text-xs leading-5 text-fg">{name}</dt>
            <dd className="min-w-0">
              <span className={cn(words ? "text-[13px] leading-5" : "font-mono text-xs leading-5", warn ? "text-status-pending-fg" : "text-fg/85")}>
                {range}
              </span>
              <p className="mt-0.5 text-xs leading-[18px] text-muted">{what}</p>
            </dd>
          </div>
        ))}
      </dl>
      <p className="border-t border-subtle px-4 py-2.5 text-xs leading-[17px] text-muted">
        {pools?.length === 0
          ? "The installer picked paas-network’s range. Docker picks each app network’s from its own ranges until it has a pool."
          : pools === null
            ? "The installer picked paas-network’s range; the app networks’ pool isn’t known yet."
            : "The installer picked ranges no route or network on this host uses."}
      </p>
    </section>
  );
}
