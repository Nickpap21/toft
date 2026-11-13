"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import type { UseQueryResult } from "@tanstack/react-query";
import { AtSign, Check, CircleDot, GitMerge, GitPullRequestClosed, MessageSquare, Pencil, Undo2, UserPlus, Zap, type LucideIcon } from "lucide-react";
import { STATUS } from "@/components/issues/meta";
import { Read } from "@/components/shell/unreachable";
import type { InboxUpdate } from "@/lib/api/inbox.service";
import type { IssueStatus } from "@/lib/api/issue.service";
import { timeAgo } from "@/lib/time";
import { cn } from "@/lib/utils";

const ICON: Record<InboxUpdate["kind"], { Icon: LucideIcon; tint: string }> = {
  issue_status: { Icon: Zap, tint: "text-status-building" },
  issue_assigned: { Icon: UserPlus, tint: "text-muted" },
  issue_comment: { Icon: MessageSquare, tint: "text-muted" },
  mention: { Icon: AtSign, tint: "text-link" },
  pull_merged: { Icon: GitMerge, tint: "text-link" },
  pull_closed: { Icon: GitPullRequestClosed, tint: "text-muted" },
  pull_comment: { Icon: MessageSquare, tint: "text-muted" },
  page_edited: { Icon: Pencil, tint: "text-muted" },
};

const statusName = (s?: unknown) => (typeof s === "string" && s in STATUS ? STATUS[s as IssueStatus].name : String(s ?? ""));

/** Where an update leads. */
export function updateHref(u: InboxUpdate): string {
  const p = u.project_id ?? "";
  if (u.issue) return `/projects/${p}/issues/${u.issue.number}`;
  if (u.pull) return `/projects/${p}/code/${encodeURIComponent(u.pull.repository)}/pulls/${u.pull.number}`;
  if (u.page) return `/projects/${p}/docs/${u.page.id}`;
  return "/inbox";
}

/** What happened, in words: who, did what, to what. */
export function updateText(u: InboxUpdate): { who: string; text: string; what: string; tail: string } {
  const d = u.detail as Record<string, unknown>;
  // An agent's work names it beside its person (D-102): "Claude Code for admin".
  const person = u.actor?.name ?? "Someone";
  const who = typeof d.agent === "string" && d.agent ? `${d.agent} for ${person}` : person;
  const issue = u.issue ? `${u.issue.key} ${u.issue.title}` : "";
  const pull = u.pull ? `!${u.pull.number} ${u.pull.title}` : "";
  switch (u.kind) {
    case "issue_status":
      return d.automatic
        ? { who: "", text: "", what: issue, tail: ` moved to ${statusName(d.to)} automatically${u.actor ? `, from ${who}’s work` : ""}` }
        : { who, text: "moved", what: issue, tail: ` to ${statusName(d.to)}` };
    case "issue_assigned":
      return { who, text: "assigned you", what: issue, tail: "" };
    case "issue_comment":
      return { who, text: "commented on", what: issue, tail: "" };
    case "mention":
      return { who, text: "mentioned you in", what: issue || pull, tail: "" };
    case "pull_merged":
      return { who, text: "merged", what: pull, tail: "" };
    case "pull_closed":
      return { who, text: "closed", what: pull, tail: "" };
    case "pull_comment":
      return { who, text: "commented on", what: pull, tail: "" };
    case "page_edited":
      return { who, text: "edited", what: u.page?.title ?? "", tail: ", a page you wrote" };
  }
  return { who, text: "", what: "", tail: "" };
}

/** Today, Yesterday, or the date. */
export function dayOf(iso: string, now: number): string {
  const d = new Date(iso);
  const t = new Date(now);
  const day = (x: Date) => Date.UTC(x.getFullYear(), x.getMonth(), x.getDate());
  const diff = Math.round((day(t) - day(d)) / 86_400_000);
  if (diff <= 0) return "Today";
  if (diff === 1) return "Yesterday";
  return d.toLocaleDateString(undefined, { day: "numeric", month: "short" });
}

