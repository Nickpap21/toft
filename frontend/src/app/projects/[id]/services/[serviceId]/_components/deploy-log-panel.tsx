"use client";

import { useRef, useEffect } from "react";
import Link from "next/link";
import { CheckCircle2, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DeployStageTracker, detectDeployStage, type PipelineType } from "@/components/ui/deploy-stage-tracker";
import type { Service } from "@/lib/api/service.service";
import { deploymentHref } from "@/components/home/home";

interface DeployLogPanelProps {
  deployLog: string[];
  deployStreaming: boolean;
  deployDone: boolean;
  deployFailed: boolean;
  deploymentId: string | null;
  service: Service;
  projectId: string;
  serviceId: string;
  onClose: () => void;
}

export function DeployLogPanel({
  deployLog,
  deployStreaming,
  deployDone,
  deployFailed,
  deploymentId,
  service,
  projectId,
  serviceId,
  onClose,
}: DeployLogPanelProps) {
  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [deployLog]);

  if (deployLog.length === 0) return null;

  return (
    <section aria-label="Deployment log" className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
      <div className="flex items-center justify-between border-b border-subtle px-4 py-2">
        <p className="text-xs font-medium text-muted">
          Deployment log
          {deploymentId && (
            <Link
              href={deploymentHref(projectId, serviceId, deploymentId)}
              className="ml-2 inline-link"
            >
              Open its page
            </Link>
          )}
        </p>
        <div className="flex items-center gap-2">
          {deployDone && <CheckCircle2 className="h-4 w-4 text-status-running" aria-label="Deployed" />}
          <Button variant="ghost" size="icon" className="h-6 w-6" onClick={onClose} aria-label="Close the deployment log">
            <X className="h-3.5 w-3.5" aria-hidden="true" />
          </Button>
        </div>
      </div>

      <div className="bg-ground">
        <DeployStageTracker
          stage={detectDeployStage(deployLog, deployDone, deployFailed, (service.source_type ?? "git") as PipelineType)}
          pipelineType={(service.source_type ?? "git") as PipelineType}
          hasComposeRepo={!!service.compose_repo_url}
        />
        <div ref={scrollRef} role="group" tabIndex={0} aria-label="Deployment log lines" className="focus-inset max-h-56 space-y-0.5 overflow-y-auto p-3 font-mono text-xs">
          {deployLog.map((line, i) => (
            <div
              key={i}
              className={
                line.toLowerCase().includes("error") || line.toLowerCase().includes("failed")
                  ? "text-status-failed-fg"
                  : line.toLowerCase().includes("success") || line.toLowerCase().includes("done")
                  ? "text-status-running-fg"
                  : "text-fg"
              }
            >
              <span className="mr-2 select-none text-muted">{String(i + 1).padStart(4, "0")}</span>
              {line}
            </div>
          ))}
          {deployStreaming && <div className="text-muted motion-safe:animate-pulse">▋</div>}
        </div>
      </div>
    </section>
  );
}
