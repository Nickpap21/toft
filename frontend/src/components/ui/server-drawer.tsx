"use client";

import { useState, useRef, useEffect, useCallback } from "react";
import { useQuery } from "@tanstack/react-query";
import { X, Terminal, Activity, Container, Loader2, SquareTerminal } from "lucide-react";
import { serverService, type SystemStats } from "@/lib/api/server.service";
import { diskGB } from "@/components/servers/capacity";
import { useIsOperator } from "@/context/AuthContext";
import { NEEDS_PASSWORD, useReauth } from "@/components/reauth";
import { Read } from "@/components/shell/unreachable";
import { PtyTerminal, type PtyState } from "@/components/ui/pty-terminal";
import { formatMemory } from "@/components/ui/capacity-dialog";

interface Props {
    serverId: string;
    serverName: string;
    open: boolean;
    onClose: () => void;
}

type Tab = "shell" | "system" | "docker" | "terminal";

const QUICK_ACTIONS = [
    { label: "disk usage",    cmd: "df -h" },
    { label: "memory",        cmd: "free -h" },
    { label: "containers",    cmd: "docker ps --format 'table {{.Names}}\\t{{.Status}}\\t{{.Ports}}'" },
    { label: "docker df",     cmd: "docker system df" },
    { label: "top processes", cmd: "ps aux --sort=-%cpu | head -15" },
    { label: "prune docker",  cmd: "docker system prune -f" },
];

function StatBar({ label, used, total, unit, format }: {
    label: string;
    used: number;
    total: number;
    unit?: string;
    format?: (n: number) => string;
}) {
    const pct = total > 0 ? Math.round((used / total) * 100) : 0;
    const barColor = pct > 85 ? "bg-status-failed" : pct > 65 ? "bg-status-pending" : "bg-link";

    const display = format
        ? `${format(used)} / ${format(total)}`
        : `${used} / ${total} ${unit}`;

    return (
        <div className="space-y-1.5">
            <div className="flex justify-between text-xs">
                <span className="text-muted">{label}</span>
                <span className="text-fg font-mono">
                    {display} <span className="text-muted">({pct}%)</span>
                </span>
            </div>
            <div className="h-1.5 rounded-full bg-raised overflow-hidden">
                <div
                    className={`h-full rounded-full transition-all duration-500 ${barColor}`}
                    style={{ width: `${pct}%` }}
                />
            </div>
        </div>
    );
}

function InfoStat({ label, value }: { label: string; value: string }) {
    return (
        <div>
            <div className="text-xs text-muted mb-0.5">{label}</div>
            <div className="text-sm text-fg font-mono">{value}</div>
        </div>
    );
}

type OutputEntry = { line: string; type: "cmd" | "out" | "err" };

