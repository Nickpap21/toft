"use client";

import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ApiError } from "@/lib/api/client";
import { watchdogQuery, watchdogService, type Watchdog } from "@/lib/api/instance.service";
import { TONE_CLASSES, type StatusTone } from "@/lib/status";
import { sinceShort } from "@/lib/time";
import { cn, formatDateTime } from "@/lib/utils";
import { card, fieldInput, fieldLabel, smallButton } from "@/components/settings/settings-frame";

/** The watchdog's chip and the Overview tile's words (D-68). */
export function watchdogState(w: Watchdog, now: number): { chip: string; tone: StatusTone; value: string; note: string } {
  if (!w.url) return { chip: "Off", tone: "pending", value: "Off", note: "Nobody hears if this server goes down" };
  if (w.held) return { chip: "Held back", tone: "pending", value: "Held back", note: "The panel isn’t healthy, so it isn’t pinging" };
  if (w.last_error) return { chip: "Failing", tone: "failed", value: "Failing", note: w.last_error };
  if (!w.last_ping_at) return { chip: "Waiting", tone: "neutral", value: "Waiting", note: "The first ping is within 5 minutes" };
  return { chip: "Pinging", tone: "running", value: sinceShort(w.last_ping_at, now), note: "Last ping; one every 5 minutes" };
}

/** The watchdog (board InstanceOverview): a URL outside the panel that it
 *  pings while healthy, and the uptime check to point a monitor at. */
export function WatchdogCard({ watchdog, now }: { watchdog: Watchdog | undefined; now: number }) {
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const url = draft ?? watchdog?.url ?? "";
  const done = (w: Watchdog) => {
    queryClient.setQueryData(watchdogQuery.queryKey, w);
    setDraft(null);
  };
  const save = useMutation({ mutationFn: () => watchdogService.set(url), onSuccess: done });
  const test = useMutation({ mutationFn: watchdogService.test, onSuccess: done });
  const health = typeof window === "undefined" ? "/api/v1/health" : `${window.location.origin}/api/v1/health`;
  const state = watchdog ? watchdogState(watchdog, now) : null;
  const error = save.error ?? test.error;
  const code = error instanceof ApiError ? (error.body as { data?: { code?: string } })?.data?.code : undefined;

  return (
    <section aria-labelledby="wd-h" className={card}>
      <div className="flex flex-wrap items-center gap-2.5 border-b border-subtle px-4 py-3">
        <h2 id="wd-h" className="text-[15px] font-semibold leading-[22px] text-fg">
          Watchdog
        </h2>
        {state && (
          <span className={cn("inline-flex h-[22px] items-center gap-1.5 rounded-full px-2 text-xs font-medium", TONE_CLASSES[state.tone].chip)}>
            <span aria-hidden="true" className={cn("h-1.5 w-1.5 rounded-full", TONE_CLASSES[state.tone].dot)} />
            {state.chip}
          </span>
        )}
      </div>
      <div className="flex flex-col gap-3.5 px-4 pb-4 pt-3.5">
        <p className="max-w-[860px] text-[13px] leading-[19px] text-fg/85">
          If this server goes down, the panel can’t tell you. A watchdog outside it can: Toft pings the URL below every 5 minutes while
          the panel is healthy, and the watchdog alerts you when the pings stop.
        </p>
        <form
          className="flex flex-wrap items-end gap-2.5"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <div className="flex min-w-0 flex-[1_1_420px] flex-col gap-1.5">
            <label htmlFor="wd-url" className={fieldLabel}>
              Ping URL
            </label>
            <input
              id="wd-url"
              type="url"
              inputMode="url"
              placeholder="https://hc-ping.com/…"
              value={url}
              onChange={(e) => setDraft(e.target.value)}
              aria-invalid={code === "invalid_url"}
              aria-describedby="wd-note"
              className={cn(fieldInput, "font-mono text-xs")}
            />
          </div>
          <button type="submit" disabled={save.isPending || draft === null} className={cn(smallButton, "h-9 px-3 text-[13px] font-medium rounded-lg")}>
            {save.isPending ? "Saving…" : url || !watchdog?.url ? "Save" : "Turn off"}
          </button>
          <button
            type="button"
            onClick={() => test.mutate()}
            disabled={test.isPending || !watchdog?.url || draft !== null}
            className={cn(smallButton, "h-9 px-3 text-[13px] font-medium rounded-lg")}
          >
            {test.isPending ? "Pinging…" : "Send a test ping"}
          </button>
        </form>
        <p id="wd-note" role={error ? "alert" : "status"} className={cn("text-xs leading-[17px]", error ? "text-status-failed-fg" : "text-muted")}>
          {error
            ? error.message.charAt(0).toUpperCase() + error.message.slice(1)
            : watchdog?.url && watchdog.last_ping_at
              ? `Last ping ${formatDateTime(watchdog.last_ping_at).slice(11, 16)}${watchdog.last_status ? `, answered ${watchdog.last_status}` : ""}${watchdog.last_error ? `: ${watchdog.last_error}` : ""}. Nothing is sent while the panel is unhealthy, so the watchdog notices that too.`
              : "Works with healthchecks.io, Better Stack heartbeats, Uptime Kuma push monitors, or any https:// URL that takes a GET. Private addresses are refused: the watchdog has to be outside this server."}
        </p>
        <div className="flex flex-wrap items-center gap-x-3.5 gap-y-2 border-t border-subtle pt-3.5">
          <span className="min-w-0 flex-[1_1_420px] text-[13px] leading-[19px] text-fg/85">
            Also point an uptime monitor at the panel’s health check. It answers 200 while the panel and its database are up.
          </span>
          <span className="flex max-w-full items-center gap-2 rounded-lg border border-subtle bg-ground py-1.5 pl-3 pr-1.5">
            <code className="truncate font-mono text-xs text-fg">{health}</code>
            <button
              type="button"
              onClick={() => {
                void navigator.clipboard?.writeText(health).then(() => setCopied(true));
              }}
              className={cn(smallButton, "h-[26px] px-2")}
            >
              {copied ? "Copied" : "Copy"}
            </button>
          </span>
        </div>
      </div>
    </section>
  );
}
