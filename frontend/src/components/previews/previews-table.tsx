"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { MoreHorizontal } from "lucide-react";
import { previewService, type Preview } from "@/lib/api/preview.service";
import { pullHref } from "@/components/code/repo";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { ago } from "@/lib/time";
import { cn } from "@/lib/utils";
import { card, h2, iconButton, mono } from "@/app/projects/_lib/ui";
import { ServiceMark, svcRow, svcTd, svcTh } from "@/app/projects/_lib/service-table";
import { firstAddress, STATUS, statusLabel, statusWhy, TONE_TEXT } from "./view";

/** A project's open previews (board ReworkServices, D-97), in the same
 *  shape as its services. Shown once previews are on, or while any is left. */
export function ProjectPreviewsSection({ projectId, repoNames, now }: {
  projectId: string;
  /** Repository ID → name, for the pull requests' links. */
  repoNames: Map<string, string>;
  now: number;
}) {
  const settingsRead = useQuery({
    queryKey: ["preview-settings", projectId],
    queryFn: () => previewService.settings(projectId),
  });
  const settings = settingsRead.data;
  const previewsRead = useQuery({
    queryKey: ["project-previews", projectId],
    queryFn: () => previewService.list(projectId).then((p) => p.data),
    refetchInterval: (q) => ((q.state.data ?? []).some((p) => p.status === "starting" || p.status === "updating" || p.status === "removing") ? 5000 : false),
  });
  const previews = previewsRead.data;
  // Without either answer, "are there any, and do they start?" is unknown;
  // rows that were read still stand when only the settings failed.
  const failed = readFailed(previewsRead) ? previewsRead : readFailed(settingsRead) && !previews?.length ? settingsRead : null;
  if (!failed && !settings?.environment_id && !previews?.length) return null;
  const list = previews ?? [];
  // Before the repositories load, or when one is gone: the Code tab.
  const href = (p: Preview) => {
    const repo = repoNames.get(p.repository_id);
    return repo ? pullHref(`/projects/${projectId}/code/${encodeURIComponent(repo)}`, p.pull_number) : `/projects/${projectId}/code`;
  };

  return (
    <section aria-labelledby="pv-h" className={card}>
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
          <h2 id="pv-h" className={h2}>Previews</h2>
          <span className="text-xs text-muted">one per open pull request, removed when it merges or closes</span>
        </div>
        <Link href={`/projects/${projectId}/settings`} className="text-[13px] text-link hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center">Preview settings</Link>
      </div>
      {failed ? (
        <div className="px-4 py-3.5">
          <LoadFailed what="the previews" onRetry={() => void failed.refetch()} />
        </div>
      ) : !previews ? null : list.length === 0 ? (
        <p className="px-4 py-3.5 text-[13px] text-fg/70">No open pull request has a preview yet. The next one opened or pushed to gets one.</p>
      ) : (
        <table aria-label="Previews" className="w-full border-collapse text-[13px] max-sm:block sm:table-fixed">
          <thead className="max-sm:hidden">
            <tr>
              <th scope="col" className={svcTh}>Preview</th>
              <th scope="col" className={cn(svcTh, "w-[150px]")}>Status</th>
              <th scope="col" className={cn(svcTh, "w-[210px]")}>Last deploy</th>
              <th scope="col" className={cn(svcTh, "w-[124px]")}><span className="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody className="max-sm:block">
            {list.map((p) => {
              const name = p.environment?.name ?? `pr-${p.pull_number}`;
              const addr = firstAddress(p);
              const sha = p.untrusted && p.approved_sha ? p.approved_sha : p.head_sha;
              const why = statusWhy(p, settings?.max_per_project);
              return (
                <tr key={`${p.repository_id}-${p.pull_number}`} className={svcRow}>
                  <td className={cn(svcTd, "max-sm:col-start-1 max-sm:row-start-1")}>
                    <span className="flex min-w-0 items-start gap-2.5">
                      <ServiceMark />
                      <span className="flex min-w-0 flex-col gap-0.5">
                        <Link href={href(p)} className="font-medium text-fg hover:underline max-sm:after:absolute max-sm:after:inset-0 max-sm:after:content-['']">{name}</Link>
                        <span className="text-xs text-fg/75 [overflow-wrap:anywhere]">
                          {[p.services.map((s) => s.name).join(", "), `!${p.pull_number} ${p.title}`, p.untrusted ? "Sandboxed" : ""].filter(Boolean).join(" · ")}
                        </span>
                        {addr && p.status !== "stopped" && (
                          <span className="min-w-0">
                            <a href={`https://${addr.url}`} target="_blank" rel="noreferrer" className={cn(mono, "relative z-[1] text-fg [overflow-wrap:anywhere] hover:underline")}>{addr.url}</a>
                            {addr.more > 0 && <span className="text-xs text-muted"> +{addr.more}</span>}
                          </span>
                        )}
                      </span>
                    </span>
                  </td>
                  <td className={cn(svcTd, "max-sm:col-start-2 max-sm:row-start-1 max-sm:text-right")}>
                    <span className="flex flex-col gap-px max-sm:items-end">
                      <span className={cn("inline-flex items-center gap-1.5 whitespace-nowrap font-medium", TONE_TEXT[STATUS[p.status].tone])}>
                        <span className="h-1.5 w-1.5 rounded-full bg-current" aria-hidden="true" />
                        {statusLabel(p)}
                      </span>
                      {why && <span className="text-xs text-muted">{why}</span>}
                    </span>
                  </td>
                  <td className={cn(svcTd, "whitespace-nowrap text-muted sm:truncate max-sm:col-start-1 max-sm:row-start-2 max-sm:self-center")}>
                    <span className={cn(mono, "mr-2 text-fg")}>{sha.slice(0, 7)}</span>
                    {ago(p.updated_at, now)}
                  </td>
                  <td className={cn(svcTd, "whitespace-nowrap py-1.5 text-right max-sm:relative max-sm:z-[1] max-sm:col-start-2 max-sm:row-start-2")}>
                    <DropdownMenu>
                      <DropdownMenuTrigger
                        className={cn(iconButton, "h-[30px] w-[30px] rounded-md bg-transparent")}
                        aria-label={`Actions for ${name}`}
                      >
                        <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem asChild>
                          <Link href={href(p)}>{p.needs_approval && p.status === "needs_approval" ? `Approve in !${p.pull_number}…` : `Open !${p.pull_number}`}</Link>
                        </DropdownMenuItem>
                        {addr && p.status !== "stopped" && (
                          <DropdownMenuItem asChild>
                            <a href={`https://${addr.url}`} target="_blank" rel="noreferrer">Open {addr.url}</a>
                          </DropdownMenuItem>
                        )}
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </section>
  );
}
