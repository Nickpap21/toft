"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Box, CheckCircle2, Circle, Lock, XCircle } from "lucide-react";
import { useAuth } from "@/context/AuthContext";
import { useCanWriteIssues } from "@/components/issues/issue-detail";
import { deploymentHref, serviceHref } from "@/components/home/home";
import { EnvChip } from "@/components/ui/env-chip";
import { LoadFailed, Read, readFailed } from "@/components/shell/unreachable";
import { agentService, type AgentApproval } from "@/lib/api/agents.service";
import { ApiError, notFound } from "@/lib/api/client";
import { allDeploymentsQuery, deploymentService, type Deployment } from "@/lib/api/deployment.service";
import type { Environment } from "@/lib/api/environment.service";
import { serviceService, type Service } from "@/lib/api/service.service";
import { secretService } from "@/lib/api/secret.service";
import { docService } from "@/lib/api/doc.service";
import { serverService } from "@/lib/api/server.service";
import { environmentsQuery } from "@/lib/queries";
import { TONE_CLASSES } from "@/lib/status";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { AlsoGoesLive, CommitList, DeploysValue, LiveValue, useGoesLive } from "@/app/projects/_lib/deploy-confirm";
import { strategyLine } from "@/app/projects/_lib/deploy-confirm-view";
import { deployTarget } from "@/app/projects/[id]/services/[serviceId]/_components/service-view";
import { card, field, h2, label, outline, primary, sum } from "@/app/projects/_lib/ui";
import {
  alsoAllows,
  approveHint,
  approveLabel,
  chip,
  clock,
  decision,
  facts,
  heading,
  isLong,
  keyOf,
  markOf,
  planSteps,
  requestVersion,
  rollbackTo,
  runSteps,
  stamp,
  startedDeployment,
  stepOf,
  stepOutput,
  targetParts,
  title,
  type Run,
} from "./view";

