"use client";

import { useEffect, useRef, useState } from "react";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, CheckCheck, MoreHorizontal } from "lucide-react";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { inboxItems, type NeedsTab } from "@/components/home/home";
import { NeedsYouRows } from "@/components/inbox/needs-you";
import { AssignedCard, UpdateRows } from "@/components/inbox/updates";
import { NextPageFailed, Read } from "@/components/shell/unreachable";
import { ApiError } from "@/lib/api/client";
import { inboxService, type InboxUpdate } from "@/lib/api/inbox.service";
import { needsYouQuery } from "@/lib/queries";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";

// The inbox (board: ReworkInbox, D-88, D-140): what needs you, the updates
// on your work, and what is assigned to you.

type Filter = "all" | "deploys" | "code" | "issues" | "docs" | "servers";
const FILTERS: { id: Filter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "deploys", label: "Deploys" },
  { id: "code", label: "Code" },
  { id: "issues", label: "Issues" },
  { id: "docs", label: "Docs" },
  { id: "servers", label: "Servers" },
];
// Needs you's tabs a filter takes in: the panel's own go with Servers.
const inFilter = (tab: NeedsTab, f: Filter) => f === "all" || tab === f || (f === "servers" && tab === "panel") || (f === "deploys" && tab === "services");

const card = "overflow-hidden rounded-[10px] border border-subtle bg-surface";
const head = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3";
const h2 = "text-[15px] font-semibold leading-[22px] text-fg";
const chip = "rounded-full bg-raised px-1.5 font-mono text-xs leading-[18px] text-fg";
// Read's loading and failed rows sit in a card that has no padding of its own.
const reads = "*:data-read-state:m-4";

