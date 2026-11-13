"use client";

import Link from "next/link";
import { safeHref } from "@/lib/routes";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ExternalLink, Layers } from "lucide-react";
import { serviceService, type Service } from "@/lib/api/service.service";
import { allDeploymentsQuery } from "@/lib/api/deployment.service";
import { observabilityService, type ObservabilityConfig } from "@/lib/api/observability.service";
import { orgService } from "@/lib/api/org.service";
import { deploymentHref } from "@/components/home/home";
import { isStoppable } from "@/lib/status";
import { ago } from "@/lib/time";
import { cn } from "@/lib/utils";
import { ansiSpans, clockTime, composeServiceOf, lineStamp, lineTime, readLine, splitStamp, startOfDay, zoneName, type LogLevel, type Span } from "../log-view";

// The Logs tab (board: private/design/canvas/ReworkServiceLogs.dc.html): the running
// container's stdout and stderr, streamed with Docker's timestamps.

/** The most lines the tab keeps; the oldest go first. */
const CAP = 5000;
/** A follow that ends with the service not stopped is opened again after
 *  these waits (about a minute in all), then the tab says Disconnected. */
const RETRY_MS = [2000, 5000, 10000, 15000, 30000];
/** A follow that lasted this long counts as having held: its end starts the tries afresh. */
const HELD_MS = 30_000;
/** How often a deploying service with no container yet is looked at again. */
const NO_CONTAINER_POLL_MS = 5000;
// One shared empty list: `current?.lines ?? []` would be a new array each
// render and fire every hook that depends on `lines`.
const NO_LINES: Line[] = [];
const TAILS = [100, 500, 1000] as const;
/** Below sm the list grows with the page instead of scrolling in a box. */
const PHONE = "(max-width: 639px)";
const onPhone = () => typeof window.matchMedia === "function" && window.matchMedia(PHONE).matches;
const LEVELS = [
  { id: "all", label: "All" },
  { id: "warn", label: "Warnings" },
  { id: "error", label: "Errors" },
] as const;

interface Line {
  id: number;
  at: number | null;
  /** at's full date, time and time zone. */
  stamp?: string;
  level: LogLevel;
  text: string;
  /** The line as written, when text is its message read from JSON; the filter searches it too. */
  source?: string;
  /** text's ANSI-coloured runs; none for a line without colours. */
  spans: Span[] | null;
  /** Written after the tab opened the stream. */
  live: boolean;
  /** Not a line: the container restarted here. */
  restart?: boolean;
}

/** A new follow's first lines over the last follow's: they are the tail read
 *  again, so what they re-read goes, what's older stays, and each restart
 *  stays where it happened. A first line with no time can't be placed, so
 *  the batch replaces the list. */
function mergeFollow(old: Line[], batch: Line[]): Line[] {
  const first = batch[0]?.at ?? null;
  if (first === null) return batch;
  const rest = [...batch];
  for (const mark of old) {
    if (!mark.restart || mark.at === null || mark.at < first) continue;
    const at = mark.at;
    const i = rest.findIndex((l) => l.at !== null && l.at > at);
    rest.splice(i < 0 ? rest.length : i, 0, mark);
  }
  return [...old.filter((l) => l.at !== null && l.at < first), ...rest];
}

/** One stream's lines: key says which stream they came from. */
interface StreamState {
  key: string;
  openedAt: number;
  container: string;
  /** The container followed runs; false: its last lines, then the end. */
  running: boolean;
  /** What went wrong, in the tab's words; "" when nothing did. */
  failure: string;
  /** The API refused the stream, or the session ended: reconnecting can't help. */
  refused: boolean;
  /** The server found no container to follow (the no_container event). */
  noContainer: boolean;
  /** Why the follow ended when failure doesn't say: the container restarted,
   *  restarted again and again, or its server can't be reached; "" when a
   *  connection closed. */
  cause: "" | "restarted" | "restarts" | "unreachable";
  lines: Line[];
  /** Lines that arrived while paused. */
  held: Line[];
  ended: number | null;
  /** The follow ended and the tab is opening another. */
  reconnecting: boolean;
}

