"use client";

import { useState } from "react";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { PriorityBars, STATUS } from "@/components/issues/meta";
import { TypeIcon } from "@/components/issues/deep";
import { DueChip } from "@/components/issues/cycles";
import { Read } from "@/components/shell/unreachable";
import { inboxService } from "@/lib/api/inbox.service";
import type { IssueStatus } from "@/lib/api/issue.service";
import { timeAgo } from "@/lib/time";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";

// My issues (D-88): the open issues assigned to you across the workspace,
// by status. No board: built from IssueList's parts.

const projectCount = (list: { project_id: string }[]) => {
  const n = new Set(list.map((i) => i.project_id)).size;
  return `${n} project${n === 1 ? "" : "s"}`;
};

const ORDER: IssueStatus[] = ["in_progress", "in_review", "todo", "backlog"];

export default function MyIssuesPage() {
  useDocumentTitle("My issues · Toft");
  const mine = useQuery({ queryKey: ["inbox", "my-issues"], queryFn: () => inboxService.myIssues() });
  const [now] = useState(() => Date.now());
  const issues = mine.data ?? [];
  return (
    <div className="flex flex-col">
      <header className="-mx-4 -mt-4 mb-5 flex flex-col gap-0.5 border-b border-subtle px-4 py-4 md:-mx-8 md:-mt-8 md:px-8">
        <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">My issues</h1>
        <p className="min-h-5 text-[13px] text-muted">
          {mine.data ? `${issues.length} open, assigned to you across ${projectCount(issues)}` : null}
        </p>
      </header>
      <div className="flex w-full max-w-[1100px] flex-col gap-5">
        <Read
          query={mine}
          what="your issues"
          heading="h2"
          isEmpty={(d) => d.length === 0}
          empty={
            <p className="rounded-[10px] border border-subtle bg-surface px-4 py-6 text-sm text-fg/70">
              Nothing open is assigned to you. Issues assigned to you in any project show here.
            </p>
          }
        >
          {(d) =>
            ORDER.map((st) => {
              const list = d.filter((i) => i.status === st);
              if (list.length === 0) return null;
              const s = STATUS[st];
              return (
                <section key={st} aria-labelledby={`st-${st}`} className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
                  <h2 id={`st-${st}`} className={cn("flex items-center gap-2 border-b border-subtle px-4 py-2.5 text-[13px] font-medium", s.tint)}>
                    {s.name}
                    <span className="font-mono text-xs text-muted">{list.length}</span>
                  </h2>
                  <ul>
                    {list.map((i) => (
                      <li key={`${i.project_id}-${i.number}`} className="border-b border-subtle last:border-b-0">
                        <Link href={`/projects/${i.project_id}/issues/${i.number}`} className="flex min-h-11 flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2 text-[13px] hover:bg-raised/40">
                          <PriorityBars priority={i.priority} />
                          {i.type && <TypeIcon type={i.type} size={16} />}
                          <span className="w-20 flex-none font-mono text-xs text-muted">{i.key}</span>
                          <span className="min-w-0 flex-[1_1_240px] truncate text-fg">{i.title}</span>
                          {i.due_on && <DueChip due={i.due_on} status={i.status as IssueStatus} />}
                          <span className="flex-none text-xs text-muted">{i.project}</span>
                          <time dateTime={i.updated_at} className="w-10 flex-none text-right font-mono text-xs text-muted">
                            {timeAgo(i.updated_at, now)}
                          </time>
                        </Link>
                      </li>
                    ))}
                  </ul>
                </section>
              );
            })
          }
        </Read>
      </div>
    </div>
  );
}
