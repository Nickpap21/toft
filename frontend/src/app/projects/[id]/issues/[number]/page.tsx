"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { issueService } from "@/lib/api/issue.service";
import { notFound } from "@/lib/api/client";
import { Read } from "@/components/shell/unreachable";
import { projectQuery } from "@/lib/queries";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { IssueActions, IssueBody, IssueTitle, StatusMenu } from "@/components/issues/issue-detail";
import { BlockedChip, TypeIcon } from "@/components/issues/deep";
import { issueHref } from "@/components/issues/meta";
import { card, h2, outline } from "@/app/projects/_lib/ui";

// One issue on a page of its own (the panel's ↗; D-81, D-83; no board:
// built from IssueList's parts): the issue's body, children, relations,
// linked work and activity, its properties beside.
export default function IssuePage() {
  const { id: projectId, number } = useParams<{ id: string; number: string }>();
  const n = Number(number);
  const { data: project } = useQuery(projectQuery(projectId));
  const issue = useQuery({ queryKey: ["issue", projectId, n], queryFn: () => issueService(projectId).get(n), enabled: n > 0 });
  useDocumentTitle(`${issue.data ? `${issue.data.key} ${issue.data.title}` : "Issue"} · Toft`);

  if (notFound(issue.error) || !(n > 0)) {
    return (
      <section className={cn(card, "flex max-w-[640px] flex-col gap-2 p-5")}>
        <h1 className={h2}>Issue not found</h1>
        <p className="text-[13px] text-fg/70">It isn&apos;t one of this project&apos;s issues.</p>
        <Link href={`/projects/${projectId}/issues`} className={cn(outline, "self-start")}>
          All issues
        </Link>
      </section>
    );
  }
  if (!issue.data) return <Read query={issue} what="this issue" heading="h1" loading={<div role="status" aria-label="Loading" className="h-96 rounded-[10px] bg-surface motion-safe:animate-pulse" />}>{() => null}</Read>;
  const d = issue.data;
  return (
    <article aria-labelledby={`issue-${n}-h`} className="flex w-full max-w-[1180px] flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2 text-[13px]">
        <Link href={`/projects/${projectId}/issues`} className="text-muted hover:text-fg">
          Issues
        </Link>
        <span aria-hidden="true" className="text-muted/70">
          /
        </span>
        {d.parent && (
          <>
            <Link href={issueHref(projectId, Number(d.parent.key.split("-").pop()))} className="max-w-[260px] truncate text-muted hover:text-fg" title={d.parent.title}>
              <span className="font-mono text-xs">{d.parent.key}</span> {d.parent.title}
            </Link>
            <span aria-hidden="true" className="text-muted/70">
              /
            </span>
          </>
        )}
        <TypeIcon type={d.type} size={16} />
        <span className="font-mono text-xs text-fg/80">{d.key}</span>
        <span className="flex-1" />
        <StatusMenu issue={d} projectId={projectId} />
        <IssueActions issue={d} projectId={projectId} />
      </div>
      <div className="flex flex-col gap-2">
        <IssueTitle issue={d} projectId={projectId} page />
        {d.blocked_by.length > 0 && (
          <span>
            <BlockedChip keys={d.blocked_by} />
          </span>
        )}
      </div>
      <IssueBody issue={d} projectId={projectId} project={project?.name ?? ""} page />
    </article>
  );
}