// An agent's request waiting for a person (board ReworkApproval and its
// approved and expired states; AgentApproval's sections 7 and 8 for
// set_secret and run_runbook_step; D-100, D-102, D-145). Owners and
// developers approve or deny it here, signed in; Needs you's Review opens it.
export default function AgentRequestPage() {
  const { id } = useParams<{ id: string }>();
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["agent-approval", id],
    queryFn: () => agentService.approval(id),
    retry: false,
    // Follow it while it waits (someone else may decide, or it expires) and
    // while an approved one runs.
    refetchInterval: (query) => {
      const d = query.state.data;
      if (d?.state === "waiting") return 5000;
      if (d?.state === "approved" && !d.result) return 2000;
      // An approved deploy until it goes live, then once its rollback
      // allowance ends (D-146).
      const rb = d?.state === "approved" ? d.rollback : null;
      if (rb && !rb.live_at && !rb.used_at) return 5000;
      if (rb?.active && rb.until) return Math.max(1000, Date.parse(rb.until) - Date.now() + 1000);
      return false;
    },
  });
  const a = q.data;
  const service = useQuery({
    queryKey: ["service", a?.service_id],
    queryFn: () => serviceService.get(a?.service_id ?? ""),
    enabled: !!a?.service_id,
    retry: false,
  });
  // For a runbook step while it waits, the container it runs in (the
  // service's first, as the backend picks it) and that container's server.
  const stepWaits = a?.tool === "run_runbook_step" && a.state === "waiting" && !!a.service_id;
  const containers = useQuery({
    queryKey: ["containers", a?.service_id],
    queryFn: () => serviceService.listContainers(a?.service_id ?? ""),
    enabled: stepWaits,
    retry: false,
  });
  const serverID = service.data?.server_id ?? "";
  const server = useQuery({
    queryKey: ["server", serverID],
    queryFn: () => serverService.get(serverID),
    enabled: stepWaits && !!serverID,
    retry: false,
  });
  // set_secret: the secret it replaces, if one exists (never its value).
  const scope = a?.service_id ? { kind: "service" as const, id: a.service_id } : a?.environment_id ? { kind: "environment" as const, id: a.environment_id } : null;
  const secrets = useQuery({
    queryKey: ["secrets", scope?.kind, scope?.id],
    queryFn: () => secretService.list(scope!),
    enabled: a?.tool === "set_secret" && !!scope,
    retry: false,
  });
  const secret = secrets.data ? (secrets.data.find((x) => x.key === keyOf(a!)) ?? null) : undefined;
  // run_runbook_step: the page as the request pinned it.
  const st = a?.tool === "run_runbook_step" ? stepOf(a) : null;
  const page = useQuery({
    queryKey: ["docs", st?.project, "version", st?.page, st?.version],
    queryFn: () => docService(st!.project).version(st!.page, st!.version),
    enabled: !!st,
    retry: false,
    staleTime: Infinity,
  });
  // The environment's chip, and for a rollback the deployment it goes back to.
  const envs = useQuery({ ...environmentsQuery(service.data?.project_id ?? ""), enabled: !!service.data?.project_id });
  const env = envs.data?.find((e) => e.id === service.data?.environment_id);
  const deps = useQuery({ ...allDeploymentsQuery(a?.service_id ?? ""), enabled: a?.tool === "rollback" && !!a.service_id });
  // The deployment an approved deploy or rollback started, until it ends:
  // whether it failed is on the deployment, not the approval.
  const startedId = a && (a.tool === "deploy" || a.tool === "rollback") ? startedDeployment(a) : "";
  const run = useQuery({
    queryKey: ["deployment", startedId],
    queryFn: () => deploymentService.get(a?.service_id ?? "", startedId),
    enabled: !!startedId && !!a?.service_id,
    retry: false,
    refetchInterval: (query) => (["pending", "building"].includes(query.state.data?.status ?? "") ? 5000 : false),
  });
  useDocumentTitle(`${a ? title(a, page.data?.title) : "Agent request"} · Toft`);

  // A poll that fails under loaded data changes nothing on the page.
  if (!a && notFound(q.error)) {
    return (
      <section className={cn(card, "flex max-w-[760px] flex-col gap-2 p-5")}>
        <h1 className="text-[15px] font-semibold text-fg">No such request</h1>
        <p className="text-[13px] text-fg/70">It may belong to another workspace, or the link is wrong.</p>
      </section>
    );
  }
  if (!a) return <Read query={q} what="this agent request" heading="h1" focus loading={<div role="status" aria-label="Loading" className="h-96 max-w-[760px] rounded-[10px] bg-surface motion-safe:animate-pulse" />}>{() => null}</Read>;

  const project = service.data?.project_id ?? "";
  const toDeployment = (dep: string) => (service.data && a.service_id ? deploymentHref(project, a.service_id, dep) : "");
  const onDecided = (next: AgentApproval) => {
    qc.setQueryData(["agent-approval", id], next);
    qc.invalidateQueries({ queryKey: ["inbox"] });
  };
  // Step 8's block for a deploy or rollback, once its service is read (D-143).
  const goes =
    service.data && (a.tool === "deploy" || (a.tool === "rollback" && deps.data))
      ? { service: service.data, target: a.tool === "rollback" ? (deps.data?.find((d) => d.id === rollbackTo(a)) ?? null) : undefined }
      : undefined;

  return (
    <div className="grid items-start gap-6 xl:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)]">
      {readFailed(service) && (
        <div className="xl:col-span-2">
          <LoadFailed what="the service it would change" onRetry={() => void service.refetch()} error={service.error} />
        </div>
      )}
      <Request
        a={a}
        now={q.dataUpdatedAt}
        run={run.data}
        env={env}
        goes={goes}
        toDeployment={toDeployment}
        serviceLink={service.data && a.service_id ? serviceHref(project, a.service_id) : ""}
        onDecided={onDecided}
        secret={secret}
        live={{ container: containers.data?.[0], server: server.data?.name }}
        step={st && page.data ? {
          name: `Step ${st.step + 1} of “${page.data.title}” · version ${st.version}, edited by ${page.data.author?.name ?? "someone"} on ${new Date(page.data.created_at).toLocaleDateString("en-GB", { day: "numeric", month: "short" })}`,
          title: page.data.title,
          command: runSteps(page.data.body)[st.step]?.command ?? "",
          href: `/projects/${st.project}/docs/${st.page}`,
        } : st && page.isError ? { name: `Step ${st.step + 1} · version ${st.version}`, title: "", command: "", href: `/projects/${st.project}/docs/${st.page}` } : undefined}
      />
      <HowItReached a={a} title={page.data?.title} />
    </div>
  );
}

type Step = { name: string; title: string; command: string; href: string };

