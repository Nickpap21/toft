"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { ArrowDownLeft, BookOpen, File, FileSymlink, Folder, FolderGit2, GitBranch, GitCommitHorizontal, GitPullRequest } from "lucide-react";
import { useOrgRole } from "@/context/AuthContext";
import type { GitTreeEntry, RepositoryDetail } from "@/lib/api/code.service";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { BranchesCard, CloneCard, CommitBar, CopyButton, PathCrumbs, RefPicker, RepoPicker } from "@/components/code/parts";
import { codeHref, commitHref, compactAgo, errorCode, useCode } from "@/components/code/repo";
import { Markdown } from "@/components/code/views";
import { WhatAPushDoes } from "@/components/code/checks";
import { ImportDialog, ImportProgress, MirrorBanner } from "@/components/code/mirrors";
import { card, primary } from "@/app/projects/_lib/ui";

// Files (boards Code, CodeRepos): the repository picker, a directory at a
// branch, tag or commit, each entry's last commit, the README, and beside
// them Clone, Branches and What a push does (D-77). An empty repository
// shows how to push to it or import one (Code, repo=empty); an import shows
// its progress, and a pull mirror says where it pulls from (CodeImport,
// D-79). Left for later: the issue and preview lines of What a push does
// (stages 4 and 5).
export default function FilesPage() {
  const sp = useSearchParams();
  const router = useRouter();
  const ref = sp.get("ref") ?? "";
  const path = sp.get("path") ?? "";
  const { git, repo, api, detail, base } = useCode();
  const role = useOrgRole();
  const canWrite = role === "owner" || role === "developer";
  const [now] = useState(() => Date.now());
  const tree = useQuery({
    queryKey: ["code-tree", api.id, ref, path],
    queryFn: () => api.tree(ref, path),
    enabled: !detail.empty,
    retry: false,
  });
  const code = errorCode(tree.error);
  useEffect(() => {
    if (code === "not_a_directory") router.replace(codeHref(base, "blob", ref, path));
  }, [code, base, ref, path, router]);
  useDocumentTitle(`${path || repo?.name || "Code"} · Toft`);

  const refName = tree.data?.ref.name ?? (ref || detail.default_branch);
  const pickerHref = (r: string) => codeHref(base, "", r, path);

  return (
    <>
      <div className="flex flex-wrap items-center gap-3">
        <RepoPicker />
        {detail.pull_mirror && !detail.empty && (
          <span className="inline-flex h-6 items-center gap-1.5 rounded-full bg-status-building-soft px-2.5 text-xs font-medium text-status-building-fg">
            <ArrowDownLeft className="h-3 w-3" aria-hidden="true" />
            Mirror
          </span>
        )}
        <span aria-hidden="true" className="text-muted/70">
          /
        </span>
        {path ? (
          <>
            {!detail.empty && <RefPicker api={api} current={refName} hrefFor={pickerHref} />}
            <PathCrumbs refName={ref} path={path} />
          </>
        ) : (
          <>
            <h1 className="sr-only">Files</h1>
            {!detail.empty && (
              <span className="flex flex-wrap items-center gap-3.5">
                <RefPicker api={api} current={refName} hrefFor={pickerHref} />
                {tree.data && (
                  <Link href={codeHref(base, "commits", ref)} className="inline-flex items-center gap-1.5 text-[13px] text-fg/70 hover:text-fg">
                    <GitCommitHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
                    <span className="font-mono text-xs text-fg">{tree.data.commit_count}</span> commits
                  </Link>
                )}
                <Link href={codeHref(base, "branches")} className="inline-flex items-center gap-1.5 text-[13px] text-fg/70 hover:text-fg">
                  <GitBranch className="h-3.5 w-3.5" aria-hidden="true" />
                  <span className="font-mono text-xs text-fg">{detail.branch_count}</span> {detail.branch_count === 1 ? "branch" : "branches"}
                </Link>
              </span>
            )}
          </>
        )}
        {canWrite && !detail.empty && !detail.pull_mirror && detail.branch_count > 1 && (
          <Link href={codeHref(base, "pulls/new")} className={cn(primary, "ml-auto")}>
            <GitPullRequest className="h-[15px] w-[15px]" aria-hidden="true" />
            New pull request
          </Link>
        )}
      </div>

      <div className="flex flex-wrap items-start gap-6">
        <div className="flex min-w-0 flex-[999_1_560px] flex-col gap-5">
          {detail.pull_mirror && !detail.empty && <MirrorBanner mirror={detail.pull_mirror} />}
          {detail.empty && detail.pull_mirror ? (
            <ImportProgress mirror={detail.pull_mirror} />
          ) : detail.empty ? (
            <EmptyRepo detail={detail} canWrite={canWrite} />
          ) : tree.error ? (
            <section role="alert" data-read-state="failed" className={cn(card, "flex flex-col gap-2 p-5")}>
              <h2 className="text-[15px] font-semibold text-fg">
                {code === "ref_not_found" ? `No branch, tag or commit called ${ref}` : code === "path_not_found" ? `Nothing at ${path} on ${refName}` : "Couldn’t read the repository"}
              </h2>
              <p className="text-[13px] text-fg/70">
                {code === "ref_not_found" || code === "path_not_found" ? "It may have been renamed or removed. " : `${tree.error.message}. `}
                <Link href={codeHref(base, "")} className="inline-link">
                  Back to {detail.default_branch}
                </Link>
              </p>
            </section>
          ) : !tree.data ? (
            <div role="status" aria-label="Loading" className="h-72 rounded-[10px] bg-surface motion-safe:animate-pulse" />
          ) : (
            <>
              <section aria-label={`Files on ${refName}`} className={card}>
                {tree.data.last_commit && <CommitBar commit={tree.data.last_commit} now={now} />}
                <div className="overflow-x-auto">
                  <table className="w-full min-w-[560px] border-collapse text-[13px]">
                    <thead>
                      <tr className="text-left text-xs text-muted">
                        <th scope="col" className="border-b border-subtle px-4 py-2 font-medium">
                          Name
                        </th>
                        <th scope="col" className="border-b border-subtle px-4 py-2 font-medium">
                          Last commit
                        </th>
                        <th scope="col" className="border-b border-subtle px-4 py-2 text-right font-medium">
                          Updated
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {path && (
                        <tr>
                          <td colSpan={3} className="border-t border-subtle px-4 py-[9px]">
                            <Link
                              href={codeHref(base, "", ref, path.split("/").slice(0, -1).join("/"))}
                              aria-label="Up to the parent directory"
                              className="inline-flex items-center gap-2.5 font-mono text-fg hover:underline"
                            >
                              <Folder className="h-[15px] w-[15px] fill-raised text-muted" aria-hidden="true" />
                              ..
                            </Link>
                          </td>
                        </tr>
                      )}
                      {tree.data.entries.map((e) => (
                        <EntryRow key={e.path} e={e} base={base} refParam={ref} now={now} />
                      ))}
                    </tbody>
                  </table>
                </div>
                {tree.data.truncated && (
                  <p className="border-t border-subtle px-4 py-2.5 text-xs text-muted">This directory has more than 1,000 entries; the first 1,000 are shown. Clone the repository to see them all.</p>
                )}
              </section>
              {tree.data.readme && (
                <article aria-label={tree.data.readme.path} className={card}>
                  <div className="flex items-center gap-2 border-b border-subtle px-4 py-2.5 text-[13px] text-fg/70">
                    <BookOpen className="h-3.5 w-3.5" aria-hidden="true" />
                    <Link href={codeHref(base, "blob", ref, tree.data.readme.path)} className="font-mono text-xs text-fg hover:underline">
                      {tree.data.readme.path.split("/").pop()}
                    </Link>
                  </div>
                  <div className="max-w-[760px] px-6 pb-6 pt-5">
                    <Markdown html={tree.data.readme.html} refName={ref} />
                  </div>
                </article>
              )}
            </>
          )}
        </div>

        <aside className="flex min-w-0 flex-[1_1_340px] flex-col gap-5">
          <CloneCard detail={detail} sshHost={git?.ssh_host} sshPort={git?.ssh_port} />
          <BranchesCard now={now} />
          <WhatAPushDoes />
        </aside>
      </div>
    </>
  );
}

