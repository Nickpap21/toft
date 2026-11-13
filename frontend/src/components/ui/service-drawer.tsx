"use client";

import { useState, useRef, useEffect, useCallback } from "react";
import { useQuery } from "@tanstack/react-query";
import { X, Terminal, Container, Loader2, SquareTerminal } from "lucide-react";
import { serviceService } from "@/lib/api/service.service";
import { serverService } from "@/lib/api/server.service";
import { useIsOperator } from "@/context/AuthContext";
import { NEEDS_PASSWORD, useReauth } from "@/components/reauth";
import type { Terminal as XTermType } from "@xterm/xterm";
import { THEME } from "@/components/ui/pty-terminal";

interface Props {
    serviceId: string;
    serviceName: string;
    serverId?: string;   // set when service is deployed on a remote server
    open: boolean;
    onClose: () => void;
}

const QUICK_ACTIONS = [
    { label: "env",       cmd: "env | sort" },
    { label: "processes", cmd: "ps aux" },
    { label: "disk",      cmd: "df -h" },
    { label: "network",   cmd: "ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null" },
    { label: "whoami",    cmd: "whoami && id" },
];

type ShellTab = "shell" | "terminal";
type OutputEntry = { line: string; type: "cmd" | "out" | "err" };

export function ServiceDrawer({ serviceId, serviceName, serverId, open, onClose }: Props) {
    // which sub-tab is active (terminal tab only available on @server + serverId)
    const [chosenTab, setTab] = useState<ShellTab>("shell");

    // Shell state
    const [output, setOutput] = useState<OutputEntry[]>([]);
    const [command, setCommand] = useState("");
    const [running, setRunning] = useState(false);
    const [history, setHistory] = useState<string[]>([]);
    const [histIdx, setHistIdx] = useState(-1);
    // "" = the service's own container (the API picks it). The host shell
    // ("@server") and its PTY are for instance operators only (D-19).
    const isOperator = useIsOperator();
    const reauth = useReauth();
    const [container, setContainer] = useState("");
    const cleanupRef = useRef<(() => void) | null>(null);
    const outputEndRef = useRef<HTMLDivElement>(null);
    const inputRef = useRef<HTMLInputElement>(null);

    // PTY state (mirrors ServerDrawer)
    const ptyContainerRef = useRef<HTMLDivElement>(null);
    const ptyTermRef = useRef<XTermType | null>(null);
    const ptyWsRef = useRef<WebSocket | null>(null);
    const [ptyConnected, setPtyConnected] = useState(false);
    const [ptyError, setPtyError] = useState<string | null>(null);

    const { data: containers = [] } = useQuery({
        queryKey: ["containers", serviceId],
        queryFn: () => serviceService.listContainers(serviceId),
        enabled: open,
        staleTime: 30_000,
        retry: false,
    });

    // Can we show the PTY terminal tab?
    const hasPty = container === "@server" && !!serverId;

    // Without a PTY the terminal tab shows the shell (the container picker
    // also switches back to it, so coming back to @server stays on the shell).
    const tab: ShellTab = !hasPty && chosenTab === "terminal" ? "shell" : chosenTab;

    // PTY lifecycle — identical to ServerDrawer
    useEffect(() => {
        if (!open || tab !== "terminal" || !serverId) return;

        let term: XTermType | null = null;
        let ws: WebSocket | null = null;
        let destroyed = false;

        const init = async () => {
            const { Terminal: XTerm } = await import("@xterm/xterm");
            const { FitAddon } = await import("@xterm/addon-fit");
            const confirmed = await reauth();

            if (destroyed || !ptyContainerRef.current) return;
            if (!confirmed) {
                setPtyError(`${NEEDS_PASSWORD} Open the Terminal tab again to confirm it.`);
                return;
            }

            term = new XTerm({
                cursorBlink: true,
                fontSize: 13,
                fontFamily: '"Cascadia Code", Menlo, Monaco, monospace',
                theme: THEME,
            });

            const fitAddon = new FitAddon();
            term.loadAddon(fitAddon);
            term.open(ptyContainerRef.current);
            fitAddon.fit();
            ptyTermRef.current = term;

            const { cols, rows } = term;
            const wsUrl = serverService.getPtyWsUrl(serverId, cols, rows);
            ws = new WebSocket(wsUrl);
            ws.binaryType = "arraybuffer";
            ptyWsRef.current = ws;

            ws.onopen = () => {
                if (destroyed) { ws?.close(); return; }
                setPtyConnected(true);
                setPtyError(null);
            };
            ws.onmessage = (e) => {
                if (e.data instanceof ArrayBuffer) {
                    term?.write(new Uint8Array(e.data));
                } else {
                    try {
                        const msg = JSON.parse(e.data as string);
                        if (msg.type === "error") setPtyError(msg.data);
                    } catch { /* ignore */ }
                }
            };
            ws.onclose = () => {
                setPtyConnected(false);
                if (!destroyed) term?.write("\r\n\x1b[90m[disconnected]\x1b[0m\r\n");
            };
            ws.onerror = () => { setPtyError("WebSocket connection failed"); };

            term.onData((data) => {
                if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "input", data }));
            });
            term.onResize(({ cols, rows }) => {
                if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "resize", cols, rows }));
            });

            const ro = new ResizeObserver(() => { try { fitAddon.fit(); } catch { /* ignore */ } });
            if (ptyContainerRef.current) ro.observe(ptyContainerRef.current);
            return () => ro.disconnect();
        };

        const cleanupPromise = init();
        return () => {
            destroyed = true;
            cleanupPromise.then((cleanupRO) => cleanupRO?.());
            ws?.close();
            term?.dispose();
            ptyTermRef.current = null;
            ptyWsRef.current = null;
            setPtyConnected(false);
            setPtyError(null);
        };
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [open, tab, serverId]);

    useEffect(() => {
        outputEndRef.current?.scrollIntoView({ behavior: "smooth" });
    }, [output]);

    useEffect(() => {
        if (open && tab === "shell") {
            const timer = setTimeout(() => inputRef.current?.focus(), 100);
            return () => clearTimeout(timer);
        }
    }, [open, tab]);

    useEffect(() => { return () => { cleanupRef.current?.(); }; }, []);

    const runCommand = useCallback((cmd: string) => {
        const trimmed = cmd.trim();
        if (!trimmed || running) return;
        setHistory((h) => [trimmed, ...h.slice(0, 49)]);
        setHistIdx(-1);
        setOutput((prev) => [...prev, { line: "$ " + trimmed, type: "cmd" }]);
        setRunning(true);
        setCommand("");
        cleanupRef.current?.();
        // The host's shell needs the password confirmed first (D-53).
        void (container === "@server" ? reauth() : Promise.resolve(true)).then((ok) => {
            if (!ok) {
                setRunning(false);
                setOutput((prev) => [...prev, { line: `Error: ${NEEDS_PASSWORD}`, type: "err" }]);
                return;
            }
            cleanupRef.current = serviceService.exec(
                serviceId,
                container,
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
    }, [running, serviceId, container, reauth]);

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
            <div className="fixed inset-0 z-40 bg-scrim/20 backdrop-blur-[1px]" onClick={onClose} />

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
                        <span className="text-sm font-medium text-fg font-mono truncate">{serviceName}</span>
                        <div className="ml-auto flex items-center gap-2 shrink-0">
                            {tab === "terminal" && (
                                <span className={`flex items-center gap-1 text-xs ${ptyConnected ? "text-status-running-fg" : "text-muted"}`}>
                                    <span className={`h-1.5 w-1.5 rounded-full ${ptyConnected ? "bg-status-running" : "bg-strong"}`} />
                                    <span className="hidden sm:inline">{ptyConnected ? "connected" : ptyError ? "not connected" : "connecting…"}</span>
                                </span>
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

                    {/* Row 2: container selector + optional Shell/Terminal tabs (scrollable) */}
                    <div className="flex items-center gap-3 px-4 pb-2 overflow-x-auto">
                        <div className="flex items-center gap-1.5 shrink-0">
                            <Container className="h-3.5 w-3.5 text-muted shrink-0" aria-hidden="true" />
                            <select
                                value={container}
                                onChange={e => { setContainer(e.target.value); setOutput([]); if (e.target.value !== "@server") setTab("shell"); }}
                                className="cursor-pointer bg-ground border border-strong/70 rounded px-2 py-0.5 text-xs text-fg"
                            >
                                <option value="">Service container</option>
                                {isOperator && <option value="@server">Host server</option>}
                                {containers.map(c => (
                                    <option key={c} value={c}>{c}</option>
                                ))}
                            </select>
                        </div>

                        {hasPty && (
                            <div className="flex items-center gap-1">
                                {([
                                    { id: "shell",    label: "Shell",    icon: Terminal       },
                                    { id: "terminal", label: "Terminal", icon: SquareTerminal },
                                ] as const).map(({ id, label, icon: Icon }) => (
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
                            </div>
                        )}
                    </div>
                </div>

                {/* Shell tab */}
                {tab === "shell" && (
                    <div className="flex flex-col flex-1 min-h-0">
                        <div className="flex flex-wrap gap-1.5 px-4 py-2 border-b border-subtle shrink-0">
                            {QUICK_ACTIONS.map((action) => (
                                <button
                                    key={action.cmd}
                                    onClick={() => runCommand(action.cmd)}
                                    disabled={running}
                                    className="cursor-pointer px-2 py-0.5 rounded text-xs text-muted bg-raised hover:text-fg border border-subtle disabled:opacity-40 transition-colors font-mono"
                                >
                                    {action.label}
                                </button>
                            ))}
                        </div>

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
                    <div className="flex flex-col flex-1 min-h-0 relative">
                        {ptyError && (
                            <div className="absolute top-2 left-4 right-4 z-10 bg-status-failed-soft border border-status-failed/35 rounded px-3 py-2 text-xs text-status-failed-fg font-mono">
                                {ptyError}
                            </div>
                        )}
                        <div
                            ref={ptyContainerRef}
                            className="flex-1 min-h-0 p-2"
                            style={{ background: THEME.background }}
                        />
                    </div>
                )}
            </div>
        </>
    );
}
