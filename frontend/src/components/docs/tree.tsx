"use client";

import { useDeferredValue, useState } from "react";
import Link from "next/link";
import { useParams, usePathname } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Pencil, Plus, Search, Trash2 } from "lucide-react";
import { useCanWriteIssues } from "@/components/issues/issue-detail";
import { docTreeQuery } from "@/lib/api/doc.service";
import { searchWorkspace } from "@/lib/api/issue.service";
import { Read } from "@/components/shell/unreachable";
import { cn } from "@/lib/utils";
import { buildTree, type TreeNode } from "./body";

/** The docs' page tree (board: Docs): search, the pages by their parents,
 *  a dot on those out of date, New page. On a phone it folds into a
 *  "Pages" button over the page. */
export function DocsTree({ projectId }: { projectId: string }) {
  const params = useParams<{ page?: string }>();
  const pathname = usePathname();
  const editing = pathname.endsWith("/edit") ? params.page : undefined;
  const canWrite = useCanWriteIssues();
  const read = useQuery(docTreeQuery(projectId));
  const pages = read.data;
  const [q, setQ] = useState("");
  const term = useDeferredValue(q.trim());
  const found = useQuery({
    queryKey: ["docs", projectId, "search", term],
    queryFn: () => searchWorkspace(term, "docs", projectId, 20),
    enabled: term.length >= 2,
  });
  const [closed, setClosed] = useState<Set<string>>(new Set());
  const [open, setOpen] = useState(false);
  const base = `/projects/${projectId}/docs`;
  const current = pages?.find((p) => p.id === params.page);

  const row = (node: TreeNode, depth: number) => {
    const p = node.page;
    const group = node.children.length > 0;
    const folded = closed.has(p.id);
    const active = p.id === params.page;
    return (
      <li key={p.id}>
        <div className={cn("group flex items-center gap-0.5 rounded-md", active && "bg-accent-soft")}>
          {group ? (
            <button
              type="button"
              aria-label={folded ? `Show the pages in ${p.title}` : `Hide the pages in ${p.title}`}
              aria-expanded={!folded}
              onClick={() => setClosed((c) => {
                const n = new Set(c);
                if (n.has(p.id)) n.delete(p.id);
                else n.add(p.id);
                return n;
              })}
              style={{ marginLeft: depth * 14 }}
              className="touch-target flex h-[30px] w-6 flex-none items-center justify-center text-muted hover:text-fg pointer-coarse:w-11"
            >
              {folded ? <ChevronRight className="h-3 w-3" strokeWidth={2.2} /> : <ChevronDown className="h-3 w-3" strokeWidth={2.2} />}
            </button>
          ) : null}
          <Link
            href={`${base}/${p.id}`}
            onClick={() => setOpen(false)}
            aria-current={active ? "page" : undefined}
            style={group ? undefined : { paddingLeft: 26 + depth * 14 }}
            className={cn(
              "touch-target flex min-h-[30px] min-w-0 flex-1 items-center gap-1.5 rounded-md pr-2",
              group && "pl-0.5",
              active ? "font-medium text-fg" : group ? "font-medium text-fg" : "text-fg/70 hover:text-fg",
            )}
          >
            <span className="min-w-0 flex-1 truncate">{p.title}</span>
            {p.stale && <span role="img" aria-label="may be out of date" className="h-1.5 w-1.5 flex-none rounded-full bg-status-pending" />}
            {editing === p.id && <Pencil role="img" aria-label="Editing" className="h-3 w-3 flex-none text-link" strokeWidth={2.2} />}
          </Link>
          {canWrite && group && (
            <Link
              href={`${base}/new?parent=${p.id}`}
              aria-label={`New page in ${p.title}`}
              className="mr-0.5 flex h-7 w-7 flex-none items-center justify-center rounded-md text-fg/70 opacity-0 hover:bg-raised hover:text-fg focus:opacity-100 group-hover:opacity-100 pointer-coarse:h-11 pointer-coarse:w-11 pointer-coarse:opacity-100"
            >
              <Plus className="h-[13px] w-[13px]" strokeWidth={2} />
            </Link>
          )}
        </div>
        {group && !folded && <ul className="flex flex-col gap-px">{node.children.map((c) => row(c, depth + 1))}</ul>}
      </li>
    );
  };

  const tree = buildTree(pages ?? []);
  return (
    <nav aria-label="Pages" className="flex flex-col gap-2.5 border-b border-subtle px-3 py-3 md:sticky md:top-0 md:max-h-screen md:min-h-full md:w-[272px] md:flex-none md:overflow-y-auto md:border-b-0 md:border-r md:py-4">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="touch-target flex h-9 items-center gap-2 rounded-lg border border-subtle bg-surface px-2.5 text-left text-sm md:hidden"
      >
        <span className="text-muted">Pages</span>
        <span className="min-w-0 flex-1 truncate text-fg">{current?.title ?? ""}</span>
        <ChevronDown className={cn("h-3.5 w-3.5 text-muted transition-transform", open && "rotate-180")} aria-hidden="true" />
      </button>
      <div className={cn("flex-col gap-2.5", open ? "flex" : "hidden md:flex")}>
        <label className="touch-target flex h-8 items-center gap-1.5 rounded-lg border border-subtle bg-surface px-2.5 text-muted focus-within:border-strong">
          <Search className="h-[13px] w-[13px] flex-none" strokeWidth={2} aria-hidden="true" />
          <span className="sr-only">Search docs</span>
          <input
            type="search"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search docs"
            className="min-w-0 flex-1 bg-transparent text-[13px] text-fg outline-none placeholder:text-muted"
          />
        </label>
        {term.length >= 2 ? (
          <Read query={found} what="the search results">
            {(results) => (
              <ul aria-label="Search results" className="flex flex-col gap-px text-[13px]">
                {results.docs.map((d) => (
                  <li key={d.id}>
                    <Link href={`${base}/${d.id}`} onClick={() => setOpen(false)} className="touch-target flex flex-col justify-center rounded-md px-2 py-1.5 hover:bg-raised">
                      <span className="truncate font-medium text-fg">{d.title}</span>
                      <span className="line-clamp-2 text-xs text-muted">{d.snippet}</span>
                    </Link>
                  </li>
                ))}
                {results.docs.length === 0 && <li className="px-2 py-1.5 text-muted">No page matches.</li>}
              </ul>
            )}
          </Read>
        ) : (
          <Read query={read} what="the pages">
            {() => <ul className="flex flex-col gap-px text-[13px]">{tree.map((n) => row(n, 0))}</ul>}
          </Read>
        )}
        {canWrite && (
          <Link
            href={`${base}/new`}
            onClick={() => setOpen(false)}
            className="touch-target flex h-8 items-center gap-1.5 rounded-md px-2 text-[13px] text-fg/70 hover:bg-raised hover:text-fg"
          >
            <Plus className="h-[13px] w-[13px]" strokeWidth={2} aria-hidden="true" />
            New page
          </Link>
        )}
        <Link
          href={`${base}/deleted`}
          onClick={() => setOpen(false)}
          className="touch-target flex h-8 items-center gap-1.5 rounded-md px-2 text-xs text-muted hover:text-fg"
        >
          <Trash2 className="h-3 w-3" strokeWidth={2} aria-hidden="true" />
          Deleted pages
        </Link>
      </div>
    </nav>
  );
}
