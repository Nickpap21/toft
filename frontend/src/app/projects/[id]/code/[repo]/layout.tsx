"use client";

import { Suspense, useEffect } from "react";
import Link from "next/link";
import { useParams, usePathname, useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { notFound } from "@/lib/api/client";
import { ReadFailed } from "@/components/shell/unreachable";
import { cn } from "@/lib/utils";
import { CodeNav } from "@/components/code/parts";
import { CodeContext, repoBase, useRepo } from "@/components/code/repo";
import { card, h2, outline } from "@/app/projects/_lib/ui";

// The pages of v0.3.1, before repositories were in the URL (D-75).
const LEGACY = ["blob", "commits", "branches", "tags"];

// One repository's pages: the context they share and the Code nav.
export default function RepoLayout({ children }: { children: React.ReactNode }) {
  const { id: projectId, repo: param } = useParams<{ id: string; repo: string }>();
  const name = decodeURIComponent(param);
  const pathname = usePathname();
  const router = useRouter();
  const { git, repos, repo, api, detail, loading, error } = useRepo(projectId, name);
  const queryClient = useQueryClient();

  // /code/blob?…, /code/commits/<sha>…: a v0.3.1 link, unless a repository
  // is called that; it opens in the first repository.
  const legacy = !!repos && !repo && LEGACY.includes(name);
  useEffect(() => {
    if (legacy && repos?.[0]) {
      const rest = decodeURIComponent(pathname).slice(`/projects/${projectId}/code`.length);
      router.replace(repoBase(projectId, repos[0].name) + rest + window.location.search);
    }
  }, [legacy, repos, pathname, projectId, router]);

  if ((repos && !repo && !legacy) || notFound(error)) {
    return (
      <section className={cn(card, "flex max-w-[640px] flex-col gap-2 p-5")}>
        <h1 className={h2}>No repository called {name} in this project</h1>
        <p className="text-[13px] text-fg/70">It may have been deleted.</p>
        <Link href={`/projects/${projectId}/code`} className={cn(outline, "self-start")}>
          The project’s code
        </Link>
      </section>
    );
  }
  if (loading || !repo || !api || !detail || !repos) {
    if (error) return <ReadFailed error={error} onRetry={() => void queryClient.refetchQueries({ type: "active" })} />;
    return <div role="status" aria-label="Loading" className="h-64 w-full rounded-[10px] bg-surface motion-safe:animate-pulse" />;
  }
  return (
    <CodeContext.Provider value={{ projectId, git, repos, repo, api, detail, base: repoBase(projectId, repo.name) }}>
      {/* useSearchParams (the nav keeps ?ref) needs a boundary. */}
      <Suspense>
        <CodeNav />
        {children}
      </Suspense>
    </CodeContext.Provider>
  );
}