/** A row of the card's key-value list (the board's .kvl). */
const kv = "grid grid-cols-[110px_minmax(0,1fr)] gap-3 border-t border-subtle px-4 py-2 text-[13px] first:border-t-0 max-sm:grid-cols-1 max-sm:gap-0.5";
const kvList = "-mx-4 -mt-2 m-0";

/** An agent's request (board ReworkApproval, D-100, D-145): the header, its
 *  words, what Toft does, and the decision. */
function Request({ a, now, run, env, goes, toDeployment, serviceLink, onDecided, secret, live, step }: {
  a: AgentApproval;
  now: number;
  run?: Run;
  env?: Environment;
  goes?: { service: Service; target?: Deployment | null };
  toDeployment: (id: string) => string;
  serviceLink: string;
  onDecided: (next: AgentApproval) => void;
  secret?: { updated_at: string; updated_by?: string } | null;
  live: Parameters<typeof facts>[3];
  step?: Step;
}) {
  // The commit Deploys names, for Approve to send (D-143).
  const [shown, setShown] = useState<string>();
  const { user } = useAuth();
  const me = user?.name ?? "";
  const canDecide = useCanWriteIssues();
  const waiting = a.state === "waiting";
  const c = chip(a, now, me, run);
  const d = decision(a, now, me, run);
  const h = heading(a, step?.title);
  const { project } = targetParts(a.target);
  const steps = planSteps(a.plan);
  const started = startedDeployment(a);
  const startedHref = started ? toDeployment(started) : "";
  const output = stepOutput(a);
  const hint = approveHint(a, canDecide);
  // At phone width a long reason or plan folds, so what Toft checked is in reach.
  const [all, setAll] = useState(false);
  const longReason = isLong(a.reason);
  const longPlan = isLong(a.plan_steps.length > 0 ? a.plan_steps.map((s) => s.text).join("\n") : a.plan);
  const clamp = (long: boolean) => long && !all && "max-md:line-clamp-3";
  // Approve and Deny go with the decision, so focus moves to what replaced
  // them. Someone else's decision, read by the poll, leaves focus alone.
  const decidedHere = useRef(false);
  return (
    <div className="flex min-w-0 max-w-[760px] flex-col gap-4 [overflow-wrap:anywhere]">
      <div className="flex flex-col gap-3">
        <span className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <span className={cn("inline-flex h-6 items-center rounded-full px-[9px] text-xs font-medium", TONE_CLASSES[c.tone].chip)}>{c.label}</span>
          {c.hint && <span className="text-xs text-muted">{c.hint}</span>}
        </span>
        <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">
          {h.text} {h.env && (env ? <EnvChip env={env} size="md" className="align-[2px]" /> : h.env)}
        </h1>
        <p className="-mt-1 text-[13px] leading-5 text-muted">
          Acting for {a.user_name} ({a.user_role}){project && ` · ${project}`} · asked at {stamp(a.created_at, now)} from{" "}
          <Link href="/settings/agents" className="inline-link">
            the connection
          </Link>
          {a.project_id && (
            <>
              {" · "}
              <Link href={`/projects/${a.project_id}/settings#s-agents`} className="inline-link">
                {waiting ? `${project || "The project"}’s rule: Ask a person` : `${project || "The project"}’s agent rules`}
              </Link>
            </>
          )}
        </p>
      </div>

      {d && (
        <div
          ref={(el) => {
            if (el && decidedHere.current) {
              decidedHere.current = false;
              el.focus();
            }
          }}
          tabIndex={-1}
          className="flex flex-col gap-2.5 rounded-lg border border-subtle bg-surface px-3.5 py-3"
        >
          <p className="flex items-start gap-2.5 text-[15px] leading-[22px] text-fg">
            <DecidedIcon a={a} failed={c.tone === "failed"} />
            <span>{d}</span>
          </p>
          {startedHref && (
            <Link href={startedHref} className="self-start text-[13px] text-link hover:underline">
              {a.tool === "rollback" ? "The rollback" : "The deploy"}
            </Link>
          )}
          {a.note && (
            <p className="text-[13px] text-fg/70">
              Note to {a.agent}: “{a.note}”
            </p>
          )}
          {output && output.lines.length > 0 && (
            <pre role="group" tabIndex={0} aria-label="The step’s output" className="max-h-72 overflow-auto whitespace-pre-wrap break-all rounded-lg border border-subtle bg-ground px-3 py-2.5 font-mono text-xs leading-[18px] text-fg/80">
              {output.lines.join("\n")}
            </pre>
          )}
        </div>
      )}

      <div className="flex flex-col gap-1.5 rounded-lg border border-dashed border-strong px-3.5 py-3 text-[13px] leading-5 text-fg/80">
        <span className="text-xs text-muted">Its reason, in its own words</span>
        <p className={cn("whitespace-pre-line", clamp(longReason))}>{a.reason}</p>
        {(a.plan_steps.length > 0 || a.plan) && (
          <span className="mt-1.5 text-xs text-muted">Its plan{a.plan_steps.length > 0 && " · the marks are Toft’s"}</span>
        )}
        {a.plan_steps.length > 0 ? (
          <ol className={cn("m-0 list-decimal pl-[18px]", clamp(longPlan))}>
            {a.plan_steps.map((s, i) => {
              const m = markOf(s.mark, a);
              return (
                <li key={i} className="mt-0.5">
                  <span className="mr-2">{s.text}</span>
                  {m && (
                    <span className={cn("inline-flex h-5 items-center whitespace-nowrap rounded-full px-[7px] align-[1px] text-[11px] font-medium", m.ask ? TONE_CLASSES.pending.chip : "bg-raised text-fg/70")}>
                      {m.label}
                    </span>
                  )}
                </li>
              );
            })}
          </ol>
        ) : steps ? (
          <ol className={cn("m-0 list-decimal pl-[18px]", clamp(longPlan))}>
            {steps.map((s, i) => (
              <li key={i} className="mt-0.5">
                {s}
              </li>
            ))}
          </ol>
        ) : (
          a.plan && <p className={cn("whitespace-pre-line", clamp(longPlan))}>{a.plan}</p>
        )}
        {(longReason || longPlan) && (
          <button
            type="button"
            aria-expanded={all}
            onClick={() => setAll(!all)}
            className="touch-target self-start rounded-sm text-xs text-link hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus md:hidden"
          >
            {all ? "Show fewer" : "Show all"}
          </button>
        )}
      </div>

      <section aria-labelledby="does-h" className={card}>
        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
          <h2 id="does-h" className={h2}>
            {waiting ? (canDecide ? "What Toft does if you approve" : "What Toft does if it’s approved") : "What it asked Toft to do"}
          </h2>
          <span className={sum}>what Toft checked, not the agent</span>
        </div>
        <div className="flex flex-col gap-3.5 p-4">
          {goes ? (
            <Goes a={a} env={h.env} goes={goes} now={now} waiting={waiting} hint={hint} onShown={setShown} />
          ) : (
            <>
              <dl className={kvList}>
                {facts(a, toDeployment, secret, live, now).map((f) => {
                  const href = f.k === "Service" || f.k === "Runs in" ? serviceLink : f.href;
                  const v = <span className={cn(f.warn ? "text-status-pending-fg" : "text-fg", f.mono && "font-mono text-xs")}>{f.v}</span>;
                  return (
                    <div key={f.k} className={kv}>
                      <dt className="text-muted">{f.k}</dt>
                      <dd className="m-0 min-w-0 break-words">
                        {href ? (
                          <Link href={href} className="hover:underline">
                            {v}
                          </Link>
                        ) : (
                          v
                        )}
                        {f.more && <span className="text-muted"> · {f.more}</span>}
                      </dd>
                    </div>
                  );
                })}
              </dl>
              {a.tool === "run_runbook_step" && step && (
                <div className="flex flex-col gap-1.5">
                  <span className={label}>The step it runs, exactly as the page has it</span>
                  <div className="overflow-hidden rounded-lg border border-subtle bg-ground">
                    <div className="flex flex-wrap justify-between gap-2 border-b border-subtle bg-raised/60 px-3 py-1.5 text-xs text-muted">
                      <span>{step.name}</span>
                      <Link href={step.href} className="text-link hover:underline">
                        Open the page
                      </Link>
                    </div>
                    <pre role="group" tabIndex={0} aria-label="The step it runs" className="focus-inset overflow-x-auto whitespace-pre-wrap px-3 py-2.5 font-mono text-[13px] leading-[19px] text-fg">
                      {step.command || "This version of the page has no such step."}
                    </pre>
                  </div>
                </div>
              )}
              {waiting && NOTE[a.tool] && (
                <div role="note" className="flex gap-2.5 rounded-lg border border-subtle bg-raised/60 px-3 py-2.5">
                  {a.tool === "set_secret" ? (
                    <Lock className="mt-0.5 h-4 w-4 flex-none text-fg/70" aria-hidden="true" />
                  ) : (
                    <Box className="mt-0.5 h-4 w-4 flex-none text-fg/70" aria-hidden="true" />
                  )}
                  <span className="text-[13px] leading-[18px] text-fg/85">{NOTE[a.tool]}</span>
                </div>
              )}
              {waiting && <p className="text-[13px] leading-[19px] text-muted">{hint}</p>}
            </>
          )}
        </div>
      </section>

      {waiting &&
        (canDecide ? (
          <Decide
            a={a}
            commit={shown}
            onDecided={(next) => {
              decidedHere.current = true;
              onDecided(next);
            }}
          />
        ) : (
          <p className="text-[13px] text-fg/70">Owners and developers of the workspace approve or deny agents’ requests. You can read it here.</p>
        ))}
    </div>
  );
}

