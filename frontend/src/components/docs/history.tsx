"use client";

import { useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { IssueMarkdown, useCanWriteIssues } from "@/components/issues/issue-detail";
import { docPageQuery, docService, docTreeQuery } from "@/lib/api/doc.service";
import { NextPageFailed, Read } from "@/components/shell/unreachable";
import { projectQuery } from "@/lib/queries";
import { ago } from "@/lib/time";
import { formatDateTime } from "@/lib/utils";
import { cn } from "@/lib/utils";
import { failure } from "./blocks";
import { prepareHTML } from "./body";

/** A page's versions, newest first, one shown as it was; Restore saves it
 *  as the next version (D-86). No board: built from the Docs board's parts. */
export function DocHistory({ projectId, pageId }: { projectId: string; pageId: string }) {
  const router = useRouter();
  const qc = useQueryClient();
  const canWrite = useCanWriteIssues();
  const [now] = useState(() => Date.now());
  const pageRead = useQuery(docPageQuery(projectId, pageId));
  const page = pageRead.data;
  const versions = useInfiniteQuery({
    queryKey: ["docs", projectId, "versions", pageId],
    queryFn: ({ pageParam }) => docService(projectId).versions(pageId, pageParam || undefined),
    initialPageParam: "",
    getNextPageParam: (last) => last.next || undefined,
  });
  const list = versions.data?.pages.flatMap((p) => p.data) ?? [];
  const [chosen, setChosen] = useState<number | null>(null);
  const shown = chosen ?? list[0]?.version ?? 0;
  const versionRead = useQuery({
    queryKey: ["docs", projectId, "version", pageId, shown],
    queryFn: () => docService(projectId).version(pageId, shown),
    enabled: shown > 0,
  });
  const version = versionRead.data;
  const restore = useMutation({
    mutationFn: () => docService(projectId).update(pageId, { title: version!.title, body: version!.body, base_version: page!.version }),
    onSuccess: (p) => {
      qc.setQueryData(docPageQuery(projectId, p.id).queryKey, p);
      qc.invalidateQueries({ queryKey: ["docs", projectId] });
      toast.success(`Version ${shown} restored as version ${p.version}`);
      router.push(`/projects/${projectId}/docs/${p.id}`);
    },
    onError: (err) => toast.error(failure(err, "Couldn’t restore that version")),
  });

  return (
    <div className="flex min-w-0 flex-1 flex-wrap items-start">
      <article className="flex min-w-0 max-w-[800px] flex-[999_1_520px] flex-col gap-[18px] px-4 pb-16 pt-5 md:px-10 md:pt-6">
        <div className="flex flex-col gap-2.5">
          <nav aria-label="Breadcrumb" className="text-[13px] text-muted [overflow-wrap:anywhere]">
            <Link href={`/projects/${projectId}/docs/${pageId}`} className="hover:text-fg hover:underline">
              {page?.title ?? "Page"}
            </Link>{" "}
            / History
          </nav>
          <div className="flex flex-wrap items-center gap-2.5">
            <h1 className="min-w-0 flex-1 text-[28px] font-semibold leading-9 tracking-[-0.015em] text-fg [overflow-wrap:anywhere]">{version?.title ?? page?.title ?? "History"}</h1>
            {canWrite && page && version && shown !== page.version && !page.deleted_at && (
              <button
                type="button"
                onClick={() => restore.mutate()}
                disabled={restore.isPending}
                className="touch-target h-8 rounded-lg border border-strong bg-raised px-3 text-[13px] font-medium text-fg hover:bg-strong/40 disabled:opacity-60"
              >
                Restore version {shown}
              </button>
            )}
          </div>
          {version && (
            <p className="text-[13px] text-muted">
              Version {version.version}
              {version.author ? ` by ${version.author.name}` : ""}, {formatDateTime(version.created_at)}
              {page && shown === page.version && " · the current one"}
            </p>
          )}
        </div>
        {/* Only a failure draws here: the page's title and Restore lean on it. */}
        <Read query={pageRead} what="this page" loading={<></>}>{() => null}</Read>
        {shown > 0 && (
          <Read query={versionRead} what={`version ${shown}`}>
            {(v) => <IssueMarkdown html={prepareHTML(v.html, projectId)} projectId={projectId} className="toft-doc" />}
          </Read>
        )}
      </article>
      <aside aria-label="Versions" className="flex min-w-0 flex-[1_1_240px] flex-col gap-1 px-4 pb-10 pt-2 text-[13px] md:pl-2 md:pr-6 md:pt-7">
        <span className="mb-1 text-xs font-medium text-muted">Versions</span>
        <Read query={versions} what="the versions">
          {() => (
            <ul className="flex flex-col gap-px">
              {list.map((v) => (
                <li key={v.version}>
                  <button
                    type="button"
                    aria-current={v.version === shown ? "true" : undefined}
                    onClick={() => setChosen(v.version)}
                    className={cn("touch-target flex w-full flex-col justify-center rounded-md px-2 py-1.5 text-left hover:bg-raised", v.version === shown && "bg-accent-soft")}
                  >
                    <span className="text-fg">
                      Version <span className="font-mono text-xs">{v.version}</span>
                      {page && v.version === page.version && <span className="text-muted"> · current</span>}
                    </span>
                    <span className="text-xs text-muted">
                      {v.author?.name ?? "Someone"}, {ago(v.created_at, now)}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Read>
        <NextPageFailed query={versions} what="older versions" className="mt-1" />
        {versions.hasNextPage && (
          <button type="button" onClick={() => versions.fetchNextPage()} className="touch-target mt-1 h-8 self-start rounded-md px-2 text-fg/70 hover:bg-raised hover:text-fg">
            Older versions
          </button>
        )}
      </aside>
    </div>
  );
}

/** The project's pages deleted in the last 30 days, each restorable. */
export function DeletedDocs({ projectId }: { projectId: string }) {
  const qc = useQueryClient();
  const canWrite = useCanWriteIssues();
  const [now] = useState(() => Date.now());
  const deleted = useQuery({ queryKey: ["docs", projectId, "deleted"], queryFn: () => docService(projectId).tree(true) });
  const { data: tree } = useQuery(docTreeQuery(projectId));
  const restore = useMutation({
    mutationFn: (id: string) => docService(projectId).restore(id),
    onSuccess: (p) => {
      qc.invalidateQueries({ queryKey: ["docs", projectId] });
      qc.invalidateQueries({ queryKey: projectQuery(projectId).queryKey });
      toast.success(`${p.title} is back`);
    },
    onError: (err) => toast.error(failure(err, "Couldn’t restore it")),
  });
  const parentOf = (id?: string | null) => tree?.find((p) => p.id === id)?.title;
  return (
    <div className="flex min-w-0 max-w-[800px] flex-1 flex-col gap-4 px-4 pb-16 pt-5 md:px-10 md:pt-6">
      <h1 className="text-[28px] font-semibold leading-9 tracking-[-0.015em] text-fg">Deleted pages</h1>
      <p className="text-[13px] text-muted">Deleted pages stay 30 days with their versions, then go for good.</p>
      <Read query={deleted} what="the deleted pages" isEmpty={(d) => d.length === 0} empty={<p className="text-sm text-fg/70">Nothing deleted in the last 30 days.</p>}>
        {(pages) => (
          <ul className="flex flex-col divide-y divide-[var(--border)] rounded-lg border border-subtle">
            {pages.map((p) => (
              <li key={p.id} className="flex flex-wrap items-center gap-3 px-3.5 py-2.5">
                <span className="flex min-w-0 flex-1 flex-col">
                  <Link href={`/projects/${projectId}/docs/${p.id}`} className="truncate text-sm text-fg hover:underline">
                    {p.title}
                  </Link>
                  <span className="text-xs text-muted">
                    Deleted {p.deleted_at ? ago(p.deleted_at, now) : ""}
                    {parentOf(p.parent_id) ? ` · from ${parentOf(p.parent_id)}` : ""}
                  </span>
                </span>
                {canWrite && (
                  <button
                    type="button"
                    onClick={() => restore.mutate(p.id)}
                    disabled={restore.isPending}
                    className="touch-target h-8 rounded-lg border border-strong bg-raised px-3 text-[13px] font-medium text-fg hover:bg-strong/40"
                  >
                    Restore
                  </button>
                )}
              </li>
            ))}
          </ul>
        )}
      </Read>
    </div>
  );
}
