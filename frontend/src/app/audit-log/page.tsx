"use client";

import { Suspense, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { AlertTriangle, X } from "lucide-react";
import { useAuth } from "@/context/AuthContext";
import {
  orgService,
  type AuditEntry,
  type AuditLogFilter,
} from "@/lib/api/org.service";
import {
  agentOutcome,
  auditAction,
  auditDetail,
  auditHref,
  auditTargetName,
} from "@/lib/audit";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn, formatDateTime, initials } from "@/lib/utils";
import { card, fieldInput, smallButton } from "@/components/settings/settings-frame";
import {
  ACTION_FILTERS,
  PERIODS,
  WHO,
  emptyLine,
  fromLink,
  periodSince,
  whoParams,
  type Period,
  type Who,
} from "./audit-log-view";

const PAGE = 50;

const OUTCOME = {
  neutral: "bg-raised text-fg/70",
  green: "bg-status-running-soft text-status-running-fg",
  amber: "bg-status-pending-soft text-status-pending-fg",
  red: "bg-status-failed-soft text-status-failed-fg",
  grey: "bg-raised text-muted",
};

const input = "h-8 text-[13px] max-sm:h-11";
// Three on a phone's row: the padding and 12 px keep "People and agents" whole.
const select = cn(input, "flex-[0_1_180px] px-2.5 max-sm:min-w-0 max-sm:pl-2 max-sm:pr-0.5 max-sm:text-xs");
const th = "border-b border-subtle px-4 py-2 text-left text-xs font-medium leading-5 text-muted";
const td = "border-t border-subtle px-4 py-2.5 align-top max-sm:block max-sm:border-0 max-sm:p-0";
const note = "px-4 py-5 max-sm:block";

/** A day's heading: "Today", "Yesterday", or "Sat 11 Oct". */
function dayOf(
  iso: string,
  now: Date,
): { key: string; label: string; date: string } {
  const d = new Date(iso);
  const key = formatDateTime(iso).slice(0, 10);
  const date = d
    .toLocaleDateString("en-GB", {
      weekday: "short",
      day: "numeric",
      month: "short",
    })
    .replace(",", "");
  const days = Math.round(
    (new Date(now.toDateString()).getTime() -
      new Date(d.toDateString()).getTime()) /
      86_400_000,
  );
  if (days === 0) return { key, label: "Today", date };
  if (days === 1) return { key, label: "Yesterday", date };
  return { key, label: date, date: "" };
}

/** The Audit log (board: ReworkAuditLog): people, agents and API keys in
 *  one list, newest first, by day, 50 at a time, with search and filters. */
export default function AuditLogPage() {
  return (
    <Suspense>
      <AuditLog />
    </Suspense>
  );
}

