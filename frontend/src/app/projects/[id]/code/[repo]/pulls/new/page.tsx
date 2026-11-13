"use client";

import { useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowLeft, Check, GitCommitHorizontal, GitPullRequest, Info } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { Avatar, RefPicker } from "@/components/code/parts";
import { MarkdownField, useCanWrite } from "@/components/code/pulls";
import { ago, codeHref, commitHref, errorCode, pullHref, shortSha, useCode } from "@/components/code/repo";
import { DiffFile } from "@/components/code/views";
import { card, outline, primary } from "@/app/projects/_lib/ui";
import { NextPageFailed } from "@/components/shell/unreachable";

// New pull request (board NewPullRequest): the branch with the changes and
// the branch they go into, what merging would bring (commits and changes),
// then a title and a description.
export default function NewPullPage() {
  const { api, detail, base, repo } = useCode();
  const sp = useSearchParams();
  const router = useRouter();
  const queryClient = useQueryClient();
  const canWrite = useCanWrite();
  const [into, setInto] = useState(sp.get("base") ?? detail.default_branch);
  const [picked, setFrom] = useState(sp.get("head") ?? "");
  const [ownTitle, setTitle] = useState<string | null>(null);
  const [body, setBody] = useState("");
  const [now] = useState(() => Date.now());
  useDocumentTitle(`New pull request · ${repo.name} · Toft`);

  // Without ?head, the most recently committed branch that isn't the base.
  const branches = useQuery({ queryKey: ["code-branches-new", api.id], queryFn: () => api.branches(undefined, 20), enabled: !picked });
  const from = picked || branches.data?.data.find((x) => x.name !== into)?.name || "";

  const ready = !!into && !!from && into !== from;
  const cmp = useQuery({ queryKey: ["code-compare", api.id, into, from], queryFn: () => api.compare(into, from), enabled: ready, retry: false });
  const diff = useInfiniteQuery({
    queryKey: ["code-compare-diff", api.id, into, from],
    queryFn: ({ pageParam }) => api.compareDiff(into, from, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next,
    enabled: !!cmp.data && cmp.data.ahead > 0,
  });
  // The title follows the branch's only commit, or its name, until typed.
  const commits = cmp.data?.commits;
  const suggested = commits?.length === 1 ? commits[0].subject : from.replace(/[-_/]+/g, " ").replace(/^\w/, (x) => x.toUpperCase());
  const title = ownTitle ?? suggested;

  const open = useMutation({
    mutationFn: () => api.openPull({ title: title.trim(), body, base: into, head: from }),
    onSuccess: (p) => {
      queryClient.invalidateQueries({ queryKey: ["repository"] });
      queryClient.invalidateQueries({ queryKey: ["repositories"] });
      router.push(pullHref(base, p.number));
    },
  });
  const existing = errorCode(open.error) === "pull_exists" ? ((open.error as ApiError).body as { data?: { number?: number } })?.data?.number : undefined;

  const status = !ready
    ? { icon: Info, tone: "text-fg/70", text: into === from ? "Pick two different branches." : "Pick the branch with your changes." }
    : cmp.isLoading
      ? { icon: Info, tone: "text-fg/70", text: "Comparing…" }
      : cmp.error
        ? { icon: AlertTriangle, tone: "text-status-pending-fg", text: errorCode(cmp.error) === "unrelated_histories" ? "These branches share no history." : "Couldn’t compare them." }
        : cmp.data!.ahead === 0
          ? { icon: Info, tone: "text-fg/70", text: `${into} already has every commit of ${from}.` }
          : cmp.data!.mergeable
            ? { icon: Check, tone: "text-status-running-fg", text: "No conflicts: it can be merged." }
            : {
                icon: AlertTriangle,
                tone: "text-status-pending-fg",
                text: `Conflicts in ${cmp.data!.conflicts.length === 1 ? "1 file" : `${cmp.data!.conflicts.length} files`}: you can open it and resolve them on the branch.`,
              };
  const canOpen = canWrite && ready && !!cmp.data && cmp.data.ahead > 0 && title.trim() !== "" && !open.isPending;
  const files = diff.data?.pages.flatMap((p) => p.data) ?? [];

  return (
    <>
      <div className="flex flex-col gap-1.5">
        <Link href={codeHref(base, "pulls")} className="inline-flex items-center gap-1.5 self-start text-[13px] text-fg/70 hover:text-fg">
          <ArrowLeft className="h-3.5 w-3.5" aria-hidden="true" />
          Pull requests
        </Link>
        <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">New pull request</h1>
        <p className="text-[13px] text-fg/70">Pick the branch with your changes and the branch they go into. Below is what merging would bring.</p>
      </div>

      <div className={cn(card, "flex flex-wrap items-center gap-2.5 px-3.5 py-3")}>
        <span className="text-xs text-muted">into</span>
        <RefPicker api={api} current={into} onPick={setInto} label="Base branch" branchesOnly />
        <ArrowLeft className="h-4 w-4 text-muted" aria-hidden="true" />
        <span className="text-xs text-muted">from</span>
        <RefPicker api={api} current={from} onPick={setFrom} label="Head branch" branchesOnly />
        <span className="flex-1" />
        <span role="status" className={cn("inline-flex items-center gap-1.5 text-[13px]", status.tone)}>
          <status.icon className="h-[15px] w-[15px]" aria-hidden="true" />
          {status.text}
        </span>
      </div>

      {canWrite && ready && cmp.data && cmp.data.ahead > 0 && (
        <form
          aria-label="Pull request"
          className={cn(card, "flex flex-col gap-3.5 p-4")}
          onSubmit={(e) => {
            e.preventDefault();
            if (canOpen) open.mutate();
          }}
        >
          <label className="flex flex-col gap-1.5 text-[13px] font-medium text-fg">
            Title
            <input
              value={title}
              maxLength={256}
              onChange={(e) => setTitle(e.target.value)}
              className="h-9 rounded-lg border border-strong bg-ground px-2.5 text-sm font-normal text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
            />
          </label>
          <MarkdownField id="pr-body" label="Description" value={body} onChange={setBody} rows={7} placeholder="What changes, and why" onSubmit={() => canOpen && open.mutate()} />
          {open.error && (
            <p role="alert" className="text-[13px] text-status-failed-fg">
              {existing ? (
                <>
                  {open.error.message}:{" "}
                  <Link href={pullHref(base, existing)} className="inline-link">
                    !{existing}
                  </Link>
                </>
              ) : open.error instanceof ApiError ? (
                open.error.message
              ) : (
                "Couldn’t open the pull request"
              )}
            </p>
          )}
          <div className="flex flex-wrap items-center justify-end gap-2.5">
            <Link href={codeHref(base, "pulls")} className={outline}>
              Cancel
            </Link>
            <button type="submit" disabled={!canOpen} className={primary}>
              <GitPullRequest className="h-[15px] w-[15px]" aria-hidden="true" />
              {open.isPending ? "Opening…" : "Open pull request"}
            </button>
          </div>
        </form>
      )}

      {cmp.data && cmp.data.ahead > 0 && (
        <>
          <p className="flex flex-wrap items-center gap-4 text-[13px] text-fg/70">
            <span className="inline-flex items-center gap-1.5">
              <GitCommitHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
              <span className="font-medium text-fg">{cmp.data.ahead === 1 ? "1 commit" : `${cmp.data.ahead} commits`}</span>
            </span>
            <span>
              <span className="font-medium text-fg">{cmp.data.stats.files === 1 ? "1 file" : `${cmp.data.stats.files} files`}</span> changed
            </span>
            <span className="font-mono text-xs text-status-running-fg">+{cmp.data.stats.additions}</span>
            <span className="font-mono text-xs text-status-failed-fg">−{cmp.data.stats.deletions}</span>
          </p>
          <ul aria-label="Commits" className={cn(card, "text-[13px]")}>
            {cmp.data.commits.map((c, i) => (
              <li key={c.sha} className={cn("flex min-h-10 items-center gap-3 px-4", i > 0 && "border-t border-subtle")}>
                <Avatar name={c.author_name} size={20} />
                <Link href={commitHref(base, c.sha)} className="min-w-0 flex-1 truncate text-fg/80 hover:text-fg hover:underline">
                  {c.subject}
                </Link>
                <span className="text-xs text-muted">{ago(c.committed_at, now)}</span>
                <Link href={commitHref(base, c.sha)} className="font-mono text-xs text-fg/70 hover:text-fg">
                  {shortSha(c.sha)}
                </Link>
              </li>
            ))}
          </ul>
          {cmp.data.commits_truncated && <p className="text-xs text-muted">The first 250 commits are listed.</p>}
          {files.map((f) => (
            <DiffFile key={(f.old_path ?? "") + f.path} f={f} view={codeHref(base, "blob", from, f.path)} />
          ))}
          <NextPageFailed query={diff} what="more files" />
          {diff.hasNextPage && (
            <div className="flex justify-center">
              <button type="button" onClick={() => diff.fetchNextPage()} disabled={diff.isFetchingNextPage} className={outline}>
                {diff.isFetchingNextPage ? "Loading…" : "More files"}
              </button>
            </div>
          )}
        </>
      )}
    </>
  );
}