export function ServerDrawer({ serverId, serverName, open, onClose }: Props) {
    // Shell, terminal and the docker view run commands on the host, which is
    // for instance operators only (D-19); everyone else sees System.
    const isOperator = useIsOperator();
    const [selectedTab, setTab] = useState<Tab>("shell");
    const tab: Tab = isOperator ? selectedTab : "system";
    const [output, setOutput] = useState<OutputEntry[]>([]);
    const [command, setCommand] = useState("");
    const [running, setRunning] = useState(false);
    const [history, setHistory] = useState<string[]>([]);
    const [histIdx, setHistIdx] = useState(-1);
    const cleanupRef = useRef<(() => void) | null>(null);
    const outputEndRef = useRef<HTMLDivElement>(null);
    const inputRef = useRef<HTMLInputElement>(null);

    const statsRead = useQuery<SystemStats>({
        queryKey: ["server-stats", serverId],
        queryFn: () => serverService.getSystemStats(serverId),
        enabled: open && tab === "system",
        staleTime: 10_000,
        retry: false,
    });
    const { isFetching: statsFetching, refetch: refetchStats } = statsRead;

    const [dockerOutput, setDockerOutput] = useState<string[]>([]);
    const [dockerLoading, setDockerLoading] = useState(false);

    // The Terminal tab's connection, for the header (PtyTerminal).
    const [pty, setPty] = useState<PtyState>({ connected: false, error: null });

    // Host shells need the password confirmed first (D-53).
    const reauth = useReauth();

    const loadDockerInfo = useCallback((): (() => void) => {
        let stopped = false;
        let cleanup: (() => void) | undefined;
        void reauth().then((ok) => {
            if (stopped) return;
            setDockerOutput([]);
            setDockerLoading(true);
            if (!ok) {
                setDockerLoading(false);
                setDockerOutput([`Error: ${NEEDS_PASSWORD} Refresh to confirm it.`]);
                return;
            }
            cleanup = serverService.exec(
                serverId,
                "echo '=== Running Containers ===' && docker ps --format 'table {{.Names}}\\t{{.Image}}\\t{{.Status}}' && echo '' && echo '=== Disk Usage ===' && docker system df",
                (line) => setDockerOutput((prev) => [...prev, line]),
                () => setDockerLoading(false),
                (msg) => { setDockerLoading(false); setDockerOutput([`Error: ${msg}`]); },
            );
        });
        return () => {
            stopped = true;
            cleanup?.();
        };
    }, [serverId, reauth]);

    useEffect(() => {
        if (open && tab === "docker") {
            const cleanup = loadDockerInfo();
            return () => cleanup?.();
        }
    }, [open, tab, loadDockerInfo]);

    useEffect(() => {
        outputEndRef.current?.scrollIntoView({ behavior: "smooth" });
    }, [output]);

    useEffect(() => {
        if (open && tab === "shell") {
            const timer = setTimeout(() => inputRef.current?.focus(), 100);
            return () => clearTimeout(timer);
        }
    }, [open, tab]);

    const runCommand = useCallback((cmd: string) => {
        const trimmed = cmd.trim();
        if (!trimmed || running) return;
        setHistory((h) => [trimmed, ...h.slice(0, 49)]);
        setHistIdx(-1);
        setOutput((prev) => [...prev, { line: "$ " + trimmed, type: "cmd" }]);
        setRunning(true);
        setCommand("");
        void reauth().then((ok) => {
            if (!ok) {
                setRunning(false);
                setOutput((prev) => [...prev, { line: `Error: ${NEEDS_PASSWORD}`, type: "err" }]);
                return;
            }
            cleanupRef.current = serverService.exec(
                serverId,
                trimmed,
                (line) => {
                    const type = line.toLowerCase().startsWith("error") ? "err" : "out";
                    setOutput((prev) => [...prev, { line, type }]);
                },
                () => setRunning(false),
                (msg) => {
                    setRunning(false);
                    setOutput((prev) => [...prev, { line: `Error: ${msg}`, type: "err" }]);
                },
            );
        });
    }, [running, serverId, reauth]);

    const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
        if (e.key === "Enter") {
            runCommand(command);
        } else if (e.key === "ArrowUp") {
            e.preventDefault();
            const next = Math.min(histIdx + 1, history.length - 1);
            setHistIdx(next);
            setCommand(history[next] ?? "");
        } else if (e.key === "ArrowDown") {
            e.preventDefault();
            const next = Math.max(histIdx - 1, -1);
            setHistIdx(next);
            setCommand(next === -1 ? "" : (history[next] ?? ""));
        } else if (e.key === "c" && e.ctrlKey) {
            cleanupRef.current?.();
            setRunning(false);
            setOutput((prev) => [...prev, { line: "^C", type: "err" }]);
        }
    };

    if (!open) return null;

    return (
        <>
            {/* Backdrop */}
            <div
                className="fixed inset-0 z-40 bg-scrim/20 backdrop-blur-[1px]"
                onClick={onClose}
            />

            {/* Drawer */}
            <div
                className="fixed bottom-0 left-0 right-0 z-50 flex flex-col bg-surface border-t border-subtle shadow-dialog"
                style={{ height: "52vh" }}
            >
                {/* Header — two rows so it never overflows on small screens */}
                <div className="relative border-b border-subtle shrink-0">
                    <div className="absolute top-1 left-1/2 -translate-x-1/2 w-8 h-1 rounded-full bg-strong" />

                    {/* Row 1: icon + name + right actions */}
                    <div className="flex items-center gap-3 px-4 pt-3 pb-1.5">
                        <Terminal className="h-4 w-4 text-muted shrink-0" aria-hidden="true" />
                        <span className="text-sm font-medium text-fg font-mono truncate">{serverName}</span>
                        <div className="ml-auto flex items-center gap-2 shrink-0">
                            {tab === "terminal" && (
                                <span className={`flex items-center gap-1 text-xs ${pty.connected ? "text-status-running-fg" : "text-muted"}`}>
                                    <span className={`h-1.5 w-1.5 rounded-full ${pty.connected ? "bg-status-running" : "bg-strong"}`} />
                                    <span className="hidden sm:inline">{pty.connected ? "connected" : pty.error ? "not connected" : pty.closed ? "disconnected" : "connecting…"}</span>
                                </span>
                            )}
                            {tab === "system" && (
                                <button onClick={() => refetchStats()} className="cursor-pointer text-xs text-muted hover:text-fg transition-colors">
                                    {statsFetching ? <Loader2 className="h-3.5 w-3.5 motion-safe:animate-spin" aria-hidden="true" /> : "↻"}
                                </button>
                            )}
                            {tab === "shell" && (
                                <button onClick={() => setOutput([])} className="cursor-pointer text-xs text-muted hover:text-fg transition-colors">
                                    clear
                                </button>
                            )}
                            <button onClick={onClose} className="cursor-pointer text-muted hover:text-fg transition-colors p-1">
                                <X className="h-4 w-4" />
                            </button>
                        </div>
                    </div>

                    {/* Row 2: tabs (scrollable) */}
                    <div className="flex items-center gap-1 px-4 pb-2 overflow-x-auto">
                        {(
                            [
                                { id: "shell",    label: "Shell",    icon: Terminal,       hostShell: true  },
                                { id: "terminal", label: "Terminal", icon: SquareTerminal, hostShell: true  },
                                { id: "system",   label: "System",   icon: Activity,       hostShell: false },
                                { id: "docker",   label: "Docker",   icon: Container,      hostShell: true  },
                            ] as const
                        ).filter(({ hostShell }) => isOperator || !hostShell).map(({ id, label, icon: Icon }) => (
                            <button
                                key={id}
                                onClick={() => setTab(id)}
                                className={`cursor-pointer shrink-0 flex items-center gap-1.5 px-3 py-1 rounded text-xs font-medium transition-colors ${
                                    tab === id
                                        ? "bg-raised text-fg"
                                        : "text-muted hover:text-fg hover:bg-raised"
                                }`}
                            >
                                <Icon className="h-3 w-3" />
                                {label}
                            </button>
                        ))}
                        {!isOperator && (
                            <span className="shrink-0 text-xs text-muted ml-2">
                                Shell access to the host is limited to instance operators.
                            </span>
                        )}
                    </div>
                </div>

                {/* Shell tab */}
                {tab === "shell" && (
                    <div className="flex flex-col flex-1 min-h-0">
                        {/* Quick actions */}
                        <div className="flex flex-wrap gap-1.5 px-4 py-2 border-b border-subtle shrink-0">
                            {QUICK_ACTIONS.map((action) => (
                                <button
                                    key={action.cmd}
                                    onClick={() => runCommand(action.cmd)}
                                    disabled={running}
                                    className="px-2 py-0.5 rounded text-xs text-muted bg-raised hover:text-fg border border-subtle disabled:opacity-40 transition-colors font-mono"
                                >
                                    {action.label}
                                </button>
                            ))}
                        </div>

                        {/* Output */}
                        <div role="group" tabIndex={0} aria-label="Command output" className="focus-inset flex-1 overflow-y-auto px-4 py-2 font-mono text-xs space-y-0.5">
                            {output.length === 0 && (
                                <p className="text-muted text-xs">
                                    Type a command below or use the quick actions above. ↑/↓ for history, Ctrl+C to cancel.
                                </p>
                            )}
                            {output.map((entry, i) => (
                                <div
                                    key={i}
                                    className={
                                        entry.type === "cmd" ? "text-link font-semibold" :
                                        entry.type === "err" ? "text-status-failed-fg" :
                                        "text-fg"
                                    }
                                >
                                    {entry.line}
                                </div>
                            ))}
                            {running && <div className="text-muted motion-safe:animate-pulse">▋</div>}
                            <div ref={outputEndRef} />
                        </div>

                        {/* Input */}
                        <div className="flex items-center gap-2 px-4 py-2.5 border-t border-subtle shrink-0">
                            <span className="text-link font-mono text-sm shrink-0">$</span>
                            <input
                                ref={inputRef}
                                value={command}
                                onChange={(e) => setCommand(e.target.value)}
                                onKeyDown={handleKeyDown}
                                disabled={running}
                                placeholder="enter command…"
                                className="flex-1 bg-transparent text-fg font-mono text-sm outline-none placeholder:text-muted disabled:opacity-50"
                            />
                            {running && <Loader2 className="h-3.5 w-3.5 text-muted motion-safe:animate-spin shrink-0" />}
                        </div>
                    </div>
                )}

                {/* Terminal tab — full PTY via WebSocket + xterm.js */}
                {tab === "terminal" && (
                    <PtyTerminal
                        url={(cols, rows) => serverService.getPtyWsUrl(serverId, cols, rows)}
                        retryHint="Open the Terminal tab again to confirm it."
                        onState={setPty}
                    />
                )}

                {/* System tab */}
                {tab === "system" && (
                    <div role="group" tabIndex={0} aria-label="Server stats" className="focus-inset flex-1 overflow-y-auto px-4 py-4">
                        <Read
                            query={statsRead}
                            what="this server’s stats"
                            loading={
                                <div className="flex items-center justify-center h-full gap-2 text-muted">
                                    <Loader2 className="h-4 w-4 motion-safe:animate-spin" aria-hidden="true" />
                                    <span className="text-sm">Collecting stats… (takes ~1s for CPU sample)</span>
                                </div>
                            }
                        >
                        {(stats) => (
                            <div className="space-y-5">
                                <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                                    <div className="space-y-4">
                                        <StatBar label="CPU"      used={stats.cpu_percent}  total={100}               unit="%" />
                                        <StatBar label="Memory"   used={stats.mem_used_mb}  total={stats.mem_total_mb} format={formatMemory} />
                                        <StatBar label="Disk (/)" used={stats.disk_used_mb} total={stats.disk_total_mb} format={diskGB} />
                                    </div>
                                    <div className="space-y-3 sm:pl-4 sm:border-l border-subtle">
                                        <InfoStat label="Load avg" value={`${stats.load_1} ${stats.load_5} ${stats.load_15}`} />
                                        <InfoStat label="Uptime"   value={stats.uptime || "—"} />
                                        {stats.docker_version && (
                                            <InfoStat label="Docker" value={stats.docker_version} />
                                        )}
                                    </div>
                                </div>
                            </div>
                        )}
                        </Read>
                    </div>
                )}

                {/* Docker tab */}
                {tab === "docker" && (
                    <div className="flex-1 overflow-y-auto px-4 py-3 font-mono text-xs">
                        {dockerLoading && dockerOutput.length === 0 && (
                            <div className="flex items-center gap-2 text-muted">
                                <Loader2 className="h-3.5 w-3.5 motion-safe:animate-spin" aria-hidden="true" />
                                <span>Loading Docker info…</span>
                            </div>
                        )}
                        {dockerOutput.map((line, i) => (
                            <div
                                key={i}
                                className={
                                    line.startsWith("===")
                                        ? "text-link font-semibold mt-3 first:mt-0"
                                        : line.toLowerCase().includes("error")
                                        ? "text-status-failed-fg"
                                        : "text-fg"
                                }
                            >
                                {line}
                            </div>
                        ))}
                        {!dockerLoading && (
                            <button
                                onClick={loadDockerInfo}
                                className="mt-3 text-xs text-muted hover:text-fg transition-colors"
                            >
                                ↻ refresh
                            </button>
                        )}
                    </div>
                )}
            </div>
        </>
    );
}