export default function InboxPage() {
  useDocumentTitle("Inbox · Toft");
  const qc = useQueryClient();
  const [done, setDone] = useState(false);
  const [filter, setFilter] = useState<Filter>("all");
  const [unread, setUnread] = useState(false);
  const needs = useQuery(needsYouQuery(done));
  const updates = useInfiniteQuery({
    queryKey: ["inbox", "updates", done, unread],
    queryFn: ({ pageParam }) => inboxService.updates({ done, unread }, pageParam || undefined),
    initialPageParam: "",
    getNextPageParam: (last) => last.next || undefined,
  });
  const mine = useQuery({ queryKey: ["inbox", "my-issues"], queryFn: () => inboxService.myIssues() });
  const [now] = useState(() => Date.now());

  const refresh = () => qc.invalidateQueries({ queryKey: ["inbox"] });
  const fail = (err: unknown) => toast.error(err instanceof ApiError ? err.message : "Couldn’t update the inbox");
  const mark = useMutation({
    mutationFn: ({ keys, ids }: { keys: string[]; ids: string[]; said: string }) => inboxService.done(keys, ids, !done),
    onSuccess: (_, { said }) => {
      toast.success(said);
      return refresh();
    },
    onError: fail,
  });
  // Where focus goes once the row marked done has left its list (WCAG 2.4.3).
  const refocus = useRef<{ from: Element | null; heading: string; index: number } | null>(null);
  const markRow = (heading: string, row: { keys: string[]; ids: string[] }, said: string) => {
    const from = document.activeElement;
    const buttons = [...(document.getElementById(heading)?.closest("section")?.querySelectorAll("[data-mark-done]") ?? [])];
    refocus.current = { from, heading, index: from ? buttons.indexOf(from) : -1 };
    mark.mutate({ ...row, said });
  };
  useEffect(() => {
    const to = refocus.current;
    if (!to || to.from?.isConnected) return;
    refocus.current = null;
    if (document.activeElement !== document.body) return; // They've moved on.
    const heading = document.getElementById(to.heading);
    const buttons = heading?.closest("section")?.querySelectorAll<HTMLElement>("[data-mark-done]");
    (buttons?.[Math.min(to.index, buttons.length - 1)] ?? heading)?.focus();
  }, [needs.data, updates.data]);
  const read = useMutation({ mutationFn: (ids: string[]) => inboxService.read(ids), onSuccess: refresh, onError: fail });
  const readAll = useMutation({ mutationFn: () => inboxService.read([], true), onSuccess: refresh, onError: fail });

  const items = inboxItems(needs.data ?? []);
  const all: InboxUpdate[] = updates.data?.pages.flatMap((p) => p.data) ?? [];
  const shownItems = items.filter((i) => inFilter(i.tab, filter));
  const shownUpdates = all.filter((u) => filter === "all" || u.category === filter);
  const count = (f: Filter) => (needs.data && updates.data ? items.filter((i) => inFilter(i.tab, f)).length + all.filter((u) => f === "all" || u.category === f).length : "—");
  const unreadCount = all.filter((u) => !u.read).length;

  return (
    <div className="flex w-full max-w-[1600px] flex-col gap-5 pb-14 max-sm:gap-4 md:-mt-3">
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 max-md:flex-col">
            <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">Inbox</h1>
            <p className="min-h-5 text-[13px] text-muted">
              {needs.data && updates.data && !done
                ? `${items.length} need${items.length === 1 ? "s" : ""} you · ${unreadCount} unread update${unreadCount === 1 ? "" : "s"}`
                : done
                  ? "What you marked done: Needs you while it still holds, up to 7 days for what’s put off, updates for 30 days"
                  : null}
            </p>
          </div>
          <div className="flex items-center gap-2 max-md:w-full">
            <span role="group" aria-label="Show" className="inline-flex rounded-lg border border-subtle bg-surface p-[3px] max-md:flex-1">
              {[false, true].map((d) => (
                <button
                  key={String(d)}
                  type="button"
                  aria-pressed={done === d}
                  onClick={() => setDone(d)}
                  className={cn("touch-target h-[30px] rounded-md px-2.5 text-[13px] font-medium max-md:flex-1", done === d ? "bg-raised text-fg" : "text-fg/70 hover:text-fg")}
                >
                  {d ? "Done" : "Inbox"}
                </button>
              ))}
            </span>
            {!done && (
              <>
                <button
                  type="button"
                  onClick={() => readAll.mutate()}
                  disabled={readAll.isPending || unreadCount === 0}
                  className="touch-target inline-flex h-9 items-center gap-1.5 rounded-lg border border-strong bg-raised px-3 text-[13px] font-medium text-fg disabled:opacity-50 max-md:hidden"
                >
                  <CheckCheck className="h-[15px] w-[15px]" strokeWidth={2} aria-hidden="true" />
                  Mark all as read
                </button>
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <button
                      type="button"
                      aria-label="More actions: mark all as read"
                      className="inline-flex h-11 w-11 flex-none items-center justify-center rounded-lg border border-subtle bg-raised text-muted hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus md:hidden"
                    >
                      <MoreHorizontal className="h-[18px] w-[18px]" aria-hidden="true" />
                    </button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem disabled={readAll.isPending || unreadCount === 0} onSelect={() => readAll.mutate()}>
                      Mark all as read
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </>
            )}
          </div>
        </div>
        <div role="group" aria-label="Filter by kind" className="flex gap-1 overflow-x-auto border-b border-subtle">
          {FILTERS.map((f) => (
            <button
              key={f.id}
              type="button"
              aria-pressed={filter === f.id}
              onClick={() => setFilter(f.id)}
              className={cn(
                "touch-target -mb-px inline-flex h-[38px] items-center gap-1.5 whitespace-nowrap border-b-2 px-3 text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus",
                filter === f.id ? "border-link text-fg" : "border-transparent text-fg/70 hover:text-fg",
              )}
            >
              {f.label}
              <span className="font-mono text-xs text-muted">{count(f.id)}</span>
            </button>
          ))}
        </div>
      </div>

      <div className="grid grid-cols-[minmax(0,1fr)] items-start gap-6 max-sm:gap-4 lg:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)]">
        <div className="flex min-w-0 flex-col gap-5 max-sm:gap-4">
          {!unread && (
            <section aria-labelledby="needs-h" className={card}>
              <div className={head}>
                <div className="flex items-center gap-2">
                  <h2 id="needs-h" tabIndex={-1} className={h2}>Needs you</h2>
                  <span className={chip}>{needs.data ? shownItems.length : "—"}</span>
                </div>
                <span className="text-xs text-muted">{done ? "Still true, but marked done" : "Stays until it’s handled. Marking done puts off what’s still true for 7 days."}</span>
              </div>
              <div className={reads}>
                <Read
                  query={needs}
                  what="what needs you"
                  isEmpty={() => shownItems.length === 0}
                  empty={
                    <p className="flex items-center gap-2 px-4 py-4 text-[13px] text-muted">
                      <Check className="h-4 w-4 text-status-running" aria-hidden="true" />
                      {done ? "Nothing here." : "Nothing needs you right now."}
                    </p>
                  }
                >
                  {() => (
                    <NeedsYouRows
                      items={shownItems}
                      now={now}
                      done={done}
                      onDone={(key) => markRow("needs-h", { keys: [key], ids: [] }, done ? "Moved back to the inbox" : shownItems.find((i) => i.key === key)?.snoozes ? "Snoozed for 7 days" : "Marked done")}
                    />
                  )}
                </Read>
              </div>
            </section>
          )}
          <section aria-labelledby="upd-h" className={card}>
            <div className={head}>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <h2 id="upd-h" tabIndex={-1} className={h2}>Updates</h2>
                <span className={chip}>{updates.data ? shownUpdates.length : "—"}</span>
                <span className="text-xs text-muted max-sm:basis-full">on work you opened, are assigned, commented on or were mentioned in</span>
              </div>
              {!done && (
                <label className="touch-target inline-flex min-h-[30px] items-center gap-2 text-[13px] text-fg/80">
                  <input type="checkbox" checked={unread} onChange={(e) => setUnread(e.target.checked)} className="h-4 w-4 accent-[var(--accent)]" />
                  Unread only
                </label>
              )}
            </div>
            <div className={reads}>
              <Read
                query={updates}
                what="your updates"
                isEmpty={() => shownUpdates.length === 0}
                empty={
                  <p className="px-4 py-4 text-[13px] text-fg/70">
                    No updates{unread ? " unread" : ""}{done ? " marked done" : ""}.{!unread && !done && " Merges, status changes, comments and mentions on your work show here."}
                  </p>
                }
              >
                {() => (
                  <UpdateRows
                    updates={shownUpdates}
                    now={now}
                    done={done}
                    onRead={(id) => read.mutate([id])}
                    onDone={(id) => markRow("upd-h", { keys: [], ids: [id] }, done ? "Moved back to the inbox" : "Marked done")}
                  />
                )}
              </Read>
            </div>
            <NextPageFailed query={updates} what="older updates" className="mx-4 mb-2" />
            {updates.hasNextPage && (
              <button type="button" onClick={() => updates.fetchNextPage()} className="mx-4 mb-3 h-8 rounded-md px-2 text-[13px] text-fg/70 hover:bg-raised hover:text-fg">
                Older updates
              </button>
            )}
          </section>
        </div>
        <aside className="flex min-w-0 flex-col gap-5">
          <AssignedCard query={mine} />
        </aside>
      </div>
    </div>
  );
}
