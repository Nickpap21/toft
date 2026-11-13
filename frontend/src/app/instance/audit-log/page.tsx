"use client";

import { Fragment, useMemo, useState } from "react";
import Link from "next/link";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { useAuth, useIsOperator } from "@/context/AuthContext";
import { instanceService, type InstanceAuditEntry } from "@/lib/api/instance.service";
import { cn, formatDateTime, initials } from "@/lib/utils";
import { CardHead, Segmented, card, fieldInput, help } from "@/components/settings/settings-frame";
import { InstanceFrame } from "@/components/instance/instance-frame";
import { auditWords } from "../accounts/accounts-view";
import { NextPageFailed } from "@/components/shell/unreachable";

type Filter = "all" | "panel" | "accounts";

const FILTERS: [Filter, string][] = [
  ["all", "All"],
  ["panel", "The panel"],
  ["accounts", "Accounts"],
];

/** Instance → Audit log (D-68, board ReworkInstanceLog, D-139): what
 *  operators did to the panel and to accounts, newest first, by day; the
 *  account acted on is named in the sentence, and automatic work is Toft's.
 *  Workspace changes stay in each workspace's own log. */
export default function InstanceAuditLogPage() {
  const { user } = useAuth();
  const isOperator = useIsOperator();
  const [now] = useState(() => Date.now());
  const [filter, setFilter] = useState<Filter>("all");
  const [q, setQ] = useState("");
  const log = useInfiniteQuery({
    queryKey: ["instance", "audit", filter],
    queryFn: ({ pageParam }) => instanceService.listAuditPage(pageParam ?? undefined, filter === "all" ? undefined : filter),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next ?? null,
    enabled: isOperator,
  });
  const entries = useMemo(() => {
    const all = log.data?.pages.flatMap((p) => p.data) ?? [];
    const needle = q.trim().toLowerCase();
    return all.filter((e) => {
      if (!needle) return true;
      const w = auditWords(e);
      return [e.actor?.name ?? "Toft", e.actor?.email, w.label, w.note, e.target?.email]
        .some((s) => s?.toLowerCase().includes(needle));
    });
  }, [log.data, q]);
  const day = (iso: string) => formatDateTime(iso).slice(0, 10);
  const today = day(new Date(now).toISOString());
  const yesterday = day(new Date(now - 86_400_000).toISOString());
  const perDay = new Map<string, number>();
  for (const e of entries) perDay.set(day(e.created_at), (perDay.get(day(e.created_at)) ?? 0) + 1);

  return (
    <InstanceFrame title="Audit log" gap="gap-6">
      <div className="grid items-start gap-6 min-[1280px]:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)]">
        <div className="flex min-w-0 flex-col gap-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <Segmented label="Show" options={FILTERS.map(([value, label]) => ({ value, label }))} value={filter} onChange={setFilter} />
            <div className="relative flex flex-[0_1_240px] items-center max-sm:flex-auto">
              <label htmlFor="log-q" className="sr-only">
                Search loaded entries
              </label>
              <Search className="pointer-events-none absolute left-2.5 h-3.5 w-3.5 text-muted" aria-hidden="true" />
              <input
                id="log-q"
                type="search"
                placeholder="Search loaded entries"
                value={q}
                onChange={(e) => setQ(e.target.value)}
                className={cn(fieldInput, "w-full pl-[30px]")}
              />
            </div>
          </div>

          <section aria-label="Instance audit log" className={cn(card, "overflow-hidden")}>
            {log.isLoading ? (
              <span className="flex flex-col gap-2.5 px-4 py-3.5 motion-safe:animate-pulse" aria-label="Loading">
                <span className="block h-2.5 w-[40%] rounded bg-raised" />
                <span className="block h-2.5 w-[60%] rounded bg-raised" />
              </span>
            ) : log.error ? (
              <p role="alert" className="px-4 py-3.5 text-[13px] text-status-failed-fg">
                Could not load the audit log: {log.error.message}
              </p>
            ) : entries.length === 0 ? (
              <p className="px-4 py-3.5 text-[13px] text-muted">
                {q.trim()
                  ? `Nothing loaded matches “${q.trim()}”.${log.hasNextPage ? " Show older to search further back." : ""}`
                  : log.hasNextPage
                    ? "Nothing loaded yet. Show older to look further back."
                    : filter === "panel"
                      ? "Nothing done to the panel yet. Address and role changes, recovery kit reads, shells and upgrades appear here."
                      : "No changes yet. Grants, revocations, deactivations, deletions and reset links appear here."}
              </p>
            ) : (
              entries.map((e, i) => {
                const d = day(e.created_at);
                const prev = i > 0 ? day(entries[i - 1].created_at) : "";
                return (
                  <Fragment key={e.id}>
                    {d !== prev && (
                      <h2
                        className={cn(
                          "flex items-center gap-2 bg-surface px-4 py-2 text-[13px] font-semibold text-fg",
                          i > 0 && "border-t border-subtle",
                        )}
                      >
                        {d === today ? "Today" : d === yesterday ? "Yesterday" : d}
                        <span className="font-mono text-xs font-normal text-muted">{perDay.get(d)}</span>
                      </h2>
                    )}
                    <Row entry={e} you={(id) => id === user?.id} />
                  </Fragment>
                );
              })
            )}
          </section>
          <NextPageFailed query={log} what="older entries" />
          {log.hasNextPage && (
            <div className="flex justify-center">
              <button
                type="button"
                disabled={log.isFetchingNextPage}
                onClick={() => log.fetchNextPage()}
                className="touch-target h-9 rounded-lg border border-subtle bg-raised px-3.5 text-[13px] font-medium text-fg disabled:opacity-50"
              >
                {log.isFetchingNextPage ? "Loading…" : "Show older"}
              </button>
            </div>
          )}
        </div>

        <aside className="flex min-w-0 flex-col gap-5">
          <section aria-labelledby="ab-h" className={card}>
            <CardHead id="ab-h" title="What’s logged here" />
            <div className="flex flex-col gap-3 px-4 py-3">
              <ul className="list-disc pl-[18px] text-[13px] leading-5 text-fg/85 [&>li+li]:mt-1">
                <li>What operators did to the panel: upgrades, shells, updates, reboots, cleaning, the watchdog</li>
                <li>What they did to accounts: operators granted and revoked, accounts deactivated, reset links</li>
              </ul>
              <p className={help}>
                Changes inside a workspace are in that workspace’s{" "}
                <Link href="/audit-log" className="inline-link">
                  audit log
                </Link>
                .
              </p>
            </div>
          </section>
        </aside>
      </div>
    </InstanceFrame>
  );
}

