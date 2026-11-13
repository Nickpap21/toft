"use client";

import Link from "next/link";
import { GitMerge } from "lucide-react";
import type { Deployment } from "@/lib/api/deployment.service";
import { cn } from "@/lib/utils";
import { repoBase, useRepos } from "@/components/code/repo";
import { Glyph, ICON, issueHref } from "./meta";

const chip = "inline-flex h-6 items-center gap-1.5 rounded-md border border-strong px-2 text-xs text-fg hover:bg-raised";

/** What a merge's deploy carries (boards Service and Deployment; D-77,
 *  D-81): "!40 merged", then each linked issue, "STO-111 shipped" or
 *  "STO-118 ships when live". */
export function DeployWork({ d, projectId, className }: { d: Deployment; projectId: string; className?: string }) {
  const { repos } = useRepos(d.pull_request ? projectId : "");
  const pull = d.pull_request;
  if (!pull) return null;
  const repo = repos?.find((r) => r.id === pull.repository_id);
  return (
    <span className={cn("inline-flex flex-wrap items-center gap-1.5", className)}>
      <Link href={repo ? `${repoBase(projectId, repo.name)}/pulls/${pull.number}` : "#"} className={chip}>
        <GitMerge className="h-3.5 w-3.5 text-env-violet-fg" aria-hidden="true" />!{pull.number} merged
      </Link>
      {(pull.issues ?? []).map((i) => {
        const shipped = i.status === "shipped";
        return (
          <Link key={i.key} href={issueHref(projectId, i.number)} className={chip}>
            <Glyph d={shipped ? ICON.check : ICON.dot} size={13} className={shipped ? "text-status-running" : "text-status-pending"} />
            {i.key} {shipped ? "shipped" : i.status === "cancelled" ? "cancelled" : "ships when live"}
          </Link>
        );
      })}
    </span>
  );
}
