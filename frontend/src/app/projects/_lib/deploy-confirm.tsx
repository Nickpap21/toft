"use client";

import { useState } from "react";
import Link from "next/link";
import { AlertCircle, RotateCcw } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { deploymentHref } from "@/components/home/home";
import { EnvChip } from "@/components/ui/env-chip";
import { repoService } from "@/lib/api/code.service";
import { allDeploymentsQuery, type Deployment } from "@/lib/api/deployment.service";
import { domainService } from "@/lib/api/domain.service";
import type { Environment } from "@/lib/api/environment.service";
import { environmentScope, envvarService, serviceScope, varsOf } from "@/lib/api/envvar.service";
import { secretService } from "@/lib/api/secret.service";
import type { Service } from "@/lib/api/service.service";
import { projectOverviewQuery, serversQuery } from "@/lib/queries";
import { ago } from "@/lib/time";
import { cn } from "@/lib/utils";
import type { VersionChoice } from "../[id]/services/[serviceId]/_components/deploy-version-dialog";
import { deploymentRef, deployTarget } from "../[id]/services/[serviceId]/_components/service-view";
import { pendingChanges } from "../[id]/services/[serviceId]/_components/variables-view";
import {
  alsoGoesLive,
  checksText,
  commitRows,
  commitsLabel,
  concurrentLine,
  strategyLine,
  type ConfirmMode,
} from "./deploy-confirm-view";
import { ProtectedConfirm } from "./protected-confirm";

const list = "m-0 list-none rounded-lg border border-subtle p-0";
const row = "flex flex-wrap items-baseline gap-x-2 gap-y-1 border-t border-subtle px-3 py-2 text-[13px] first:border-t-0";
const lab = "text-[13px] font-medium text-fg";
const help = "text-xs leading-[18px] text-muted";
/** Also goes live shows this many rows before "N more". */
const ALSO_SHOWN = 5;
const CHECK_TONE = { ok: "text-status-running-fg", bad: "text-status-failed-fg", pending: "text-status-pending-fg" };

/** What a deploy, roll back or redeploy takes live (D-143): the reads it
 *  needs (they start once enabled) and what they add up to. DeployConfirm
 *  and the agent approval page (7g step 9) both show it. live names the
 *  deployment to count from when it isn't the service's live one: an
 *  approved request's "Live then". */
