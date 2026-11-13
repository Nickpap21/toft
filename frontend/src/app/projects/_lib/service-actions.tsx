"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  ExternalLink,
  FileText,
  MoreHorizontal,
  RotateCcw,
  Settings2,
  Square,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  CapacityDialog,
  capacityWarningFrom,
  type CapacityWarning,
} from "@/components/ui/capacity-dialog";
import { EnvChip } from "@/components/ui/env-chip";
import { deploymentHref, serviceHref } from "@/components/home/home";
import { deploymentService } from "@/lib/api/deployment.service";
import type { Environment } from "@/lib/api/environment.service";
import { serviceService, type Service } from "@/lib/api/service.service";
import {
  isServing,
  isStoppable,
  statusLabel,
  statusTone,
  TONE_CLASSES,
} from "@/lib/status";
import { cn } from "@/lib/utils";
import { DeployConfirm } from "./deploy-confirm";
import { iconButton, small } from "./ui";

/** A service's state in a table: a dot and its label (the boards' rule for
 *  tables; the soft chip is for page headers). */
export function StatusText({
  status,
  className,
}: {
  status: string;
  className?: string;
}) {
  const tone = statusTone(status);
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 font-medium",
        TONE_CLASSES[tone].text,
        className,
      )}
    >
      <span
        aria-hidden="true"
        className={cn("h-1.5 w-1.5 rounded-full", TONE_CLASSES[tone].dot)}
      />
      {statusLabel(status)}
    </span>
  );
}

const DEPLOYING = new Set(["pending", "queued", "building"]);

/**
 * A service row's actions on the project boards (Project, SharedServices):
 * Follow logs while it deploys, else Deploy, and a ⋯ menu with Restart…,
 * View logs, Settings, Logs in Grafana and Stop…. Restart and Stop confirm
 * first, naming what happens (D-47).
 */
