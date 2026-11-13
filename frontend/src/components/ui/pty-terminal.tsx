"use client";

import { useEffect, useRef, useState } from "react";
import type { Terminal as XTermType } from "@xterm/xterm";
import { NEEDS_PASSWORD, useReauth } from "@/components/reauth";

export interface PtyState {
    connected: boolean;
    error: string | null;
    /** The connection ended after it opened. */
    closed?: boolean;
}

interface Props {
    /** The WebSocket URL for a terminal of cols by rows. */
    url: (cols: number, rows: number) => string;
    /** Says how to try again when the password wasn't confirmed. */
    retryHint: string;
    onState?: (s: PtyState) => void;
}

/** xterm takes colour strings, not CSS variables (DESIGN.md → Themes). */
export const THEME = {
    background: "#0d1117",
    foreground: "#e6edf3",
    cursor: "#58a6ff",
    selectionBackground: "#264f78",
    black: "#0d1117",
    brightBlack: "#6e7681",
    red: "#ff7b72",
    brightRed: "#ffa198",
    green: "#3fb950",
    brightGreen: "#56d364",
    yellow: "#d29922",
    brightYellow: "#e3b341",
    blue: "#58a6ff",
    brightBlue: "#79c0ff",
    magenta: "#bc8cff",
    brightMagenta: "#d2a8ff",
    cyan: "#39c5cf",
    brightCyan: "#56d4dd",
    white: "#b1bac4",
    brightWhite: "#f0f6fc",
};

/** A root shell on a host, in xterm.js over a WebSocket: a server's
 *  (ServerDrawer) or the panel's own (D-68). It asks for the password
 *  first (D-53) and closes the socket when it unmounts.
 *
 *  Client → server: {"type":"input","data":…} and {"type":"resize",…};
 *  server → client: raw output bytes, or {"type":"error","data":…}. */
export function PtyTerminal({ url, retryHint, onState }: Props) {
    const box = useRef<HTMLDivElement>(null);
    const [error, setError] = useState<string | null>(null);
    const reauth = useReauth();
    // The latest callbacks, without restarting the terminal when they change.
    const latest = useRef({ url, onState, retryHint });
    useEffect(() => {
        latest.current = { url, onState, retryHint };
    });

    useEffect(() => {
        let term: XTermType | null = null;
        let ws: WebSocket | null = null;
        let destroyed = false;
        let ro: ResizeObserver | null = null;
        const report = (connected: boolean, err: string | null) => {
            setError(err);
            latest.current.onState?.({ connected, error: err });
        };

        void (async () => {
            const { Terminal: XTerm } = await import("@xterm/xterm");
            const { FitAddon } = await import("@xterm/addon-fit");
            const confirmed = await reauth();
            if (destroyed || !box.current) return;
            if (!confirmed) {
                report(false, `${NEEDS_PASSWORD} ${latest.current.retryHint}`);
                return;
            }

            term = new XTerm({ cursorBlink: true, fontSize: 13, fontFamily: '"Cascadia Code", Menlo, Monaco, monospace', theme: THEME });
            const fit = new FitAddon();
            term.loadAddon(fit);
            term.open(box.current);
            fit.fit();
            term.focus();

            ws = new WebSocket(latest.current.url(term.cols, term.rows));
            ws.binaryType = "arraybuffer";
            ws.onopen = () => {
                if (destroyed) ws?.close();
                else report(true, null);
            };
            ws.onmessage = (e) => {
                if (e.data instanceof ArrayBuffer) {
                    term?.write(new Uint8Array(e.data));
                    return;
                }
                try {
                    const msg = JSON.parse(e.data as string);
                    if (msg.type === "error") report(false, msg.data);
                } catch {
                    /* not JSON: ignore */
                }
            };
            ws.onclose = () => {
                if (destroyed) return;
                latest.current.onState?.({ connected: false, error: null, closed: true });
                term?.write("\r\n\x1b[90m[disconnected]\x1b[0m\r\n");
            };
            ws.onerror = () => report(false, "The connection to the shell failed.");
            term.onData((data) => {
                if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "input", data }));
            });
            term.onResize(({ cols, rows }) => {
                if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "resize", cols, rows }));
            });
            ro = new ResizeObserver(() => {
                try {
                    fit.fit();
                } catch {
                    /* closed while resizing */
                }
            });
            ro.observe(box.current);
        })();

        return () => {
            destroyed = true;
            ro?.disconnect();
            ws?.close();
            term?.dispose();
        };
    }, [reauth]);

    return (
        <div className="relative flex min-h-0 flex-1 flex-col">
            {error && (
                <div role="alert" className="absolute left-4 right-4 top-2 z-10 rounded border border-status-failed/35 bg-status-failed-soft px-3 py-2 font-mono text-xs text-status-failed-fg">
                    {error}
                </div>
            )}
            <div ref={box} className="min-h-0 flex-1 p-2" style={{ background: THEME.background }} />
        </div>
    );
}