function EntryRow({ e, base, refParam, now }: { e: GitTreeEntry; base: string; refParam: string; now: number }) {
  const Icon = e.kind === "dir" ? Folder : e.kind === "symlink" ? FileSymlink : e.kind === "submodule" ? FolderGit2 : File;
  const label = (
    <>
      <Icon className={cn("h-[15px] w-[15px] flex-none text-muted", e.kind === "dir" && "fill-raised")} aria-hidden="true" />
      <span className="font-mono text-[13px]">{e.name}</span>
    </>
  );
  return (
    <tr>
      <td className="w-[1%] whitespace-nowrap border-t border-subtle py-[9px] pl-4 pr-10">
        {e.kind === "submodule" ? (
          <span className="inline-flex items-center gap-2.5 text-fg" title="A submodule: another repository, not browsed here">
            {label}
          </span>
        ) : (
          <Link
            href={codeHref(base, e.kind === "dir" ? "" : "blob", refParam, e.path)}
            className="inline-flex items-center gap-2.5 text-fg hover:underline"
          >
            {label}
          </Link>
        )}
      </td>
      <td className="max-w-0 border-t border-subtle px-4 py-[9px] text-fg/70">
        {e.last_commit && (
          <Link href={commitHref(base, e.last_commit.sha)} className="block truncate hover:text-fg hover:underline">
            {e.last_commit.subject}
          </Link>
        )}
      </td>
      <td className="whitespace-nowrap border-t border-subtle px-4 py-[9px] text-right font-mono text-xs text-muted">
        {e.last_commit ? compactAgo(e.last_commit.committed_at, now) : ""}
      </td>
    </tr>
  );
}