function AuditLog() {
  const params = useSearchParams();
  const { user } = useAuth();
  const workspace = user?.orgName ?? "";
  useDocumentTitle(
    ["Audit log", workspace, "Toft"].filter(Boolean).join(" · "),
  );
  const [q, setQ] = useState("");
  const [search, setSearch] = useState("");
  // ?agent= and ?actor= come from links (Settings → Agents has ?agent=any).
  const linked = fromLink(params.get("agent") ?? "");
  const [who, setWho] = useState<Who>(linked.who);
  const [onlyAgent, setOnlyAgent] = useState(linked.agent);
  const [onlyActor, setOnlyActor] = useState(() => params.get("actor") ?? "");
  const [action, setAction] = useState("");
  const [period, setPeriod] = useState<Period>("7");
  const [now] = useState(() => new Date());

  // Search as the person types, a request per pause.
  useEffect(() => {
    const t = setTimeout(() => setSearch(q.trim()), 300);
    return () => clearTimeout(t);
  }, [q]);

  const filter = useMemo<AuditLogFilter>(() => {
    const w = whoParams(who);
    return {
      q: search || undefined,
      actor: onlyActor || undefined,
      agent: onlyAgent || w.agent,
      api_key: w.api_key,
      actions: ACTION_FILTERS.find((a) => a.label === action)?.actions,
      since: periodSince(period, now),
      limit: PAGE,
    };
  }, [search, onlyActor, onlyAgent, who, action, period, now]);
  const count = useQuery({
    queryKey: ["audit-log-count", filter],
    queryFn: () => orgService.countAuditLog(filter),
  });
  const log = useInfiniteQuery({
    queryKey: ["audit-log-page", filter],
    queryFn: ({ pageParam }) =>
      orgService.listAuditLogPage({ ...filter, cursor: pageParam ?? undefined }),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next ?? null,
  });
  const entries = useMemo(() => log.data?.pages.flatMap((p) => p.data) ?? [], [log.data]);
  const days = useMemo(() => {
    const out: {
      key: string;
      label: string;
      date: string;
      rows: AuditEntry[];
    }[] = [];
    for (const e of entries) {
      const d = dayOf(e.created_at, now);
      const last = out[out.length - 1];
      if (last?.key === d.key) last.rows.push(e);
      else out.push({ ...d, rows: [e] });
    }
    return out;
  }, [entries, now]);
  const failure = log.error instanceof Error ? log.error.message : "the panel did not answer";
  const filtered = !!(search || who || action || onlyAgent || onlyActor);
  const periodPhrase = PERIODS.find((p) => p.value === period)!.phrase;
  const whoPhrase = WHO.find((w) => w.value === who)!.phrase;
  // A link's filter leaves the address with its chip, so a reload doesn't bring it back.
  const writeLink = (agent: string, actor: string) => {
    const qs = new URLSearchParams();
    if (agent) qs.set("agent", agent);
    if (actor) qs.set("actor", actor);
    const s = qs.toString();
    window.history.replaceState(null, "", s ? `?${s}` : window.location.pathname);
  };
  const dropAgent = () => {
    setOnlyAgent("");
    writeLink("", onlyActor);
  };
  const dropActor = () => {
    setOnlyActor("");
    writeLink(onlyAgent, "");
  };
  const clear = () => {
    setQ("");
    setSearch("");
    setWho("");
    setAction("");
    setOnlyAgent("");
    setOnlyActor("");
    writeLink("", "");
  };
  const chips = [
    onlyAgent && {
      label: `Only ${entries.find((e) => e.agent_id === onlyAgent)?.agent_name ?? "this agent"}`,
      remove: dropAgent,
    },
    onlyActor && {
      label: `Only ${entries.find((e) => e.actor_id === onlyActor)?.actor ?? "this person"}`,
      remove: dropActor,
    },
  ].filter((c) => !!c);

  return (
    <div className="flex w-full max-w-[1600px] flex-col gap-5 pb-14 max-sm:gap-4 md:-mt-3">
      <div className="flex min-w-0 flex-wrap items-center gap-x-3 max-sm:flex-col max-sm:items-start max-sm:gap-0.5">
        <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">
          Audit log
        </h1>
        <span className="text-[13px] text-muted" aria-live="polite">
          {count.data === undefined
            ? count.isError
              ? "Count unavailable"
              : "Counting…"
            : `${count.data} ${count.data === 1 ? "entry" : "entries"} ${periodPhrase} · ${whoPhrase}`}
        </span>
      </div>

      <section aria-labelledby="au-h" className={card}>
        <h2 id="au-h" className="sr-only">
          Entries
        </h2>
        <form
          role="search"
          aria-label="Filter the audit log"
          className="flex flex-wrap items-center gap-2 border-b border-subtle px-4 py-3"
          onSubmit={(e) => e.preventDefault()}
        >
          <input
            type="search"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Person, project, service or server"
            aria-label="Search the audit log"
            className={cn(fieldInput, input, "max-w-[320px] flex-[1_1_220px] max-sm:max-w-none max-sm:basis-full")}
          />
          <select
            aria-label="Who"
            value={who}
            onChange={(e) => {
              setWho(e.target.value as Who);
              dropAgent();
            }}
            className={cn(fieldInput, select, "max-sm:flex-[1.55_1_0]")}
          >
            {WHO.map((w) => (
              <option key={w.value} value={w.value}>
                {w.label}
              </option>
            ))}
          </select>
          <select
            aria-label="Action"
            value={action}
            onChange={(e) => setAction(e.target.value)}
            className={cn(fieldInput, select, "max-sm:flex-1")}
          >
            <option value="">All actions</option>
            {ACTION_FILTERS.map((a) => (
              <option key={a.label} value={a.label}>
                {a.label}
              </option>
            ))}
          </select>
          <select
            aria-label="Period"
            value={period}
            onChange={(e) => setPeriod(e.target.value as Period)}
            className={cn(fieldInput, select, "max-sm:flex-[1.05_1_0]")}
          >
            {PERIODS.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label}
              </option>
            ))}
          </select>
          {chips.length > 0 && (
            <div className="flex flex-wrap gap-2 max-sm:basis-full">
              {chips.map((c) => (
                <button
                  key={c.label}
                  type="button"
                  onClick={c.remove}
                  aria-label={`Remove the filter: ${c.label}`}
                  className={smallButton}
                >
                  {c.label}
                  <X className="h-3 w-3 text-muted" aria-hidden="true" />
                </button>
              ))}
            </div>
          )}
        </form>

        <table
          aria-label={`Audit log of ${workspace}`}
          className="w-full border-collapse text-[13px] max-sm:block sm:table-fixed"
        >
          <thead className="max-sm:hidden">
            <tr>
              <th scope="col" className={cn(th, "w-[76px]")}>
                Time
              </th>
              <th scope="col" className={cn(th, "w-[18%]")}>
                Who
              </th>
              <th scope="col" className={cn(th, "w-[24%]")}>
                Did
              </th>
              <th scope="col" className={cn(th, "w-[18%]")}>
                On
              </th>
              <th scope="col" className={th}>
                Details
              </th>
            </tr>
          </thead>
          {log.isLoading ? (
            <tbody className="max-sm:block">
              <tr className="max-sm:block">
                <td colSpan={5} className={note}>
                  <span
                    role="status"
                    className="flex flex-col gap-2.5 motion-safe:animate-pulse"
                    aria-label="Loading the audit log"
                  >
                    <span className="block h-2.5 w-[70%] rounded bg-raised" />
                    <span className="block h-2.5 w-[50%] rounded bg-raised" />
                    <span className="block h-2.5 w-[60%] rounded bg-raised" />
                  </span>
                </td>
              </tr>
            </tbody>
          ) : log.error && !log.isFetchNextPageError && entries.length === 0 ? (
            <tbody className="max-sm:block">
              <tr className="max-sm:block">
                <td colSpan={5} role="alert" className={note}>
                  <span className="flex flex-wrap items-center justify-between gap-3">
                    <span className="flex items-center gap-2.5 text-status-failed-fg">
                      <AlertTriangle
                        className="h-4 w-4 flex-none"
                        aria-hidden="true"
                      />
                      Could not load the audit log: {failure}. The entries are
                      not lost.
                    </span>
                    <button
                      type="button"
                      onClick={() => log.refetch()}
                      className={smallButton}
                    >
                      Retry
                    </button>
                  </span>
                </td>
              </tr>
            </tbody>
          ) : entries.length === 0 ? (
            <tbody className="max-sm:block">
              <tr className="max-sm:block">
                <td colSpan={5} className={note}>
                  <span className="flex flex-wrap items-center justify-between gap-3">
                    <span className="text-fg/70">
                      {emptyLine({
                        hasMore: log.hasNextPage,
                        search,
                        filtered,
                        phrase: periodPhrase,
                      })}
                    </span>
                    {filtered && (
                      <button
                        type="button"
                        onClick={clear}
                        className={smallButton}
                      >
                        Clear filters
                      </button>
                    )}
                  </span>
                </td>
              </tr>
            </tbody>
          ) : (
            days.map((d, di) => (
              <tbody key={d.key} className="max-sm:block">
                <tr className="max-sm:block">
                  <th
                    scope="rowgroup"
                    colSpan={5}
                    className={cn(
                      "px-4 pb-1.5 pt-4 text-left text-[13px] font-semibold text-fg max-sm:block max-sm:pb-1",
                      di > 0 && "border-t border-subtle",
                    )}
                  >
                    {d.label}
                    {d.date && (
                      <span className="ml-2 text-xs font-normal text-muted">
                        {d.date}
                      </span>
                    )}
                  </th>
                </tr>
                {d.rows.map((e, i) => (
                  <Row key={e.id} e={e} workspace={workspace} first={i === 0} />
                ))}
              </tbody>
            ))
          )}
        </table>

        {(entries.length > 0 || log.hasNextPage) && (
          <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
            <span>
              Times in your time zone. An agent’s row says who it acted for and
              which rule decided; a secret’s value is never recorded.
            </span>
            {log.hasNextPage && (
              <span className="flex flex-wrap items-center gap-x-3 gap-y-2">
                {log.isFetchNextPageError && !log.isFetchingNextPage && (
                  <span role="alert" className="flex items-center gap-2 text-status-failed-fg">
                    <AlertTriangle className="h-4 w-4 flex-none" aria-hidden="true" />
                    Could not load older entries: {failure}.
                  </span>
                )}
                <button
                  type="button"
                  disabled={log.isFetchingNextPage}
                  onClick={() => log.fetchNextPage()}
                  className={cn(smallButton, "font-medium")}
                >
                  {log.isFetchingNextPage ? "Loading…" : log.isFetchNextPageError ? "Retry" : "Older entries"}
                </button>
              </span>
            )}
          </div>
        )}
      </section>
    </div>
  );
}