/** The decided card's mark, in the chip's colour. */
function DecidedIcon({ a, failed }: { a: AgentApproval; failed: boolean }) {
  const cls = "mt-[3px] h-4 w-4 flex-none";
  if (failed) return <XCircle className={cn(cls, "text-status-failed-fg")} aria-hidden="true" />;
  if (a.state === "approved") return <CheckCircle2 className={cn(cls, "text-status-running-fg")} aria-hidden="true" />;
  return <Circle className={cn(cls, "text-muted")} aria-hidden="true" />;
}

/** Step 8's "what goes live" for a deploy or rollback request (D-143).
 *  After a deploy went live it counts from what was live then (the
 *  rollback it allowed names it) to the commit that went live. Also goes
 *  live is drawn only while it waits: the live deploy moves after. */
function Goes({ a, env, goes, now, waiting, hint, onShown }: {
  a: AgentApproval;
  env: string;
  goes: { service: Service; target?: Deployment | null };
  now: number;
  waiting: boolean;
  hint: string;
  onShown: (commit?: string) => void;
}) {
  const rb = a.rollback;
  const then = a.state === "approved" && a.tool === "deploy" && !!rb;
  const g = useGoesLive({
    enabled: true,
    service: goes.service,
    envName: env,
    mode: a.tool === "rollback" ? "rollback" : "deploy",
    target: goes.target,
    version: then && rb.commit ? { ...requestVersion(a), commitSha: rb.commit } : requestVersion(a),
    live: then ? rb.to_deployment_id : undefined,
  });
  // As DeployConfirm's Confirm: a request that names a commit keeps it.
  const shown = waiting && g.repo && !requestVersion(a)?.commitSha ? g.headSha : undefined;
  useEffect(() => onShown(shown), [shown, onShown]);
  const also = alsoAllows(a, now, g.next || undefined);
  // The approval's line says the roll back already while it waits.
  const strategy = strategyLine(goes.service, g.liveRef, g.next || deployTarget(goes.service) || "the new version", g.mode, !(waiting && a.rollback));
  return (
    <>
      <dl className={kvList}>
        <div className={kv}>
          <dt className="text-muted">{then ? "Live then" : "Live now"}</dt>
          <dd className="m-0 min-w-0 [overflow-wrap:anywhere]">
            <LiveValue g={g} />
          </dd>
        </div>
        <div className={kv}>
          <dt className="text-muted">{a.tool === "rollback" ? "Goes back to" : "Deploys"}</dt>
          <dd className="m-0 min-w-0 [overflow-wrap:anywhere]">
            <DeploysValue g={g} />
          </dd>
        </div>
        {also && (
          <div className={kv}>
            <dt className="text-muted">Also allows</dt>
            <dd className="m-0 min-w-0 [overflow-wrap:anywhere]">
              {also.v} <span className="text-muted">· {also.more}</span>
            </dd>
          </div>
        )}
      </dl>
      <CommitList g={g} />
      {waiting && <AlsoGoesLive g={g} />}
      <p className="text-[13px] leading-[19px] text-muted">
        {strategy}
        {waiting && ` ${hint}`}
      </p>
      {waiting && g.busy && <p className="text-xs leading-[18px] text-muted">{g.busy}</p>}
    </>
  );
}

