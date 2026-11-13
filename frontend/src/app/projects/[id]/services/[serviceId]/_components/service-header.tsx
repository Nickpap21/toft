"use client";

import Link from "next/link";
import { ArrowUp, Box, ChevronDown, Layers, MoreHorizontal, RotateCcw, SquareTerminal } from "lucide-react";
import { Button } from "@/components/ui/button";
import { StatusBadge } from "@/components/ui/status-badge";
import { LastDeployFailed } from "@/components/ui/last-deploy-failed";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { Service } from "@/lib/api/service.service";
import type { Environment } from "@/lib/api/environment.service";
import type { Server } from "@/lib/api/server.service";
import type { Domain } from "@/lib/api/domain.service";
import { TemporaryChip, domainURL } from "@/components/ui/temporary-chip";
import type { ResourceRow } from "@/lib/api/resources.service";
import { serviceHref } from "@/components/home/home";
import { deployTarget, kindText, limitsText, sourceText } from "./service-view";
import { useRepos } from "@/components/code/repo";

interface ServiceHeaderProps {
  service: Service;
  currentEnv?: Environment;
  server?: Server;
  domain?: Domain;
  /** A compose stack's limits, one row per compose service. */
  composeLimits?: ResourceRow[];
  /** A compose stack's own IMAGE_TAG variable, which Deploy runs. */
  imageTag?: string;
  projectId: string;
  canDeploy: boolean;
  isDeploying: boolean;
  /** Something of the service runs, so it can restart or stop. */
  isRunning: boolean;
  deployPending: boolean;
  /** Unsaved variables on the page: Save is the one primary action (D-145). */
  deploySecondary?: boolean;
  restartPending: boolean;
  stopPending: boolean;
  updateAvailable: boolean;
  /** Opens "Deploy a version"; absent for services without versions. */
  onVersions?: () => void;
  onDeploy: () => void;
  onRestart: () => void;
  onStop: () => void;
  onShell: () => void;
}

const btn = "h-9 gap-1.5 rounded-lg px-3 text-[13px] font-medium";

