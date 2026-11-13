"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { Ban, Check, Circle, CircleMinus, Layers, LoaderCircle, Lock, X, Zap } from "lucide-react";
import type { CheckState, CheckSummary, CommitCheck, DeployTarget } from "@/lib/api/code.service";
import type { EnvColor } from "@/lib/api/environment.service";
import { EnvChip } from "@/components/ui/env-chip";
import { useOrgRole } from "@/context/AuthContext";
import { cn } from "@/lib/utils";
import { codeHref, useCode } from "./repo";

// Commit checks and deploys from the project's own repositories (D-77;
// boards CodePipelines, PullRequestChecks, PullRequest, Code).

const STATE: Record<CheckState, { icon: typeof Check; className: string; label: string }> = {
  success: { icon: Check, className: "text-status-running-fg", label: "Passed" },
  failure: { icon: X, className: "text-status-failed-fg", label: "Failed" },
  cancelled: { icon: Ban, className: "text-muted", label: "Cancelled" },
  skipped: { icon: CircleMinus, className: "text-muted", label: "Skipped" },
  running: { icon: LoaderCircle, className: "text-status-building-fg motion-safe:animate-spin", label: "Running" },
  pending: { icon: Circle, className: "text-muted", label: "Pending" },
};

export function CheckIcon({ state, className }: { state: CheckState; className?: string }) {
  const s = STATE[state];
  const Icon = s.icon;
  return <Icon role="img" aria-label={s.label} className={cn("h-3.5 w-3.5 flex-none", s.className, className)} />;
}

const ATTENTION: Record<CheckState, number> = { failure: 0, cancelled: 1, running: 2, pending: 3, success: 4, skipped: 5 };

/** Checks in the order a person reads them (board PullRequestChecks): what
 *  failed, then what runs, then the rest; the API's order within each. */
export function byAttention<T extends { state: CheckState }>(checks: T[]): T[] {
  return [...checks].sort((a, b) => ATTENTION[a.state] - ATTENTION[b.state]);
}

/** "All 4 passed", "1 failing, 1 running, 2 passed". */
export function summaryText(s: CheckSummary): string {
  if (s.state === "success") return s.total === 1 ? "Passed" : `All ${s.total} passed`;
  return [s.failed ? `${s.failed} failing` : "", s.pending ? `${s.pending} running` : "", s.passed ? `${s.passed} passed` : ""].filter(Boolean).join(", ");
}

/** A commit's checks together, as one icon (commits, branches). */
export function SummaryIcon({ summary }: { summary: CheckSummary | null | undefined }) {
  if (!summary) return null;
  const state: CheckState = summary.state === "pending" ? "running" : summary.state;
  return (
    <span title={summaryText(summary)} className="inline-flex">
      <CheckIcon state={state} />
    </span>
  );
}

/** A check's name as shown: Toft's own deploy checks
 *  (toft/deploy/<environment>/<service>) read "Deploy <service>". */
export function checkTitle(name: string): { title: string; env?: string; deploy: boolean } {
  if (!name.startsWith("toft/deploy/")) return { title: name, deploy: false };
  const parts = name.slice("toft/deploy/".length).split("/");
  if (parts.length >= 2) return { title: `Deploy ${parts.slice(1).join("/")}`, env: parts[0], deploy: true };
  return { title: `Deploy ${parts[0]}`, deploy: true };
}

/** "48s", "1m 51s": how long a check ran (until now while it runs). */
export function checkDuration(c: Pick<CommitCheck, "started_at" | "completed_at">, now: number): string {
  if (!c.started_at) return "";
  const end = c.completed_at ? new Date(c.completed_at).getTime() : now;
  const s = Math.max(0, Math.round((end - new Date(c.started_at).getTime()) / 1000));
  if (s === 0 && c.completed_at) return ""; // reported finished in one go
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
}

/** Where a deploy check's deployment is. */
export function deploymentHref(projectId: string, d: { id: string; service_id: string }): string {
  return `/projects/${projectId}/services/${d.service_id}?tab=deployments&deployment=${d.id}`;
}

/** A commit's checks, newest state first (useQuery). */
export function useChecks(sha: string | undefined) {
  const { api } = useCode();
  return useQuery({ queryKey: ["checks", api.id, sha], queryFn: () => api.checks(sha!), enabled: !!sha, refetchInterval: (q) => (q.state.data?.summary?.state === "pending" ? 5000 : false) });
}