export function ServiceRowActions({
  service,
  last,
  serverName,
  env,
  domain,
  canDeploy,
  grafanaUrl,
  menuOnly,
}: {
  service: Service;
  last?: { id: string; status: string; commit_sha?: string; image_tag?: string } | null;
  serverName: string;
  /** The service's environment; a protected one confirms Deploy (D-55). */
  env?: Pick<Environment, "name" | "color" | "protected">;
  domain?: string;
  canDeploy: boolean;
  grafanaUrl?: string;
  /** The ⋯ menu alone, without Deploy or Follow logs (ReworkSharedServices). */
  menuOnly?: boolean;
}) {
  const queryClient = useQueryClient();
  const [confirm, setConfirm] = useState<"deploy" | "restart" | "stop" | null>(null);
  const [capacity, setCapacity] = useState<CapacityWarning | null>(null);
  const deployButton = useRef<HTMLButtonElement>(null);
  const href = serviceHref(service.project_id, service.id);
  const deploying =
    service.runtime_status === "deploying" ||
    (!!last && DEPLOYING.has(last.status));
  const containers =
    service.source_type === "docker-compose"
      ? "Its containers"
      : "Its container";
  const plural = service.source_type === "docker-compose";
  // Deploy waits for the service's environment: a protected one confirms.
  const unknownEnv = !!service.environment_id && !env;

  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ["service", service.id] });
    queryClient.invalidateQueries({ queryKey: ["env-services"] });
    queryClient.invalidateQueries({ queryKey: ["project-scoped-services"] });
    queryClient.invalidateQueries({ queryKey: ["org-scoped-services"] });
  };
  const deploy = useMutation({
    mutationFn: ({ force, commitSha }: { force?: boolean; commitSha?: string }) =>
      deploymentService.trigger(service.id, { commitSha }, force),
    onSuccess: () => {
      setConfirm(null);
      toast.success(`Deploying ${service.name}`);
      refresh();
    },
    onError: (err: unknown) => {
      const warning = capacityWarningFrom(err);
      if (warning) {
        setCapacity(warning);
        return;
      }
      toast.error(
        err instanceof Error && err.message
          ? `${service.name}: ${err.message}`
          : `Failed to deploy ${service.name}`,
      );
    },
  });
  const restart = useMutation({
    mutationFn: () => serviceService.restart(service.id),
    onSuccess: () => {
      setConfirm(null);
      toast.success(`${service.name} restarted`);
      refresh();
    },
    onError: (err: unknown) => {
      setConfirm(null);
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : `Failed to restart ${service.name}`,
      );
    },
  });
  const stop = useMutation({
    mutationFn: () => serviceService.stop(service.id),
    onSuccess: () => {
      setConfirm(null);
      toast.success(`${service.name} stopped`);
      refresh();
    },
    onError: (err: unknown) =>
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : `Failed to stop ${service.name}`,
      ),
  });

  return (
    <span className="inline-flex items-center gap-1.5">
      {menuOnly ? null : deploying && last ? (
        <Link
          href={deploymentHref(service.project_id, service.id, last.id)}
          className={cn(small, "h-[30px] px-2.5 text-xs font-normal rounded-md")}
        >
          Follow logs
        </Link>
      ) : canDeploy ? (
        <button
          ref={deployButton}
          type="button"
          className={cn(small, "h-[30px] px-2.5 text-xs font-normal rounded-md")}
          disabled={deploy.isPending || unknownEnv}
          onClick={() => (env?.protected ? setConfirm("deploy") : deploy.mutate({}))}
        >
          {deploy.isPending ? "Deploying…" : env?.protected ? "Deploy…" : "Deploy"}
        </button>
      ) : null}
      <DropdownMenu>
        <DropdownMenuTrigger
          className={cn(iconButton, "h-[30px] w-[30px] bg-transparent rounded-md", menuOnly && "border-transparent")}
          aria-label={`More actions for ${service.name}`}
        >
          <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-[188px]">
          {canDeploy && isServing(service.runtime_status) && (
            <DropdownMenuItem onSelect={() => setConfirm("restart")}>
              <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
              Restart…
            </DropdownMenuItem>
          )}
          <DropdownMenuItem asChild>
            <Link href={`${href}?tab=logs`}>
              <FileText className="h-3.5 w-3.5" aria-hidden="true" />
              View logs
            </Link>
          </DropdownMenuItem>
          <DropdownMenuItem asChild>
            <Link href={`${href}?tab=settings`}>
              <Settings2 className="h-3.5 w-3.5" aria-hidden="true" />
              Settings
            </Link>
          </DropdownMenuItem>
          {grafanaUrl && (
            <DropdownMenuItem asChild>
              <a href={grafanaUrl} target="_blank" rel="noopener noreferrer">
                <ExternalLink className="h-3.5 w-3.5" aria-hidden="true" />
                Logs in Grafana
              </a>
            </DropdownMenuItem>
          )}
          {canDeploy && isStoppable(service.runtime_status) && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                onSelect={() => setConfirm("stop")}
                className="text-status-failed-fg focus:text-status-failed-fg"
              >
                <Square className="h-3.5 w-3.5" aria-hidden="true" />
                Stop…
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      {env?.protected && (
        <DeployConfirm
          open={confirm === "deploy"}
          service={service}
          env={env}
          pending={deploy.isPending}
          onCancel={() => setConfirm(null)}
          onConfirm={(commitSha) => deploy.mutate({ commitSha })}
        />
      )}
      <Dialog
        open={confirm === "restart"}
        onOpenChange={(o) => !o && !restart.isPending && setConfirm(null)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="flex flex-wrap items-center gap-2">
            Restart {service.name}
            {env ? <> in <EnvChip env={env} size="md" /></> : null}?
          </DialogTitle>
            <DialogDescription>
              {containers} on {serverName} {plural ? "restart" : "restarts"} in
              place: the same version, settings and data, with a few seconds of
              downtime.
            </DialogDescription>
          </DialogHeader>
          <p className="text-[13px] leading-5 text-muted">
            A restart doesn&apos;t apply settings, variables or secrets saved
            since the live deploy. Deploy for those.
          </p>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setConfirm(null)}
              disabled={restart.isPending}
            >
              Cancel
            </Button>
            <Button
              onClick={() => restart.mutate()}
              disabled={restart.isPending}
            >
              {restart.isPending ? "Restarting…" : `Restart ${service.name}`}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={confirm === "stop"}
        onOpenChange={(o) => !o && !stop.isPending && setConfirm(null)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              Stop {service.name}
              {env ? <> in <EnvChip env={env} size="md" /></> : null}?
            </DialogTitle>
            <DialogDescription>
              {containers} on {serverName} {plural ? "stop" : "stops"}.{" "}
              {domain ? (
                <>
                  Requests to {domain} fail until you deploy {service.name}{" "}
                  again.
                </>
              ) : (
                <>Nothing of it runs until you deploy {service.name} again.</>
              )}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setConfirm(null)}
              disabled={stop.isPending}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => stop.mutate()}
              disabled={stop.isPending}
            >
              {stop.isPending ? "Stopping…" : `Stop ${service.name}`}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <CapacityDialog
        warning={capacity}
        serviceName={service.name}
        returnFocus={deployButton}
        onCancel={() => setCapacity(null)}
        onDeployAnyway={() => {
          setCapacity(null);
          deploy.mutate({ force: true, commitSha: deploy.variables?.commitSha });
        }}
      />
    </span>
  );
}
