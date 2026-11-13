"use client";

import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Lock } from "lucide-react";
import { agentService, type AgentPolicy } from "@/lib/api/agents.service";
import type { Environment } from "@/lib/api/environment.service";
import type { Service } from "@/lib/api/service.service";
import { reauthError, useReauth } from "@/components/reauth";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import { card, ghostSm, h2 } from "@/app/projects/_lib/ui";
import { cn, possessive } from "@/lib/utils";
import {
  DECISION_LABEL,
  POLICY_ROWS,
  cellOf,
  classesOf,
  projectLoosens,
  resetLoosens,
  type Action,
  type Decision,
  type EnvClass,
} from "@/app/settings/agents/view";
import { DecisionMenu } from "./decision-menu";
import { chipText, dataNote, envHints, loosenAsk, ownLede } from "./project-card-view";

// The board's cells: dashed follows the workspace, solid is the project's own.
const VALUE: Record<Decision, string> = {
  allow: "text-status-running-fg",
  ask: "text-status-pending-fg",
  never: "text-fg/80",
};
const OWN: Record<Decision, string> = {
  allow: "border-status-running/40 bg-status-running/[0.08]",
  ask: "border-status-pending/40 bg-status-pending/[0.08]",
  never: "border-fg/30 bg-fg/[0.05]",
};
// The phone's visible column names, and the full ones screen readers hear.
const COLUMNS = ["Previews", "Others", "Protected"];
const COLUMN_NAMES = ["Previews", "Other environments", "Protected"];

/** A project's Agents card in its settings (boards ProjectAgents, D-104, and
 *  ReworkProjectSettings): what agents may do in it, each cell the
 *  workspace's (dashed) or the project's own (solid). Owners change a cell
 *  here, as under Settings → Agents → Projects with their own rules;
 *  others read it. */