export function useGoesLive({
  enabled,
  service,
  envName,
  mode,
  target,
  version,
  live: liveId,
}: {
  enabled: boolean;
  service: Service;
  envName: string;
  mode: ConfirmMode;
  target?: Deployment | null;
  version?: VersionChoice & { commitSha?: string };
  live?: string;
}) {
  const [now] = useState(() => Date.now());
  const sid = service.id;
  const { data: deployments, isError: deploymentsFailed } = useQuery({ ...allDeploymentsQuery(sid), enabled });
  const { data: domains } = useQuery({ queryKey: ["domains", sid], queryFn: () => domainService.list(sid), enabled });
  const own = serviceScope(sid);
  const envScope = service.environment_id ? environmentScope(service.environment_id) : null;
  const { data: vars } = useQuery({ queryKey: ["envvars", own.kind, own.id], queryFn: () => envvarService.list(own), select: varsOf, enabled });
  const { data: secrets } = useQuery({ queryKey: ["secrets", own.kind, own.id], queryFn: () => secretService.list(own), enabled });
  const { data: envVars } = useQuery({ queryKey: ["envvars", "environment", envScope?.id ?? ""], queryFn: () => envvarService.list(envScope!), select: varsOf, enabled: enabled && !!envScope });
  const { data: envSecrets } = useQuery({ queryKey: ["secrets", "environment", envScope?.id ?? ""], queryFn: () => secretService.list(envScope!), enabled: enabled && !!envScope });
  const { data: overview } = useQuery({ ...projectOverviewQuery(service.project_id), enabled: enabled && !!service.project_id });
  const { data: servers } = useQuery({ ...serversQuery, enabled });

  const live = deployments?.find((d) => d.id === (liveId ?? service.active_deployment_id)) ?? null;
  const repo = service.source_type === "git" && service.repository_id && service.project_id ? repoService(service.project_id, service.repository_id) : null;
  const headRef = version?.commitSha || version?.gitRef || service.git_branch || "main";
  // The branch or tag it deploys from, when it names one: a commit alone names none.
  const headName = version?.gitRef || (version?.commitSha ? "" : headRef);
  const [base, head] =
    mode === "deploy" ? [live?.commit_sha ?? "", headRef] : mode === "rollback" ? [target?.commit_sha ?? "", live?.commit_sha ?? ""] : ["", ""];
  const compare = useQuery({
    queryKey: ["compare", service.repository_id, base, head],
    queryFn: () => repo!.compare(base, head),
    enabled: enabled && !!repo && !!base && !!head,
  });
  // A first deploy has nothing to compare with, and a compare can fail (the
  // live commit force-pushed away): the branch's head alone, so what
  // Confirm sends is still the commit shown.
  const tip = useQuery({
    queryKey: ["commits", service.repository_id, headRef, "tip"],
    queryFn: () => repo!.commits(headRef, ""),
    enabled: enabled && !!repo && mode === "deploy" && ((!!deployments && !live?.commit_sha) || compare.isError),
  });

  const headCommit = mode === "deploy" ? (compare.data ? compare.data.commits.at(-1) : tip.data?.data[0]) : undefined;
  const headSha = mode === "deploy" ? (compare.data?.head.sha ?? headCommit?.sha) : undefined;
  const liveRef = live ? deploymentRef(live) || live.id.slice(0, 8) : "";
  const targetRef = target ? deploymentRef(target) || target.id.slice(0, 8) : "";
  const versionRef = version?.commitSha?.slice(0, 7) || version?.gitRef || (version?.imageTag ? version.imageTag.slice(version.imageTag.lastIndexOf("/") + 1) : "");
  const next = mode === "deploy" ? headSha?.slice(0, 7) || versionRef : targetRef;

  const rows = compare.data ? commitRows(compare.data.commits, deployments ?? [], now) : [];
  const since = live ? (live.started_at ?? live.created_at) : null;
  const also = alsoGoesLive(
    pendingChanges({ own: vars ?? [], ownSecrets: secrets ?? [], env: envVars ?? [], envSecrets: envSecrets ?? [], since, envName }),
    domains ?? [],
    since,
  );
  const busy = concurrentLine(overview?.recent_deployments ?? [], sid, envName, now);
  const server = service.server_id ? (servers?.find((s) => s.id === service.server_id)?.name ?? "its server") : "the panel host";
  // Where it answers now: a domain not routed yet is under Also goes live.
  const domain = domains?.find((d) => d.routed_at)?.hostname;
  const strategy = strategyLine(service, liveRef, next || deployTarget(service) || "the new version", mode);
  return { now, service, mode, target, version, deployments, deploymentsFailed, tip, live, repo, headRef, headName, compare, headCommit, headSha, liveRef, targetRef, versionRef, next, rows, also, busy, server, domain, strategy };
}

type GoesLive = ReturnType<typeof useGoesLive>;

/** Live now: the live deployment's commit, message and age. */
export function LiveValue({ g }: { g: GoesLive }) {
  if (g.live) {
    return (
      <>
        <span className="font-mono text-xs text-fg">{g.liveRef}</span> {g.live.commit_message}{" "}
        <span className="text-muted">· {ago(g.live.finished_at ?? g.live.created_at, g.now)}</span>
      </>
    );
  }
  return <span className="text-muted">{g.deployments ? "Nothing yet" : g.deploymentsFailed ? "Couldn’t read its deployments" : "Reading…"}</span>;
}