/** Where the request reached people (Needs you and the Inbox, D-100) and
 *  who may decide: a side column from 1200 px, after the request below. */
function HowItReached({ a, title: pageTitle }: { a: AgentApproval; title?: string }) {
  const { user } = useAuth();
  const h = heading(a, pageTitle);
  const { project } = targetParts(a.target);
  const included = a.user_role === "owner" || a.user_role === "developer" ? `, ${a.user_name} included` : "";
  return (
    <section aria-labelledby="reach-h" className={cn(card, "[overflow-wrap:anywhere]")}>
      <div className="border-b border-subtle px-4 py-3">
        <h2 id="reach-h" className={h2}>
          How it reached you
        </h2>
      </div>
      <div className="flex flex-col gap-3.5 p-4">
        <div className="flex flex-col gap-1 rounded-xl bg-raised px-3 py-2.5 text-[13px] leading-[18px]">
          <span className="flex justify-between text-[11px] text-muted">
            <span>Needs you · Agent asks</span>
            <span>{clock(a.created_at)}</span>
          </span>
          <b className="font-semibold text-fg">
            {h.text.replace(" wants to ", " asks to ")}
            {h.env && ` ${h.env}`}
          </b>
          <span className="text-fg/80">
            For {a.user_name}
            {project && ` · ${project}`}. Expires at {clock(a.expires_at)}.
          </span>
        </div>
        <p className="text-[13px] leading-[19px] text-muted">
          Every owner and developer of {user?.orgName || "the workspace"} gets it{included}: in the Inbox and on Home’s Needs you. The first decision counts; the others see who decided.
        </p>
      </div>
    </section>
  );
}

