"use client";

import { useEffect } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { useCanWriteIssues } from "@/components/issues/issue-detail";
import { firstPage } from "@/components/docs/body";
import { docTreeQuery } from "@/lib/api/doc.service";
import { Read } from "@/components/shell/unreachable";

// The Docs tab opens the tree's first page, or says how to start.
export default function DocsIndex() {
  const projectId = useParams<{ id: string }>().id;
  const router = useRouter();
  const canWrite = useCanWriteIssues();
  const pages = useQuery(docTreeQuery(projectId));
  const first = pages.data ? firstPage(pages.data) : undefined;
  useEffect(() => {
    if (first) router.replace(`/projects/${projectId}/docs/${first.id}`);
  }, [first, projectId, router]);
  const loading = <div role="status" aria-label="Loading the pages" className="h-40 w-full rounded-[10px] bg-surface motion-safe:animate-pulse" />;
  return (
    <div className="flex flex-1 flex-col items-start gap-3 px-4 py-10 md:px-10">
      <Read
        query={pages}
        what="the pages"
        heading="h1"
        loading={loading}
        isEmpty={(p) => !firstPage(p)}
        empty={
          <>
            <h1 className="text-xl font-semibold text-fg">No pages yet</h1>
            <p className="max-w-prose text-sm text-muted">
              Write how this project works: runbooks with commands readers run in its services, decisions, onboarding. A page that shows a
              service warns its readers when the service changes after the page was last verified.
            </p>
            {canWrite && (
              <Link href={`/projects/${projectId}/docs/new`} className="touch-target inline-flex h-9 items-center rounded-lg bg-accent px-3.5 text-sm font-medium text-on-accent">
                Write the first page
              </Link>
            )}
          </>
        }
      >
        {() => loading}
      </Read>
    </div>
  );
}