function Row({ entry: e, you }: { entry: InstanceAuditEntry; you: (id: string) => boolean }) {
  const words = auditWords(e);
  const who = e.actor?.name ?? "Toft";
  return (
    <div className="grid grid-cols-[64px_minmax(0,1fr)_minmax(0,200px)] items-center gap-3 border-t border-subtle px-4 py-2.5 text-[13px] max-sm:grid-cols-[48px_minmax(0,1fr)] max-sm:items-start max-sm:gap-y-1.5">
      <time
        dateTime={e.created_at}
        title={formatDateTime(e.created_at)}
        className="font-mono text-xs text-fg/70 max-sm:row-span-2 max-sm:pt-0.5"
      >
        {formatDateTime(e.created_at).slice(11)}
      </time>
      <span className="flex min-w-0 flex-col">
        <span className="font-medium text-fg [overflow-wrap:anywhere]">{words.label}</span>
        {words.note && <span className={cn(help, "[overflow-wrap:anywhere]")}>{words.note}</span>}
      </span>
      <span className="flex min-w-0 items-center gap-2 text-fg/85 max-sm:col-start-2">
        <span
          aria-hidden="true"
          className="flex h-[22px] w-[22px] flex-none items-center justify-center rounded-full bg-raised text-[10px] font-semibold text-fg"
        >
          {initials(who)}
        </span>
        <span className="min-w-0 truncate" title={e.actor?.email}>
          {who}
          {e.actor && you(e.actor.id) && <span className="text-muted"> (you)</span>}
        </span>
      </span>
    </div>
  );
}
