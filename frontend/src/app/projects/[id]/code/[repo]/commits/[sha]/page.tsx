"use client";

import { useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Code2 } from "lucide-react";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { CheckLine, summaryText, useChecks } from "@/components/code/checks";
import { Avatar, CopyButton } from "@/components/code/parts";
import { ago, codeHref, commitHref, errorCode, shortSha, useCode } from "@/components/code/repo";
import { DiffFile } from "@/components/code/views";
import { card, outline } from "@/app/projects/_lib/ui";
import { LoadFailed, NextPageFailed, readFailed } from "@/components/shell/unreachable";


// A commit (board CodeCommit): its message, author, parents and SHA, then
// each file it changed against its first parent, unified, 20 files a page.
export default function CommitPage() {
  const { sha } = useParams<{ sha: string }>();
  const { api, base } = useCode();
  const [now] = useState(() => Date.now());
  const commit = useQuery({ queryKey: ["code-commit", api.id, sha], queryFn: () => api.commit(sha), retry: false });
  const diffs = useInfiniteQuery({
    queryKey: ["code-diff", api.id, sha],
    queryFn: ({ pageParam }) => api.diff(sha, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next,
    enabled: !!commit.data,
  });
  const c = commit.data;
  useDocumentTitle(`${c?.subject ?? shortSha(sha)} · Toft`);

  if (commit.error) {
    const code = errorCode(commit.error);
    return (
      <section role="alert" data-read-state="failed" className={cn(card, "flex flex-col gap-2 p-5")}>
        <h1 className="text-[15px] font-semibold text-fg">{code === "ref_not_found" || code === "bad_ref" ? `No commit ${sha} in this repository` : "Couldn’t read the commit"}</h1>
        <Link href={codeHref(base, "commits")} className="text-[13px] text-link hover:underline">
          All commits
        </Link>
      </section>
    );
  }
  if (!c) return <div role="status" aria-label="Loading" className="h-72 rounded-[10px] bg-surface motion-safe:animate-pulse" />;
  const files = diffs.data?.pages.flatMap((p) => p.data) ?? [];

  return (
    <>
      <header className={cn(card, "flex flex-col gap-3 px-5 py-[18px]")}>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <h1 className="min-w-0 flex-[1_1_420px] text-xl font-semibold leading-7 tracking-[-0.01em] text-fg [overflow-wrap:anywhere]">{c.subject}</h1>
          <Link href={codeHref(base, "", c.sha)} className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-strong px-3 text-[13px] text-fg hover:bg-raised">
            <Code2 className="h-3.5 w-3.5" aria-hidden="true" />
            Browse files
          </Link>
        </div>
        {c.body && <pre className="whitespace-pre-wrap font-mono text-[13px] leading-5 text-fg/80 [overflow-wrap:anywhere]">{c.body}</pre>}
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-subtle pt-3 text-[13px] text-fg/70">
          <span className="flex items-center gap-2">
            <Avatar name={c.author_name} />
            <span>
              <span className="font-medium text-fg [overflow-wrap:anywhere]" title={c.author_email}>
                {c.author_name}
              </span>{" "}
              committed {ago(c.committed_at, now)}
            </span>
          </span>
          <span className="flex-1" />
          <span>
            {c.parents.length === 0 ? (
              "No parent"
            ) : (
              <>
                {c.parents.length === 1 ? "1 parent" : `${c.parents.length} parents`}{" "}
                {c.parents.map((p, i) => (
                  <span key={p}>
                    {i > 0 && " + "}
                    <Link href={commitHref(base, p)} className="font-mono text-xs inline-link">
                      {shortSha(p)}
                    </Link>
                  </span>
                ))}
              </>
            )}
          </span>
          <span className="flex min-w-0 items-center gap-1.5">
            commit
            <code className="min-w-0 truncate font-mono text-xs text-fg">{c.sha}</code>
            <CopyButton text={c.sha} label="Copy the full SHA" className="flex h-[26px] w-[26px] flex-none items-center justify-center rounded-md border border-strong text-muted hover:text-fg" />
          </span>
        </div>
      </header>

      <CommitChecks sha={c.sha} now={now} />

      <p className="flex flex-wrap items-center gap-2 text-[13px] text-fg/70">
        <span>
          <span className="font-medium text-fg">
            {c.stats.files} {c.stats.files === 1 ? "file" : "files"}
          </span>{" "}
          changed{c.parents.length > 1 && " against the first parent"}
        </span>
        <span className="font-mono text-xs text-status-running-fg">+{c.stats.additions}</span>
        <span className="font-mono text-xs text-status-failed-fg">−{c.stats.deletions}</span>
      </p>

      {files.map((f) => (
        <DiffFile
          key={(f.old_path ?? "") + f.path}
          f={f}
          view={f.status === "deleted" ? (c.parents[0] ? codeHref(base, "blob", c.parents[0], f.path) : "") : codeHref(base, "blob", c.sha, f.path)}
        />
      ))}
      {diffs.isLoading && <div role="status" aria-label="Loading" className="h-40 rounded-[10px] bg-surface motion-safe:animate-pulse" />}
      {readFailed(diffs) && <LoadFailed what="the changed files" onRetry={() => void diffs.refetch()} error={diffs.error} />}
      <NextPageFailed query={diffs} what="more files" />
      {diffs.hasNextPage && (
        <div className="flex justify-center">
          <button type="button" onClick={() => diffs.fetchNextPage()} disabled={diffs.isFetchingNextPage} className={outline}>
            {diffs.isFetchingNextPage ? "Loading…" : "More files"}
          </button>
        </div>
      )}
      {c.files_truncated && !diffs.hasNextPage && (
        <p className="text-xs text-muted">This commit changes more than 3,000 files; the first 3,000 are listed. Clone the repository to see the rest.</p>
      )}
    </>
  );
}

/** The commit's checks (D-77): its deploys and what CI reported. */
function CommitChecks({ sha, now }: { sha: string; now: number }) {
  const checks = useChecks(sha);
  const cs = checks.data?.checks ?? [];
  if (readFailed(checks)) return <LoadFailed what="this commit’s checks" onRetry={() => void checks.refetch()} error={checks.error} />;
  if (!cs.length) return null;
  return (
    <section aria-labelledby="checks-h" className={card}>
      <h2 id="checks-h" className="flex items-center gap-2 border-b border-subtle px-4 py-2.5 text-[13px] font-medium text-fg">
        Checks <span className="font-normal text-muted">{checks.data?.summary && summaryText(checks.data.summary)}</span>
      </h2>
      <div className="flex flex-col gap-2 px-4 py-3">
        {cs.map((c) => (
          <CheckLine key={c.name} c={c} now={now} compact />
        ))}
      </div>
    </section>
  );
}
