"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Layers, Server, TriangleAlert } from "lucide-react";
import { IssueMarkdown, useCanWriteIssues } from "@/components/issues/issue-detail";
import { notFound } from "@/lib/api/client";
import { docPageQuery, docService, type DocPage } from "@/lib/api/doc.service";
import { Read } from "@/components/shell/unreachable";
import { projectQuery } from "@/lib/queries";
import { ago } from "@/lib/time";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { allChanges, changeText, contents, prepareHTML, splitBlocks, staleSentence } from "./body";
import { failure, IssueBlock, PullBlock, RunBlock, ServiceBlock } from "./blocks";

/** A page as readers see it (board: Docs). */
export function DocReader({ projectId, pageId }: { projectId: string; pageId: string }) {
  const read = useQuery(docPageQuery(projectId, pageId));
  const { data: project } = useQuery(projectQuery(projectId));
  if (!read.data && notFound(read.error)) return <p className="px-4 py-10 text-sm text-muted md:px-10">This page doesn’t exist, or isn’t this project’s.</p>;
  return (
    <div className={read.data ? "contents" : "min-w-0 flex-1 px-4 py-10 md:px-10"}>
      <Read query={read} what="this page" heading="h1" loading={<div role="status" aria-label="Loading this page" className="h-64 rounded-[10px] bg-surface motion-safe:animate-pulse" />}>
        {(page) => <Page page={page} project={project?.name ?? ""} />}
      </Read>
    </div>
  );
}

function initials(name: string): string {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((w) => w[0]?.toUpperCase())
    .join("");
}