/** Deploys (or Goes back to): what runs once it's done. */
export function DeploysValue({ g }: { g: GoesLive }) {
  const { service, mode, target, headCommit } = g;
  if (mode !== "deploy") {
    return target ? (
      <>
        <span className="font-mono text-xs text-fg">{g.targetRef}</span> {mode === "redeploy" ? "again, with today’s variables and settings" : target.commit_message}{" "}
        {mode === "rollback" && <span className="text-muted">· {ago(target.created_at, g.now)}</span>}
      </>
    ) : null;
  }
  if (headCommit) {
    const checks = checksText(headCommit.checks);
    return (
      <>
        <span className="font-mono text-xs text-fg">{headCommit.sha.slice(0, 7)}</span> {headCommit.subject}{" "}
        <span className="text-muted">
          · {g.headName && `${g.headName}, `}
          {ago(headCommit.committed_at, g.now)}
        </span>{" "}
        {checks && <span className={cn("whitespace-nowrap text-xs", CHECK_TONE[checks.tone])}>{checks.text}</span>}
      </>
    );
  }
  if (g.compare.data && g.live) {
    return (
      <>
        <span className="font-mono text-xs text-fg">{g.liveRef}</span> again <span className="text-muted">· {g.headName || g.headRef.slice(0, 7)} has nothing new</span>
      </>
    );
  }
  if (g.repo && (g.tip.isError || (g.compare.isError && !g.live?.commit_sha))) return <>The head of {g.headName || g.headRef.slice(0, 7)} when it builds</>;
  if (g.repo) return <span className="text-muted">Reading commits…</span>;
  if (service.source_type === "git") return g.headName ? <>The head of {g.headName} when it builds</> : <span className="font-mono text-xs text-fg">{g.versionRef}</span>;
  if (g.versionRef) return <span className="font-mono text-xs text-fg">{g.versionRef}</span>;
  const image = deployTarget(service);
  return image ? <span className="font-mono text-xs text-fg">{image}</span> : <>Its compose stack</>;
}

/** "4 commits go live" (or leave), each with its author; a commit whose
 *  own deploy failed here says so, with a Why link. */
