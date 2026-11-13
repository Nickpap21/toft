"use client";

import { useState, useSyncExternalStore } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Check, Plus } from "lucide-react";
import { useAuth, useIsOperator, useOrgRole } from "@/context/AuthContext";
import { BASE_URL, apiOrigin } from "@/lib/api/client";
import {
  agentService,
  type AgentConnection,
  type AgentPause,
  type AgentPolicy,
  type AgentPolicyCell,
} from "@/lib/api/agents.service";
import { orgService, type AuditEntry } from "@/lib/api/org.service";
import { projectsQuery, workspaceEnvironmentsQuery, workspaceServicesQuery } from "@/lib/queries";
import { DecisionMenu } from "@/components/agents/decision-menu";
import { projectHealth } from "@/components/shell/health";
import { PROJECT_LETTER } from "@/components/shell/sidebar-projects";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { sinceShort, untilHint } from "@/lib/time";
import { cn, formatDateTime, initials, possessive } from "@/lib/utils";
import { reauthError, useReauth } from "@/components/reauth";
import { LoadFailed, readFailed } from "@/components/shell/unreachable";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  SectionHead,
  SettingsFrame,
  cancelButton,
  card,
  dangerButton,
  dialogContent,
  dialogFooter,
  dialogHeader,
  dialogText,
  dialogTitle,
  phoneLabel,
  phoneNote,
  phoneRow,
  phoneTd,
  smallButton,
  th,
} from "@/components/settings/settings-frame";
import {
  DECISION_LABEL,
  ENV_CLASSES,
  POLICY_ROWS,
  callTarget,
  cellOf,
  classesOf,
  capitalize,
  envColumns,
  clientLabel,
  expiresSoon,
  loosens,
  minutesLeft,
  outcomeChip,
  projectDiffs,
  projectLoosens,
  reaches,
  resetLoosens,
  waitingTitle,
  type Action,
  type Decision,
  type EnvClass,
  type Tone,
} from "./view";

const noop = () => () => {};

/** Where agents connect: this panel's origin; "" while prerendering. */
function usePanelOrigin(): string {
  return useSyncExternalStore(noop, () => window.location.origin, () => "");
}

/** The API's absolute origin; BASE_URL ("" when same-origin) while prerendering. */
function useApiOrigin(): string {
  return useSyncExternalStore(noop, apiOrigin, () => BASE_URL);
}

const READ_TOOLS = ["list_projects", "get_service", "get_deploy_logs", "get_capacity", "search_docs", "get_issue", "get_diff"];
const ACT_TOOLS = ["deploy", "rollback", "create_preview", "create_branch", "open_pr", "create_issue", "comment", "set_env_var", "set_secret", "run_runbook_step"];
const NEVER_TOOLS = ["read_secret", "change_roles", "delete_org", "remove_server"];

const LOOSEN_WHY = "Loosening a rule lets agents do more without a person, so Toft asks for your password first.";

const DECISION_CHIP: Record<Decision, string> = {
  allow: "border-status-running/30 bg-status-running-soft text-status-running-fg",
  ask: "border-status-pending/30 bg-status-pending-soft text-status-pending-fg",
  never: "border-subtle bg-transparent text-fg/80",
};

const TONE_CHIP: Record<Tone, string> = {
  ok: "bg-status-running-soft text-status-running-fg",
  wait: "bg-status-pending-soft text-status-pending-fg",
  no: "bg-status-failed-soft text-status-failed-fg",
};

const toolChip = "rounded-sm border border-subtle px-[7px] font-mono text-xs leading-[22px]";
const policyChip = "inline-flex h-7 items-center rounded-full border px-2.5 text-xs font-medium";

type AgentAuditEntry = AuditEntry & { agent_id?: string | null; agent_name?: string | null };

const errText = (err: unknown) => reauthError(err);

/** Settings → Agents (board: Agents, D-100): who is waiting for a person,
 *  how to connect, the tools, the policy, connected agents and their calls. */