function Page({ page, project }: { page: DocPage; project: string }) {
  const projectId = page.project_id;
  useDocumentTitle(`${page.title} · Toft`);
  const canWrite = useCanWriteIssues();
  const router = useRouter();
  const qc = useQueryClient();
  const [now] = useState(() => Date.now());
  const [showChanges, setShowChanges] = useState(false);
  const html = useMemo(() => prepareHTML(page.html, projectId), [page.html, projectId]);
  const pieces = useMemo(() => splitBlocks(html), [html]);
  const toc = useMemo(() => contents(page.html), [page.html]);
  const runbook = page.blocks.some((b) => b.kind === "run");
  const svcs = new Map(page.services.map((s) => [s.id, s]));
  const servers = [...new Set(page.services.map((s) => s.server ?? "panel server"))];
  const parent = page.path.at(-1);
  const editor = page.updated_by ?? page.created_by;

  const verify = useMutation({
    mutationFn: () => docService(projectId).verify(page.id),
    onSuccess: (p) => {
      qc.setQueryData(docPageQuery(projectId, page.id).queryKey, p);
      qc.invalidateQueries({ queryKey: ["docs", projectId, "tree"] });
      toast.success("Marked as verified");
    },
    onError: (err) => toast.error(failure(err, "Couldn’t mark it verified")),
  });
  const restore = useMutation({
    mutationFn: () => docService(projectId).restore(page.id),
    onSuccess: (p) => {
      qc.setQueryData(docPageQuery(projectId, page.id).queryKey, p);
      qc.invalidateQueries({ queryKey: ["docs", projectId] });
    },
    onError: (err) => toast.error(failure(err, "Couldn’t restore it")),
  });

  let step = -1;
  return (
    <div className="flex min-w-0 flex-1 flex-wrap items-start">
      <article className="flex min-w-0 max-w-[800px] flex-[999_1_520px] flex-col gap-[18px] px-4 pb-16 pt-5 md:px-10 md:pt-6">
        <div className="flex flex-col gap-2.5">
          <nav aria-label="Breadcrumb" className="text-[13px] text-muted [overflow-wrap:anywhere]">
            {page.path.map((p) => (
              <span key={p.id}>
                <Link href={`/projects/${projectId}/docs/${p.id}`} className="hover:text-fg hover:underline">
                  {p.title}
                </Link>{" "}
                /{" "}
              </span>
            ))}
            {!parent && <span>Docs /</span>}
          </nav>
          <h1 className="text-[28px] font-semibold leading-9 tracking-[-0.015em] text-fg [overflow-wrap:anywhere]">{page.title}</h1>
          <div className="flex flex-wrap items-center gap-2.5 text-[13px] text-muted">
            {editor && (
              <span aria-hidden="true" className="flex h-[22px] w-[22px] items-center justify-center rounded-full bg-raised text-[10px] font-semibold text-fg">
                {initials(editor.name)}
              </span>
            )}
            <span>
              {editor?.name ?? "Someone"} {page.version > 1 ? "edited" : "wrote this"} {ago(page.updated_at, now)}
            </span>
            <span aria-hidden="true">·</span>
            <span>
              {page.verified_at ? `Last verified ${ago(page.verified_at, now)}${page.verified_by ? ` by ${page.verified_by.name}` : ""}` : "Never verified"}
            </span>
            <span className="flex-1" />
            {canWrite && !page.deleted_at && (
              <Link
                href={`/projects/${projectId}/docs/${page.id}/edit`}
                className="touch-target inline-flex h-[30px] items-center rounded-md border border-strong bg-raised px-2.5 text-[13px] text-fg hover:bg-strong/40"
              >
                Edit
              </Link>
            )}
            <Link
              href={`/projects/${projectId}/docs/${page.id}/history`}
              className="touch-target inline-flex h-[30px] items-center rounded-md px-2.5 text-[13px] text-fg/70 hover:bg-raised hover:text-fg"
            >
              History
            </Link>
          </div>
        </div>

        {page.deleted_at && (
          <div role="note" className="flex flex-wrap items-center gap-3 rounded-lg border border-subtle bg-surface px-3.5 py-3 text-[13px] text-fg/80">
            <span className="flex-1">This page was deleted {ago(page.deleted_at, now)}. It goes for good 30 days after.</span>
            {canWrite && (
              <button type="button" onClick={() => restore.mutate()} disabled={restore.isPending} className="touch-target h-8 rounded-lg border border-strong bg-raised px-3 text-[13px] font-medium text-fg">
                Restore
              </button>
            )}
          </div>
        )}

        {page.stale && (
          <div role="note" className="flex flex-wrap gap-3 rounded-lg border border-status-pending/35 bg-status-pending/[0.07] px-3.5 py-3">
            <TriangleAlert aria-hidden="true" className="mt-px h-[18px] w-[18px] flex-none text-status-pending" strokeWidth={1.9} />
            <div className="flex min-w-0 flex-[1_1_360px] flex-col gap-0.5 text-[13px]">
              <span className="font-semibold text-status-pending-fg">This {runbook ? "runbook" : "page"} may be out of date</span>
              <span className="text-fg/80 [overflow-wrap:anywhere]">{staleSentence(page, project)}</span>
              {showChanges && (
                <ul className="mt-1.5 flex list-disc flex-col gap-0.5 pl-4 text-fg/80">
                  {allChanges(page.services).map(({ svc, change }, i) => (
                    <li key={i}>
                      {changeText(svc.name, change)}
                      {change.to && change.field !== "image" && change.field !== "server" && ` ${ago(change.to, now)}`}
                    </li>
                  ))}
                </ul>
              )}
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <button type="button" onClick={() => setShowChanges((s) => !s)} aria-expanded={showChanges} className="touch-target h-8 rounded-lg px-2.5 text-[13px] text-status-pending-fg hover:bg-status-pending/10">
                What changed
              </button>
              {canWrite && (
                <button
                  type="button"
                  onClick={() => verify.mutate()}
                  disabled={verify.isPending}
                  className="touch-target h-8 rounded-lg border border-strong bg-raised px-3 text-[13px] font-medium text-fg hover:bg-strong/40 disabled:opacity-60"
                >
                  Mark as verified
                </button>
              )}
            </div>
          </div>
        )}

        <div
          className="flex flex-col gap-[18px]"
          onClick={(e) => {
            const a = (e.target as HTMLElement).closest("a[data-toft-page]");
            if (a instanceof HTMLAnchorElement && !e.metaKey && !e.ctrlKey && !e.shiftKey && e.button === 0) {
              e.preventDefault();
              router.push(a.pathname);
            }
          }}
        >
          {pieces.map((piece, i) => {
            if ("html" in piece) return <IssueMarkdown key={i} html={piece.html} projectId={projectId} className="toft-doc" />;
            const b = page.blocks[piece.block];
            switch (b?.kind) {
              case "service":
                return <ServiceBlock key={i} svc={svcs.get(b.service_id ?? "")} projectId={projectId} now={now} />;
              case "run":
                step++;
                return <RunBlock key={i} block={b} step={step} page={page} svc={svcs.get(b.service_id ?? "")} canWrite={canWrite && !page.deleted_at} />;
              case "issue":
                return <IssueBlock key={i} block={b} projectId={projectId} />;
              case "pull":
                return <PullBlock key={i} block={b} projectId={projectId} />;
              default:
                return <IssueMarkdown key={i} html={piece.raw} projectId={projectId} className="toft-doc" />;
            }
          })}
        </div>
      </article>

      <aside className="flex min-w-0 flex-[1_1_240px] flex-col gap-[22px] px-4 pb-10 pt-2 text-[13px] md:pl-2 md:pr-6 md:pt-7">
        {toc.length > 0 && <Contents toc={toc} />}
        {page.services.length > 0 && (
          <div className="flex flex-col gap-2">
            <span className="text-xs font-medium text-muted">Linked to</span>
            <span className="flex items-start gap-2 text-fg">
              <Layers aria-hidden="true" className="mt-0.5 h-3.5 w-3.5 flex-none text-muted" strokeWidth={1.9} />
              <span>
                {page.services.map((s, i) => (
                  <span key={s.id}>
                    {i > 0 && ", "}
                    <Link href={`/projects/${projectId}/services/${s.id}`} className="hover:underline">
                      {s.name}
                    </Link>
                  </span>
                ))}
              </span>
            </span>
            <span className="flex items-center gap-2 text-fg">
              <Server aria-hidden="true" className="h-3.5 w-3.5 flex-none text-muted" strokeWidth={1.9} />
              {servers.join(", ")}
            </span>
          </div>
        )}
        {page.referenced_by.length > 0 && (
          <div className="flex flex-col gap-2">
            <span className="text-xs font-medium text-muted">Referenced by</span>
            {page.referenced_by.map((r) => (
              <Link key={r.id} href={`/projects/${r.project_id}/docs/${r.id}`} className="text-fg hover:underline">
                {r.title}
              </Link>
            ))}
          </div>
        )}
        <div className="flex flex-col gap-1.5 border-t border-subtle pt-3.5 text-fg/70">
          {page.created_by && (
            <span>
              Owner <span className="text-fg">{page.created_by.name}</span>
            </span>
          )}
          <span>
            Version <span className="font-mono text-xs text-fg">{page.version}</span>
          </span>
        </div>
      </aside>
    </div>
  );
}

/** On this page, with the section in view marked. */
function Contents({ toc }: { toc: { id: string; text: string }[] }) {
  const [active, setActive] = useState(toc[0]?.id ?? "");
  useEffect(() => {
    const seen = new Map<string, boolean>();
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) seen.set(e.target.id, e.isIntersecting);
        const first = toc.find((t) => seen.get(t.id));
        if (first) setActive(first.id);
      },
      { rootMargin: "0px 0px -60% 0px" },
    );
    for (const t of toc) {
      const el = document.getElementById(t.id);
      if (el) io.observe(el);
    }
    return () => io.disconnect();
  }, [toc]);
  return (
    <nav aria-label="On this page" className="flex flex-col gap-1.5">
      <span className="text-xs font-medium text-muted">On this page</span>
      {toc.map((t) => (
        <a key={t.id} href={`#${t.id}`} className={cn("hover:text-fg", t.id === active ? "text-fg" : "text-fg/70")}>
          {t.text}
        </a>
      ))}
    </nav>
  );
}