/** One check: its state, name (an environment for a deploy), description,
 *  how long it ran, and where to see it. compact is the conversation's
 *  and the pipelines' line; otherwise the Checks tab's row. */
export function CheckLine({ c, now, compact }: { c: CommitCheck; now: number; compact?: boolean }) {
  const { projectId } = useCode();
  const t = checkTitle(c.name);
  const href = c.deployment ? deploymentHref(projectId, c.deployment) : c.details_url;
  const link = c.deployment ? (c.state === "running" ? "Follow" : "Logs") : c.details_url ? "Details ↗" : "";
  const external = !c.deployment && !!c.details_url;
  // In the compact line the row's height is the link's, so 24 px reach it.
  const linkClass = cn("touch-target inline-flex items-center whitespace-nowrap text-xs text-link hover:underline", compact && "self-stretch");
  const linkEl =
    href && link ? (
      external ? (
        <a href={href} target="_blank" rel="noreferrer noopener" className={linkClass}>
          {link}
        </a>
      ) : (
        <Link href={href} className={linkClass}>
          {link}
        </Link>
      )
    ) : null;
  const env = t.env ? <EnvChip env={{ name: t.env, color: envColorOf(t.env), protected: false }} /> : null;
  if (compact) {
    return (
      <div className="flex min-h-6 min-w-0 items-center gap-2 text-xs">
        <CheckIcon state={c.state} />
        <span className="whitespace-nowrap text-fg">{t.title}</span>
        {env}
        <span className={cn("min-w-0 truncate", c.state === "failure" ? "text-status-failed-fg" : "text-muted")}>{c.description}</span>
        <span className="flex-1" />
        <span className="whitespace-nowrap font-mono text-muted">{checkDuration(c, now)}</span>
        {linkEl}
      </div>
    );
  }
  return (
    <div className="flex items-start gap-3 px-4 py-3">
      <CheckIcon state={c.state} className="mt-0.5 h-4 w-4" />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex flex-wrap items-center gap-2">
          <span className={cn("min-w-0 text-[13px] text-fg [overflow-wrap:anywhere]", !t.deploy && "font-mono")}>{t.title}</span>
          {env}
          <span className={cn("text-[13px]", c.state === "failure" ? "text-status-failed-fg" : "text-fg/70")}>{c.description}</span>
        </div>
        <span className="text-xs text-muted">{c.reporter ? `Reported by ${c.reporter.name}` : t.deploy ? "Toft’s deploy" : "Reported by an API key"}</span>
      </div>
      <div className="flex flex-none items-center gap-3 pt-px">
        <span className="font-mono text-xs text-muted">{checkDuration(c, now)}</span>
        {linkEl}
      </div>
    </div>
  );
}

/** The environment colours the panel gives names (D-55), for a check's
 *  environment, of which only the name is in the check. */
function envColorOf(name: string): EnvColor {
  const n = name.toLowerCase();
  if (n === "staging" || n === "preprod") return "amber";
  if (n === "test" || n === "qa") return "violet";
  if (n === "dev" || n === "development") return "teal";
  if (n === "preview") return "blue";
  return "gray";
}

/** How a CI reports a check (boards CodePipelines, PullRequestChecks). */
export function ReportChecksCard() {
  return (
    <section aria-labelledby="ci-h" className="flex flex-col gap-2.5 rounded-[10px] border border-subtle bg-surface p-4">
      <h2 id="ci-h" className="text-[15px] font-semibold leading-[22px] text-fg">
        Report checks from CI
      </h2>
      <p className="text-[13px] leading-[19px] text-fg/70">
        Your CI posts each check on the commit it ran, with an API key that has the <code className="font-mono text-xs text-fg">checks</code> scope. Posting a name again updates it.
      </p>
      <pre role="group" tabIndex={0} aria-label="Command to report a check" className="overflow-x-auto rounded-lg border border-subtle bg-ground px-3 py-2.5 font-mono text-[11px] leading-[18px] text-fg">
        {`curl -X POST \\\n  -H "Authorization: Bearer $TOFT_API_KEY" \\\n  -d '{"name":"ci/test","state":"success",\n       "details_url":"'$RUN_URL'"}' \\\n  …/repositories/<id>/commits/$SHA/checks`}
      </pre>
      <p className="text-xs leading-[17px] text-muted">
        States: pending, running, success, failure, cancelled, skipped. Toft’s own deploys report as <code className="font-mono text-fg/70">toft/deploy/…</code>.
      </p>
      <Link href="/settings/api-keys" className="self-start text-[13px] text-link hover:underline">
        New API key
      </Link>
    </section>
  );
}

