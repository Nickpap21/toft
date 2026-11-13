"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, ChevronRight, CircleAlert, CircleDot, CircleX, Database, FileText, HardDrive, KeyRound, Server as ServerIcon, ShieldCheck, X, Zap, type LucideIcon } from "lucide-react";
import { timeAgo, type NeedsIcon, type NeedsItem } from "@/components/home/home";
import { FreeSpaceButton } from "@/components/servers/disk-card";
import { ApiError } from "@/lib/api/client";
import { inboxService } from "@/lib/api/inbox.service";
import { TONE_CLASSES, type StatusTone } from "@/lib/status";
import { cn } from "@/lib/utils";

const ICON: Record<NeedsIcon, LucideIcon> = {
  failed: X,
  service: CircleAlert,
  server: ServerIcon,
  disk: HardDrive,
  volume: Database,
  issue: CircleDot,
  doc: FileText,
  preview: ShieldCheck,
  agent: Zap,
  backup: CircleX,
  kit: KeyRound,
};

const TONE_TEXT: Record<StatusTone, string> = {
  running: "text-status-running-fg",
  building: "text-status-building-fg",
  pending: "text-status-pending-fg",
  failed: "text-status-failed-fg",
  neutral: "text-muted",
};

const focus = "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus";
const button = `touch-target inline-flex h-8 items-center justify-center gap-1.5 whitespace-nowrap rounded-lg border border-strong bg-raised px-3 text-[13px] font-medium text-fg hover:border-muted disabled:opacity-60 ${focus}`;
const age = "flex-none font-mono text-xs leading-5 text-muted";

function Tile({ item }: { item: NeedsItem }) {
  const Icon = ICON[item.icon];
  return (
    <span className={cn("flex h-8 w-8 flex-none items-center justify-center rounded-lg", TONE_CLASSES[item.tone].chip)}>
      <Icon className="h-4 w-4" aria-hidden="true" />
    </span>
  );
}

/** Needs you (board Inbox, D-88, D-140): what needs the person, in the
 *  server's order, each with one thing to do; in the inbox each can be
 *  marked done. */
export function NeedsYouRows({ items, now, onDone, done = false }: { items: NeedsItem[]; now: number; onDone?: (key: string) => void; done?: boolean }) {
  return (
    <ul className="flex flex-col">
      {items.map((item) => (
        <li key={item.key} className="flex flex-wrap items-start gap-x-3 gap-y-2 border-t border-subtle px-4 py-3 first:border-t-0">
          <Tile item={item} />
          <div className="flex min-w-0 flex-1 flex-col sm:basis-[300px]">
            <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
              <span className={cn("text-xs font-semibold", TONE_TEXT[item.tone])}>{item.kind}</span>
              <Link href={item.href} className="text-sm font-medium text-fg [overflow-wrap:anywhere] hover:underline">{item.title}</Link>
            </div>
            {item.meta && <p className="mt-0.5 line-clamp-3 text-[13px] leading-[19px] text-muted [overflow-wrap:anywhere]">{item.meta}</p>}
            {item.code && (
              <code className="mt-2 line-clamp-3 whitespace-pre-wrap break-words rounded-lg border border-subtle bg-ground px-2.5 py-2 font-mono text-xs leading-[18px] text-status-failed-fg">
                {item.code}
              </code>
            )}
            {item.related && (
              <p className="mt-2 flex flex-wrap items-center gap-1.5 text-xs text-muted">
                <CircleDot className="h-3.5 w-3.5" aria-hidden="true" />
                {item.related.label}
                <Link href={item.related.href} className="font-mono text-link hover:underline">{item.related.key}</Link>
              </p>
            )}
          </div>
          <div className="ml-auto flex items-center gap-2 max-sm:w-full max-sm:pl-11">
            <time dateTime={item.at} className={cn(age, "min-w-7 text-right max-sm:text-left")}>
              {timeAgo(item.at, now)}
            </time>
            {item.disk ? (
              <FreeSpaceButton serverId={item.disk.serverId} where={item.disk.where} className={cn(button, "max-sm:flex-1")} />
            ) : (
              <Link href={item.href} className={cn(button, "max-sm:flex-1")}>
                {item.action}
              </Link>
            )}
            {onDone && (
              <button
                type="button"
                data-mark-done
                aria-label={done ? `Move “${item.title}” back to the inbox` : item.snoozes ? `Snooze “${item.title}” for 7 days` : `Mark “${item.title}” done`}
                onClick={() => onDone(item.key)}
                className={cn("touch-target flex h-8 w-8 flex-none items-center justify-center rounded-md text-muted hover:bg-raised hover:text-fg", focus)}
              >
                <Check className="h-4 w-4" strokeWidth={2} aria-hidden="true" />
              </button>
            )}
          </div>
        </li>
      ))}
    </ul>
  );
}

/** Home's Needs you rows (board ReworkHome): the whole row opens it; on a
 *  phone a chevron takes the action's place. */
export function HomeNeedsRows({ items, now }: { items: NeedsItem[]; now: number }) {
  return (
    <ul className="flex flex-col">
      {items.map((item) => (
        <li key={item.key} className="border-t border-subtle first:border-t-0">
          <Link
            href={item.href}
            className={cn("flex items-start gap-3 px-4 py-3 hover:bg-raised/40 sm:items-center", focus)}
          >
            <Tile item={item} />
            <span className="flex min-w-0 flex-1 flex-col">
              <span className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
                <span className={cn("text-xs font-semibold", TONE_TEXT[item.tone])}>{item.kind}</span>
                <span className="text-sm font-medium text-fg [overflow-wrap:anywhere]">{item.title}</span>
              </span>
              <span className="mt-0.5 line-clamp-3 text-[13px] leading-[19px] text-muted [overflow-wrap:anywhere]">
                {[item.meta, item.code].filter(Boolean).join(" · ")}
              </span>
            </span>
            <time dateTime={item.at} className={cn(age, "min-w-7 text-right max-sm:hidden")}>{timeAgo(item.at, now)}</time>
            <span className={cn(button, "pointer-events-none max-sm:hidden")}>{item.action}</span>
            <ChevronRight className="mt-2 h-4 w-4 flex-none text-muted sm:hidden" aria-hidden="true" />
          </Link>
        </li>
      ))}
    </ul>
  );
}

/** Opens an issue from a failed deploy (D-88), or the one already open. */
export function OpenFailureIssue({ projectId, serviceId, deploymentId, className, label = "Open issue from failure" }: {
  projectId: string;
  serviceId: string;
  deploymentId: string;
  className?: string;
  label?: string;
}) {
  const router = useRouter();
  const qc = useQueryClient();
  const open = useMutation({
    mutationFn: () => inboxService.openFromFailure(serviceId, deploymentId),
    onSuccess: (issue) => {
      qc.invalidateQueries({ queryKey: ["inbox"] });
      router.push(`/projects/${projectId}/issues/${issue.number}`);
    },
    onError: (err) => {
      const data = err instanceof ApiError ? (err.body as { data?: { code?: string; key?: string } } | null)?.data : undefined;
      if (data?.code === "already_open" && data.key) {
        router.push(`/projects/${projectId}/issues/${data.key.split("-").pop()?.replace(/^0+/, "")}`);
        return;
      }
      toast.error(err instanceof ApiError ? err.message : "Couldn’t open the issue");
    },
  });
  return (
    <button type="button" onClick={() => open.mutate()} disabled={open.isPending} className={className}>
      {label}
    </button>
  );
}
