import Link from "next/link";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";
import { deploymentHref } from "@/components/home/home";

/** "Last deploy failed": a service whose latest deploy failed while the
 *  previous version serves on, so its status still reads running. Nothing
 *  while a deploy runs, or when the last one did not fail. With projectId it
 *  links to the service's deployments; without, it is text, for use inside a link (a
 *  service card) where a second link would nest. */
export function LastDeployFailed({
  service,
  projectId,
  className,
}: {
  service: { id: string; last_deploy_failed?: boolean; runtime_status?: string };
  projectId?: string;
  className?: string;
}) {
  if (!service.last_deploy_failed || service.runtime_status === "deploying") return null;
  const classes = cn(
    "inline-flex items-center gap-1 rounded-full bg-status-failed-soft px-2 py-0.5 text-xs font-medium text-status-failed-fg",
    className,
  );
  const content = (
    <>
      <X className="h-3 w-3" aria-hidden="true" />
      Last deploy failed
    </>
  );
  if (!projectId) return <span className={classes}>{content}</span>;
  return (
    <Link
      href={deploymentHref(projectId, service.id)}
      className={cn(classes, "hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus")}
    >
      {content}
    </Link>
  );
}