export function CommitList({ g }: { g: GoesLive }) {
  if (g.compare.isError) return <p role="alert" className={help}>Couldn’t list the commits since the live deploy: {g.compare.error instanceof Error ? g.compare.error.message : "the repository didn’t answer"}.</p>;
  if (g.rows.length === 0) return null;
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <span className={lab}>{commitsLabel(g.compare.data!.commits.length, g.compare.data!.commits_truncated, g.mode)}</span>
      <ul className={list}>
        {g.rows.map((c) => (
          <li key={c.sha} className={row}>
            <span className="font-mono text-xs text-fg">{c.sha}</span>
            <span className="min-w-0 text-fg">{c.subject}</span>
            <span className="ml-auto text-xs text-muted">{c.author}</span>
            {c.failed && (
              <span className="flex basis-full items-start gap-1.5 text-xs leading-5 text-status-pending-fg">
                <AlertCircle className="mt-[3px] h-3.5 w-3.5 flex-none" aria-hidden="true" />
                <span className="min-w-0 flex-1">{c.failed.text}</span>
                <Link href={deploymentHref(g.service.project_id, g.service.id, c.failed.deploymentId)} className="flex-none text-link hover:underline" aria-label={`Why ${c.sha} failed`}>
                  Why
                </Link>
              </span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** Also goes live: variables, secrets and domains changed since the live deploy. */
export function AlsoGoesLive({ g }: { g: GoesLive }) {
  const [all, setAll] = useState(false);
  const rows = all ? g.also : g.also.slice(0, ALSO_SHOWN);
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <span className={lab}>Also goes live</span>
      {g.also.length > 0 ? (
        <>
          <ul className={list}>
            {rows.map((a) => (
              <li key={a.key} className={row}>
                <span className="font-mono text-xs text-fg">{a.key}</span>
                <span className="min-w-0 font-mono text-xs text-muted [overflow-wrap:anywhere]">{a.text}</span>
                <span className="ml-auto text-xs text-muted">
                  {a.by ? `${a.by}, ` : ""}
                  {ago(a.at, g.now)}
                </span>
              </li>
            ))}
          </ul>
          {g.also.length > rows.length && (
            <button type="button" onClick={() => setAll(true)} className="touch-target self-start rounded-sm text-xs text-link hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus">
              {g.also.length - rows.length} more
            </button>
          )}
          <p className={help}>Variables, secrets and domains changed since the live deploy apply with it.</p>
        </>
      ) : (
        <p className={help}>Nothing else: its variables, secrets and domains are as the live deploy has them.</p>
      )}
    </div>
  );
}

/** A deploy, roll back or redeploy into a protected environment (D-55,
 *  D-143; board ReworkDeployConfirm): what runs now, what deploys, the
 *  commits it moves, what else goes live with it and who else is
 *  deploying there. Its reads start when it opens; none of them blocks
 *  Cancel or the action. For a service building from a project repository
 *  onConfirm gets the commit it showed, so a push while it's open doesn't
 *  ship an unseen commit (D-77 builds exactly it). */
export function DeployConfirm({
  open,
  service,
  env,
  mode = "deploy",
  target,
  version,
  pending,
  onCancel,
  onConfirm,
}: {
  open: boolean;
  service: Service;
  env: Pick<Environment, "name" | "color" | "protected">;
  mode?: ConfirmMode;
  /** Roll back and Redeploy: the deployment that runs again. */
  target?: Deployment | null;
  /** Deploy a version, Deploy <sha> again. */
  version?: VersionChoice & { commitSha?: string };
  pending: boolean;
  onCancel: () => void;
  onConfirm: (commitSha?: string) => void;
}) {
  const g = useGoesLive({ enabled: open, service, envName: env.name, mode, target, version });
  const verb = mode === "deploy" ? "Deploy" : mode === "rollback" ? "Roll back" : "Redeploy";
  const action =
    mode === "deploy" ? (g.next ? `Deploy ${g.next} to ${env.name}` : `Deploy to ${env.name}`) : mode === "rollback" ? `Roll back to ${g.targetRef}` : `Redeploy ${g.targetRef}`;

  return (
    <ProtectedConfirm
      open={open}
      title={
        <>
          {verb} {service.name} {mode === "deploy" ? "to" : "in"}{" "}
          <span className="inline-flex items-center gap-0.5">
            <EnvChip env={env} size="md" />?
          </span>
        </>
      }
      description={
        <>
          A protected environment: {g.domain ? <span className="font-mono text-xs">{g.domain}</span> : service.name} on {g.server}.
        </>
      }
      action={action}
      pending={pending}
      pendingAction={mode === "rollback" ? "Rolling back…" : mode === "redeploy" ? "Redeploying…" : "Deploying…"}
      onCancel={onCancel}
      onConfirm={() => onConfirm(mode === "deploy" && g.repo && !version?.commitSha ? g.headSha : undefined)}
    >
      <dl className="m-0 grid grid-cols-[84px_minmax(0,1fr)] gap-x-3 gap-y-2 text-[13px] max-sm:grid-cols-1 max-sm:gap-y-0.5 [&>dd]:[overflow-wrap:anywhere] max-sm:[&>dd]:mb-2">
        <dt className="text-muted">Live now</dt>
        <dd>
          <LiveValue g={g} />
        </dd>
        <dt className="text-muted">{mode === "rollback" ? "Goes back to" : "Deploys"}</dt>
        <dd>
          <DeploysValue g={g} />
        </dd>
      </dl>

      <CommitList g={g} />
      <AlsoGoesLive g={g} />

      <p className="flex gap-2 rounded-lg border border-subtle px-3 py-2.5 text-[13px] text-fg/80">
        <RotateCcw className="mt-0.5 h-4 w-4 flex-none text-muted" aria-hidden="true" />
        <span>{g.strategy}</span>
      </p>
      {g.busy && <p className={help}>{g.busy}</p>}
    </ProtectedConfirm>
  );
}