/** The board's note under the facts, for the tools that have one. */
const NOTE: Record<string, string> = {
  set_secret:
    "Toft keeps the value encrypted with the panel’s key until this is decided, then deletes it: once it is set on Approve, at once on Deny, and within 5 minutes of it expiring. It is never in this page, the API, the audit log or the agent’s audit entry.",
  run_runbook_step:
    "If someone edits the page before you decide, Approve runs nothing and the agent is told the step changed. It runs only this step, once; any other step asks again.",
};

function Decide({ a, commit, onDecided }: { a: AgentApproval; commit?: string; onDecided: (next: AgentApproval) => void }) {
  const qc = useQueryClient();
  const [note, setNote] = useState("");
  const decide = useMutation({
    mutationFn: (approve: boolean) => (approve ? agentService.approve(a.id, note, commit) : agentService.deny(a.id, note)),
    onSuccess: (next) => {
      onDecided(next);
      if (next.state === "approved") {
        const err = next.result && next.result.status >= 300;
        if (err) toast.error("Approved, but it didn’t go through");
        else toast.success(next.result ? "Approved: it ran" : "Approved: it’s running");
      } else toast.success("Denied: nothing ran");
    },
    onError: (err) => {
      // Someone decided first, or it expired: show what it is now.
      if (err instanceof ApiError && err.status === 409) qc.invalidateQueries({ queryKey: ["agent-approval", a.id] });
      toast.error(err instanceof ApiError && err.status < 500 ? err.message : "It didn’t go through. Try again.");
    },
  });
  return (
    <>
      <div className="flex flex-col gap-1.5">
        <label htmlFor="ap-note" className={label}>
          Note to {a.agent} <span className="font-normal text-muted">optional; it reads it either way</span>
        </label>
        <input
          id="ap-note"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          maxLength={2000}
          placeholder="For example: watch checkout’s error rate too"
          className={cn(field, "phone-input")}
        />
      </div>
      {/* On phones a bar pinned to the bottom of the page (D-145). */}
      <div className="flex flex-wrap items-center justify-end gap-2 max-md:sticky max-md:-bottom-4 max-md:z-10 max-md:-mx-4 max-md:-mb-4 max-md:border-t max-md:border-subtle max-md:bg-ground max-md:px-4 max-md:pb-[calc(12px+env(safe-area-inset-bottom))] max-md:pt-3">
        <span className="mr-auto text-xs leading-[18px] text-muted max-md:order-2 max-md:basis-full">Only a signed-in person can decide. Agents and API keys never can.</span>
        <button type="button" onClick={() => decide.mutate(false)} disabled={decide.isPending} className={cn(outline, "justify-center max-md:flex-1")}>
          {decide.isPending && !decide.variables ? "Denying…" : "Deny"}
        </button>
        <button type="button" onClick={() => decide.mutate(true)} disabled={decide.isPending} className={cn(primary, "justify-center whitespace-nowrap max-md:flex-[2]")}>
          {decide.isPending && decide.variables ? "Approving…" : approveLabel(a)}
        </button>
      </div>
    </>
  );
}