/** The inbox's updates, by day (board: Inbox). */
export function UpdateRows({ updates, now, onRead, onDone, done }: {
  updates: InboxUpdate[];
  now: number;
  onRead: (id: string) => void;
  onDone: (id: string) => void;
  done: boolean;
}) {
  const router = useRouter();
  const days = updates.map((u) => dayOf(u.created_at, now));
  return (
    <ul className="flex flex-col">
      {updates.map((u, i) => {
        const day = days[i];
        const header = i === 0 || day !== days[i - 1];
        const { Icon, tint } = ICON[u.kind];
        const t = updateText(u);
        const excerpt = typeof (u.detail as Record<string, unknown>).excerpt === "string" ? String((u.detail as Record<string, unknown>).excerpt) : "";
        const href = updateHref(u);
        return (
          <li key={u.id} className="contents">
            {header && <div className={cn("flex min-h-8 items-center bg-raised/40 px-4 text-xs font-semibold text-fg/70", i > 0 && "border-t border-subtle")}>{day}</div>}
            <div className={cn("group flex items-start gap-2.5 py-2 pl-2.5 pr-3 hover:bg-raised/40", !header && "border-t border-subtle")}>
              <span aria-hidden="true" className={cn("mt-[7px] h-1.5 w-1.5 flex-none rounded-full", u.read ? "bg-transparent" : "bg-link")} />
              <Icon aria-hidden="true" className={cn("mt-0.5 h-4 w-4 flex-none", tint)} strokeWidth={1.9} />
              <Link
                href={href}
                onClick={(e) => {
                  if (!u.read) onRead(u.id);
                  if (!e.metaKey && !e.ctrlKey) {
                    e.preventDefault();
                    router.push(href);
                  }
                }}
                className="min-w-0 flex-1 text-[13px] text-fg/80 [overflow-wrap:anywhere]"
              >
                {!u.read && <span className="sr-only">Unread: </span>}
                {t.who && <span className="font-medium text-fg">{t.who} </span>}
                {t.text && <>{t.text} </>}
                <span className="font-medium text-fg hover:underline">{t.what}</span>
                {t.tail}
                {excerpt && <span className="mt-0.5 block line-clamp-2 text-fg/70">“{excerpt}”</span>}
              </Link>
              <span className="hidden flex-none font-mono text-xs text-muted sm:inline">{u.project}</span>
              <time dateTime={u.created_at} className="w-8 flex-none text-right font-mono text-xs text-muted">
                {timeAgo(u.created_at, now)}
              </time>
              <button
                type="button"
                data-mark-done
                aria-label={done ? "Move back to the inbox" : "Mark done"}
                onClick={() => onDone(u.id)}
                className="-my-1 flex h-7 w-7 flex-none items-center justify-center rounded-md text-muted hover:bg-raised hover:text-fg"
              >
                {done ? <Undo2 className="h-3.5 w-3.5" /> : <Check className="h-3.5 w-3.5" strokeWidth={2} />}
              </button>
            </div>
          </li>
        );
      })}
    </ul>
  );
}

/** Assigned to you, by status (board: Inbox). */
export function AssignedCard({ query }: { query: UseQueryResult<{ key: string; number: number; title: string; status: string; priority: string; project_id: string }[]> }) {
  const issues = query.data ?? [];
  const order: IssueStatus[] = ["in_progress", "in_review", "todo", "backlog"];
  return (
    <section aria-labelledby="mine-h" className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
      <div className="flex items-center justify-between gap-2 border-b border-subtle px-4 py-3">
        <div className="flex items-center gap-2">
          <h2 id="mine-h" className="text-[15px] font-semibold leading-[22px] text-fg">
            Assigned to you
          </h2>
          <span className="rounded-full bg-raised px-[7px] font-mono text-xs leading-5 text-fg">{query.data ? issues.length : "—"}</span>
        </div>
        <Link href="/issues/mine" className="text-[13px] text-link hover:underline">
          My issues
        </Link>
      </div>
      <div className="*:data-read-state:m-4">
        <Read query={query} what="your assigned issues" isEmpty={(d) => d.length === 0} empty={<p className="px-4 py-3.5 text-[13px] text-muted">Nothing assigned to you is open.</p>}>
          {() => (
            <div className="flex flex-col pb-2">
              {order.map((st) => {
                const list = issues.filter((i) => i.status === st);
                if (list.length === 0) return null;
                return (
                  <div key={st} className="flex flex-col">
                    <div className={cn("flex items-center gap-1.5 px-4 pb-1 pt-2.5 text-xs", STATUS[st].tint)}>
                      <CircleDot aria-hidden="true" className="h-3 w-3" />
                      {STATUS[st].name} <span className="text-muted">{list.length}</span>
                    </div>
                    {list.slice(0, 6).map((i) => (
                      <Link key={i.key} href={`/projects/${i.project_id}/issues/${i.number}`} className="grid grid-cols-[64px_minmax(0,1fr)] items-baseline gap-3 px-4 py-1.5 text-[13px] leading-[19px] text-fg hover:bg-raised/40 pointer-coarse:min-h-11 pointer-coarse:items-center">
                        <span className="truncate font-mono text-xs text-muted">{i.key}</span>
                        <span className="[overflow-wrap:anywhere]">
                          {i.title}
                          {i.priority === "urgent" && (
                            <span className="ml-1.5 rounded border border-status-pending/40 px-1.5 text-[11px] leading-4 text-status-pending-fg">Urgent</span>
                          )}
                        </span>
                      </Link>
                    ))}
                  </div>
                );
              })}
            </div>
          )}
        </Read>
      </div>
    </section>
  );
}
