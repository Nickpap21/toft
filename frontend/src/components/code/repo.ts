"use client";

import { createContext, useContext, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { ApiError } from "@/lib/api/client";
import { codeService, gitInfoQuery, repoService, type GitInfo, type Mirror, type Repository, type RepositoryDetail } from "@/lib/api/code.service";
import { useAuth } from "@/context/AuthContext";
import { readFailed } from "@/components/shell/unreachable";

/** The project's repositories (one or more, D-71, D-75). */
export function useRepos(projectId: string) {
  const { token } = useAuth();
  const git = useQuery({ ...gitInfoQuery, enabled: !!token });
  const repos = useQuery({
    queryKey: ["repositories", projectId],
    queryFn: () => codeService.repositories(projectId),
    enabled: git.data?.enabled === true && !!projectId,
  });
  return {
    git: git.data,
    repos: repos.data,
    loading: git.isLoading || repos.isLoading,
    error: repos.error ?? git.error,
    failed: readFailed(repos) || readFailed(git),
    retry: () => {
      void git.refetch();
      if (git.data?.enabled) void repos.refetch();
    },
  };
}

/** One repository of the project, by name, with what the browser opens on
 *  (default branch, counts) and its API. */
export function useRepo(projectId: string, name: string) {
  const { git, repos, loading, error } = useRepos(projectId);
  const repo = repos?.find((r) => r.name === name);
  const api = useMemo(() => (repo ? repoService(projectId, repo.id) : null), [projectId, repo]);
  const detail = useQuery({
    queryKey: ["repository", projectId, repo?.id],
    queryFn: () => api!.detail(),
    enabled: !!api,
    // While an import or a mirror's sync runs, follow it (D-79).
    refetchInterval: (q) => (mirrorBusy(q.state.data?.pull_mirror) ? 2000 : false),
  });
  return { git, repos, repo, api, detail: detail.data, loading: loading || detail.isLoading, error: error ?? detail.error };
}

/** Whether a mirror is waiting or running: its page follows it. */
export function mirrorBusy(m?: Mirror | null): boolean {
  return !!m && (m.state === "importing" || m.state === "syncing" || m.state === "pending");
}

/** What every Code page under /projects/<id>/code/<repo> works with. */
export interface CodeCtx {
  projectId: string;
  git?: GitInfo;
  repos: Repository[];
  repo: Repository;
  api: ReturnType<typeof repoService>;
  detail: RepositoryDetail;
  /** /projects/<id>/code/<repo>, which every link starts with. */
  base: string;
}

export const CodeContext = createContext<CodeCtx | null>(null);

export function useCode(): CodeCtx {
  const c = useContext(CodeContext);
  if (!c) throw new Error("useCode outside a repository's pages");
  return c;
}

/** The API's error code ("ref_not_found", "not_a_file"…), or "". */
export function errorCode(err: unknown): string {
  if (!(err instanceof ApiError)) return "";
  const body = err.body as { code?: unknown; data?: { code?: unknown } } | null;
  const code = body?.code ?? body?.data?.code;
  return typeof code === "string" ? code : "";
}

export type CodePage = "" | "blob" | "commits" | "branches" | "tags" | "pulls" | "pulls/new" | "pipelines" | "settings";

/** A repository webhook's page (board RepositoryWebhook). */
export function webhookHref(base: string, id: string): string {
  return `${base}/settings/webhooks/${id}`;
}

/** A repository's page (base is /projects/<id>/code/<repo>), with ?ref and
 *  ?path when they are set. */
export function codeHref(base: string, page: CodePage, ref?: string, path?: string): string {
  const q = new URLSearchParams();
  if (ref) q.set("ref", ref);
  if (path) q.set("path", path);
  const s = q.toString();
  return `${base}${page ? `/${page}` : ""}${s ? `?${s}` : ""}`;
}

export function commitHref(base: string, sha: string): string {
  return `${base}/commits/${sha}`;
}

export function pullHref(base: string, n: number, tab?: "commits" | "checks" | "files"): string {
  return `${base}/pulls/${n}${tab ? `?tab=${tab}` : ""}`;
}

export function repoBase(projectId: string, name: string): string {
  return `/projects/${projectId}/code/${encodeURIComponent(name)}`;
}

const DAY = 86_400_000;

/** "now", "4m ago", "3h ago" within a day, then "on Oct 16" (with the year
 *  when it isn't this one). */
export function ago(iso: string, now: number): string {
  const ms = Math.max(0, now - new Date(iso).getTime());
  if (ms < 60_000) return "now";
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m ago`;
  if (ms < DAY) return `${Math.floor(ms / 3_600_000)}h ago`;
  const d = new Date(iso);
  const sameYear = d.getFullYear() === new Date(now).getFullYear();
  return `on ${d.toLocaleDateString("en-US", { month: "short", day: "numeric", ...(sameYear ? {} : { year: "numeric" }) })}`;
}

/** "now", "12m", "1h", "3d", "3w", "2mo", "1y": the board's column. */
export function compactAgo(iso: string, now: number): string {
  const m = Math.max(0, Math.floor((now - new Date(iso).getTime()) / 60_000));
  if (m < 1) return "now";
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  const d = Math.floor(h / 24);
  if (d < 7) return `${d}d`;
  if (d < 35) return `${Math.floor(d / 7)}w`;
  if (d < 365) return `${Math.floor(d / 30)}mo`;
  return `${Math.floor(d / 365)}y`;
}

/** "712 B", "4.1 KB", "2.3 MB". */
export function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

export const shortSha = (sha: string) => sha.slice(0, 7);
