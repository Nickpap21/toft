"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { FileText, GitPullRequest } from "lucide-react";
import { STATUS } from "@/components/issues/meta";
import { serviceWork } from "@/lib/api/doc.service";
import type { IssueStatus } from "@/lib/api/issue.service";
import { LoadFailed } from "@/components/shell/unreachable";
import { cn } from "@/lib/utils";

const PRIORITY: Record<string, { name: string; tint: string }> = {
  urgent: { name: "Urgent", tint: "text-status-pending-fg" },
  high: { name: "High", tint: "text-fg/70" },
};
const row = "flex min-h-9 items-center gap-2.5 px-4 text-[13px] text-fg hover:bg-raised";
const label = "px-4 pb-1 pt-2 text-xs font-medium text-muted";

/** A service's Connected work (board: Service, D-86): open pull requests that
 *  change its files, open issues labelled with its name, and the pages that
 *  name it, verified or out of date. */
export function ConnectedWork({ serviceId, projectId, name, dir }: { serviceId: string; projectId: string; name: string; dir?: string }) {
  const work = useQuery({ queryKey: ["service-work", serviceId], queryFn: () => serviceWork(serviceId), staleTime: 30_000 });
  const data = work.data;
  const where = dir && dir !== "/" && dir !== "." ? dir.replace(/^\/+|\/+$/g, "") : name;
  const empty = data && data.pulls.length + data.issues.length + data.pages.length === 0;
  return (
    <section aria-labelledby="conn-h" className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
      <div className="flex flex-col gap-0.5 border-b border-subtle px-4 py-3">
        <h2 id="conn-h" className="text-[15px] font-semibold leading-[22px] text-fg">
          Connected work
        </h2>
        <span className="text-xs text-muted">Everything that touches {where}</span>
      </div>
      <div className="flex flex-col pb-2 pt-1.5">
        {!data && work.error != null && (
          <div className="px-4 py-2">
            <LoadFailed what="the connected work" onRetry={() => void work.refetch()} />
          </div>
        )}
        {empty && <p className="px-4 py-2 text-[13px] text-muted">No open pull request, issue labelled {name} or page about it yet.</p>}
        {!!data?.pulls.length && (
          <>
            <div className={label}>Open pull requests</div>
            {data.pulls.map((p) => (
              <Link key={`${p.repository_id}-${p.number}`} href={`/projects/${projectId}/code/${encodeURIComponent(p.repository)}/pulls/${p.number}`} className={row}>
                <GitPullRequest aria-hidden="true" className="h-[15px] w-[15px] flex-none text-link" strokeWidth={1.9} />
                <span className="font-mono text-xs text-muted">!{p.number}</span>
                <span className="min-w-0 flex-1 truncate">{p.title}</span>
                {p.checks && (
                  <span className={cn("text-xs", p.checks === "success" ? "text-status-running-fg" : p.checks === "failure" ? "text-status-failed-fg" : "text-muted")}>
                    {p.checks === "success" ? "checks passed" : p.checks === "failure" ? "checks failed" : "checks running"}
                  </span>
                )}
              </Link>
            ))}
          </>
        )}
        {!!data?.issues.length && (
          <>
            <div className={label}>Issues labelled {name}</div>
            {data.issues.map((i) => {
              const st = STATUS[i.status as IssueStatus];
              const pr = PRIORITY[i.priority];
              return (
                <Link key={i.key} href={`/projects/${i.project_id}/issues/${i.number}`} className={row}>
                  <svg aria-hidden="true" viewBox="0 0 24 24" className={cn("h-[15px] w-[15px] flex-none", st?.tint ?? "text-muted")} fill="none" stroke="currentColor" strokeWidth={1.9} strokeLinecap="round">
                    <path d="M3 12a9 9 0 1 0 18 0a9 9 0 1 0-18 0M11 12a1 1 0 1 0 2 0a1 1 0 1 0-2 0" />
                  </svg>
                  <span className="font-mono text-xs text-muted">{i.key}</span>
                  <span className="min-w-0 flex-1 truncate">{i.title}</span>
                  {pr && <span className={cn("text-xs", pr.tint)}>{pr.name}</span>}
                </Link>
              );
            })}
          </>
        )}
        {!!data?.pages.length && (
          <>
            <div className={label}>Runbooks</div>
            {data.pages.map((p) => (
              <Link key={p.id} href={`/projects/${projectId}/docs/${p.id}`} className={row}>
                <FileText aria-hidden="true" className="h-[15px] w-[15px] flex-none text-muted" strokeWidth={1.9} />
                <span className="min-w-0 flex-1 truncate">{p.title}</span>
                <span className={cn("text-xs", p.stale ? "text-status-pending-fg" : p.verified_at ? "text-status-running-fg" : "text-muted")}>
                  {p.stale ? "out of date" : p.verified_at ? "verified" : "not verified"}
                </span>
              </Link>
            ))}
          </>
        )}
      </div>
    </section>
  );
}
