"use client";

import { useEffect } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { useBreadcrumbs } from "@/context/BreadcrumbContext";
import { projectQuery } from "@/lib/queries";
import { cn } from "@/lib/utils";
import { useRepos } from "@/components/code/repo";
import { card, h2, outline } from "@/app/projects/_lib/ui";

// The project's Code page (D-73, D-75): the project's repositories, each
// under /projects/<id>/code/<repo>. Boards: Code, CodeRepos, CodeFile,
// CodeCommits, CodeCommit, CodeBranches, PullRequests, NewPullRequest,
// PullRequest, PullRequestMerge, PullRequestChanges, RepositorySettings.
export default function CodeLayout({ children }: { children: React.ReactNode }) {
  const projectId = useParams<{ id: string }>().id;
  const { git } = useRepos(projectId);
  const { data: project } = useQuery(projectQuery(projectId));
  const { set: setBreadcrumbs } = useBreadcrumbs();
  useEffect(() => {
    if (!project) return;
    setBreadcrumbs([
      { label: "Projects", href: "/projects" },
      { label: project.name, href: `/projects/${projectId}` },
      { label: "Code", href: "" },
    ]);
  }, [project, projectId, setBreadcrumbs]);

  if (git && !git.enabled) {
    return (
      <section className={cn(card, "flex max-w-[640px] flex-col gap-2 p-5")}>
        <h1 className={h2}>Git hosting is off on this instance</h1>
        <p className="text-[13px] text-fg/70">The operator installed Toft without it (TOFT_GIT=off), so projects have no repository here.</p>
        <Link href={`/projects/${projectId}`} className={cn(outline, "self-start")}>
          Back to the project
        </Link>
      </section>
    );
  }
  return <div className="flex w-full max-w-[1320px] flex-col gap-5 pb-14 md:-mt-4">{children}</div>;
}