function Row({
  e,
  workspace,
  first,
}: {
  e: AuditEntry;
  workspace: string;
  first: boolean;
}) {
  const a = auditAction(e.action);
  // An agent's call: the tool is the action, with its outcome (D-100).
  const call = e.action === "agent.call";
  const outcome = call ? agentOutcome(e) : undefined;
  const detail = auditDetail(e, workspace);
  const hasDetail = !!(detail.text || detail.code || detail.after || detail.also);
  const href = auditHref(e);
  const name = auditTargetName(e);
  const cell = cn(td, first && "border-t-0");
  return (
    <tr
      className={cn(
        "max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1 max-sm:px-4 max-sm:py-3",
        !first && "max-sm:border-t max-sm:border-subtle",
      )}
    >
      <td
        className={cn(
          cell,
          "font-mono text-xs leading-5 text-fg/70 max-sm:col-start-2 max-sm:row-start-1 max-sm:text-right",
        )}
      >
        <time dateTime={e.created_at} title={formatDateTime(e.created_at)}>
          {formatDateTime(e.created_at).slice(11)}
        </time>
      </td>
      <td className={cn(cell, "[overflow-wrap:anywhere] max-sm:col-start-1 max-sm:row-start-1")}>
        {e.agent_id ? (
          <span className="flex items-start gap-2" title={e.agent_id}>
            <span
              aria-hidden="true"
              className="flex h-[22px] w-[22px] flex-none items-center justify-center rounded-md border border-dashed border-strong text-[10px] font-semibold text-fg"
            >
              {initials(e.agent_name ?? "")}
            </span>
            <span className="min-w-0">
              <span className="block font-medium text-fg">
                {e.agent_name}
              </span>
              <span className="block text-xs leading-[18px] text-muted" title={e.actor_email}>
                for {e.actor}
              </span>
            </span>
          </span>
        ) : (
          <span className="flex items-start gap-2" title={e.actor_email}>
            <span
              aria-hidden="true"
              className="flex h-[22px] w-[22px] flex-none items-center justify-center rounded-full bg-raised text-[10px] font-semibold text-fg"
            >
              {initials(e.actor)}
            </span>
            <span className="min-w-0">
              <span className="block font-medium text-fg">
                {e.actor}
              </span>
              {e.api_key_id && (
                <span className="block text-xs leading-[18px] text-muted" title={e.api_key_id}>
                  {e.api_key_name ? `with key ${e.api_key_name}` : "with a revoked key"}
                </span>
              )}
            </span>
          </span>
        )}
      </td>
      <td className={cn(cell, "text-fg max-sm:col-span-full")}>
        {outcome ? (
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span className="font-mono text-xs">
              {String(e.detail?.tool ?? "")}
            </span>
            <span
              className={cn(
                "inline-flex h-5 items-center whitespace-nowrap rounded-full px-[7px] text-[11px] font-medium",
                OUTCOME[outcome.tone],
                // Refused and Allowed are both neutral; a refusal keeps its line.
                e.detail?.outcome === "refused" && "text-fg/85 ring-1 ring-inset ring-subtle",
              )}
            >
              {outcome.label}
            </span>
          </span>
        ) : (
          a.label
        )}
      </td>
      <td className={cn(cell, "[overflow-wrap:anywhere] max-sm:col-span-full")}>
        {href ? (
          <Link
            href={href}
            title={e.target.id}
            className="text-fg underline decoration-strong underline-offset-[3px] hover:decoration-fg pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center"
          >
            {name}
          </Link>
        ) : (
          <span title={e.target.id} className="text-fg/70">
            {name}
            {e.target.deleted && name !== `a deleted ${e.target.kind}` && (
              <span className="text-muted"> (deleted)</span>
            )}
          </span>
        )}
      </td>
      <td
        className={cn(
          cell,
          "text-fg/85 [overflow-wrap:anywhere] max-sm:col-span-full",
          !hasDetail && "max-sm:hidden",
        )}
      >
        {detail.text}{" "}
        {detail.code && (
          <span className="font-mono text-xs text-fg/85">{detail.code}</span>
        )}{" "}
        {detail.after}
        {detail.also && <span className="mt-1 block text-fg">{detail.also}</span>}
      </td>
    </tr>
  );
}