export function ProjectAgentsCard({ projectId, projectName, workspace, isOwner, environments, services }: {
  projectId: string;
  projectName: string;
  workspace: string;
  isOwner: boolean;
  environments: Environment[];
  services: Service[];
}) {
  const queryClient = useQueryClient();
  const reauth = useReauth();
  const policy = useQuery({ queryKey: ["agents", "policy"], queryFn: () => agentService.policy() });

  const ws = policy.data?.cells ?? [];
  const own = policy.data?.projects.find((p) => p.project_id === projectId)?.cells ?? [];
  const hints = envHints(environments);
  const done = (p: AgentPolicy | null) => p && queryClient.setQueryData(["agents", "policy"], p);

  const setCell = useMutation({
    mutationFn: async (v: { action: Action; cls: EnvClass; own?: Decision; to: Decision | "" }) => {
      const base = cellOf(ws, v.action, v.cls) ?? "ask";
      if (projectLoosens(v.own, base, v.to)) {
        const envs = v.cls === "any" ? "" : hints[v.cls];
        const ask = loosenAsk(projectName, workspace, v.action, v.cls, v.own ?? base, v.to, base, envs === "none yet" ? "" : envs);
        if (!(await reauth(ask.why, { title: ask.title, button: ask.button }))) return null;
      }
      return agentService.setProjectPolicy(projectId, [{ action: v.action, env_class: v.cls, decision: v.to }]);
    },
    onSuccess: done,
    onError: (err) => toast.error(reauthError(err)),
  });
  const reset = useMutation({
    mutationFn: async () => {
      if (resetLoosens(own, ws) &&
        !(await reauth(`Dropping ${possessive(projectName)} own rules lets agents do more where ${workspace} is looser. Confirm with your password.`,
          { title: `Use ${possessive(workspace)} rules in ${projectName}?`, button: "Confirm and loosen" }))) return null;
      return agentService.resetProjectPolicy(projectId);
    },
    onSuccess: done,
    onError: (err) => toast.error(reauthError(err)),
  });

  if (readFailed(policy)) {
    return (
      <section id="s-agents" tabIndex={-1} aria-labelledby="agents-h" className={cn(card, "focus:outline-none")}>
        <div className="border-b border-subtle px-4 py-3">
          <h2 id="agents-h" className={h2}>Agents</h2>
        </div>
        <div className="px-4 py-3">
          <LoadFailed what="the agent rules" onRetry={() => void policy.refetch()} />
        </div>
      </section>
    );
  }

  const lede = !policy.data
    ? `What agents may do in ${projectName}.`
    : own.length
    ? `What agents may do in ${projectName}. ${ownLede(own, ws, workspace)}`
    : `What agents may do in ${projectName}: ${possessive(workspace)} rules, as Settings → Agents sets them.${isOwner ? " Change a cell to give it a rule of its own." : ""}`;

  return (
    <section id="s-agents" tabIndex={-1} aria-labelledby="agents-h" className={cn(card, "focus:outline-none")}>
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
        <span className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <h2 id="agents-h" className={h2}>Agents</h2>
          <span
            className={cn(
              "inline-flex h-[22px] items-center whitespace-nowrap rounded-full px-2 text-xs font-medium",
              own.length ? "bg-accent-soft text-link" : "bg-raised text-fg/85",
            )}
          >
            {chipText(own, workspace)}
          </span>
        </span>
        {isOwner && own.length > 0 && (
          <button type="button" disabled={reset.isPending} onClick={() => reset.mutate()} className={ghostSm}>
            Use {possessive(workspace)} rules{resetLoosens(own, ws) ? "…" : ""}
          </button>
        )}
      </div>
      <p className="px-4 pb-1 pt-3 text-[13px] leading-[19px] text-fg/70">{lede}</p>

      <div className="px-4 pb-3 pt-2">
        {policy.isLoading ? (
          <span className="flex flex-col gap-2.5 py-3 motion-safe:animate-pulse" role="status" aria-label="Loading">
            <span className="block h-2.5 w-[52%] rounded bg-raised" />
            <span className="block h-2.5 w-[34%] rounded bg-raised" />
          </span>
        ) : (
          <div role="table" aria-label={`Agent rules for ${projectName}`} className="text-[13px]">
            <div role="row" className="grid grid-cols-[minmax(0,1.3fr)_repeat(3,minmax(0,1fr))] gap-2 pb-1.5 text-xs text-muted max-sm:hidden">
              <span role="columnheader">Action</span>
              <span role="columnheader">Previews</span>
              <span role="columnheader">Other environments</span>
              <span role="columnheader" className="inline-flex items-center gap-1">
                <Lock className="h-[11px] w-[11px]" aria-hidden="true" />
                Protected
              </span>
            </div>
            {[...POLICY_ROWS].sort((a, b) => classesOf(b.action).length - classesOf(a.action).length).map((r, i) => {
              const classes = classesOf(r.action);
              return (
                <div key={r.action} role="row"
                  className={cn("grid items-center gap-2 border-t border-subtle py-1.5 sm:grid-cols-[minmax(0,1.3fr)_repeat(3,minmax(0,1fr))]",
                    "max-sm:grid-cols-3 max-sm:py-2.5", i === 0 && "max-sm:border-t-0")}>
                  <span role="rowheader" className="flex min-w-0 flex-col gap-0.5 max-sm:col-span-3">
                    <span className="text-fg">{r.short}</span>
                    <span className="break-words font-mono text-[11px] text-muted">{r.tools.split(" ").join(" · ")}</span>
                  </span>
                  {classes.map((cls, ci) => {
                    const mine = cellOf(own, r.action, cls);
                    const base = cellOf(ws, r.action, cls) ?? "ask";
                    const v = mine ?? base;
                    const wide = cls === "any";
                    const box = cn(
                      "touch-target flex min-h-11 w-full flex-col items-start justify-center gap-px rounded-lg border px-2.5 py-[5px] text-left max-sm:px-2",
                      mine ? cn("border-solid", OWN[v]) : "border-dashed border-strong",
                    );
                    // The name is the visible text, with the row and column
                    // before it for screen readers.
                    const content = (
                      <>
                        <span className="sr-only">{r.short}, </span>
                        <span aria-hidden="true" className="text-[11px] text-muted sm:hidden">{wide ? "Everywhere" : COLUMNS[ci]}{" "}</span>
                        <span className="sr-only">{wide ? "everywhere" : COLUMN_NAMES[ci]}: </span>
                        <span className={cn("text-[13px] font-medium", VALUE[v])}>{DECISION_LABEL[v]}</span>{" "}
                        <span className={cn("text-[11px]", mine ? "text-link" : "text-muted")}>
                          {mine ? "own" : `as ${workspace}`}
                          {wide && " · not tied to an environment"}
                        </span>
                      </>
                    );
                    return (
                      <div key={cls} role="cell" className={cn(wide && "sm:col-span-3 max-sm:col-span-3")}>
                        {isOwner ? (
                          <DecisionMenu
                            applies={v}
                            selected={mine ?? ""}
                            asWorkspace={{ name: workspace, decision: base }}
                            ownHint="this project’s own"
                            ariaLabel=""
                            disabled={setCell.isPending}
                            className={cn(box, "cursor-pointer hover:bg-raised/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus")}
                            onPick={(to) => setCell.mutate({ action: r.action, cls, own: mine, to })}
                          >
                            {content}
                          </DecisionMenu>
                        ) : (
                          <span className={box}>{content}</span>
                        )}
                      </div>
                    );
                  })}
                </div>
              );
            })}
          </div>
        )}
      </div>

      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] leading-[19px] text-muted">
        <span className="min-w-0 flex-[1_1_360px]">
          Dashed follows {workspace}, later changes too; solid is {possessive(projectName)} own.{" "}
          {isOwner ? "Loosening a cell asks for your password." : "Owners change these rules."}{" "}
          {dataNote(services, environments)}
        </span>
        <Link href="/settings/agents" className="text-[13px] text-link hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center">
          {possessive(workspace)} rules
        </Link>
      </div>
    </section>
  );
}