/** "web and api", "web, api and worker". */
export function andList(names: string[]): string {
  return names.length <= 1 ? (names[0] ?? "") : `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

/** What a merge or a push to branch deploys, one line per environment and
 *  server: "web and api to production on fsn1-prod-01". */
export function deployLines(targets: DeployTarget[], branch: string): string[] {
  const groups = new Map<string, { env: string | null; server: string | null; names: string[] }>();
  for (const t of targets) {
    if (t.branch !== branch || !t.deploy_on_push) continue;
    const k = `${t.environment_name ?? ""}\0${t.server_name ?? ""}`;
    const g = groups.get(k) ?? { env: t.environment_name, server: t.server_name, names: [] };
    g.names.push(t.service_name);
    groups.set(k, g);
  }
  return [...groups.values()].map((g) => `${andList(g.names)}${g.env ? ` to ${g.env}` : ""} on ${g.server ?? "the panel’s server"}`);
}

/** What a push does (board Code's aside): the default branch's protection
 *  and, branch by branch, the services a push deploys. */
export function WhatAPushDoes() {
  const { base, detail } = useCode();
  const owner = useOrgRole() === "owner";
  const targets = detail.deploys ?? [];
  const branches = [...new Set(targets.filter((t) => t.deploy_on_push).map((t) => t.branch))];
  const rows: { icon: typeof Layers; tone: string; text: React.ReactNode }[] = [];
  if (detail.protect_default_branch) {
    rows.push({ icon: Lock, tone: "text-muted", text: <>{detail.protected_branch ?? detail.default_branch} is protected: changes reach it through a pull request.</> });
  }
  for (const b of branches) {
    rows.push({
      icon: Layers,
      tone: "text-status-building-fg",
      text: (
        <>
          A push to <code className="font-mono text-xs text-fg">{b}</code> deploys {deployLines(targets, b).join("; ")}.
        </>
      ),
    });
  }
  if (!branches.length) {
    rows.push({
      icon: Layers,
      tone: "text-muted",
      text: targets.length
        ? "Services build from this repository, but each deploys only by hand: Deploy on push is off."
        : "No service builds from this repository yet. Set it as a service’s source to deploy on push.",
    });
  }
  // Issues move on pushes and pull requests (D-81).
  rows.push({ icon: Zap, tone: "text-link", text: "A branch whose name contains an issue key moves that issue to In progress; its pull request moves it to In review." });
  return (
    <section aria-labelledby="push-h" className="overflow-hidden rounded-[10px] border border-subtle bg-surface">
      <h2 id="push-h" className="border-b border-subtle px-4 py-3 text-[15px] font-semibold leading-[22px] text-fg">
        What a push does
      </h2>
      <ul className="flex flex-col gap-3 px-4 pb-4 pt-3.5 text-[13px] leading-[19px] text-fg/80">
        {rows.map((r, i) => (
          <li key={i} className="flex gap-2.5">
            <r.icon className={cn("mt-0.5 h-4 w-4 flex-none", r.tone)} aria-hidden="true" />
            <span className="[overflow-wrap:anywhere]">{r.text}</span>
          </li>
        ))}
      </ul>
      {owner && (
        <div className="px-4 pb-4">
          <Link href={codeHref(base, "settings")} className="text-[13px] text-link hover:underline">
            Repository settings
          </Link>
        </div>
      )}
    </section>
  );
}

/** "Deploying web" or "api didn’t deploy" beside a commit (board Code's
 *  latest commit), from its deploy checks. */
export function DeployChip({ sha }: { sha: string }) {
  const { projectId } = useCode();
  const checks = useChecks(sha);
  const deploys = (checks.data?.checks ?? []).filter((c) => c.deployment && (c.state === "running" || c.state === "pending" || c.state === "failure"));
  if (!deploys.length) return null;
  const c = deploys[0];
  const t = checkTitle(c.name);
  const service = t.title.replace(/^Deploy /, "");
  const failed = c.state === "failure";
  return (
    <Link
      href={deploymentHref(projectId, c.deployment!)}
      className={cn("inline-flex items-center gap-1.5 text-xs hover:underline", failed ? "text-status-failed-fg" : "text-status-building-fg")}
    >
      <CheckIcon state={c.state} />
      {failed ? `${service} didn’t deploy` : `Deploying ${service}`}
      {deploys.length > 1 && <span className="text-muted">+{deploys.length - 1}</span>}
    </Link>
  );
}
