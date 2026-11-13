"use client";

import { useState } from "react";
import Link from "next/link";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Code2, GitBranch, Tag } from "lucide-react";
import type { Page } from "@/lib/api/client";
import type { GitBranch as Branch, GitTag, PullRequest } from "@/lib/api/code.service";
import { DefaultChip, IssueChip, PullChip, useOpenPullsByHead } from "./parts";
import { AgentChip } from "@/components/agent-person";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { card, h1, outline } from "@/app/projects/_lib/ui";
import { ago, codeHref, commitHref, shortSha, useCode } from "./repo";
import { NextPageFailed, ReadFailed } from "@/components/shell/unreachable";

const browse = "inline-flex h-[30px] w-[30px] items-center justify-center rounded-md border border-strong text-muted hover:text-fg";

/** Branches or Tags (board CodeBranches, list=branches|tags), 50 a page. */
export function RefsList({ kind }: { kind: "branches" | "tags" }) {
  const { repo, api, base, detail } = useCode();
  const pulls = useOpenPullsByHead();
  const [now] = useState(() => Date.now());
  const list = useInfiniteQuery({
    queryKey: ["code-" + kind, api.id],
    queryFn: ({ pageParam }): Promise<Page<(Branch | GitTag)[]>> => (kind === "branches" ? api.branches(pageParam) : api.tags(pageParam)),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next,
  });
  const title = kind === "branches" ? "Branches" : "Tags";
  useDocumentTitle(`${title} · ${repo?.name ?? "Code"} · Toft`);
  const items = list.data?.pages.flatMap((p) => p.data) ?? [];

  return (
    <>
      <h1 className={h1}>{title}</h1>
      {!list.data && list.error ? (
        <ReadFailed heading="h2" error={list.error} onRetry={() => void list.refetch()} />
      ) : !list.data ? (
        <div role="status" aria-label="Loading" className="h-60 rounded-[10px] bg-surface motion-safe:animate-pulse" />
      ) : items.length === 0 ? (
        <p className={cn(card, "p-5 text-[13px] text-fg/70")}>
          {kind === "branches" ? "No branches yet. The first push creates them." : "No tags yet. Push one with git push --tags."}
        </p>
      ) : (
        <ul aria-label={title} className={cn(card, "text-sm")}>
          {items.map((x, i) => (
            <li key={x.name} className={cn("flex flex-wrap items-center gap-x-4 gap-y-2.5 px-4 py-3", i > 0 && "border-t border-subtle")}>
              {"default" in x ? (
                <BranchRow b={x} base={base} now={now} protect={detail.protect_default_branch} pull={pulls.get(x.name)} agent={detail.agent_branches?.[x.name]} />
              ) : (
                <TagRow t={x} base={base} now={now} />
              )}
            </li>
          ))}
        </ul>
      )}
      <NextPageFailed query={list} what={`more ${kind}`} />
      {list.hasNextPage && (
        <div className="flex justify-center">
          <button type="button" onClick={() => list.fetchNextPage()} disabled={list.isFetchingNextPage} className={outline}>
            {list.isFetchingNextPage ? "Loading…" : `More ${kind}`}
          </button>
        </div>
      )}
    </>
  );
}

function BranchRow({ b, base, now, protect, pull, agent }: { b: Branch; base: string; now: number; protect: boolean; pull?: PullRequest; agent?: string }) {
  const width = (n: number) => `${Math.min(100, n * 12)}%`;
  return (
    <>
      <div className="flex min-w-0 flex-[1_1_340px] flex-col gap-1">
        <span className="flex flex-wrap items-center gap-2">
          <GitBranch className="h-3.5 w-3.5 flex-none text-muted" aria-hidden="true" />
          <Link href={codeHref(base, "", b.name)} className="break-all font-mono text-[13px] text-fg hover:underline">
            {b.name}
          </Link>
          {b.default && <DefaultChip protect={protect} />}
          {/* An agent's branch: only that agent pushes to it (D-102). */}
          {agent && <AgentChip label={agent} className="h-5 text-xs" />}
          {pull ? <PullChip base={base} pull={pull} /> : b.issues?.map((i) => <IssueChip key={i.key} base={base} issue={i} />)}
        </span>
        <span className="truncate pl-[22px] text-xs text-muted">
          <span className="text-fg/80" title={b.commit.author_email}>
            {b.commit.author_name}
          </span>{" "}
          ·{" "}
          <Link href={commitHref(base, b.commit.sha)} className="hover:text-fg hover:underline">
            {b.commit.subject}
          </Link>{" "}
          · {ago(b.commit.committed_at, now)}
          {agent && " · pushes from this agent only"}
        </span>
      </div>
      {!b.default && b.ahead !== null && b.behind !== null && (
        <span aria-label={`${b.ahead} commits ahead of the default branch, ${b.behind} behind`} className="inline-flex w-[132px] flex-col gap-1">
          <span aria-hidden="true" className="flex justify-between font-mono text-[11px] text-fg/70">
            <span>{b.behind} behind</span>
            <span>{b.ahead} ahead</span>
          </span>
          <span aria-hidden="true" className="flex h-1">
            <span className="flex flex-1 justify-end border-r border-strong">
              <span className="rounded-l-sm bg-muted/70" style={{ width: width(b.behind) }} />
            </span>
            <span className="flex flex-1">
              <span className="rounded-r-sm bg-link" style={{ width: width(b.ahead) }} />
            </span>
          </span>
        </span>
      )}
      <span className="inline-flex gap-1.5">
        <Link href={codeHref(base, "commits", b.name)} className="inline-flex h-[30px] items-center rounded-md border border-strong px-2.5 text-xs text-fg hover:bg-raised">
          Commits
        </Link>
        <Link href={codeHref(base, "", b.name)} aria-label={`Browse files on ${b.name}`} className={browse}>
          <Code2 className="h-3.5 w-3.5" aria-hidden="true" />
        </Link>
      </span>
    </>
  );
}

function TagRow({ t, base, now }: { t: GitTag; base: string; now: number }) {
  return (
    <>
      <div className="flex min-w-0 flex-[1_1_340px] flex-col gap-1">
        <span className="flex flex-wrap items-center gap-2">
          <Tag className="h-3.5 w-3.5 flex-none text-muted" aria-hidden="true" />
          <Link href={codeHref(base, "", t.name)} className="break-all font-mono text-[13px] text-fg hover:underline">
            {t.name}
          </Link>
        </span>
        <span className="truncate pl-[22px] text-xs text-muted">
          {t.message} · {ago(t.created_at, now)}
        </span>
      </div>
      <Link href={commitHref(base, t.sha)} className="font-mono text-xs text-fg/70 hover:text-fg hover:underline">
        {shortSha(t.sha)}
      </Link>
      <Link href={codeHref(base, "", t.name)} aria-label={`Browse files at ${t.name}`} className={browse}>
        <Code2 className="h-3.5 w-3.5" aria-hidden="true" />
      </Link>
    </>
  );
}
