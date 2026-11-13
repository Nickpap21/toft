"use client";

import { useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useInfiniteQuery } from "@tanstack/react-query";
import { GitCommitHorizontal, GitPullRequest, MessageSquare } from "lucide-react";
import type { PullRequest } from "@/lib/api/code.service";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { RepoPicker } from "@/components/code/parts";
import { stateIcon, useCanWrite } from "@/components/code/pulls";
import { ago, codeHref, pullHref, useCode } from "@/components/code/repo";
import { card, outline, primary } from "@/app/projects/_lib/ui";
import { NextPageFailed, ReadFailed } from "@/components/shell/unreachable";
import { byline } from "./[number]/view";

type State = "open" | "merged" | "closed";

// Pull requests (board PullRequests): open, merged or closed, newest first,
// with how far each open one is ahead and whether it conflicts.
export default function PullsPage() {
  const { api, detail, base, repo } = useCode();
  const sp = useSearchParams();
  const router = useRouter();
  const state = (["open", "merged", "closed"].includes(sp.get("state") ?? "") ? sp.get("state") : "open") as State;
  const canWrite = useCanWrite();
  const [now] = useState(() => Date.now());
  const pulls = useInfiniteQuery({
    queryKey: ["code-pulls-page", api.id, state],
    queryFn: ({ pageParam }) => api.pulls(state, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next,
  });
  useDocumentTitle(`Pull requests · ${repo.name} · Toft`);
  const all = pulls.data?.pages.flatMap((p) => p.data) ?? [];
  const counts = detail.pulls;
  const pressed = (on: boolean) => cn("inline-flex h-7 items-center gap-1.5 rounded-md px-2.5 text-[13px] font-medium", on ? "bg-raised text-fg" : "text-fg/70 hover:text-fg");

  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-3.5">
          <RepoPicker />
          <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">Pull requests</h1>
          <span role="group" aria-label="Show" className="inline-flex gap-0.5 rounded-lg border border-subtle bg-ground p-0.5">
            {(["open", "merged", "closed"] as State[]).map((s) => (
              <button key={s} type="button" aria-pressed={state === s} onClick={() => router.replace(`${codeHref(base, "pulls")}${s === "open" ? "" : `?state=${s}`}`)} className={pressed(state === s)}>
                {s[0].toUpperCase() + s.slice(1)} <span className="font-mono text-[11px] text-muted">{counts[s]}</span>
              </button>
            ))}
          </span>
        </div>
        {canWrite && !detail.empty && (
          <Link href={codeHref(base, "pulls/new")} className={primary}>
            <GitPullRequest className="h-[15px] w-[15px]" aria-hidden="true" />
            New pull request
          </Link>
        )}
      </div>

      {!pulls.data && pulls.error ? (
        <ReadFailed heading="h2" error={pulls.error} onRetry={() => void pulls.refetch()} />
      ) : !pulls.data ? (
        <div role="status" aria-label="Loading" className="h-48 rounded-[10px] bg-surface motion-safe:animate-pulse" />
      ) : all.length === 0 ? (
        <section aria-labelledby="empty-h" className={cn(card, "flex flex-col items-start gap-2.5 p-6")}>
          <h2 id="empty-h" className="text-[15px] font-semibold text-fg">
            No {state} pull requests
          </h2>
          {state === "open" && (
            <>
              <p className="max-w-[620px] text-[13px] text-fg/70">
                Push a branch, then open a pull request to merge it into {detail.default_branch}. Toft shows its commits and changes, tells you when it conflicts,
                and merges it in one step.
              </p>
              <pre className="mt-1 rounded-lg border border-subtle bg-ground px-3 py-2.5 font-mono text-xs leading-[19px] text-fg">
                {`git switch -c my-change\ngit push -u toft my-change`}
              </pre>
            </>
          )}
        </section>
      ) : (
        <ul aria-label={`${state} pull requests`} className={cn(card, "text-sm")}>
          {all.map((p, i) => (
            <PullRow key={p.number} p={p} base={base} now={now} first={i === 0} />
          ))}
        </ul>
      )}
      <NextPageFailed query={pulls} what="more pull requests" />
      {pulls.hasNextPage && (
        <div className="flex justify-center">
          <button type="button" onClick={() => pulls.fetchNextPage()} disabled={pulls.isFetchingNextPage} className={outline}>
            {pulls.isFetchingNextPage ? "Loading…" : "More pull requests"}
          </button>
        </div>
      )}
    </>
  );
}

function PullRow({ p, base, now, first }: { p: PullRequest; base: string; now: number; first: boolean }) {
  const s = stateIcon(p.state);
  const tint = p.state === "open" ? "text-status-running-fg" : p.state === "merged" ? "text-env-violet-fg" : "text-status-failed-fg";
  const when =
    p.state === "merged" && p.merged_at
      ? `merged by ${p.merged_by?.name ?? "someone"} ${ago(p.merged_at, now)}`
      : p.state === "closed" && p.closed_at
        ? `closed ${ago(p.closed_at, now)}`
        : `opened ${ago(p.created_at, now)}`;
  return (
    <li className={cn("flex flex-wrap items-start gap-3 px-4 py-[13px]", !first && "border-t border-subtle")}>
      <s.icon className={cn("mt-0.5 h-4 w-4 flex-none", tint)} aria-label={s.label} />
      <div className="flex min-w-0 flex-[1_1_380px] flex-col gap-1">
        <span className="flex flex-wrap items-baseline gap-2">
          <Link href={pullHref(base, p.number)} className="font-medium text-fg hover:underline">
            {p.title}
          </Link>
          <span className="font-mono text-xs text-muted">!{p.number}</span>
          {p.conflicts && <span className="inline-flex h-5 items-center rounded-sm bg-status-pending-soft px-[7px] text-[11px] font-medium text-status-pending-fg">Conflicts</span>}
        </span>
        <span className="flex flex-wrap items-center gap-1.5 text-xs text-muted">
          <span>
            <span className="text-fg/80 [overflow-wrap:anywhere]">{byline(p.author, "a deleted account")}</span> {when}
          </span>
          <span aria-hidden="true">·</span>
          <span className="break-all font-mono text-fg/70">{p.head_branch}</span>
          <span aria-label="into">→</span>
          <span className="break-all font-mono text-fg/70">{p.base_branch}</span>
        </span>
      </div>
      <span className="inline-flex items-center gap-3.5 pt-0.5 text-xs text-muted">
        {p.ahead !== null && (
          <span aria-label={`${p.ahead} commits`} className="inline-flex items-center gap-[5px]">
            <GitCommitHorizontal className="h-[13px] w-[13px]" aria-hidden="true" />
            <span className="font-mono">{p.ahead}</span>
          </span>
        )}
        <span aria-label={`${p.comments} comments`} className="inline-flex min-w-[30px] items-center gap-[5px]">
          <MessageSquare className="h-[13px] w-[13px]" aria-hidden="true" />
          <span className="font-mono">{p.comments}</span>
        </span>
      </span>
    </li>
  );
}