export default function AgentsPage() {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const role = useOrgRole();
  const operator = useIsOperator();
  const owner = role === "owner";
  const reauth = useReauth();
  const [now] = useState(() => Date.now());
  const [pausing, setPausing] = useState<"workspace" | "instance" | null>(null);
  const [revoking, setRevoking] = useState<AgentConnection | null>(null);
  const workspace = user?.orgName ?? "";

  const pause = useQuery({ queryKey: ["agents", "pause"], queryFn: () => agentService.pause() });
  const waiting = useQuery({ queryKey: ["agents", "approvals"], queryFn: () => agentService.waiting() });
  const policy = useQuery({ queryKey: ["agents", "policy"], queryFn: () => agentService.policy() });
  const connections = useQuery({ queryKey: ["agents", "connections"], queryFn: () => agentService.connections() });
  const envs = useQuery(workspaceEnvironmentsQuery);
  const ENV_COLS = envColumns(envs.data ?? []);
  // agent.call rows are the agents' calls (the API's ?agent=any adds the
  // agents' other rows, which this list doesn't show).
  const calls = useQuery({
    queryKey: ["agents", "calls"],
    queryFn: () => orgService.listAuditLog({ action: "agent.call", limit: 6 }) as Promise<AgentAuditEntry[]>,
  });

  const resume = useMutation({
    mutationFn: async (where: "workspace" | "instance") => {
      if (!(await reauth("Resuming lets agents act again, so Toft asks for your password first."))) return null;
      return where === "workspace" ? agentService.resumeWorkspace() : agentService.resumeInstance();
    },
    onSuccess: (p) => {
      if (!p) return;
      queryClient.setQueryData(["agents", "pause"], p);
      toast.success("Agents resumed");
    },
    onError: (err) => toast.error(errText(err)),
  });

  const setCell = useMutation({
    mutationFn: async ({ cell, to }: { cell: AgentPolicyCell; to: Decision }) => {
      if (
        loosens(cell.decision, to) &&
        !(await reauth(LOOSEN_WHY))
      ) {
        return null;
      }
      return agentService.setPolicy([{ action: cell.action, env_class: cell.env_class, decision: to }]);
    },
    onSuccess: (p) => p && queryClient.setQueryData(["agents", "policy"], p),
    onError: (err) => toast.error(errText(err)),
  });

  const paused = pause.data && (pause.data.workspace || pause.data.instance);
  const cellFor = (action: Action, cls: EnvClass) =>
    policy.data?.cells.find((c) => c.action === action && c.env_class === cls);
  const policyCell = (action: Action, label: string, cls: EnvClass, where: string) => {
    const cell = cellFor(action, cls);
    if (!cell) {
      return policy.isLoading ? (
        <span role="status" className="block h-2.5 w-16 rounded bg-raised motion-safe:animate-pulse" aria-label="Loading" />
      ) : (
        <span className="text-muted">–</span>
      );
    }
    const text = DECISION_LABEL[cell.decision];
    const aria = `${label}, ${where}: ${text}`;
    return owner ? (
      <DecisionMenu
        applies={cell.decision}
        selected={cell.decision}
        ariaLabel={aria}
        disabled={setCell.isPending}
        className={cn(policyChip, DECISION_CHIP[cell.decision], "flex-none cursor-pointer")}
        onPick={(to) => to && setCell.mutate({ cell, to })}
      />
    ) : (
      <span className={cn(policyChip, DECISION_CHIP[cell.decision], "flex-none")} aria-label={aria}>
        {text}
      </span>
    );
  };

  return (
    <SettingsFrame
      title="Agents"
      gap="gap-6"
      action={
        (owner && !pause.data?.workspace) || (operator && !pause.data?.instance) ? (
          <div className="flex flex-wrap gap-2">
            {operator && !pause.data?.instance && (
              <button type="button" className={pauseButton} onClick={() => setPausing("instance")}>
                <PauseIcon className="h-3.5 w-3.5" />
                Pause on this panel
              </button>
            )}
            {owner && !pause.data?.workspace && (
              <button type="button" className={pauseButton} onClick={() => setPausing("workspace")}>
                <PauseIcon className="h-3.5 w-3.5" />
                Pause all agents
              </button>
            )}
          </div>
        ) : undefined
      }
    >
      <p className="max-w-[860px] text-[13px] leading-[19px] text-fg/70">
        Agents work in Toft over MCP, each as the person who connected it and never with more than that
        person’s role. The policy below decides what they do alone, what waits for a person, and what they
        never do. Every call lands in the audit log.
      </p>

      {paused ? (
        <div className="flex flex-col gap-3">
          {pause.data?.workspace && (
            <PausedBanner
              title={`Agents are paused in ${workspace}`}
              body={`${pause.data.workspace.by} paused them at ${hhmm(pause.data.workspace)}. Every call is refused, and the waiting requests were turned down. Deploys already running finish. Connections stay, so nobody signs in again when an owner resumes.`}
              canResume={owner}
              busy={resume.isPending}
              onResume={() => resume.mutate("workspace")}
            />
          )}
          {pause.data?.instance && (
            <PausedBanner
              title="Agents are paused on this panel"
              body={`An instance operator paused them at ${hhmm(pause.data.instance)} for every workspace. Only an instance operator can resume them.`}
              canResume={operator}
              busy={resume.isPending}
              onResume={() => resume.mutate("instance")}
            />
          )}
        </div>
      ) : readFailed(waiting) ? (
        <LoadFailed what="the waiting requests" onRetry={() => void waiting.refetch()} />
      ) : (
        (waiting.data?.length ?? 0) > 0 && (
          <section
            aria-labelledby="ask-h"
            className="flex flex-col rounded-[10px] border border-status-pending/40 bg-status-pending/5"
          >
            <div className="flex flex-wrap items-center gap-2.5 px-4 py-3">
              <span className="inline-flex h-[22px] items-center rounded-full bg-status-pending-soft px-2 text-xs font-medium text-status-pending-fg">
                {waiting.data!.length} waiting
              </span>
              <h2 id="ask-h" className="text-[15px] font-semibold leading-[22px] text-fg">
                Agents waiting for a person
              </h2>
              <span className="flex-1" />
              <span className="text-xs text-muted">Owners and developers approve, in Needs you or here</span>
            </div>
            {waiting.data!.map((w) => (
              <div
                key={w.id}
                className="flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-status-pending/20 px-4 py-2.5"
              >
                <span className="flex min-w-0 flex-[1_1_360px] flex-col [overflow-wrap:anywhere]">
                  <span className="font-medium text-fg">{waitingTitle(w)}</span>
                  <span className="text-xs leading-5 text-muted">
                    acting for {w.user_name} · “{w.reason}”{w.tool === "set_secret" && " · value hidden"}
                  </span>
                </span>
                <span className="whitespace-nowrap text-xs text-status-pending-fg">
                  expires in {minutesLeft(w.expires_at, now)} min
                </span>
                <Link
                  href={`/settings/agents/requests/${w.id}`}
                  aria-label={`Review: ${waitingTitle(w)}`}
                  className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")}
                >
                  Review
                </Link>
              </div>
            ))}
          </section>
        )
      )}

      <div className="flex flex-wrap items-start gap-6">
        <ConnectCard />
        <section aria-labelledby="tools-h" className={cn(card, "flex min-w-0 flex-[1_1_440px] flex-col gap-3 p-4")}>
          <h2 id="tools-h" className="text-[15px] font-semibold leading-[22px] text-fg">
            Tools agents get
          </h2>
          <ToolGroup name="Read" tools={READ_TOOLS} />
          <ToolGroup name="Act, within the policy below" tools={ACT_TOOLS} />
          <ToolGroup name="Never exposed" tools={NEVER_TOOLS} never />
          <p className="text-xs leading-[17px] text-muted">
            No tool reads a secret, changes a role, deletes the workspace or removes a server, whatever the policy
            says.
          </p>
        </section>
      </div>

      <section aria-labelledby="pol-h" className="flex flex-col gap-3">
        <SectionHead
          id="pol-h"
          title="What agents may do"
          note={`Owners change it. Loosening a rule asks for your password. Applies to every agent in ${workspace}.`}
        />
        <div role="group" tabIndex={0} aria-label="What agents may do" className={cn(card, "overflow-x-auto")}>
          <table className="w-full border-collapse text-[13px] max-sm:block sm:min-w-[680px]">
            <thead className="max-sm:hidden">
              <tr>
                <th scope="col" className={th}>
                  Action
                </th>
                {ENV_COLS.map((e) => (
                  <th key={e.cls} scope="col" className={cn(th, "w-[170px]")}>
                    <span className="flex flex-col leading-[17px]">
                      <span className="text-fg/85">{e.name}</span>
                      <span className="font-normal">{e.hint}</span>
                    </span>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="max-sm:block">
              <tr className={phoneRow}>
                <PolicyLabel
                  label="Read projects, services, logs, capacity, docs, issues and diffs"
                  tools="list_projects get_service get_deploy_logs …"
                />
                <td colSpan={3} className={cn(phoneTd, "py-1.5 max-sm:col-span-2")}>
                  <span className="flex items-center gap-2.5">
                    <span className={cn(policyChip, DECISION_CHIP.allow, "flex-none")} aria-label="Read, everywhere: Allow, always">
                      Allow
                    </span>
                    <span className="min-w-0 flex-1 text-xs leading-[17px] text-muted">
                      Narrowed by projects when someone connects an agent
                    </span>
                  </span>
                </td>
              </tr>
              {POLICY_ROWS.map((r) => (
                <tr key={r.action} className={phoneRow}>
                  <PolicyLabel label={r.label} tools={r.tools} note={r.note} />
                  {r.hint ? (
                    <td colSpan={3} className={cn(phoneTd, "py-1.5 max-sm:col-span-2")}>
                      <span className="flex items-center gap-2.5">
                        {policyCell(r.action, r.label, "any", "everywhere")}
                        <span className="min-w-0 flex-1 text-xs leading-[17px] text-muted">{r.hint}</span>
                      </span>
                    </td>
                  ) : (
                    ENV_COLS.map((e) => (
                      <td key={e.cls} className={cn(phoneTd, "py-1.5 max-sm:col-span-2 max-sm:flex max-sm:items-center max-sm:justify-between max-sm:gap-3")}>
                        <span className={phoneLabel}>{e.name}</span>
                        {policyCell(r.action, r.label, e.cls, e.name)}
                      </td>
                    ))
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {policy.error && (
          <p role="alert" className="text-xs text-status-failed-fg">
            Could not load the policy: {errText(policy.error)}
          </p>
        )}
        <p className="text-xs leading-[17px] text-muted">
          Ask a person: the agent says why and what it plans, and waits up to 30 minutes for an owner or developer. A
          database or cache (Postgres, MySQL, Redis and the like) counts as Protected wherever it runs, for deploys,
          variables, secrets and runbook steps; a preview’s copy stays a preview. Runbook steps on a server’s host are
          for instance operators with their password, so agents never run them.
        </p>
      </section>

      <ProjectRules workspace={workspace} owner={owner} />

      <section aria-labelledby="list-h" className="flex flex-col gap-3">
        <SectionHead
          id="list-h"
          title="Connected agents"
          count={connections.data?.length ?? "–"}
          note={`Owners see every agent in ${workspace}; others see their own. Revoking ends its calls at once.`}
        />
        <div className={cn(card, "overflow-x-auto")}>
          <table className="w-full table-fixed border-collapse text-[13px] [overflow-wrap:anywhere] max-sm:block sm:min-w-[900px]">
            <thead className="max-sm:hidden">
              <tr>
                <th scope="col" className={th}>Agent</th>
                <th scope="col" className={th}>Connected by</th>
                <th scope="col" className={th}>Reaches</th>
                <th scope="col" className={th}>Last call</th>
                <th scope="col" className={th}>Expires</th>
                <th scope="col" className={cn(th, "w-[110px]")}>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody className="max-sm:block">
              {connections.isLoading ? (
                <tr aria-busy="true" className="max-sm:block">
                  <td colSpan={6} className={cn(phoneNote, "py-3.5")}>
                    <span role="status" className="flex flex-col gap-2.5 motion-safe:animate-pulse" aria-label="Loading">
                      <span className="block h-2.5 w-[62%] rounded bg-raised" />
                      <span className="block h-2.5 w-[44%] rounded bg-raised" />
                    </span>
                  </td>
                </tr>
              ) : connections.error ? (
                <tr className="max-sm:block">
                  <td colSpan={6} role="alert" className={cn(phoneNote, "py-5")}>
                    <span className="flex flex-wrap items-center justify-between gap-3">
                      <span className="flex items-center gap-2.5 text-status-failed-fg">
                        <AlertTriangle className="h-4 w-4 flex-none" aria-hidden="true" />
                        Could not load connected agents: {errText(connections.error)}
                      </span>
                      <button
                        type="button"
                        className={cn(smallButton, "h-8 px-3 text-[13px] font-medium rounded-lg")}
                        onClick={() => connections.refetch()}
                      >
                        Retry
                      </button>
                    </span>
                  </td>
                </tr>
              ) : connections.data?.length === 0 ? (
                <tr className="max-sm:block">
                  <td colSpan={6} className={cn(phoneNote, "py-7")}>
                    <span className="flex flex-col gap-0.5">
                      <span className="font-medium text-fg">No agents connected</span>
                      <span className="text-fg/70">
                        Connect Claude Code with the command above; it shows up here once you authenticate it.
                      </span>
                    </span>
                  </td>
                </tr>
              ) : (
                connections.data?.map((c) => {
                  const soon = expiresSoon(c.expires_at, now);
                  return (
                    <tr key={c.id} className={phoneRow}>
                      <td className={cn(phoneTd, "max-sm:col-start-1 max-sm:row-start-1")}>
                        <span className="flex flex-col">
                          <span className="font-medium text-fg">{c.name}</span>
                          <span className="text-xs leading-5 text-muted">{clientLabel(c)}</span>
                        </span>
                      </td>
                      <td className={cn(phoneTd, "max-sm:col-span-2")}>
                        <span className={phoneLabel}>Connected by </span>
                        <span className="inline-flex items-center gap-2">
                          <span
                            aria-hidden="true"
                            className="flex h-[22px] w-[22px] flex-none items-center justify-center rounded-full bg-raised text-[10px] font-semibold text-fg"
                          >
                            {initials(c.user_name)}
                          </span>
                          <span className="flex flex-col leading-[17px]">
                            <span className="text-fg">{c.user_name}</span>
                            <span className="text-xs text-muted">{capitalize(c.user_role)}</span>
                          </span>
                        </span>
                      </td>
                      <td className={cn(phoneTd, "max-sm:col-span-2")}>
                        <span className="flex flex-col leading-[18px]">
                          <span className="text-fg/85">
                            <span className={phoneLabel}>Reaches </span>
                            {reaches(c)}
                          </span>
                          <span className="font-mono text-xs text-muted">{c.scopes.join(", ")}</span>
                        </span>
                      </td>
                      <td className={cn(phoneTd, "max-sm:col-span-2")}>
                        <span className={phoneLabel}>Last call </span>
                        {c.last_used_at ? (
                          <span className="flex flex-col leading-[18px] max-sm:inline">
                            <span className="text-fg/85" title={formatDateTime(c.last_used_at)}>
                              {sinceShort(c.last_used_at, now)}
                            </span>
                            <span className="font-mono text-xs text-muted max-sm:block">{c.last_tool}</span>
                          </span>
                        ) : (
                          <span className="text-muted">Never</span>
                        )}
                      </td>
                      <td className={cn(phoneTd, "max-sm:col-span-2")}>
                        <span className="flex flex-col leading-[18px]">
                          <span className="font-mono text-xs text-fg/85">
                            <span className={cn(phoneLabel, "font-sans")}>Expires </span>
                            {formatDateTime(c.expires_at)}
                          </span>
                          <span className={cn("text-xs", soon ? "text-status-pending-fg" : "text-muted")}>
                            {untilHint(c.expires_at, now)}
                            {soon && `: ${c.user_name} signs in again`}
                          </span>
                        </span>
                      </td>
                      <td className={cn(phoneTd, "py-1.5 text-right max-sm:col-start-2 max-sm:row-start-1")}>
                        <button
                          type="button"
                          className={smallButton}
                          aria-label={`Revoke ${c.name} connected by ${c.user_name}`}
                          onClick={() => setRevoking(c)}
                        >
                          Revoke
                        </button>
                      </td>
                    </tr>
                  );
                })
              )}
            </tbody>
          </table>
        </div>
      </section>

      <section aria-labelledby="log-h" className="flex flex-col gap-3">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <h2 id="log-h" className="text-[15px] font-semibold leading-[22px] text-fg">
            Recent agent calls
          </h2>
          <Link href="/audit-log?agent=any" className="text-[13px] text-link hover:underline">
            All of them in the audit log
          </Link>
        </div>
        <div role="group" tabIndex={0} aria-label="Recent agent calls" className="overflow-x-auto rounded-[10px] border border-subtle bg-ground">
          {calls.isLoading ? (
            <span role="status" className="flex flex-col gap-2.5 px-4 py-3.5 motion-safe:animate-pulse" aria-label="Loading">
              <span className="block h-2.5 w-[62%] rounded bg-raised" />
              <span className="block h-2.5 w-[44%] rounded bg-raised" />
            </span>
          ) : calls.error ? (
            <p role="alert" className="px-4 py-3 text-[13px] text-status-failed-fg">
              Could not load agent calls: {errText(calls.error)}
            </p>
          ) : calls.data?.length === 0 ? (
            <p className="px-4 py-3 text-[13px] text-muted">No agent has called a tool yet.</p>
          ) : (
            calls.data?.map((e, i) => {
              const chip = outcomeChip(e.detail?.outcome);
              return (
                <div
                  key={e.id}
                  className={cn(
                    "flex min-w-[820px] items-center gap-4 px-4 py-2 text-[13px]",
                    i > 0 && "border-t border-subtle",
                  )}
                >
                  <span className="w-11 flex-none font-mono text-xs text-muted">
                    {formatDateTime(e.created_at).slice(11)}
                  </span>
                  <span className="w-[200px] flex-none truncate text-fg/85">
                    {e.agent_name ?? "Agent"} · for {e.actor}
                  </span>
                  <span className="w-[140px] flex-none font-mono text-xs text-fg">
                    {typeof e.detail?.tool === "string" ? e.detail.tool : ""}
                  </span>
                  <span className="min-w-0 flex-1 truncate text-fg/70">{callTarget(e, workspace)}</span>
                  <span
                    className={cn(
                      "inline-flex h-5 items-center whitespace-nowrap rounded-full px-[7px] text-xs font-medium",
                      TONE_CHIP[chip.tone],
                    )}
                  >
                    {chip.label}
                  </span>
                </div>
              );
            })
          )}
        </div>
      </section>

      <PauseDialog
        where={pausing}
        workspace={workspace}
        onClose={() => setPausing(null)}
      />
      <RevokeDialog target={revoking} onClose={() => setRevoking(null)} />
    </SettingsFrame>
  );
}

const pauseButton =
  "inline-flex h-9 items-center gap-1.5 rounded-lg border border-danger/45 px-3 text-[13px] font-medium text-status-failed-fg hover:bg-status-failed-soft";

/** The board's pause glyph: two strokes. */
function PauseIcon({ className }: { className: string }) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true" className={className}>
      <path d="M9 5v14M15 5v14" />
    </svg>
  );
}

const hhmm = (p: NonNullable<AgentPause["workspace"]>) => formatDateTime(p.at).slice(11);

function PolicyLabel({ label, tools, note }: { label: string; tools: string; note?: string }) {
  return (
    <th scope="row" className={cn(phoneTd, "py-2 text-left font-normal max-sm:col-span-2")}>
      <span className="flex flex-col leading-[18px]">
        <span className="text-fg">{label}</span>
        <span className="font-mono text-xs text-muted">{tools}</span>
        {note && (
          <span className="mt-[3px] inline-flex items-center gap-[5px] text-xs text-status-pending-fg">
            <DatabaseIcon />
            {note}
          </span>
        )}
      </span>
    </th>
  );
}

/** The board's database glyph. */
function DatabaseIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true" className="h-3 w-3 flex-none">
      <ellipse cx="12" cy="5" rx="8" ry="3" />
      <path d="M4 5v14c0 1.7 3.6 3 8 3s8-1.3 8-3V5M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3" />
    </svg>
  );
}

function ToolGroup({ name, tools, never }: { name: string; tools: string[]; never?: boolean }) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className={cn("text-xs font-medium", never ? "text-muted" : "text-fg/70")}>{name}</span>
      <ul className="flex flex-wrap gap-1.5" aria-label={name}>
        {tools.map((t) => (
          <li key={t} className={cn(toolChip, never ? "text-muted line-through" : "text-fg")}>
            {t}
          </li>
        ))}
      </ul>
    </div>
  );
}

function ConnectCard() {
  const panel = usePanelOrigin();
  const api = useApiOrigin();
  const mcp = `${panel}/mcp`;
  const command = `claude mcp add --transport http toft ${mcp}`;
  const [copied, setCopied] = useState(false);
  return (
    <section aria-labelledby="conn-h" className={cn(card, "flex min-w-0 flex-[1_1_440px] flex-col gap-3 p-4")}>
      <h2 id="conn-h" className="text-[15px] font-semibold leading-[22px] text-fg">
        Connect Claude Code
      </h2>
      <div className="overflow-hidden rounded-lg border border-subtle bg-ground">
        <div className="flex items-center gap-2 border-b border-subtle bg-raised/60 py-1.5 pl-3 pr-1.5 text-xs text-muted">
          <span className="font-mono">terminal</span>
          <span className="flex-1" />
          <button
            type="button"
            className={cn(smallButton, "h-7 rounded-md")}
            aria-label="Copy the command"
            onClick={() =>
              navigator.clipboard.writeText(command).then(
                () => setCopied(true),
                () => toast.error("Couldn't copy: select the text and copy it"),
              )
            }
          >
            {copied && <Check className="h-3.5 w-3.5" aria-hidden="true" />}
            {copied ? "Copied" : "Copy"}
          </button>
        </div>
        <pre role="group" tabIndex={0} aria-label="Command to connect Claude Code" className="focus-inset overflow-x-auto px-3.5 py-3 font-mono text-[13px] leading-[21px] text-fg/90">{command}</pre>
      </div>
      <p className="text-[13px] leading-[19px] text-fg/70">
        Then run <span className="font-mono text-xs text-fg">/mcp</span> in Claude Code and choose Authenticate.
        Toft opens in your browser: you pick the workspace, the projects and what it may do. It gets a token of its
        own, never your session, for 90 days.
      </p>
      <div className="flex flex-col gap-1 border-t border-subtle pt-2.5 text-[13px] text-fg/70">
        <span>
          Other MCP clients: <span className="break-all font-mono text-xs text-fg">{mcp}</span> (Streamable HTTP,
          OAuth)
        </span>
        <span>
          Scripts and CI:{" "}
          <Link href="/settings/api-keys" className="inline-link">
            API keys
          </Link>{" "}
          · REST:{" "}
          <a href={`${api}/api/v1/docs`} target="_blank" rel="noreferrer" className="inline-link">
            API reference
          </a>
        </span>
      </div>
    </section>
  );
}

function PausedBanner({
  title,
  body,
  canResume,
  busy,
  onResume,
}: {
  title: string;
  body: string;
  canResume: boolean;
  busy: boolean;
  onResume: () => void;
}) {
  return (
    <div
      role="status"
      className="flex flex-wrap items-center gap-x-4 gap-y-2.5 rounded-[10px] border border-status-failed/40 bg-status-failed/[0.06] px-4 py-3"
    >
      <PauseIcon className="h-4 w-4 flex-none text-status-failed-fg" />
      <span className="flex min-w-0 flex-[1_1_420px] flex-col gap-0.5">
        <span className="font-semibold text-fg">{title}</span>
        <span className="text-[13px] leading-[19px] text-fg/85">{body}</span>
      </span>
      {canResume && (
        <button type="button" className={cancelButton} disabled={busy} onClick={onResume}>
          {busy ? "Resuming…" : "Resume agents"}
        </button>
      )}
    </div>
  );
}

function PauseDialog({
  where,
  workspace,
  onClose,
}: {
  where: "workspace" | "instance" | null;
  workspace: string;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const pause = useMutation({
    mutationFn: () => (where === "instance" ? agentService.pauseInstance() : agentService.pauseWorkspace()),
    onSuccess: (p) => {
      queryClient.setQueryData(["agents", "pause"], p);
      queryClient.invalidateQueries({ queryKey: ["agents", "approvals"] });
      toast.success(where === "instance" ? "Agents paused on this panel" : `Agents paused in ${workspace}`);
      close();
    },
  });
  const close = () => {
    onClose();
    pause.reset();
  };
  return (
    <Dialog open={where !== null} onOpenChange={(o) => !o && !pause.isPending && close()}>
      <DialogContent role="alertdialog" className={dialogContent} showClose={false}>
        <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
          <DialogTitle className={dialogTitle}>
            {where === "instance" ? "Pause agents on this panel?" : `Pause all agents in ${workspace}?`}
          </DialogTitle>
          <DialogDescription className={dialogText}>
            {where === "instance" ? "In every workspace, every" : "Every"} agent call is refused from now on and
            the requests waiting for a person are turned down. Deploys already running finish. Connections stay,
            and resuming asks for your password.
          </DialogDescription>
        </DialogHeader>
        {pause.error && (
          <p role="alert" className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
            {errText(pause.error)}
          </p>
        )}
        <div className={dialogFooter}>
          <button type="button" className={cancelButton} disabled={pause.isPending} onClick={close}>
            Cancel
          </button>
          <button type="button" className={dangerButton} disabled={pause.isPending} onClick={() => pause.mutate()}>
            {pause.isPending ? "Pausing…" : "Pause agents"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function RevokeDialog({ target, onClose }: { target: AgentConnection | null; onClose: () => void }) {
  const queryClient = useQueryClient();
  const revoke = useMutation({
    mutationFn: () => agentService.revoke(target!.id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["agents", "connections"] });
      toast.success(`${target?.name} revoked`);
      close();
    },
  });
  const close = () => {
    onClose();
    revoke.reset();
  };
  return (
    <Dialog open={target !== null} onOpenChange={(o) => !o && !revoke.isPending && close()}>
      <DialogContent role="alertdialog" className={dialogContent} showClose={false}>
        <DialogHeader className={cn(dialogHeader, "gap-1.5")}>
          <DialogTitle className={dialogTitle}>Revoke {target?.name}?</DialogTitle>
          <DialogDescription className={dialogText}>
            {target?.name}, connected by {target?.user_name}, loses its tokens now and its calls stop at once. To use
            it again, {target?.user_name} connects it again and consents.
          </DialogDescription>
        </DialogHeader>
        {revoke.error && (
          <p role="alert" className="mx-5 mt-3 rounded-lg bg-status-failed-soft px-3 py-2 text-[13px] text-status-failed-fg">
            {errText(revoke.error)}
          </p>
        )}
        <div className={dialogFooter}>
          <button type="button" className={cancelButton} disabled={revoke.isPending} onClick={close}>
            Cancel
          </button>
          <button type="button" className={dangerButton} disabled={revoke.isPending} onClick={() => revoke.mutate()}>
            {revoke.isPending ? "Revoking…" : "Revoke agent"}
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

const ruleCount = (n: number) => (n === 1 ? "1 rule of its own" : `${n} rules of its own`);

/** "Projects with their own rules" (board Agents, D-101): each project's
 *  cells that differ from the workspace's, and its table to edit them. A
 *  dashed cell follows the workspace; a solid one is the project's own. */
function ProjectRules({ workspace, owner }: { workspace: string; owner: boolean }) {
  const queryClient = useQueryClient();
  const reauth = useReauth();
  const policy = useQuery({ queryKey: ["agents", "policy"], queryFn: () => agentService.policy() });
  const projects = useQuery(projectsQuery);
  const services = useQuery({ ...workspaceServicesQuery, meta: { quietFailure: "Only tints a project's letter; nothing on this page says how a project's services are doing." } });
  const [open, setOpen] = useState("");
  // A project picked with "Add a project": listed until it has a rule.
  const [added, setAdded] = useState<string[]>([]);

  const ws = policy.data?.cells ?? [];
  const withRules = policy.data?.projects ?? [];
  const listed = [
    ...withRules,
    ...added
      .filter((id) => !withRules.some((p) => p.project_id === id))
      .map((id) => ({ project_id: id, name: projects.data?.find((p) => p.id === id)?.name ?? "", cells: [] })),
  ];
  const candidates = (projects.data ?? []).filter((p) => !listed.some((l) => l.project_id === p.id));
  const done = (p: AgentPolicy | null) => p && queryClient.setQueryData(["agents", "policy"], p);

  const setCell = useMutation({
    mutationFn: async (v: { project: string; action: Action; cls: EnvClass; own?: Decision; to: Decision | "" }) => {
      const base = cellOf(ws, v.action, v.cls) ?? "ask";
      if (projectLoosens(v.own, base, v.to) && !(await reauth(LOOSEN_WHY))) return null;
      return agentService.setProjectPolicy(v.project, [{ action: v.action, env_class: v.cls, decision: v.to }]);
    },
    onSuccess: done,
    onError: (err) => toast.error(errText(err)),
  });
  const reset = useMutation({
    mutationFn: async (p: { project_id: string; cells: AgentPolicyCell[] }) => {
      if (p.cells.length === 0) return null;
      if (resetLoosens(p.cells, ws) && !(await reauth(LOOSEN_WHY))) return null;
      return agentService.resetProjectPolicy(p.project_id);
    },
    onSuccess: (p, v) => {
      done(p);
      if (p || v.cells.length === 0) {
        setAdded((a) => a.filter((id) => id !== v.project_id));
        if (open === v.project_id) setOpen("");
      }
    },
    onError: (err) => toast.error(errText(err)),
  });

  const tint = (id: string) => {
    const mine = services.data?.filter((s) => s.project_id === id);
    return PROJECT_LETTER[mine?.length ? projectHealth(mine).health : "idle"];
  };

  return (
    <section aria-labelledby="proj-h" className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id="proj-h" className="text-[15px] font-semibold leading-[22px] text-fg">
          Projects with their own rules
          <span className="ml-1 rounded-full bg-raised px-[7px] text-xs font-medium text-fg/85">{policy.data ? withRules.length : "—"}</span>
        </h2>
        {owner && (
          <DropdownMenu>
            <DropdownMenuTrigger className={cn(smallButton, "h-8 gap-1.5 px-3 text-[13px] font-medium rounded-lg")}>
              <Plus className="h-3.5 w-3.5" aria-hidden="true" />
              Add a project
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="max-h-80 overflow-y-auto">
              <DropdownMenuLabel>Projects following {workspace}</DropdownMenuLabel>
              {readFailed(projects) ? (
                <DropdownMenuItem disabled>Couldn’t load the projects</DropdownMenuItem>
              ) : !projects.data ? (
                <DropdownMenuItem disabled>Loading…</DropdownMenuItem>
              ) : candidates.length === 0 ? (
                <DropdownMenuItem disabled>Every project is listed</DropdownMenuItem>
              ) : (
                candidates.map((p) => (
                  <DropdownMenuItem
                    key={p.id}
                    onSelect={() => {
                      setAdded((a) => [...a, p.id]);
                      setOpen(p.id);
                    }}
                  >
                    {p.name}
                  </DropdownMenuItem>
                ))
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
      <p className="max-w-[860px] text-[13px] leading-[18px] text-fg/70">
        A project’s rule replaces {possessive(workspace)} for that one cell; every cell it leaves alone follows {workspace},
        including later changes. Owners set them, and loosening a cell, against {possessive(workspace)} or the project’s own,
        asks for your password. The audit log says which rule decided each call.
      </p>
      <div className={cn(card, "overflow-hidden")}>
        {readFailed(policy) ? (
          <div className="px-4 py-3.5">
            <LoadFailed what="the project rules" onRetry={() => void policy.refetch()} />
          </div>
        ) : policy.isLoading ? (
          <span role="status" className="flex flex-col gap-2.5 px-4 py-3.5 motion-safe:animate-pulse" aria-label="Loading">
            <span className="block h-2.5 w-[52%] rounded bg-raised" />
            <span className="block h-2.5 w-[34%] rounded bg-raised" />
          </span>
        ) : listed.length === 0 ? (
          <p className="px-4 py-3.5 text-[13px] text-fg/70">
            Every project follows {possessive(workspace)} rules.{owner && " Add a project to give it rules of its own."}
          </p>
        ) : (
          listed.map((p, i) => {
            const isOpen = open === p.project_id;
            const diffs = projectDiffs(p.cells, ws);
            return (
              <div key={p.project_id} className={cn("flex flex-col", i > 0 && "border-t border-subtle")}>
                <div className="flex flex-wrap items-center gap-x-4 gap-y-2.5 px-4 py-3">
                  <span
                    aria-hidden="true"
                    className={cn("flex h-[22px] w-[22px] flex-none items-center justify-center rounded-md text-[11px] font-semibold", tint(p.project_id))}
                  >
                    {p.name.trim()[0]?.toUpperCase() ?? "?"}
                  </span>
                  <span className="flex min-w-0 flex-[1_1_420px] flex-col gap-1">
                    <span className="font-semibold text-fg">
                      {p.name} <span className="text-xs font-normal text-muted">{diffs.length ? ruleCount(diffs.length) : "no rules of its own yet"}</span>
                    </span>
                    {diffs.map((d) => (
                      <span key={d.where} className="flex flex-wrap items-center gap-1.5 text-[13px] text-fg/85">
                        <span>{d.where}</span>
                        <span className={cn("inline-flex h-5 items-center rounded-full px-[7px] text-xs font-medium", DECISION_CHIP[d.decision])}>
                          {DECISION_LABEL[d.decision]}
                        </span>
                        <span className="text-xs text-muted">
                          {workspace}: {DECISION_LABEL[d.ws]}
                        </span>
                      </span>
                    ))}
                  </span>
                  {owner && (
                    <span className="flex gap-2">
                      <button
                        type="button"
                        aria-expanded={isOpen}
                        aria-controls={`proj-${p.project_id}`}
                        onClick={() => setOpen(isOpen ? "" : p.project_id)}
                        className={cn(smallButton, "h-[30px] rounded-md px-2.5 text-xs")}
                      >
                        {isOpen ? "Done" : "Edit"}
                      </button>
                      <button
                        type="button"
                        disabled={reset.isPending}
                        onClick={() => reset.mutate(p)}
                        className="h-[30px] rounded-md border border-strong px-2.5 text-xs text-fg/70 hover:bg-raised hover:text-fg disabled:opacity-60"
                      >
                        Use {possessive(workspace)} rules
                      </button>
                    </span>
                  )}
                </div>
                {isOpen && (
                  <div id={`proj-${p.project_id}`} className="overflow-x-auto px-4 pb-3.5">
                    <table className="w-full min-w-[680px] border-separate border-spacing-0 overflow-hidden rounded-lg border border-subtle bg-ground text-[13px]">
                      <thead>
                        <tr className="text-left text-xs text-muted">
                          <th scope="col" className="px-3 py-2 font-medium">{p.name}</th>
                          {ENV_CLASSES.map((e) => (
                            <th key={e.cls} scope="col" className="w-[170px] px-3 py-2 font-medium text-fg/85">{e.name}</th>
                          ))}
                        </tr>
                      </thead>
                      <tbody>
                        {POLICY_ROWS.map((r) => (
                          <tr key={r.action}>
                            <th scope="row" className="border-t border-subtle px-3 py-1.5 text-left font-normal text-fg/85">{r.label}</th>
                            {classesOf(r.action).map((cls) => {
                              const own = cellOf(p.cells, r.action, cls);
                              const base = cellOf(ws, r.action, cls) ?? "ask";
                              const v = own ?? base;
                              const where = ENV_CLASSES.find((e) => e.cls === cls)?.name ?? "everywhere";
                              return (
                                <td key={cls} colSpan={cls === "any" ? 3 : 1} className="border-t border-subtle px-3 py-[5px]">
                                  <span className="inline-flex items-center gap-2">
                                    <DecisionMenu
                                      applies={v}
                                      selected={own ?? ""}
                                      asWorkspace={{ name: workspace, decision: base }}
                                      ownHint={`${possessive(p.name)} own`}
                                      ariaLabel={`${r.label}, ${where}: ${DECISION_LABEL[v]}, ${own ? `${possessive(p.name)} own` : `as ${workspace}`}`}
                                      disabled={setCell.isPending}
                                      className={cn(
                                        "inline-flex h-[26px] items-center whitespace-nowrap rounded-full border px-[9px] text-xs font-medium",
                                        DECISION_CHIP[v],
                                        own ? "border-solid" : "border-dashed opacity-70",
                                      )}
                                      onPick={(to) => setCell.mutate({ project: p.project_id, action: r.action, cls, own, to })}
                                    />
                                    <span className={cn("whitespace-nowrap text-xs", own ? "text-link" : "text-muted/80")}>
                                      {own ? p.name : `as ${workspace}`}
                                    </span>
                                  </span>
                                </td>
                              );
                            })}
                          </tr>
                        ))}
                      </tbody>
                    </table>
                    <p className="mt-2 text-xs text-muted">
                      Dashed: follows {workspace}. Solid: {possessive(p.name)} own; choose “As {workspace}” in its menu to drop it.
                    </p>
                  </div>
                )}
              </div>
            );
          })
        )}
      </div>
    </section>
  );
}
