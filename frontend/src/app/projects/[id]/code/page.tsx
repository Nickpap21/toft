"use client";

import { useEffect } from "react";
import { useParams, useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { repoBase, useRepos } from "@/components/code/repo";
import { ReadFailed } from "@/components/shell/unreachable";

// /projects/<id>/code opens the project's first repository, keeping ?ref
// and ?path (v0.3.1's links).
export default function CodeIndex() {
  const projectId = useParams<{ id: string }>().id;
  const router = useRouter();
  const queryClient = useQueryClient();
  const { repos, error } = useRepos(projectId);
  useEffect(() => {
    if (repos?.[0]) router.replace(repoBase(projectId, repos[0].name) + window.location.search);
  }, [repos, projectId, router]);
  if (error && !repos) return <ReadFailed error={error} onRetry={() => void queryClient.refetchQueries({ type: "active" })} />;
  return <div role="status" aria-label="Loading" className="h-64 w-full rounded-[10px] bg-surface motion-safe:animate-pulse" />;
}