export function ServiceHeader({
  service,
  currentEnv,
  server,
  domain,
  composeLimits,
  imageTag,
  projectId,
  canDeploy,
  isDeploying,
  isRunning,
  deployPending,
  deploySecondary,
  restartPending,
  stopPending,
  updateAvailable,
  onVersions,
  onDeploy,
  onRestart,
  onStop,
  onShell,
}: ServiceHeaderProps) {
  // The project's repository it builds from, by name (D-77).
  const { repos } = useRepos(service.repository_id ? projectId : "");
  const busy = isDeploying || deployPending;
  // A protected environment confirms first (D-55), so the label says so.
  const ask = currentEnv?.protected ? "…" : "";
  const target = deployTarget(service, imageTag);
  const deployLabel = busy ? "Deploying…" : target ? `Deploy ${target}${ask}` : `Deploy${ask}`;
  const isCompose = service.source_type === "docker-compose";
  const limits = limitsText(service.cpu_limit, service.memory_limit);
  const settingsHref = `${serviceHref(projectId, service.id)}?tab=settings#set-resources`;
  const label = "text-muted";
  const value = "font-mono text-xs text-fg [overflow-wrap:anywhere]";

  return (
    <div className="flex flex-col gap-3">
      <p className="text-[13px] text-muted">
        <Link href={projectId ? `/projects/${projectId}/services` : "/shared"} className="hover:text-fg hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center">
          {projectId ? "Services" : "Workspace services"}
        </Link>
        <span aria-hidden="true" className="mx-1.5">/</span>
        {kindText(service)}
      </p>
      <div className="flex flex-wrap items-center justify-between gap-3">
        {/* A long name truncates; the actions keep their line. */}
        <div className="flex min-w-0 flex-1 items-center gap-3 max-md:flex-wrap [&>*:not(h1)]:flex-none">
          <span className="flex h-8 w-8 flex-none items-center justify-center rounded-lg border border-strong bg-raised text-muted">
            <Box className="h-4 w-4" aria-hidden="true" />
          </span>
          <h1 className="min-w-0 truncate text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">{service.name}</h1>
          <StatusBadge status={isDeploying ? "deploying" : service.runtime_status} />
          {!isDeploying && <LastDeployFailed service={service} projectId={projectId} />}
          {updateAvailable && onVersions && (
            <button
              type="button"
              onClick={onVersions}
              className="inline-flex items-center gap-1 rounded-full bg-status-pending-soft px-2 py-0.5 text-xs font-medium pointer-coarse:min-h-11 text-status-pending-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
            >
              <ArrowUp className="h-3 w-3" aria-hidden="true" />
              Update available
            </button>
          )}
        </div>

        {/* On phones: Deploy full width, Shell, Restart and Stop under ⋯. */}
        <div className="flex flex-none flex-wrap items-center gap-2 max-md:w-full">
          <Button variant="outline" size="sm" className={`${btn} ${canDeploy ? "max-md:hidden" : ""}`} onClick={onShell}>
            <SquareTerminal className="h-4 w-4" aria-hidden="true" />
            Shell
          </Button>
          {canDeploy && (
            <>
              <Button
                variant="outline"
                size="sm"
                className={`${btn} max-md:hidden`}
                disabled={!isRunning || busy || restartPending}
                onClick={onRestart}
                title={!isRunning ? "Nothing runs: deploy to start it" : busy ? "Wait for the deploy to finish" : undefined}
              >
                <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
                {restartPending ? "Restarting…" : "Restart"}
              </Button>
              <div className="inline-flex max-md:flex-1">
                <Button
                  size="sm"
                  variant={deploySecondary ? "outline" : "default"}
                  className={`${btn} rounded-r-none max-md:h-11 max-md:flex-1`}
                  disabled={busy}
                  onClick={onDeploy}
                  title={service.effective_deploy_strategy === "recreate"
                    ? "Stop then start: a few seconds of downtime while the previous container stops and the new one starts"
                    : undefined}
                >
                  <Layers className="h-4 w-4" aria-hidden="true" />
                  {deployLabel}
                </Button>
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button size="sm" variant={deploySecondary ? "outline" : "default"} className={`h-9 rounded-l-none px-2 max-md:h-11 max-md:w-11 ${deploySecondary ? "border-l-0" : "border-l border-on-accent/25"}`} aria-label="More deploy actions">
                      <ChevronDown className="h-4 w-4" aria-hidden="true" />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    {onVersions && (
                      <>
                        <DropdownMenuItem disabled={busy} onSelect={onVersions}>Deploy a version…</DropdownMenuItem>
                        <DropdownMenuSeparator />
                      </>
                    )}
                    <DropdownMenuItem
                      disabled={!isRunning || stopPending}
                      onSelect={onStop}
                      className="text-status-failed-fg focus:text-status-failed-fg"
                    >
                      Stop {service.name}…
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="outline" size="sm" className="h-11 w-11 rounded-lg p-0 md:hidden" aria-label="More actions: shell, restart, stop">
                    <MoreHorizontal className="h-[18px] w-[18px]" aria-hidden="true" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onSelect={onShell}>Shell</DropdownMenuItem>
                  <DropdownMenuItem disabled={!isRunning || busy || restartPending} onSelect={onRestart}>Restart</DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem
                    disabled={!isRunning || stopPending}
                    onSelect={onStop}
                    className="text-status-failed-fg focus:text-status-failed-fg"
                  >
                    Stop {service.name}…
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </>
          )}
        </div>
      </div>

      <dl className="flex flex-wrap items-baseline gap-x-6 gap-y-1.5 text-[13px] max-md:flex-col max-md:flex-nowrap max-md:items-stretch">
        <div className="flex items-baseline gap-2">
          <dt className={label}>Domain</dt>
          {domain ? (
            <dd className="flex items-baseline gap-1.5">
              <a
                href={domainURL(domain)}
                target="_blank"
                rel="noreferrer"
                className={`${value} underline underline-offset-2 hover:text-link`}
              >
                {domain.hostname}
              </a>
              {domain.temporary && <TemporaryChip className="self-center" />}
              {/* Every router is HTTPS with a Let's Encrypt certificate; a
                  temporary name answers over HTTP until it has one (D-150). */}
              {domain.temporary && !domain.certificate_at ? (
                <span className="text-xs text-status-pending-fg">certificate pending</span>
              ) : (
                <span className="text-xs text-status-running-fg">TLS</span>
              )}
            </dd>
          ) : (
            <dd className="text-muted">none</dd>
          )}
        </div>
        <div className="flex min-w-0 items-baseline gap-2">
          <dt className={label}>Source</dt>
          <dd className={`${value} truncate`}>{sourceText(service, repos?.find((r) => r.id === service.repository_id)?.full_name)}</dd>
        </div>
        <div className="flex items-baseline gap-2">
          <dt className={label}>Server</dt>
          <dd className={value}>
            {!server
              ? "the panel host"
              : server.shared
              ? <>{server.name} <span className="font-sans text-[13px] text-muted">· shared by {server.shared_by}</span></>
              : server.name}
          </dd>
        </div>
        <div className="flex min-w-0 items-baseline gap-2">
          <dt className={label}>Limits</dt>
          <dd className={value}>
            {isCompose ? (
              composeLimits && composeLimits.length > 0 ? (
                composeLimits.map((r, i) => (
                  <span key={r.compose_service}>
                    {i > 0 && " · "}
                    {r.compose_service}{" "}
                    {r.memory.limit ? (
                      limitsText(r.cpu.limit, r.memory.limit)
                    ) : (
                      <Link href={settingsHref} className="font-sans text-[13px] text-status-pending-fg underline-offset-4 hover:underline">
                        none set
                      </Link>
                    )}
                  </span>
                ))
              ) : (
                <span className="font-sans text-[13px] text-muted">per compose service, in Settings</span>
              )
            ) : (
              limits || (
                <Link href={settingsHref} className="font-sans text-[13px] text-status-pending-fg underline-offset-4 hover:underline">
                  none set
                </Link>
              )
            )}
          </dd>
        </div>
      </dl>
    </div>
  );
}