/** An empty repository: how to push to it, or import one (Code,
 *  repo=empty; CodeImport). */
function EmptyRepo({ detail, canWrite }: { detail: RepositoryDetail; canWrite: boolean }) {
  const commands = `git remote add toft ${detail.ssh_url}\ngit push -u toft --all\ngit push toft --tags`;
  const [importing, setImporting] = useState(false);
  return (
    <section aria-labelledby="empty-h" className={cn(card, "flex flex-col gap-4 p-5")}>
      <div className="flex flex-col gap-1">
        <h2 id="empty-h" className="text-[15px] font-semibold leading-[22px] text-fg">
          This repository is empty
        </h2>
        <p className="text-[13px] text-fg/70">Push an existing repository or import one. Nothing deploys until a service uses this repository as its source.</p>
      </div>
      <div className="flex flex-col gap-2">
        <h3 className="text-[13px] font-semibold text-fg">Push an existing repository</h3>
        <div className="overflow-hidden rounded-lg border border-subtle bg-ground">
          <div className="flex items-center gap-2 border-b border-subtle bg-raised/50 py-1.5 pl-3 pr-1.5 text-xs text-muted">
            <span className="font-mono">terminal</span>
            <span className="flex-1" />
            <CopyButton
              text={commands}
              label="Copy the commands"
              className="inline-flex h-7 items-center gap-1.5 rounded-md border border-subtle bg-raised px-2.5 text-xs text-fg hover:bg-raised/70"
            >
              Copy
            </CopyButton>
          </div>
          <pre role="group" tabIndex={0} aria-label="Commands to push an existing repository" className="focus-inset overflow-x-auto px-3.5 py-3 font-mono text-[13px] leading-[21px] text-fg">{commands}</pre>
        </div>
      </div>
      {canWrite && (
        <div className="flex flex-wrap items-center gap-3 border-t border-subtle pt-4">
          <div className="flex flex-[1_1_320px] flex-col gap-0.5">
            <h3 className="text-[13px] font-semibold text-fg">Import from GitHub or GitLab</h3>
            <p className="text-[13px] text-fg/70">Copies every branch and tag. You can keep pulling from the original until you switch over.</p>
          </div>
          <button type="button" onClick={() => setImporting(true)} className={primary}>
            Import repository
          </button>
          <ImportDialog open={importing} onClose={() => setImporting(false)} />
        </div>
      )}
    </section>
  );
}
