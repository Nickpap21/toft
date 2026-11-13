"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, Copy, Database, GitPullRequest, Layers, Play, Square } from "lucide-react";
import { useIsOperator } from "@/context/AuthContext";
import { NEEDS_PASSWORD, useReauth } from "@/components/reauth";
import { STATUS } from "@/components/issues/meta";
import { ApiError } from "@/lib/api/client";
import { docService, type DocBlock, type DocPage, type DocService } from "@/lib/api/doc.service";
import type { IssueStatus } from "@/lib/api/issue.service";
import { statusLabel, statusTone, TONE_CLASSES } from "@/lib/status";
import { ago } from "@/lib/time";
import { cn } from "@/lib/utils";

const DATABASES = /postgres|mysql|mariadb|redis|valkey|mongo|clickhouse/i;
const mono = "font-mono text-xs";

/** A live service block (board: Docs, DocsEditor): the service as it is now. */
export function ServiceBlock({ svc, projectId, now }: { svc?: DocService; projectId: string; now: number }) {
  if (!svc) {
    return (
      <p className="rounded-lg border border-dashed border-subtle px-3.5 py-3 text-sm text-muted">
        A service this page names is gone, or isn’t this project’s.
      </p>
    );
  }
  const Icon = DATABASES.test(svc.image || svc.name) ? Database : Layers;
  const tone = statusTone(svc.status);
  const facts = [
    svc.image || (svc.commit ? svc.commit.slice(0, 7) : ""),
    svc.server ?? "panel server",
    svc.deployed_at ? `deployed ${ago(svc.deployed_at, now)}` : "",
    svc.secret_set ? `${svc.secret_set.key} set ${ago(svc.secret_set.at, now)}` : "",
  ].filter(Boolean);
  return (
    <figure className="flex flex-wrap items-center gap-3.5 rounded-lg border border-subtle bg-surface px-3.5 py-3">
      <span aria-hidden="true" className="flex h-8 w-8 items-center justify-center rounded-lg bg-raised text-fg/75">
        <Icon className="h-4 w-4" strokeWidth={1.8} />
      </span>
      <div className="flex min-w-0 flex-[1_1_260px] flex-col gap-0.5">
        <span className="flex flex-wrap items-center gap-2">
          <span className="font-semibold text-fg [overflow-wrap:anywhere]">{svc.name}</span>
          <span className={cn("inline-flex items-center gap-1.5 text-xs font-medium", TONE_CLASSES[tone].text)}>
            <span aria-hidden="true" className={cn("h-1.5 w-1.5 rounded-full", TONE_CLASSES[tone].dot)} />
            {statusLabel(svc.status)}
          </span>
          <span className="rounded border border-strong px-1.5 text-[11px] leading-4 text-muted">live</span>
        </span>
        <span className={cn(mono, "break-words text-fg/70")}>{facts.join(" · ")}</span>
      </div>
      <Link
        href={`/projects/${projectId}/services/${svc.id}`}
        className="touch-target inline-flex h-[30px] items-center rounded-md border border-strong px-2.5 text-[13px] text-fg hover:bg-raised"
      >
        Open service
      </Link>
      <figcaption className="sr-only">Live status of the {svc.name} service</figcaption>
    </figure>
  );
}

/** A command block (board: Docs): runs the saved page's step in the
 *  service's container, or on its server for operators (D-86, Q-12). */
