"use client";

import { useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { Layers, MoreHorizontal } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { deploymentHref, serviceHref } from "@/components/home/home";
import { useRedeploy } from "@/app/projects/_lib/redeploy";
import { readFailed } from "@/components/shell/unreachable";
import { allDeploymentsQuery, type Deployment } from "@/lib/api/deployment.service";
import type { Service } from "@/lib/api/service.service";
import { TONE_CLASSES } from "@/lib/status";
import { ago } from "@/lib/time";
import { cn } from "@/lib/utils";
import { RollbackDialog } from "../rollback-dialog";
import { deploymentRef, deploymentRefTitle, inProgress, repoPath } from "../service-view";
import {
  canRollBack,
  deployAgain,
  deployLine,
  filterDeployments,
  howDeploys,
  IMAGE_REMOVED_SHORT,
  noMatchText,
  rollbackRemoved,
  rowButton,
  rowState,
  STATUS_FILTERS,
  wasRefused,
  type StatusFilter,
} from "../deployment-view";
import { DeploymentDetail } from "./deployment-detail";

// Deploys (board: private/design/canvas/ReworkDeploys.dc.html; D-44): every
// deploy the panel keeps, newest first, with how the service deploys
// beside them. With ?deployment=<id>, that deployment's page
// (ReworkDeployment).

const PER_PAGE = 25;

interface DeploymentsTabProps {
  service: Service;
  projectId: string;
  /** The service's environment, for whether it asks before a deploy. */
  env?: { name: string; protected?: boolean };
  /** Where the service runs, for the deployment page's Details. */
  serverName?: string;
  /** The deployment whose page is open, from ?deployment=. */
  deploymentId: string;
  canDeploy: boolean;
  onDeploy: (opts?: { commitSha?: string }) => void;
  deployBusy: boolean;
}

export function DeploymentsTab(props: DeploymentsTabProps) {
  const { service, projectId, env, deploymentId, canDeploy, onDeploy, deployBusy } = props;
  const serviceId = service.id;
  const read = useQuery({ ...allDeploymentsQuery(serviceId), refetchInterval: 60_000 });
  const { data, isLoading, refetch, dataUpdatedAt } = read;
  const [status, setStatus] = useState<StatusFilter>("all");
  const [shownCount, setShownCount] = useState(PER_PAGE);
  const [rollbackTo, setRollbackTo] = useState<Deployment | null>(null);
  const { redeploy, dialog: redeployDialog } = useRedeploy();
  // ⌘K's Roll back… opens the rollback to the newest version that can roll
  // back (?rollback=1, D-50).
  const searchParams = useSearchParams();
  const router = useRouter();
  const wantsRollback = canDeploy && searchParams.get("rollback") === "1";

  if (deploymentId) return <DeploymentDetail {...props} />;

  const all = data ?? [];
  const activeId = service.active_deployment_id;
  const liveId = activeId ?? all.find((d) => d.status === "running")?.id;
  const liveKnown = !!activeId;
  const live = all.find((d) => d.id === liveId) ?? null;
  const latest = wantsRollback ? (all.find((d) => canRollBack(d, liveId, live, liveKnown)) ?? null) : null;
  const closeRollback = () => {
    setRollbackTo(null);
    if (!searchParams.has("rollback")) return;
    const next = new URLSearchParams(searchParams.toString());
    next.delete("rollback");
    router.replace(`?${next.toString()}`, { scroll: false });
  };
  const { rows, counts } = filterDeployments(all, status, liveId, liveKnown);
  const shown = rows.slice(0, shownCount);
  const now = dataUpdatedAt;

  if (readFailed(read)) {
    return (
      <div role="alert" className="flex flex-wrap items-center justify-between gap-3 rounded-[10px] border border-subtle bg-surface px-4 py-3.5 text-[13px]">
        <span className="text-fg">Couldn&apos;t load the deploys of {service.name}.</span>
        <button type="button" className={rowButton} onClick={() => void refetch()}>Retry</button>
      </div>
    );
  }
  if (isLoading) {
    return <div role="status" className="h-64 rounded-[10px] border border-subtle bg-surface motion-safe:animate-pulse"><span className="sr-only">Loading deploys</span></div>;
  }
  if (all.length === 0) return <NotDeployed service={service} projectId={projectId} canDeploy={canDeploy} onDeploy={onDeploy} deployBusy={deployBusy} />;

  const pick = (s: StatusFilter) => { setStatus(s); setShownCount(PER_PAGE); };
  const copySha = (d: Deployment) => {
    if (!navigator.clipboard) {
      toast.error("Couldn’t copy: this page isn’t on HTTPS");
      return;
    }
    navigator.clipboard.writeText(d.commit_sha || d.image_tag).then(
      () => toast.success(d.commit_sha ? "Commit SHA copied" : "Image copied"),
      () => toast.error("Couldn’t copy: the browser refused"),
    );
  };
  const td = "border-t border-subtle px-4 py-2.5 align-middle max-sm:border-0 max-sm:p-0";

  return (
    <div className="flex flex-wrap items-start gap-6">
      {wantsRollback && !latest && (
        <p role="status" className="basis-full text-[13px] text-fg/85">
          Nothing to roll back to: no earlier version of {service.name} differs from the one live now.
        </p>
      )}

      <section aria-labelledby="dl-h" className="min-w-0 flex-[999_1_560px] overflow-hidden rounded-[10px] border border-subtle bg-surface">
        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
          <div className="flex items-baseline gap-2">
            <h2 id="dl-h" className="text-[15px] font-semibold leading-[22px] text-fg">Deploys</h2>
            <span className="text-xs text-muted">{all.length} · newest first</span>
          </div>
          <span role="group" aria-label="Status" className="flex flex-wrap gap-1.5">
            {STATUS_FILTERS.map((f) => (
              <button
                key={f.key}
                type="button"
                aria-pressed={status === f.key}
                onClick={() => pick(f.key)}
                className={cn(
                  "inline-flex h-[30px] items-center gap-1.5 rounded-full border px-2.5 text-[13px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:h-11",
                  status === f.key ? "border-accent bg-accent/20 text-fg" : "border-strong text-fg/70 hover:text-fg",
                )}
              >
                {f.label}
                <span className="font-mono text-xs text-muted">{counts[f.key]}</span>
              </button>
            ))}
          </span>
        </div>

        <table aria-label={`Deploys of ${service.name}`} className="w-full table-fixed border-collapse text-[13px] max-sm:block">
          <thead className="max-sm:hidden">
            <tr className="text-left text-xs text-muted">
              <th scope="col" className="w-[122px] px-4 py-2 font-medium">Status</th>
              <th scope="col" className="px-4 py-2 font-medium">Commit</th>
              <th scope="col" className="w-[104px] px-4 py-2 font-medium">When</th>
              <th scope="col" className="w-[118px] px-4 py-2"><span className="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody className="max-sm:block">
            {shown.map((d) => {
              const state = rowState(d, liveId, liveKnown);
              const ref = deploymentRef(d) || d.id.slice(0, 8);
              const href = deploymentHref(projectId, serviceId, d.id);
              const rollback = canDeploy && canRollBack(d, liveId, live, liveKnown);
              const removed = canDeploy && rollbackRemoved(d, liveId, live, liveKnown);
              const stopped = d.status === "failed";
              const again = stopped && !wasRefused(d) && canDeploy ? deployAgain(d, service) : null;
              // The live one runs again with today's variables (7g step 8).
              const redeployable = canDeploy && state.label === "Live" && !!d.image_tag && !d.image_removed;
              return (
                <tr key={d.id} className="max-sm:relative max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1 max-sm:border-t max-sm:border-subtle max-sm:px-4 max-sm:py-3 max-sm:first:border-t-0">
                  <td className={cn(td, "whitespace-nowrap max-sm:col-start-1 max-sm:row-start-1")}>
                    <span className={cn("inline-flex items-center gap-1.5 font-medium", TONE_CLASSES[state.tone].text)}>
                      <span className={cn("h-1.5 w-1.5 rounded-full", TONE_CLASSES[state.tone].dot)} aria-hidden="true" />
                      {state.label}
                    </span>
                  </td>
                  <td className={cn(td, "max-sm:col-start-1 max-sm:row-start-2")}>
                    {/* On phones the commit's link covers the card, under the ⋯. */}
                    <Link href={href} title={deploymentRefTitle(d)} className="[overflow-wrap:anywhere] hover:underline max-sm:after:absolute max-sm:after:inset-0 max-sm:after:content-['']">
                      <span className="mr-2 font-mono text-xs text-fg">{ref}</span>
                      {d.commit_message && <span className="text-fg/85">{d.commit_message}</span>}
                    </Link>
                    <span className="block text-xs leading-[18px] text-muted [overflow-wrap:anywhere]">{deployLine(d, now)}</span>
                    {removed && <span id={`rb-why-${d.id}`} className="block text-xs leading-[18px] text-muted">{IMAGE_REMOVED_SHORT}</span>}
                  </td>
                  <td className={cn(td, "whitespace-nowrap text-muted max-sm:col-start-1 max-sm:row-start-3")}>
                    <time dateTime={d.created_at} title={d.created_at}>{ago(d.created_at, now)}</time>
                  </td>
                  <td className={cn(td, "whitespace-nowrap text-right max-sm:col-start-2 max-sm:row-span-3 max-sm:row-start-1 max-sm:self-center")}>
                    <span className="max-sm:hidden">
                      {stopped ? (
                        <Link href={href} className={rowButton} aria-label={`Why deployment ${ref} ${wasRefused(d) ? "was refused" : "stopped"}`}>Why</Link>
                      ) : inProgress(d) ? (
                        <Link href={href} className={rowButton}>Follow log</Link>
                      ) : rollback ? (
                        <button type="button" aria-label={`Roll back ${service.name} to ${ref}`} className={rowButton} onClick={() => setRollbackTo(d)}>Roll back</button>
                      ) : removed ? (
                        <button type="button" aria-disabled="true" aria-label={`Roll back ${service.name} to ${ref}`} aria-describedby={`rb-why-${d.id}`}
                          className={cn(rowButton, "cursor-not-allowed text-muted opacity-60")}>Roll back</button>
                      ) : null}
                    </span>
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <button
                          type="button"
                          aria-label={`Actions for deployment ${ref}`}
                          className="relative z-[1] -my-1.5 -mr-2 inline-flex h-11 w-11 items-center justify-center rounded-lg text-muted hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus sm:hidden"
                        >
                          <MoreHorizontal className="h-[18px] w-[18px]" aria-hidden="true" />
                        </button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem asChild>
                          <Link href={href}>{stopped ? (wasRefused(d) ? "Why it was refused" : "Why it stopped") : inProgress(d) ? "Follow log" : "Open deployment"}</Link>
                        </DropdownMenuItem>
                        {again && <DropdownMenuItem disabled={deployBusy} onSelect={() => onDeploy(again.opts)}>Deploy {again.ref} again</DropdownMenuItem>}
                        {rollback && <DropdownMenuItem onSelect={() => setRollbackTo(d)}>Roll back to {ref}</DropdownMenuItem>}
                        {redeployable && <DropdownMenuItem onSelect={() => void redeploy(serviceId, d.id)}>Redeploy {ref}</DropdownMenuItem>}
                        <DropdownMenuItem onSelect={() => copySha(d)}>{d.commit_sha ? "Copy SHA" : "Copy image"}</DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </td>
                </tr>
              );
            })}
            {shown.length === 0 && (
              <tr className="max-sm:block">
                <td colSpan={4} className="border-t border-subtle px-4 py-5 max-sm:block">
                  <span className="flex flex-wrap items-center justify-between gap-3">
                    <span className="text-muted">{noMatchText(status)}</span>
                    <button type="button" className={rowButton} onClick={() => pick("all")}>Show all</button>
                  </span>
                </td>
              </tr>
            )}
          </tbody>
        </table>

        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
          <span>Toft keeps the last 200 deploys of {service.name}, with their logs</span>
          {rows.length > shown.length && (
            <button type="button" className={cn(rowButton, "pointer-coarse:h-11")} onClick={() => setShownCount((n) => n + PER_PAGE)}>Show older</button>
          )}
        </div>
      </section>

      <aside className="flex min-w-0 flex-[1_1_320px] flex-col gap-6">
        <section aria-labelledby="how-h" className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
          <div className="border-b border-subtle px-4 py-3">
            <h2 id="how-h" className="text-[15px] font-semibold leading-[22px] text-fg">How {service.name} deploys</h2>
          </div>
          <dl className="text-[13px]">
            {howDeploys(service, env).map(([k, v], i) => (
              <div key={k} className={cn("grid grid-cols-[110px_minmax(0,1fr)] gap-3 px-4 py-2.5 max-sm:grid-cols-1 max-sm:gap-1", i > 0 && "border-t border-subtle")}>
                <dt className="text-muted">{k}</dt>
                <dd className="text-fg">
                  {v.split("`").map((part, j) => (j % 2 ? <span key={j} className="font-mono text-xs">{part}</span> : part))}
                </dd>
              </div>
            ))}
          </dl>
          <div className="border-t border-subtle px-4 py-2.5 text-[13px]">
            <Link href={`${serviceHref(projectId, serviceId)}?tab=settings#set-deploy`} className="text-link hover:underline">Deploy settings</Link>
          </div>
        </section>
      </aside>

      <RollbackDialog service={service} target={rollbackTo ?? latest} live={live} onClose={closeRollback} />
      {redeployDialog}
    </div>
  );
}

/** The board's empty view: what a first deploy does, and where it starts. */
function NotDeployed({ service, projectId, canDeploy, onDeploy, deployBusy }: Pick<DeploymentsTabProps, "service" | "projectId" | "canDeploy" | "onDeploy" | "deployBusy">) {
  const isGit = service.source_type === "git";
  const branch = service.git_branch || "the branch";
  const what =
    isGit ? `Deploying ${branch} clones ${repoPath(service.git_repo_url ?? "")}${service.base_directory ? ` (${service.base_directory})` : ""}, builds it${service.build_pack === "nixpacks" ? " with Nixpacks" : service.build_pack === "dockerfile" ? " with its Dockerfile" : ""} and starts it.`
    : service.source_type === "docker-image" ? `Deploying pulls ${service.docker_image ?? "the image"} and starts it.`
    : `Deploying runs the compose ${service.compose_repo_url ? `file from ${repoPath(service.compose_repo_url)}` : "file"} and starts its services.`;
  return (
    <section aria-labelledby="empty-h" className="flex flex-wrap items-start gap-4 rounded-[10px] border border-subtle bg-surface p-6">
      <span className="flex h-10 w-10 flex-none items-center justify-center rounded-lg border border-strong bg-raised text-muted">
        <Layers className="h-[18px] w-[18px]" aria-hidden="true" />
      </span>
      <div className="flex min-w-0 flex-[1_1_360px] flex-col gap-1.5">
        <h2 id="empty-h" className="text-[15px] font-semibold leading-[22px] text-fg">{service.name} hasn&apos;t been deployed yet</h2>
        <p className="max-w-[640px] text-[13px] text-muted">
          {what} Each deployment then gets a row here and a page of its own, with its pipeline and log.
          {isGit && ` A push to ${branch} deploys too, once the project's webhook is set up.`}
        </p>
        {canDeploy && (
          <div className="mt-2 flex flex-wrap gap-2">
            <Button size="sm" className="h-9 gap-1.5 rounded-lg px-3.5 text-[13px]" disabled={deployBusy} onClick={() => onDeploy()}>
              <Layers className="h-4 w-4" aria-hidden="true" />
              {isGit && service.git_branch ? `Deploy ${service.git_branch}` : "Deploy"}
            </Button>
            {isGit && (
              <Link href={`${serviceHref(projectId, service.id)}?tab=overview`} className={cn(rowButton, "h-9 rounded-lg px-3 text-[13px] font-medium")}>
                Set up deploy on push
              </Link>
            )}
          </div>
        )}
      </div>
    </section>
  );
}
