"use client";

import { useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { ChevronDown, GitBranch } from "lucide-react";
import type { Pipeline } from "@/lib/api/code.service";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { CheckIcon, CheckLine, ReportChecksCard } from "@/components/code/checks";
import { ago, codeHref, commitHref, pullHref, shortSha, useCode } from "@/components/code/repo";
import { card, h1, outline } from "@/app/projects/_lib/ui";
import { LoadFailed, NextPageFailed, readFailed } from "@/components/shell/unreachable";

// Pipelines (board CodePipelines, D-77): every push and merge, newest first,
// with the checks of the commit it moved the branch to: the deploys it
// started and what CI reported. Kept 90 days.
export default function PipelinesPage() {
  const { repo, api, base, detail } = useCode();
  const router = useRouter();
  const branch = useSearchParams().get("branch") ?? "";
  const [now] = useState(() => Date.now());
  useDocumentTitle(`Pipelines · ${repo.name} · Toft`);
  const pipelines = useInfiniteQuery({
    queryKey: ["pipelines", api.id, branch],
    queryFn: ({ pageParam }) => api.pipelines(branch, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next,
    refetchInterval: (q) => (q.state.data?.pages[0]?.data.some(running) ? 5000 : false),
  });
  const branches = useQuery({ queryKey: ["code-branches-all", api.id], queryFn: () => api.branches(undefined, 200), enabled: !detail.empty });
  const all = pipelines.data?.pages.flatMap((p) => p.data) ?? [];

  return (
    <>
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="flex flex-col gap-1">
          <h1 className={h1}>Pipelines</h1>
          <p className="text-[13px] text-fg/70">What each push and merge did: the deploys it started and what CI reported on its commit. Kept 90 days.</p>
        </div>
        <DropdownMenu>
          <DropdownMenuTrigger
            aria-label={`Branch: ${branch || "all branches"}. Change`}
            className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-strong bg-ground px-2.5 text-[13px] text-fg hover:bg-raised"
          >
            <GitBranch className="h-3.5 w-3.5" aria-hidden="true" />
            <span className={cn(branch && "font-mono text-xs")}>{branch || "All branches"}</span>
            <ChevronDown className="h-3.5 w-3.5 text-muted" aria-hidden="true" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="max-h-[360px] w-[260px] overflow-y-auto">
            <DropdownMenuItem onSelect={() => router.push(codeHref(base, "pipelines"))}>All branches</DropdownMenuItem>
            {branches.data?.data.map((b) => (
              <DropdownMenuItem key={b.name} onSelect={() => router.push(`${codeHref(base, "pipelines")}?branch=${encodeURIComponent(b.name)}`)} className="font-mono text-xs">
                {b.name}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <div className="flex flex-wrap items-start gap-6">
        <div className="flex min-w-0 flex-[999_1_600px] flex-col gap-3">
          {readFailed(pipelines) ? (
            <div className={cn(card, "p-5")}>
              <LoadFailed what="the pipelines" onRetry={() => void pipelines.refetch()} error={pipelines.error} />
            </div>
          ) : pipelines.isLoading ? (
            <div role="status" aria-label="Loading" className="h-72 rounded-[10px] bg-surface motion-safe:animate-pulse" />
          ) : all.length === 0 ? (
            <section className="flex flex-col gap-2 rounded-[10px] border border-dashed border-strong bg-surface/60 px-6 py-8">
              <h2 className="text-[15px] font-semibold leading-[22px] text-fg">{branch ? `No pushes to ${branch} yet` : "No pushes yet"}</h2>
              <p className="max-w-[560px] text-[13px] leading-[19px] text-fg/70">
                A push to a branch, or a merge, shows here with the deploys it started and the checks CI reported on its commit.
                {!(detail.deploys ?? []).length && ` Nothing builds from ${repo.name} yet: set it as a service’s source to deploy on push.`}
              </p>
            </section>
          ) : (
            <>
              <ol className={card}>
                {all.map((p, i) => (
                  <PipelineRow key={p.id} p={p} now={now} first={i === 0} />
                ))}
              </ol>
              <NextPageFailed query={pipelines} what="older pipelines" />
              {pipelines.hasNextPage && (
                <div className="flex justify-center">
                  <button type="button" onClick={() => pipelines.fetchNextPage()} disabled={pipelines.isFetchingNextPage} className={outline}>
                    {pipelines.isFetchingNextPage ? "Loading…" : "Older"}
                  </button>
                </div>
              )}
            </>
          )}
        </div>
        <aside className="flex min-w-0 flex-[1_1_320px] flex-col gap-4">
          <ReportChecksCard />
        </aside>
      </div>
    </>
  );
}

const running = (p: Pipeline) => !p.dispatched_at || p.checks.summary?.state === "pending";

function PipelineRow({ p, now, first }: { p: Pipeline; now: number; first: boolean }) {
  const { base } = useCode();
  const state = !p.dispatched_at ? "pending" : p.checks.summary ? (p.checks.summary.state === "pending" ? "running" : p.checks.summary.state) : null;
  const who = p.actor?.name ?? (p.kind === "mirror" ? "The mirror" : "Someone");
  const verb =
    p.kind === "mirror" ? (
      /^0+$/.test(p.before) ? (
        "pulled a new branch,"
      ) : p.forced ? (
        "pulled a rewrite of"
      ) : (
        `pulled ${p.commits === 1 ? "1 commit" : `${p.commits} commits`} into`
      )
    ) : p.kind === "merge" ? (
      <>
        merged{" "}
        {p.pull_number && (
          <Link href={pullHref(base, p.pull_number)} className="inline-link">
            !{p.pull_number}
          </Link>
        )}{" "}
        into
      </>
    ) : p.forced ? (
      "force-pushed"
    ) : /^0+$/.test(p.before) ? (
      "pushed a new branch,"
    ) : (
      `pushed ${p.commits === 1 ? "1 commit" : `${p.commits} commits`} to`
    );
  return (
    <li className={cn("flex flex-wrap gap-x-4 gap-y-2.5 px-4 py-3.5", !first && "border-t border-subtle")}>
      <div className="flex min-w-0 flex-[1_1_340px] gap-3">
        <span className={cn("mt-px flex h-[22px] w-[22px] flex-none items-center justify-center rounded-full", state ? stateBg[state] : "bg-raised")}>
          {state ? <CheckIcon state={state} className="h-3 w-3" /> : <span role="img" aria-label="No checks" className="h-1 w-1 rounded-full bg-muted" />}
        </span>
        <div className="flex min-w-0 flex-col gap-[3px]">
          <div className="flex flex-wrap items-baseline gap-2">
            <Link href={commitHref(base, p.after)} className="font-medium text-fg [overflow-wrap:anywhere] hover:underline">
              {p.subject || shortSha(p.after)}
            </Link>
            <span className="font-mono text-xs text-muted">{shortSha(p.after)}</span>
          </div>
          <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted">
            <span>
              <span className="text-fg/80 [overflow-wrap:anywhere]">{who}</span> {verb}
            </span>
            <code className="break-all rounded-sm bg-raised px-1.5 font-mono text-[11px] text-fg/80">{p.branch}</code>
            <span>· {ago(p.created_at, now)}</span>
          </div>
        </div>
      </div>
      <div className="flex min-w-0 flex-[1_1_360px] flex-col gap-1.5">
        {!p.dispatched_at ? (
          <span className="text-xs text-muted">Starting its deploys…</span>
        ) : p.checks.checks.length === 0 ? (
          <span className="text-xs text-muted">{p.error ? "" : "Nothing builds from this branch, and CI reported nothing."}</span>
        ) : (
          p.checks.checks.map((c) => <CheckLine key={c.name} c={c} now={now} compact />)
        )}
        {p.error && <span className={cn("text-xs [overflow-wrap:anywhere]", p.error.startsWith("superseded") || p.error.startsWith("skipped") ? "text-muted" : "text-status-failed-fg")}>{errorText(p.error)}</span>}
      </div>
    </li>
  );
}

const stateBg = {
  success: "bg-status-running-soft",
  failure: "bg-status-failed-soft",
  running: "bg-status-building-soft",
  pending: "bg-raised",
} as const;

/** The dispatcher's note, said for people. */
function errorText(e: string): string {
  if (e.startsWith("superseded")) return "A newer push to this branch deployed instead.";
  if (e.startsWith("skipped")) return "Not deployed: the panel got to it over an hour later.";
  return e.charAt(0).toUpperCase() + e.slice(1);
}