// The board names only warnings and errors; an info or debug line has no level shown.
const LEVEL_LOOK: Record<LogLevel, { label: string; tint: string; row: string }> = {
  error: { label: "ERROR", tint: "text-status-failed-fg", row: "bg-status-failed-soft/60" },
  warn: { label: "WARN", tint: "text-status-pending-fg", row: "bg-status-pending-soft/45" },
  info: { label: "", tint: "", row: "" },
  debug: { label: "", tint: "", row: "" },
  "": { label: "", tint: "", row: "" },
};

const tool = "inline-flex h-[30px] items-center gap-1.5 rounded-md border border-strong bg-raised px-2.5 text-xs font-medium text-fg hover:bg-raised/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-50 pointer-coarse:h-11";
const field = "h-8 rounded-lg border border-strong bg-ground px-2.5 text-[13px] text-fg focus:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:h-11";

interface LogsTabProps {
  service: Service;
  serviceId: string;
  projectId: string;
  /** Where it runs, as the header names it. */
  serverName: string;
  /** Its server's page, when the viewer has one to open. */
  serverId?: string;
  canDeploy: boolean;
  deployLabel: string;
  onDeploy: () => void;
  onDeployments: () => void;
}

export function LogsTab({ service, serviceId, projectId, serverName, serverId, canDeploy, deployLabel, onDeploy, onDeployments }: LogsTabProps) {
  const isCompose = service.source_type === "docker-compose";
  const canStream = isStoppable(service.runtime_status);
  const [tail, setTail] = useState<(typeof TAILS)[number]>(500);
  const [picked, setPicked] = useState("");
  const [level, setLevel] = useState<(typeof LEVELS)[number]["id"]>("all");
  const [filter, setFilter] = useState("");
  const [paused, setPaused] = useState<number | null>(null);
  const [session, setSession] = useState(0);
  // Today's midnight; moved on by the next line after it passes.
  const [dayStart, setDayStart] = useState(() => startOfDay(Date.now()));

  const { data: containers, isError: containersFailed, refetch: refetchContainers } = useQuery({
    queryKey: ["containers", serviceId],
    queryFn: () => serviceService.listContainers(serviceId),
    enabled: isCompose,
    staleTime: 15_000,
    retry: false,
  });
  // A compose stack opens on the compose service its domains go to.
  const routed = isCompose && service.compose_route_service
    ? (containers ?? []).find((c) => composeServiceOf(c, serviceId) === service.compose_route_service) ?? ""
    : "";
  const container = picked || routed;

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
  const hasLoki = [projectObs, orgObs].some((o) => (o as ObservabilityConfig | undefined)?.loki_url || (o as ObservabilityConfig | undefined)?.loki_service_id);

  const { data: lastStop, dataUpdatedAt: stopReadAt } = useQuery({
    queryKey: ["audit", "service.stopped", serviceId],
    queryFn: () => orgService.listAuditLog({ action: "service.stopped", resource: `service:${serviceId}`, limit: 1 }),
    enabled: service.runtime_status === "stopped",
  });

  // A new live deployment replaces the container: follow the new one.
  const key = `${serviceId}|${container}|${tail}|${session}|${service.active_deployment_id ?? ""}`;
  const [stream, setStream] = useState<StreamState | null>(null);
  const current = stream && stream.key === key ? stream : null;
  // A deploy began after the tab stopped looking for a container.
  if (current?.noContainer && current.ended && service.runtime_status === "deploying") setSession((n) => n + 1);
  // A new stream is another deployment, container or tail: Paused was about the old one.
  const [pausedKey, setPausedKey] = useState(key);
  if (pausedKey !== key) {
    setPausedKey(key);
    setPaused(null);
  }
  const pausedRef = useRef(paused);
  useEffect(() => { pausedRef.current = paused; }, [paused]);
  const runtimeRef = useRef(service.runtime_status);
  useEffect(() => { runtimeRef.current = service.runtime_status; }, [service.runtime_status]);
  // Pausing while the tab reconnects stops it trying.
  const stopReconnectRef = useRef(() => {});

  useEffect(() => {
    if (!canStream) return;
    const openedAt = Date.now();
    const fresh = (): StreamState => ({ key, openedAt, container: "", running: true, failure: "", refused: false, noContainer: false, cause: "", lines: [], held: [], ended: null, reconnecting: false });
    const update = (change: (s: StreamState) => Partial<StreamState>) =>
      setStream((prev) => {
        const base = prev && prev.key === key ? prev : fresh();
        return { ...base, ...change(base) };
      });
    let next = 0;
    // Lines arrive in bursts (the first tail lines at once): batch them per frame.
    let pending: Line[] = [];
    let timer: ReturnType<typeof setTimeout> | null = null;
    // A new follow's first lines take over from the last follow's: the tail is read again.
    let replace = false;
    let reconnecting = false;
    const flush = () => {
      timer = null;
      const batch = pending;
      const replacing = replace;
      pending = [];
      replace = false;
      reconnecting = false;
      setDayStart(startOfDay(Date.now()));
      update((base) =>
        pausedRef.current
          ? { reconnecting: false, held: [...base.held, ...batch].slice(-CAP) }
          : { reconnecting: false, lines: (replacing ? mergeFollow(base.lines, batch) : [...base.lines, ...batch]).slice(-CAP) },
      );
    };
    const end = () => {
      setPaused(null);
      update((base) => ({ ended: Date.now(), reconnecting: false, lines: [...base.lines, ...base.held].slice(-CAP), held: [] }));
    };
    let attempt = 0;
    let retry: ReturnType<typeof setTimeout> | null = null;
    let stop = () => {};
    // Docker's time of the newest line, and of the last restart marked at it.
    let lastAt: number | null = null;
    let markedAt: number | null = null;
    const open = () => {
      const started = Date.now();
      let running = true;
      let sawContainer = false;
      let failed = false;
      let refused = false;
      let absent = false;
      stop = serviceService.streamLiveLogs(
        serviceId,
        (raw) => {
          const { at, rest } = splitStamp(raw);
          if (at !== null) lastAt = at;
          const { plain, spans } = ansiSpans(rest);
          const { level: lvl, text } = readLine(plain);
          // Docker's time says which lines were written before the tab opened;
          // a line without one counts as live once the first burst is over.
          // A JSON line read as its message loses its colours.
          pending.push({
            id: next++,
            at,
            stamp: at !== null ? lineStamp(at) : undefined,
            level: lvl,
            text,
            source: text === plain ? undefined : plain,
            spans: text === plain ? spans : null,
            live: at !== null ? at >= openedAt : Date.now() - openedAt > 1500,
          });
          if (!timer) timer = setTimeout(flush, 50);
        },
        () => {
          if (timer) {
            clearTimeout(timer);
            flush();
          }
          if (Date.now() - started >= HELD_MS) attempt = 0;
          // The server found no container: the empty state says why. A slot
          // runs before its deployment finishes and sets the stream's key,
          // so look again while it deploys; a new deployment is a new stream.
          if (absent) {
            update(() => ({ noContainer: true }));
            if (runtimeRef.current !== "deploying") return end();
            retry = setTimeout(() => {
              retry = null;
              open();
            }, NO_CONTAINER_POLL_MS);
            return;
          }
          // A follow that ends cleanly while the container runs means it
          // restarted: mark the place, and say so if the tries run out.
          if (!failed && running && sawContainer) {
            const lastLine = lastAt;
            if (lastLine !== null && lastLine !== markedAt) {
              markedAt = lastLine;
              const mark: Line = { id: next++, at: lastLine, level: "", text: "", spans: null, live: true, restart: true };
              update((base) => (pausedRef.current !== null ? { held: [...base.held, mark].slice(-CAP) } : { lines: [...base.lines, mark].slice(-CAP) }));
            }
            update(() => ({ cause: attempt >= RETRY_MS.length ? "restarts" : "restarted" }));
          }
          if (pausedRef.current !== null || !running || refused || attempt >= RETRY_MS.length) return end();
          reconnecting = true;
          update(() => ({ reconnecting: true }));
          retry = setTimeout(() => {
            retry = null;
            replace = true;
            update(() => ({ failure: "" }));
            open();
          }, RETRY_MS[attempt++]);
        },
        (message, status) => {
          failed = true;
          refused = status === 401 || status === 403 || status === 404;
          // The API's own words, as a sentence; a session that ended is
          // leaving this page anyway. No status: the connection went, which
          // the tab says in its own words.
          const failure = refused ? `${message.charAt(0).toUpperCase()}${message.slice(1)}${message.endsWith(".") ? "" : "."}` : status ? "The log couldn’t be read." : "";
          update(() => ({ failure, refused, cause: "" }));
        },
        tail,
        container || undefined,
        {
          timestamps: true,
          onContainer: (name, isRunning) => {
            running = isRunning;
            sawContainer = true;
            reconnecting = false;
            update(() => ({ container: name, running: isRunning, reconnecting: false, noContainer: false, cause: "" }));
          },
          onFailure: (message) => {
            failed = true;
            if (message.startsWith("no_container") && (runtimeRef.current === "deploying" || runtimeRef.current === "error")) {
              absent = true;
            } else if (message.startsWith("server_unreachable")) {
              update(() => ({ failure: "", cause: "unreachable" }));
            } else {
              update(() => ({ failure: message.includes("fell behind") ? "The log moved faster than this page could read it." : "The log couldn’t be read.", cause: "" }));
            }
          },
        },
      );
    };
    open();
    stopReconnectRef.current = () => {
      if (!reconnecting) return;
      reconnecting = false;
      if (retry) clearTimeout(retry);
      retry = null;
      stop();
      end();
    };
    return () => {
      if (timer) clearTimeout(timer);
      if (retry) clearTimeout(retry);
      stop();
    };
  }, [key, canStream, serviceId, tail, container]);

  // Follow the newest line while the view sits at the bottom.
  const scrollRef = useRef<HTMLDivElement>(null);
  const stickRef = useRef(true);
  const lines = current?.lines ?? NO_LINES;
  // Going back to All, or clearing the filter, keeps the row at the top of the narrower view in view.
  const keepRowRef = useRef<string | null>(null);
  useLayoutEffect(() => {
    const id = keepRowRef.current;
    if (id === null) return;
    keepRowRef.current = null;
    stickRef.current = false;
    scrollRef.current?.querySelector(`[data-line="${id}"]`)?.scrollIntoView({ block: "center" });
  }, [level, filter]);
  useEffect(() => {
    const el = scrollRef.current;
    if (el && stickRef.current && !onPhone()) el.scrollTop = el.scrollHeight;
  }, [lines.length, level, filter]);

  // On a phone the list grows with the page (D-155) and <main> scrolls: it
  // follows the newest line while the reader is at the list's end (scrolled
  // or jumped there, or a short log's end in view when a live line comes),
  // and Jump to latest floats while the end is out of view. The tab opens
  // at its top: the lines from before it opened never move the page.
  const followRef = useRef(false);
  const [behind, setBehind] = useState(false);
  // Set before the list's ResizeObserver or a scroll hears of the change.
  const tailLiveRef = useRef(false);
  const hasLinesRef = useRef(false);
  const tailLive = lines.at(-1)?.live ?? false;
  useLayoutEffect(() => {
    tailLiveRef.current = tailLive;
    hasLinesRef.current = lines.length > 0;
  }, [tailLive, lines.length]);
  useEffect(() => {
    const el = scrollRef.current;
    const main = document.getElementById("main");
    if (!el || !main) return;
    const below = () => el.getBoundingClientRect().bottom - main.getBoundingClientRect().bottom;
    const onScroll = () => {
      // An empty list's end is always in view (the page clamping its scroll
      // as a tab swaps in is no reason to follow).
      followRef.current = hasLinesRef.current && below() < 40;
      setBehind(onPhone() && !followRef.current);
    };
    let wasInView = false;
    // grew: the list changed size; otherwise <main> did (rotation, the
    // keyboard), which only says whether the end is in view.
    const onGrow = (grew: boolean) => {
      if (!onPhone()) return setBehind(false);
      const b = below();
      if (grew && b > 0 && (followRef.current || (wasInView && tailLiveRef.current))) {
        followRef.current = true;
        main.scrollTop += b;
        return;
      }
      wasInView = hasLinesRef.current && b < 40;
      setBehind(b >= 40);
    };
    const grow = new ResizeObserver((entries) => onGrow(entries.some((e) => e.target === el)));
    grow.observe(el);
    grow.observe(main);
    main.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      grow.disconnect();
      main.removeEventListener("scroll", onScroll);
    };
  }, [canStream]);
  const jumpToLatest = () => {
    const el = scrollRef.current;
    const main = document.getElementById("main");
    if (!el || !main) return;
    followRef.current = true;
    main.scrollTop += el.getBoundingClientRect().bottom - main.getBoundingClientRect().bottom;
    // The button goes once the end is in view; the log keeps the focus.
    el.focus({ preventScroll: true });
  };

  const firstRowInView = () => {
    const box = scrollRef.current;
    if (!box) return null;
    const top = Math.max(box.getBoundingClientRect().top, document.getElementById("main")?.getBoundingClientRect().top ?? 0);
    for (const row of box.querySelectorAll<HTMLElement>("[data-line]")) {
      if (row.getBoundingClientRect().bottom >= top) return row.dataset.line ?? null;
    }
    return null;
  };
  const pickLevel = (id: (typeof LEVELS)[number]["id"]) => {
    if (id === "all" && level !== "all") keepRowRef.current = firstRowInView();
    setLevel(id);
  };

  const resume = () => {
    setStream((prev) => (prev && prev.key === key ? { ...prev, lines: [...prev.lines, ...prev.held].slice(-CAP), held: [] } : prev));
    setPaused(null);
  };
  const reconnect = () => {
    setPaused(null);
    setSession((n) => n + 1);
  };
  const pause = () => {
    setPaused(Date.now());
    stopReconnectRef.current();
  };

  const needle = filter.trim().toLowerCase();
  // Up to 5,000 lines, re-filtered only when they or the filters change, not
  // on every render a 50 ms live batch causes.
  const { shown, backlog, firstLive, counts, dated, restarts } = useMemo(() => {
    const shown = lines.filter(
      (l) =>
        (level === "all" || l.level === level) &&
        (!needle || l.text.toLowerCase().includes(needle) || l.source?.toLowerCase().includes(needle)),
    );
    let backlog = 0, warns = 0, errors = 0, dated = false, restarts = 0;
    for (const l of lines) {
      if (l.restart) restarts++;
      if (!l.live) backlog++;
      if (l.level === "warn") warns++;
      if (l.level === "error") errors++;
      if (l.at !== null && l.at < dayStart) dated = true;
    }
    return { shown, backlog, firstLive: shown.findIndex((l) => l.live), counts: { all: 0, warn: warns, error: errors }, dated, restarts };
  }, [lines, level, needle, dayStart]);
  const loaded = lines.length - restarts;
  const scope = `the last ${loaded === 1 ? "line" : `${loaded.toLocaleString("en")} lines`}`;
  const held = current?.held.length ?? 0;
  const ended = canStream ? current?.ended ?? null : null;
  const followed = isCompose ? container || current?.container || "" : current?.container || "";
  const name = isCompose && followed ? composeServiceOf(followed, serviceId) || service.name : service.name;

  const deploying = service.runtime_status === "deploying";
  const noContainer = !!current?.noContainer && (deploying || service.runtime_status === "error");
  const { data: deployments } = useQuery({ ...allDeploymentsQuery(serviceId), enabled: noContainer });
  const latest = deployments?.[0];

  // The board's status line: a coloured word and when.
  const status = !canStream
    ? service.runtime_status === "idle"
      ? { label: "Not deployed", cls: "text-muted", when: "" }
      : { label: "Stopped", cls: "text-muted", when: `${service.name} isn’t running` }
    : noContainer
    ? deploying
      ? { label: "Deploying", cls: "text-status-building-fg", when: "no container yet" }
      : { label: "No container", cls: "text-muted", when: "" }
    : current && !current.running
    ? { label: "Not running", cls: "text-status-pending-fg", when: current.container ? `${current.container} has stopped` : "" }
    : ended && current?.refused
    ? { label: "Unavailable", cls: "text-status-pending-fg", when: "" }
    : ended
    ? { label: "Disconnected", cls: "text-status-pending-fg", when: `at ${clockTime(ended)}` }
    : current?.reconnecting
    ? { label: "Reconnecting…", cls: "text-status-building-fg", when: current.cause === "restarted" ? `${name} restarted` : `to ${serverName}` }
    : paused !== null
    ? { label: "Paused", cls: "text-status-pending-fg", when: `at ${clockTime(paused)}` }
    : !current
    ? { label: "Connecting", cls: "text-status-building-fg", when: `to ${serverName}` }
    : { label: "Live", cls: "text-status-running-fg", when: `from ${clockTime(current.openedAt)}` };

  const stopper = lastStop?.[0];
  const now = stopReadAt;

  const bar = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-fg/85";

  return (
    <section aria-labelledby="log-h" className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
      <h2 id="log-h" className="sr-only">Logs</h2>
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
        <div role="status" className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          <span className={cn("inline-flex items-center gap-1.5 text-[13px] font-medium", status.cls)}>
            <span className="h-1.5 w-1.5 rounded-full bg-current" aria-hidden="true" />
            {status.label}
          </span>
          {status.when && <span className="text-xs text-muted">{status.when}</span>}
        </div>
        <div className="flex items-center gap-2">
          {canStream && !ended && current && !noContainer && (
            <button type="button" className={tool} aria-pressed={paused !== null} onClick={() => (paused !== null ? resume() : pause())}>
              {paused !== null ? "Resume" : "Pause"}
            </button>
          )}
          {grafanaUrl && (
            <a
              href={grafanaUrl}
              target="_blank"
              rel="noreferrer"
              aria-label="Open Grafana, opens in a new tab"
              className="hidden h-[30px] items-center gap-1.5 rounded-md px-2.5 text-xs font-medium text-fg/75 hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus sm:inline-flex"
            >
              Open Grafana
              <ExternalLink className="h-3 w-3" aria-hidden="true" />
            </a>
          )}
        </div>
      </div>

      {noContainer ? (
        <div className="flex flex-col items-start gap-2 px-4 py-4">
          <p className="text-[13px] font-medium text-fg">
            {deploying ? `${service.name} hasn’t started yet` : `${service.name} has no container`}
          </p>
          <p className="max-w-[640px] text-[13px] leading-[19px] text-muted">
            {deploying
              ? "It’s still deploying, so there’s nothing to show. Its lines appear here once it starts."
              : "Its last deploy didn’t leave one running, so there’s nothing to show. The deployment’s output says why."}
          </p>
          <div className="flex flex-wrap gap-2 pt-1">
            {latest ? (
              <Link href={deploymentHref(projectId, serviceId, latest.id)} className={tool}>Open its deployment</Link>
            ) : (
              <button type="button" className={tool} onClick={onDeployments}>See its deploys</button>
            )}
          </div>
        </div>
      ) : !canStream ? (
        <div className="flex flex-col items-start gap-2 px-4 py-4">
          <p className="text-[13px] font-medium text-fg">
            {service.runtime_status === "idle"
              ? `${service.name} hasn’t been deployed, so nothing is streaming`
              : `${service.name} is stopped, so nothing is streaming`}
          </p>
          <p className="max-w-[640px] text-[13px] leading-[19px] text-muted">
            {stopper && service.runtime_status === "stopped" && <>{stopper.actor} stopped it {ago(stopper.created_at, now)}. </>}
            Deploying starts it{service.runtime_status === "stopped" ? " again" : ""}, and its new lines show here.
          </p>
          <div className="flex flex-wrap gap-2 pt-1">
            {canDeploy && (
              <button type="button" className={tool} onClick={onDeploy}>
                <Layers className="h-3.5 w-3.5" aria-hidden="true" />
                {deployLabel}
              </button>
            )}
          </div>
        </div>
      ) : (
        <>
          <div role="toolbar" aria-label="Log view" className="flex flex-wrap items-center gap-2 border-b border-subtle px-4 py-2.5">
            <input
              type="search"
              value={filter}
              onChange={(e) => {
                if (!e.target.value.trim() && filter.trim()) keepRowRef.current = firstRowInView();
                setFilter(e.target.value);
              }}
              placeholder="Filter lines"
              aria-label="Filter lines"
              className={cn(field, "min-w-0 flex-[1_1_220px] placeholder:text-muted sm:max-w-[360px]")}
            />
            <span role="group" aria-label="Levels" className="inline-flex gap-0.5 rounded-lg border border-strong p-0.5">
              {LEVELS.map((l) => (
                <button
                  key={l.id}
                  type="button"
                  aria-pressed={level === l.id}
                  title={l.id === "all" ? undefined : `In ${scope}`}
                  onClick={() => pickLevel(l.id)}
                  className={cn(
                    "h-[26px] rounded-md px-2.5 text-[13px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:h-11",
                    level === l.id ? "bg-raised text-fg" : "text-fg/70 hover:text-fg",
                  )}
                >
                  {l.label}
                  {l.id !== "all" && <span className="ml-1.5 text-[11px] text-muted">{counts[l.id].toLocaleString("en")}</span>}
                </button>
              ))}
            </span>
            {isCompose && (containers ?? []).length > 1 && (
              <select
                value={followed}
                onChange={(e) => setPicked(e.target.value)}
                aria-label="Container"
                className={cn(field, "w-auto max-w-[280px] cursor-pointer font-mono text-xs")}
              >
                {(containers ?? []).map((c) => (
                  <option key={c} value={c}>
                    {composeServiceOf(c, serviceId) ? `${composeServiceOf(c, serviceId)} · ${c}` : c}
                  </option>
                ))}
              </select>
            )}
            {isCompose && containersFailed && !containers && (
              <span className="inline-flex items-center gap-2 text-xs text-muted">
                The other containers couldn’t be listed.
                <button type="button" className={tool} onClick={() => void refetchContainers()}>Retry</button>
              </span>
            )}
            <select
              value={tail}
              onChange={(e) => setTail(Number(e.target.value) as (typeof TAILS)[number])}
              aria-label="Start with"
              className={cn(field, "hidden w-auto cursor-pointer font-sans sm:block")}
            >
              {TAILS.map((n) => <option key={n} value={n}>The last {n.toLocaleString("en")} lines</option>)}
            </select>
          </div>

          {behind && paused === null && shown.length > 0 && (
            // Before the list, so swipe and Tab reach it without passing every line.
            <button
              type="button"
              onClick={jumpToLatest}
              className={cn(tool, "fixed bottom-[calc(16px+env(safe-area-inset-bottom))] left-1/2 z-20 -translate-x-1/2 rounded-full px-4 shadow-menu sm:hidden")}
            >
              Jump to latest
            </button>
          )}
          <div
            ref={scrollRef}
            role="log"
            tabIndex={0}
            aria-label={`${name} log lines`}
            aria-live="off"
            onScroll={(e) => {
              const el = e.currentTarget;
              stickRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
            }}
            className="max-h-[max(560px,calc(100vh-420px))] overflow-auto bg-ground max-sm:max-h-none max-sm:overflow-visible py-2 font-mono text-xs leading-5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus"
          >
            {shown.map((l, i) => {
              if (l.restart) {
                return (
                  <div key={l.id} className="my-1.5 border-t border-dashed border-strong px-4 pt-0.5 font-sans text-[11px] text-muted">
                    {name} restarted
                  </div>
                );
              }
              const look = LEVEL_LOOK[l.level];
              return (
                <div key={l.id} data-line={l.id}>
                  {i === firstLive && backlog > 0 && current && (
                    <div className="my-1.5 border-t border-dashed border-strong px-4 pt-0.5 font-sans text-[11px] text-muted">
                      Live from {clockTime(current.openedAt)}, when this tab opened
                    </div>
                  )}
                  {/* Columns from sm; on a phone the time and level sit above the text. */}
                  <div className={cn("px-4 py-1 sm:grid sm:gap-3 sm:py-px", dated ? "sm:grid-cols-[112px_52px_minmax(0,1fr)]" : "sm:grid-cols-[72px_52px_minmax(0,1fr)]", look.row)}>
                    <span title={l.stamp} className="block text-[11px] text-muted sm:text-xs">{l.at !== null ? lineTime(l.at, dayStart) : ""}</span>
                    <span className={cn("block text-[11px] sm:text-xs", look.label ? look.tint : "hidden sm:block")}>{look.label}</span>
                    <span className="block whitespace-pre-wrap text-fg/85 [overflow-wrap:anywhere]">
                      {l.spans ? l.spans.map((s, j) => <span key={j} className={s.cls || undefined}>{s.text}</span>) : l.text}
                    </span>
                  </div>
                </div>
              );
            })}
            {current && lines.length > 0 && shown.length === 0 && (
              <p className="px-4 py-2 font-sans text-xs text-muted">
                {needle ? `No ${level === "error" ? "error" : level === "warn" ? "warning" : "line"} matches “${filter.trim()}”` : level === "error" ? "No errors" : "No warnings"} in {scope}.
              </p>
            )}
            {current && lines.length === 0 && held === 0 && !ended && !current.reconnecting && (
              <p className="px-4 py-2 font-sans text-xs text-muted">{name} hasn’t written anything yet.</p>
            )}
          </div>

          {paused !== null && !ended && (
            <div className={bar}>
              <span>
                Paused at {clockTime(paused)}. {held === 0 ? "New lines are held." : `${held.toLocaleString("en")} new ${held === 1 ? "line is" : "lines are"} held.`}
              </span>
              <button type="button" className={tool} onClick={resume}>
                {held > 0 ? `Resume and show ${held.toLocaleString("en")} ${held === 1 ? "line" : "lines"}` : "Resume"}
              </button>
            </div>
          )}
          {ended && current && !current.running && (
            <div role="status" className={bar}>
              <span>
                {current.container || service.name} isn’t running.{" "}
                {lines.length > 0 ? "These are the last lines it wrote before it stopped, which often say why." : "It wrote nothing before it stopped."}
              </span>
              <button type="button" className={tool} onClick={reconnect}>Check again</button>
            </div>
          )}
          {ended && !noContainer && (!current || current.running) && (
            <div role="alert" className={bar}>
              <span>
                {current?.failure ||
                  (current?.cause === "unreachable"
                    ? `${serverName} can’t be reached.`
                    : current?.cause === "restarts"
                    ? `${name} keeps restarting.`
                    : current?.cause === "restarted"
                    ? `${name} restarted at ${clockTime(ended)}.`
                    : `The connection to Toft closed at ${clockTime(ended)}.`)}
                {!current?.refused && ` Lines ${service.name} writes now aren’t shown until you reconnect.`}
              </span>
              <span className="flex flex-wrap items-center gap-2">
                {current?.cause === "unreachable" && serverId && <Link href={`/servers/${serverId}`} className={tool}>Open {serverName}</Link>}
                {!current?.refused && <button type="button" className={tool} onClick={reconnect}>Reconnect</button>}
              </span>
            </div>
          )}

          <div className="border-t border-subtle px-4 py-2.5 text-xs text-muted">
            The current container’s stdout and stderr as Docker keeps them; levels are read from each line. Counts cover the lines loaded here.
            Times are in your time zone ({zoneName(dayStart)}).{" "}
            {grafanaUrl ? (
              <>Older lines are in <a href={grafanaUrl} target="_blank" rel="noreferrer" className="inline-link focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus">Grafana</a>.</>
            ) : hasLoki ? (
              <>Older lines are in Loki: <Link href="/shared?tab=logs" className="inline-link focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus">Workspace services → Logs and metrics</Link>.</>
            ) : (
              `Older lines stay in Docker’s logs on ${serverName}.`
            )}{" "}
            A deploy’s own output, health check included, stays on{" "}
            <button type="button" onClick={onDeployments} className="inline-link focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus">
              its deployment
            </button>
            .{lines.length >= CAP ? ` Keeps the last ${CAP.toLocaleString("en")} lines.` : ""}
          </div>
        </>
      )}
    </section>
  );
}
