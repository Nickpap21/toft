"use client";

import { useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Code2, GitCommitHorizontal, X } from "lucide-react";
import type { GitCommit } from "@/lib/api/code.service";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { SummaryIcon } from "@/components/code/checks";
import { Avatar, CopyButton, RefPicker } from "@/components/code/parts";
import { ago, codeHref, commitHref, errorCode, shortSha, useCode } from "@/components/code/repo";
import { card, h1, outline } from "@/app/projects/_lib/ui";
import { NextPageFailed } from "@/components/shell/unreachable";

// Commits (board CodeCommits): newest first, by day, from a branch, tag or
// commit, optionally only those touching a path; 50 a page.
export default function CommitsPage() {
  const sp = useSearchParams();
  const ref = sp.get("ref") ?? "";
  const path = sp.get("path") ?? "";
  const { repo, api, detail, base } = useCode();
  const [now] = useState(() => Date.now());
  const commits = useInfiniteQuery({
    queryKey: ["code-commits", api.id, ref, path],
    queryFn: ({ pageParam }) => api.commits(ref, path, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next,
    enabled: !detail.empty,
    retry: false,
  });
  useDocumentTitle(`Commits · ${repo?.name ?? "Code"} · Toft`);

  const refName = ref || detail.default_branch;
  const all = commits.data?.pages.flatMap((p) => p.data) ?? [];
  const days = byDay(all);
  const code = errorCode(commits.error);

  return (
    <>
      <div className="flex flex-wrap items-center gap-3.5">
        <h1 className={h1}>Commits</h1>
        {!detail.empty && <RefPicker api={api} current={refName} hrefFor={(r) => codeHref(base, "commits", r, path)} />}
        {path && (
          <span className="inline-flex h-7 items-center gap-1.5 rounded-full border border-subtle bg-surface pl-2.5 pr-1 text-xs text-fg/70">
            Touching <span className="font-mono text-fg">{path}</span>
            <Link href={codeHref(base, "commits", ref)} aria-label="Show every commit" className="inline-flex h-[22px] w-[22px] items-center justify-center rounded-full hover:bg-raised hover:text-fg">
              <X className="h-3 w-3" aria-hidden="true" />
            </Link>
          </span>
        )}
      </div>

      {detail.empty ? (
        <p className={cn(card, "p-5 text-[13px] text-fg/70")}>No commits yet. The first push brings them.</p>
      ) : !commits.data && commits.error ? (
        <section role="alert" data-read-state="failed" className={cn(card, "flex flex-col gap-2 p-5")}>
          <h2 className="text-[15px] font-semibold text-fg">{code === "ref_not_found" ? `No branch, tag or commit called ${ref}` : "Couldn’t read the commits"}</h2>
          <Link href={codeHref(base, "commits")} className="text-[13px] text-link hover:underline">
            Commits on {detail.default_branch}
          </Link>
        </section>
      ) : !commits.data ? (
        <div role="status" aria-label="Loading" className="h-72 rounded-[10px] bg-surface motion-safe:animate-pulse" />
      ) : all.length === 0 ? (
        <p className={cn(card, "p-5 text-[13px] text-fg/70")}>No commit on {refName} touches {path}.</p>
      ) : (
        <>
          {days.map((d) => (
            <section key={d.label} aria-label={d.label} className="flex flex-col gap-2">
              <h2 className="flex items-center gap-2 text-[13px] font-medium text-fg/70">
                <GitCommitHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
                {d.label}
              </h2>
              <ul className={cn(card, "text-sm")}>
                {d.commits.map((c, i) => (
                  <li key={c.sha} className={cn("flex flex-wrap items-center gap-3 px-4 py-3", i > 0 && "border-t border-subtle")}>
                    <div className="flex min-w-0 flex-[1_1_360px] flex-col gap-[3px]">
                      <Link href={commitHref(base, c.sha)} className="truncate font-medium text-fg hover:underline">
                        {c.subject}
                      </Link>
                      <span className="flex flex-wrap items-center gap-1.5 text-xs text-muted">
                        <Avatar name={c.author_name} size={18} />
                        <span className="text-fg/80" title={c.author_email}>
                          {c.author_name}
                        </span>
                        committed {ago(c.committed_at, now)}
                      </span>
                    </div>
                    <SummaryIcon summary={c.checks} />
                    <span className="inline-flex items-center overflow-hidden rounded-md border border-strong">
                      <Link href={commitHref(base, c.sha)} className="px-2.5 font-mono text-xs leading-7 text-fg hover:bg-raised">
                        {shortSha(c.sha)}
                      </Link>
                      <CopyButton text={c.sha} label="Copy the full SHA" className="flex h-7 w-[30px] items-center justify-center border-l border-strong text-muted hover:text-fg" />
                    </span>
                    <Link
                      href={codeHref(base, "", c.sha)}
                      aria-label={`Browse files at ${shortSha(c.sha)}`}
                      className="inline-flex h-[30px] w-[30px] items-center justify-center rounded-md border border-strong text-muted hover:text-fg"
                    >
                      <Code2 className="h-3.5 w-3.5" aria-hidden="true" />
                    </Link>
                  </li>
                ))}
              </ul>
            </section>
          ))}
          <NextPageFailed query={commits} what="older commits" />
          {commits.hasNextPage && (
            <div className="flex justify-center">
              <button type="button" onClick={() => commits.fetchNextPage()} disabled={commits.isFetchingNextPage} className={outline}>
                {commits.isFetchingNextPage ? "Loading…" : "Older commits"}
              </button>
            </div>
          )}
        </>
      )}
    </>
  );
}

/** Commits grouped by the day they were committed, in the viewer's zone. */
function byDay(commits: GitCommit[]): { label: string; commits: GitCommit[] }[] {
  const days: { label: string; commits: GitCommit[] }[] = [];
  for (const c of commits) {
    const label = `Commits on ${new Date(c.committed_at).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" })}`;
    if (days.at(-1)?.label === label) days.at(-1)!.commits.push(c);
    else days.push({ label, commits: [c] });
  }
  return days;
}