export function RunBlock({
  block, step, page, svc, canWrite,
}: { block: DocBlock; step: number; page: DocPage; svc?: DocService; canWrite: boolean }) {
  const isOperator = useIsOperator();
  const reauth = useReauth();
  const qc = useQueryClient();
  const [copied, setCopied] = useState(false);
  const [lines, setLines] = useState<string[] | null>(null);
  const [running, setRunning] = useState(false);
  const stop = useRef<(() => void) | null>(null);
  const host = !!block.host;
  const canRun = canWrite && !!svc && (!host || isOperator);
  const where = svc ? (host ? svc.server ?? "the panel’s server" : svc.name) : "a missing service";

  async function run() {
    if (host && !(await reauth("Commands on a server act on the server itself, so Toft asks for your password first."))) {
      setLines([NEEDS_PASSWORD]);
      return;
    }
    setLines([]);
    setRunning(true);
    stop.current = docService(page.project_id).run(
      page.id, step, page.version, host,
      (line) => setLines((l) => [...(l ?? []), line]),
      () => setRunning(false),
      (msg) => {
        setLines((l) => [...(l ?? []), `Error: ${msg}`]);
        if (/changed since/i.test(msg)) qc.invalidateQueries({ queryKey: ["docs", page.project_id, "page", page.id] });
      },
    );
  }

  return (
    <div className="overflow-hidden rounded-lg border border-subtle bg-ground">
      <div className="flex items-center gap-2 border-b border-subtle bg-surface py-1.5 pl-3 pr-1.5 text-xs text-muted">
        <span className="font-mono">sh</span>
        <span aria-hidden="true">·</span>
        <span className="min-w-0 flex-1 truncate">
          {host ? "runs on " : "runs inside "}
          <span className="font-mono text-fg">{where}</span>
          {host && " · operators only"}
        </span>
        <button
          type="button"
          aria-label={copied ? "Copied" : "Copy command"}
          className="flex h-7 w-7 items-center justify-center rounded-md text-muted hover:bg-raised hover:text-fg pointer-coarse:h-11 pointer-coarse:w-11"
          onClick={() =>
            navigator.clipboard.writeText(block.command ?? "").then(
              () => {
                setCopied(true);
                setTimeout(() => setCopied(false), 1500);
              },
              () => toast.error("Couldn’t copy: select it and copy it"),
            )
          }
        >
          {copied ? <Check className="h-[13px] w-[13px]" aria-hidden="true" /> : <Copy className="h-[13px] w-[13px]" aria-hidden="true" />}
        </button>
        {canRun &&
          (running ? (
            <button
              type="button"
              onClick={() => stop.current?.()}
              className="touch-target inline-flex h-7 items-center gap-1.5 rounded-md border border-strong bg-raised px-2.5 text-xs font-medium text-fg"
            >
              <Square className="h-[10px] w-[10px]" fill="currentColor" aria-hidden="true" />
              Stop
            </button>
          ) : (
            <button
              type="button"
              onClick={run}
              className="touch-target inline-flex h-7 items-center gap-1.5 rounded-md border border-strong bg-raised px-2.5 text-xs font-medium text-fg hover:bg-strong/40"
            >
              <Play className="h-[11px] w-[11px]" fill="currentColor" aria-hidden="true" />
              Run
            </button>
          ))}
      </div>
      <pre role="group" tabIndex={0} aria-label="Command to run" className="focus-inset overflow-x-auto px-3.5 py-3 font-mono text-[13px] leading-[21px] text-fg/90">{block.command}</pre>
      {lines !== null && (
        <div role="log" tabIndex={0} aria-live="polite" aria-label="Output" className="focus-inset max-h-72 overflow-auto border-t border-subtle bg-surface px-3.5 py-2.5 font-mono text-xs leading-5 text-fg/80">
          {lines.length === 0 && running && <span className="text-muted">Running…</span>}
          {lines.map((l, i) => (
            <div key={i} className={cn("whitespace-pre-wrap break-words", l.startsWith("Error") || l.startsWith("ERROR") ? "text-status-failed-fg" : "")}>
              {l}
            </div>
          ))}
          {!running && lines.length > 0 && <div className="mt-1 text-muted">Done.</div>}
        </div>
      )}
    </div>
  );
}

/** An issue block (board: Docs): the issue's status as it moves. */
export function IssueBlock({ block, projectId }: { block: DocBlock; projectId: string }) {
  const is = block.issue;
  if (!is) return <p className="text-sm text-muted">An issue this page names doesn’t exist.</p>;
  const st = STATUS[is.status as IssueStatus];
  return (
    <Link
      href={`/projects/${is.project_id || projectId}/issues/${is.number}`}
      className="flex flex-wrap items-center gap-2.5 rounded-lg border border-subtle bg-surface px-3.5 py-3 text-fg hover:border-strong"
    >
      <svg aria-hidden="true" viewBox="0 0 24 24" className={cn("h-4 w-4", st?.tint ?? "text-muted")} fill="none" stroke="currentColor" strokeWidth={1.9} strokeLinecap="round">
        <path d="M3 12a9 9 0 1 0 18 0a9 9 0 1 0-18 0M11 12a1 1 0 1 0 2 0a1 1 0 1 0-2 0" />
      </svg>
      <span className={cn(mono, "text-muted")}>{is.key}</span>
      <span className="min-w-0 flex-[1_1_200px] font-medium">{is.title}</span>
      <span className={cn("text-xs", st?.tint ?? "text-muted")}>
        {st?.name ?? is.status}
        {is.assignee && ` · ${is.assignee.name}`}
      </span>
    </Link>
  );
}

const PULL_STATE: Record<string, string> = { open: "Open", merged: "Merged", closed: "Closed" };
const CHECKS: Record<string, string> = { success: "checks passed", failure: "checks failed", pending: "checks running" };

/** A pull request block: whether it merged, and its checks. */
export function PullBlock({ block, projectId }: { block: DocBlock; projectId: string }) {
  const pr = block.pull;
  if (!pr) return <p className="text-sm text-muted">A pull request this page names doesn’t exist.</p>;
  return (
    <Link
      href={`/projects/${projectId}/code/${encodeURIComponent(pr.repository)}/pulls/${pr.number}`}
      className="flex flex-wrap items-center gap-2.5 rounded-lg border border-subtle bg-surface px-3.5 py-3 text-fg hover:border-strong"
    >
      <GitPullRequest aria-hidden="true" className={cn("h-4 w-4", pr.state === "merged" ? "text-env-violet-fg" : pr.state === "open" ? "text-status-running-fg" : "text-muted")} strokeWidth={1.9} />
      <span className={cn(mono, "text-muted")}>
        {pr.repository}!{pr.number}
      </span>
      <span className="min-w-0 flex-[1_1_200px] font-medium">{pr.title}</span>
      <span className="text-xs text-muted">
        {PULL_STATE[pr.state] ?? pr.state}
        {pr.checks && ` · ${CHECKS[pr.checks] ?? pr.checks}`}
      </span>
    </Link>
  );
}

/** Why a failed request failed, for a toast. */
export function failure(err: unknown, fallback: string): string {
  return err instanceof ApiError ? err.message : fallback;
}
