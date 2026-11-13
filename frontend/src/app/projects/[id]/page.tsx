"use client";

import { useRepos, pullHref, repoBase } from "@/components/code/repo";

import { Suspense, useEffect, useState } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";
import { useMutation, useQueries, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, CircleDot, ExternalLink, GitPullRequest, MoreHorizontal, Plus } from "lucide-react";
import { deploymentHref, inboxItems, serviceHref } from "@/components/home/home";
import { NeedsYouRows } from "@/components/inbox/needs-you";
import { EnvChip } from "@/components/ui/env-chip";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { withEnv, useEnvContext } from "@/components/shell/env-context";
import { STATUS as PREVIEW_STATUS } from "@/components/previews/view";
import type { Environment } from "@/lib/api/environment.service";
import { observabilityService } from "@/lib/api/observability.service";
import { previewService } from "@/lib/api/preview.service";
import { repoService } from "@/lib/api/code.service";
import { serviceService, type Service } from "@/lib/api/service.service";
import { connectTo } from "@/lib/network-alias";
import { issueService, type IssueStatus } from "@/lib/api/issue.service";
import { cycleName } from "@/components/issues/cycles";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import { useRedeploy } from "../_lib/redeploy";
import { useBreadcrumbs } from "@/context/BreadcrumbContext";
import {
  environmentsQuery,
  needsYouQuery,
  projectOverviewQuery,
  projectQuery,
  serversQuery,
} from "@/lib/queries";
import { safeHref } from "@/lib/routes";
import { notFound } from "@/lib/api/client";
import { LoadFailed, Read, allRead, readFailed } from "@/components/shell/unreachable";
import { projectHealth } from "@/components/shell/health";
import { PROJECT_LETTER } from "@/components/shell/sidebar-projects";
import { TONE_CLASSES, type StatusTone } from "@/lib/status";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import {
  deploymentRef,
  deploymentRefTitle,
} from "./services/[serviceId]/_components/service-view";
import { rowState } from "./services/[serviceId]/_components/deployment-view";
import { envState, kindOf, serverName } from "../_lib/projects-view";
import { ago } from "@/lib/time";
import { NewEnvironmentDialog } from "../_lib/new-environment-dialog";
import { StatusText } from "../_lib/service-actions";
import { ServiceMark } from "../_lib/service-table";
import { card, h1, h2, mono, outline, primary } from "../_lib/ui";

// A project's Overview (board: private/design/canvas/ReworkProject.dc.html,
// layout A, D-155): what's happening in the environment looked at. The
// health line, Needs attention (the Inbox's Needs you for this project),
// recent deploys and connected work; beside them its services, its
// environments with their previews, and the workspace services it reaches.
// In All environments the same, across every environment.

const OPEN: IssueStatus[] = ["backlog", "todo", "in_progress", "in_review"];
const FAILING = new Set(["error", "unhealthy"]);
const DEPLOYING = new Set(["deploying", "building", "pending", "queued"]);

const cardHead = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3";
const sum = "text-xs text-muted";
const link = "text-[13px] text-link hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center";
const th = "px-4 py-2 text-left text-xs font-medium text-muted";
const td = "border-t border-subtle px-4 py-2.5 align-middle";
const row = "flex min-h-11 items-center gap-2.5 border-t border-subtle px-4 py-1.5 text-[13px] text-fg hover:bg-raised/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus";
const sub = "px-4 pb-1 pt-2.5 text-xs font-medium text-muted";
const listRow = "flex min-h-9 items-center gap-2.5 px-4 py-1 text-[13px] text-fg pointer-coarse:min-h-11";

export default function ProjectPage() {
  // useSearchParams (the environment context) needs a boundary.
  return (
    <Suspense>
      <ProjectOverview />
    </Suspense>
  );
}

function ProjectOverview() {
  const params = useParams();
  const projectId = params.id as string;
  const role = useOrgRole();
  const { user } = useAuth();
  const canManage = role === "owner" || role === "developer";
  const { redeploy, dialog: redeployDialog } = useRedeploy();
  const { set: setBreadcrumbs } = useBreadcrumbs();

  const projectRead = useQuery(projectQuery(projectId));
  const { data: project, error: projectError } = projectRead;
  // Only once the project loads: a missing one shows Project not found.
  const environmentsRead = useQuery({
    ...environmentsQuery(projectId),
    enabled: !!project,
  });
  const environments = environmentsRead.data;
  const overviewRead = useQuery({
    ...projectOverviewQuery(projectId),
    enabled: !!project,
  });
  const { data: overview, dataUpdatedAt } = overviewRead;
  const servicesRead = useQuery({
    queryKey: ["project-services", projectId],
    queryFn: () => serviceService.listForProject(projectId),
    enabled: !!project,
  });
  const envServices = servicesRead.data;
  // The current cycle's share of this project (D-90).
  const cyclesRead = useQuery({
    queryKey: ["project-cycles", projectId],
    queryFn: () => issueService(projectId).cycles(),
    enabled: !!project,
    staleTime: 60_000,
  });
  const cycles = cyclesRead.data;
  const cycle = cycles?.enabled ? cycles.cycles.find((c) => c.state === "current") : undefined;
  const projectServicesRead = useQuery({
    queryKey: ["project-scoped-services", projectId],
    queryFn: () => serviceService.listProjectScoped(projectId),
    enabled: !!project,
  });
  const projectServices = projectServicesRead.data;
  const workspaceRead = useQuery({
    queryKey: ["org-scoped-services"],
    queryFn: () => serviceService.listOrgScoped(),
    enabled: !!project,
  });
  const workspaceServices = workspaceRead.data;
  const { data: servers } = useQuery(serversQuery);
  const { data: obs } = useQuery({
    queryKey: ["observability-project", projectId],
    queryFn: () => observabilityService.getForProject(projectId),
    enabled: !!project,
    staleTime: 30_000,
  });
  // Home's and the Inbox's query: one fetch for all three.
  const needsRead = useQuery({ ...needsYouQuery(), enabled: !!project });
  const needs = needsRead.data;
  const previewsRead = useQuery({
    queryKey: ["project-previews", projectId],
    queryFn: () => previewService.list(projectId).then((p) => p.data),
    enabled: !!project,
  });
  const previews = previewsRead.data;
  const grafanaUrl = safeHref(
    obs && "grafana_url" in obs ? obs.grafana_url : undefined,
  );
  // Times are relative to the last fetch of what they belong to.
  const now = dataUpdatedAt;

  useDocumentTitle(
    project
      ? `${project.name} · ${user?.orgName ? `${user.orgName} · ` : ""}Toft`
      : "Toft",
  );
  useEffect(() => {
    if (!project) return;
    setBreadcrumbs([
      { label: "Projects", href: "/projects" },
      { label: project.name, href: "" },
    ]);
  }, [project, setBreadcrumbs]);

  const ctx = useEnvContext(projectId, environments);
  const { repos, git, failed: reposFailed, retry: retryRepos } = useRepos(projectId);
  const [creating, setCreating] = useState(false);
  const exportCompose = useMutation({
    mutationFn: () => serviceService.exportCompose(projectId),
    onSuccess: () => toast.success("docker-compose.yml downloaded"),
    onError: () => toast.error("Failed to export the compose file"),
  });

  if (notFound(projectError)) {
    return (
      <NotFound id={projectId} workspace={user?.orgName ?? "this workspace"} />
    );
  }
  if (!project) {
    return (
      <Read
        query={projectRead}
        what="the project"
        heading="h1"
        loading={
          <div
            className="h-64 w-full max-w-[1600px] rounded-[10px] bg-surface motion-safe:animate-pulse"
            aria-label="Loading the project"
          />
        }
      >
        {() => null}
      </Read>
    );
  }

  const byService = new Map(
    (overview?.services ?? []).map((o) => [o.service_id, o]),
  );
  const ownEnvs = (environments ?? []).filter((e) => !e.preview);
  const ownIds = new Set(ownEnvs.map((e) => e.id));
  const svcsOf = (envId: string) =>
    (envServices ?? []).filter((s) => s.environment_id === envId).sort((a, b) => a.name.localeCompare(b.name));
  // The services looked at: the environment's, or every own environment's.
  // Unknown until both reads have answered: what wasn't read isn't none.
  const shown = !envServices || !environments
    ? undefined
    : ctx.env
      ? svcsOf(ctx.env.id)
      : envServices.filter((s) => ownIds.has(s.environment_id ?? "")).sort((a, b) => a.name.localeCompare(b.name));
  const shownRead = allRead(shown, servicesRead, environmentsRead);
  const all = [...(envServices ?? []), ...(projectServices ?? [])];
  const serverIds = [...new Set(all.map((s) => s.server_id ?? ""))];
  const liveIds = new Set(
    all.map((s) => s.active_deployment_id).filter(Boolean),
  );
  const reachable = workspaceServices && envServices && projectServices
    ? workspaceServices.filter((w) => serverIds.includes(w.server_id ?? ""))
    : undefined;
  const reachRead = allRead(reachable, workspaceRead, servicesRead, projectServicesRead);
  const serversText = serverIds.map((id) => serverName(id, servers)).join(", ");
  const envName = ctx.env?.name ?? "all environments";
  const recent = (overview?.recent_deployments ?? [])
    .filter((d) => !ctx.env || !d.environment_name || d.environment_name === ctx.env.name)
    .slice(0, 5);
  const projectNeeds = (needs ?? []).filter((n) => n.project_id === projectId);
  const newServiceHref = `/projects/${projectId}/services/new${ctx.env ? `?env=${ctx.env.id}` : ""}`;
  const repo = repos?.[0];

  return (
    <div className="flex w-full max-w-[1600px] flex-col gap-5 pb-14 max-sm:gap-4 md:-mt-3">
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 flex-wrap items-center gap-3">
            <span
              aria-hidden="true"
              className={cn("flex h-8 w-8 flex-none items-center justify-center rounded-lg text-[15px] font-bold max-sm:hidden", PROJECT_LETTER[projectHealth(all).health])}
            >
              {project.name.trim()[0]?.toUpperCase()}
            </span>
            <h1 className={cn(h1, "min-w-0 break-words")}>{project.name}</h1>
            {project.description && (
              <span className={sum}>{project.description}</span>
            )}
          </div>
          <div className="flex items-center gap-2 max-sm:w-full">
            {(shown?.length ?? 0) > 0 && (
              <button
                type="button"
                className={cn(outline, "max-sm:hidden")}
                onClick={() => exportCompose.mutate()}
                disabled={exportCompose.isPending}
              >
                Export compose
              </button>
            )}
            {canManage && (
              <Link href={newServiceHref} className={cn(primary, "max-sm:flex-1 max-sm:justify-center")}>
                <Plus className="h-[15px] w-[15px]" aria-hidden="true" />
                Add service
              </Link>
            )}
            {(shown?.length ?? 0) > 0 && (
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <button
                    type="button"
                    aria-label="More actions"
                    className="inline-flex h-11 w-11 flex-none items-center justify-center rounded-lg border border-subtle bg-raised text-muted hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus sm:hidden"
                  >
                    <MoreHorizontal className="h-[18px] w-[18px]" aria-hidden="true" />
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem disabled={exportCompose.isPending} onSelect={() => exportCompose.mutate()}>
                    Export compose
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            )}
          </div>
        </div>
        <dl className="flex flex-wrap gap-x-6 gap-y-1.5 text-[13px] max-sm:flex-col">
          {repo && (
            <div className="flex min-w-0 gap-2">
              <dt className="text-muted">Repository</dt>
              <dd className={cn(mono, "self-center [overflow-wrap:anywhere]")}>
                <Link href={repoBase(projectId, repo.name)} className="text-fg underline underline-offset-2 hover:text-link">
                  {repo.full_name}
                </Link>
                {(repos?.length ?? 0) > 1 && (
                  <span className="font-sans text-muted"> and {repos!.length - 1} more</span>
                )}
              </dd>
            </div>
          )}
          {all.length > 0 && (
            <div className="flex min-w-0 gap-2">
              <dt className="text-muted">{serverIds.length > 1 ? "Servers" : "Server"}</dt>
              <dd className={cn(mono, "self-center [overflow-wrap:anywhere]")}>{serversText}</dd>
            </div>
          )}
          <div className="flex gap-2">
            <dt className="text-muted">Created</dt>
            <dd className={cn(mono, "self-center")}>{project.created_at.slice(0, 10)}</dd>
          </div>
          {readFailed(cyclesRead) && (
            <div className="flex gap-2">
              <dt className="text-muted">Current cycle</dt>
              <dd><LoadFailed what="the current cycle" onRetry={() => void cyclesRead.refetch()} error={cyclesRead.error} /></dd>
            </div>
          )}
          {cycle && cycle.counts.scope > 0 && (
            <div className="flex gap-2">
              <dt className="text-muted">{cycleName(cycle)}</dt>
              <dd>
                <Link href={`/projects/${projectId}/issues?view=cycle`} className="text-fg hover:underline">
                  {cycle.counts.shipped} of {cycle.counts.scope} shipped
                </Link>
                <span className="text-muted"> · {cycle.days_left === 0 ? "last day" : `${cycle.days_left} ${cycle.days_left === 1 ? "day" : "days"} left`}</span>
              </dd>
            </div>
          )}
        </dl>
        {shown && <HealthLine projectId={projectId} label={ctx.env?.name ?? "All environments"} services={shown} />}
      </div>

      <div className="grid items-start gap-6 max-sm:gap-4 lg:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)]">
        <div className="flex min-w-0 flex-col gap-5 max-sm:gap-4">
          <section aria-labelledby="na-h" className={card}>
            <div className={cardHead}>
              <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
                <h2 id="na-h" className={h2}>Needs attention</h2>
                {projectNeeds.length > 0 && (
                  <span className={sum}>{projectNeeds.length} in {project.name}</span>
                )}
              </div>
              <Link href="/inbox" className={link}>Open Inbox</Link>
            </div>
            <Read
              query={needsRead}
              what="what needs attention"
              isEmpty={() => projectNeeds.length === 0}
              empty={
                <p className="flex items-center gap-2 px-4 py-3.5 text-[13px] text-muted">
                  <Check className="h-4 w-4 text-status-running" aria-hidden="true" />
                  Nothing in {project.name} needs you.
                </p>
              }
            >
              {() => (
                <div className="[&_li:first-child]:border-t-0">
                  <NeedsYouRows items={inboxItems(projectNeeds, needsRead.dataUpdatedAt)} now={needsRead.dataUpdatedAt} />
                </div>
              )}
            </Read>
          </section>

          <section aria-labelledby="dep-h" className={card}>
            <div className={cardHead}>
              <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
                <h2 id="dep-h" className={h2}>Recent deploys</h2>
                <span className={sum}>{envName}</span>
              </div>
            </div>
            <Read query={overviewRead} what="the recent deploys">
              {() => (
            <table className="w-full border-collapse text-[13px] max-sm:block sm:table-fixed">
              <thead className={recent.length === 0 ? "hidden" : "max-sm:hidden"}>
                <tr>
                  <th scope="col" className={cn(th, "w-32")}>Status</th>
                  <th scope="col" className={th}>Service and commit</th>
                  <th scope="col" className={cn(th, "w-[200px]")}>When</th>
                  <th scope="col" className={cn(th, "w-20")}><span className="sr-only">Actions</span></th>
                </tr>
              </thead>
              <tbody className="max-sm:block">
                {recent.length === 0 && (
                  <tr className="max-sm:block">
                    <td colSpan={4} className="px-4 py-3 text-muted max-sm:block">
                      {ctx.env ? `Nothing deployed to ${ctx.env.name} yet.` : "Nothing deployed yet."}
                    </td>
                  </tr>
                )}
                {recent.map((d, i) => {
                  const live = d.status === "running" && liveIds.has(d.id) ? d.id : undefined;
                  const state = rowState(d, live);
                  const ref = deploymentRef(d);
                  const name = all.find((s) => s.id === d.service_id)?.name ?? d.service_name;
                  const href = deploymentHref(projectId, d.service_id, d.id);
                  return (
                    <tr
                      key={d.id}
                      className={cn(
                        "max-sm:relative max-sm:grid max-sm:grid-cols-[minmax(0,1fr)_auto] max-sm:gap-x-3 max-sm:gap-y-1 max-sm:px-4 max-sm:py-3",
                        i > 0 && "max-sm:border-t max-sm:border-subtle",
                      )}
                    >
                      <td className={cn(td, "whitespace-nowrap max-sm:col-start-1 max-sm:row-start-1 max-sm:border-0 max-sm:p-0")}>
                        <span className={cn("inline-flex items-center gap-1.5 font-medium", TONE_CLASSES[state.tone].text)}>
                          <span className={cn("h-1.5 w-1.5 rounded-full", TONE_CLASSES[state.tone].dot)} aria-hidden="true" />
                          {state.label}
                        </span>
                      </td>
                      <td className={cn(td, "max-sm:col-start-1 max-sm:row-start-2 max-sm:border-0 max-sm:p-0")}>
                        {/* On phones the link covers the card, under the ⋯. */}
                        <Link href={href} className="[overflow-wrap:anywhere] hover:underline max-sm:after:absolute max-sm:after:inset-0 max-sm:after:content-['']">
                          <span className="mr-2 font-medium text-fg">{name}</span>
                          {!ctx.env && d.environment_name && (
                            <span className={cn(mono, "mr-2 text-muted")}>{d.environment_name}</span>
                          )}
                          <span className={cn(mono, "mr-2 text-fg")} title={deploymentRefTitle(d)}>{ref || "—"}</span>
                          {d.commit_message && <span className="text-fg/85">{d.commit_message}</span>}
                        </Link>
                      </td>
                      <td className={cn(td, "text-muted [overflow-wrap:anywhere] max-sm:col-start-1 max-sm:row-start-3 max-sm:border-0 max-sm:p-0")}>
                        <span className="whitespace-nowrap">{ago(d.created_at, now)}</span>
                        {d.triggered_by && ` · ${d.triggered_by}`}
                      </td>
                      <td className={cn(td, "whitespace-nowrap py-1 text-right max-sm:col-start-2 max-sm:row-span-3 max-sm:row-start-1 max-sm:self-center max-sm:border-0 max-sm:p-0")}>
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <button
                              type="button"
                              aria-label={`Actions for deployment ${ref || state.label} of ${name}`}
                              className="relative z-[1] inline-flex h-8 w-8 items-center justify-center rounded-lg text-muted hover:bg-raised hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:h-11 pointer-coarse:w-11"
                            >
                              <MoreHorizontal className="h-[18px] w-[18px]" aria-hidden="true" />
                            </button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem asChild>
                              <Link href={href}>{d.status === "failed" ? "View logs" : "Open deployment"}</Link>
                            </DropdownMenuItem>
                            {live && canManage && d.image_tag && (
                              <DropdownMenuItem onSelect={() => void redeploy(d.service_id, d.id)}>Redeploy {ref || "it"}</DropdownMenuItem>
                            )}
                            {d.commit_sha && (
                              <DropdownMenuItem
                                onSelect={() =>
                                  void (navigator.clipboard?.writeText(d.commit_sha) ?? Promise.reject(new Error("no clipboard"))).then(
                                    () => toast.success("Commit SHA copied"),
                                    () => toast.error("Couldn’t copy the SHA"),
                                  )
                                }
                              >
                                Copy SHA
                              </DropdownMenuItem>
                            )}
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
              )}
            </Read>
            {redeployDialog}
          </section>

          <ConnectedWork projectId={projectId} repos={git?.enabled === false ? [] : repos} reposFailed={reposFailed} retryRepos={retryRepos} openIssues={project.open_issues} previews={previews ?? []} />
        </div>

        <aside className="flex min-w-0 flex-col gap-5 max-sm:gap-4" aria-label={`About ${project.name}`}>
          <section aria-labelledby="svc-h" className={card}>
            <div className={cardHead}>
              <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
                <h2 id="svc-h" className={h2}>Services</h2>
                <span className={sum}>{shown ? shown.length : "—"} in {envName}</span>
              </div>
              <Link href={withEnv(`/projects/${projectId}/services`, ctx.envId)} className={link}>All services</Link>
            </div>
            <Read
              query={shownRead}
              what="the services"
              isEmpty={(list) => list.length === 0}
              empty={
                <p className="px-4 py-3 text-[13px] text-muted">
                  No services in {ctx.env ? <span className={cn(mono, "text-fg")}>{ctx.env.name}</span> : project.name} yet.
                </p>
              }
            >
              {(list) => (
                <ul className="[&>li:first-child>a]:border-t-0">
                  {list.map((s) => (
                    <ServiceRow key={s.id} s={s} env={ctx.env ? undefined : environments?.find((e) => e.id === s.environment_id)} last={byService.get(s.id)?.last_deployment?.created_at} now={now} />
                  ))}
                </ul>
              )}
            </Read>
            {readFailed(projectServicesRead) ? (
              <div className="border-t border-subtle px-4 py-3">
                <LoadFailed what="the project services" onRetry={() => void projectServicesRead.refetch()} />
              </div>
            ) : (projectServices?.length ?? 0) > 0 && (
              <>
                <div className="flex flex-wrap items-center justify-between gap-2 border-t border-subtle px-4 pb-2 pt-3">
                  <h3 className="text-[13px] font-semibold">{projectServices!.length === 1 ? "Project service" : "Project services"}</h3>
                  <span className={sum}>once for every environment</span>
                </div>
                <ul className="[&>li:first-child>a]:border-t-0">
                  {projectServices!.map((s) => (
                    <ServiceRow key={s.id} s={s} last={byService.get(s.id)?.last_deployment?.created_at} now={now} />
                  ))}
                </ul>
              </>
            )}
          </section>

          <section id="environments" aria-labelledby="env-h" className={card}>
            <div className={cardHead}>
              <h2 id="env-h" className={h2}>Environments</h2>
              <span className={sum}>
                {environments ? ownEnvs.length : "—"}
                {(previews?.length ?? 0) > 0 && ` · ${previews!.length} ${previews!.length === 1 ? "preview" : "previews"}`}
              </span>
            </div>
            <Read query={environmentsRead} what="the environments">
              {() => (
            <ul className="[&>li:first-child>a]:border-t-0">
              {ownEnvs.map((env) => {
                const svcs = svcsOf(env.id);
                const st = envState(svcs);
                const ids = [...new Set(svcs.map((s) => s.server_id ?? ""))];
                const current = ctx.env?.id === env.id;
                return (
                  <li key={env.id}>
                    <Link
                      href={withEnv(`/projects/${projectId}`, env.id)}
                      aria-current={current ? "true" : undefined}
                      className={cn(row, "py-2", current && "bg-selected hover:bg-selected")}
                    >
                      {!current && <span className="sr-only">Switch to </span>}
                      <span className="flex min-w-0 flex-1 flex-col items-start gap-1">
                        <EnvChip env={env} size="md" />
                        <span className={cn(sum, "[overflow-wrap:anywhere]")}>
                          {envServices ? (ids.length === 0 ? "no services yet" : ids.length === 1 ? serverName(ids[0], servers) : `${ids.length} servers`) : "—"}
                        </span>
                      </span>
                      {envServices && <Tone tone={st.tone} label={st.label} current={current} />}
                    </Link>
                  </li>
                );
              })}
              {(previews ?? []).map((p) => {
                const env = environments?.find((e) => e.name === p.environment?.name);
                const ids = env ? [...new Set(svcsOf(env.id).map((s) => s.server_id ?? ""))] : [];
                const repoName = repos?.find((r) => r.id === p.repository_id)?.name ?? "";
                const st = PREVIEW_STATUS[p.status];
                return (
                  <li key={`${p.repository_id}-${p.pull_number}`}>
                    <Link href={repoName ? pullHref(repoBase(projectId, repoName), p.pull_number) : `/projects/${projectId}/code`} className={cn(row, "py-2")}>
                      <span className="flex min-w-0 flex-1 flex-col items-start gap-1">
                        {env ? <EnvChip env={env} size="md" /> : <span className={cn(mono, "text-fg")}>pr-{p.pull_number}</span>}
                        <span className={cn(sum, "[overflow-wrap:anywhere]")}>
                          {ids.length === 1 ? `${serverName(ids[0], servers)} · ` : ""}preview of !{p.pull_number}
                        </span>
                      </span>
                      <Tone tone={st.tone === "muted" ? "neutral" : st.tone} label={st.label} />
                    </Link>
                  </li>
                );
              })}
            </ul>
              )}
            </Read>
            {readFailed(previewsRead) && (
              <div className="border-t border-subtle px-4 py-3">
                <LoadFailed what="the previews" onRetry={() => void previewsRead.refetch()} />
              </div>
            )}
            {((projectServices?.length ?? 0) > 0 || canManage) && (
              <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
                <span>
                  {(projectServices?.length ?? 0) > 0
                    ? `${projectServices!.map((s) => s.name).join(", ")} ${projectServices!.length === 1 ? "runs" : "run"} once for ${ownEnvs.length === 2 ? "both" : "every environment"}`
                    : ""}
                </span>
                {canManage && (
                  <button type="button" className={cn(link, "font-medium")} onClick={() => setCreating(true)}>
                    New environment
                  </button>
                )}
              </div>
            )}
          </section>

          <section aria-labelledby="ws-h" className={card}>
            <div className={cardHead}>
              <h2 id="ws-h" className={h2}>Workspace services</h2>
              {(reachable?.length ?? 0) > 0 && <span className={sum}>by alias on {serversText}</span>}
            </div>
            <Read
              query={reachRead}
              what="the workspace services"
              isEmpty={(list) => list.length === 0}
              empty={
                <p className="px-4 py-3 text-[13px] text-muted">
                  {(workspaceServices?.length ?? 0) === 0
                    ? "The workspace has no shared services yet."
                    : `None runs on ${serversText || "a server of this project"}.`}
                </p>
              }
            >
              {(list) => (
                <ul className="py-1">
                  {list.map((w) => (
                    <li key={w.id}>
                      <Link href={serviceHref("", w.id)} className={cn(listRow, "hover:bg-raised/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus")}>
                        <ServiceMark slug={w.addon_slug} />
                        <span className="min-w-0 flex-1 truncate">{w.name}</span>
                        <span className={cn(mono, "truncate text-muted")}>{w.connect_to?.port ? connectTo(w) : ""}</span>
                      </Link>
                    </li>
                  ))}
                </ul>
              )}
            </Read>
            <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-t border-subtle px-4 py-2.5">
              <Link href="/shared" className={link}>All workspace services</Link>
              {grafanaUrl && (
                <a href={grafanaUrl} target="_blank" rel="noopener noreferrer" className={cn(link, "inline-flex items-center gap-1")}>
                  Open Grafana
                  <ExternalLink className="h-3 w-3" aria-hidden="true" />
                </a>
              )}
            </div>
          </section>
        </aside>
      </div>

      <NewEnvironmentDialog
        projectId={projectId}
        open={creating}
        onOpenChange={setCreating}
        onCreated={(env) => ctx.set(env.id)}
      />
    </div>
  );
}

/** "production · 4 running · 1 deploying · none failing", or "1 failing:"
 *  with each failing service's name, a link to it (past three, the rest on
 *  the Services page). */
function HealthLine({ projectId, label, services }: { projectId: string; label: string; services: Service[] }) {
  const n = (pred: (s: Service) => boolean) => services.filter(pred).length;
  const running = n((s) => s.runtime_status === "running");
  const deploying = n((s) => DEPLOYING.has(s.runtime_status));
  const failing = services.filter((s) => FAILING.has(s.runtime_status));
  const stopped = n((s) => s.runtime_status === "stopped");
  const idle = n((s) => s.runtime_status === "idle");
  return (
    <p className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[13px]">
      <span className="text-muted">{label}</span>
      {services.length === 0 && <span className="text-muted">no services yet</span>}
      {running > 0 && <Tone tone="running" label={`${running} running`} />}
      {deploying > 0 && <Tone tone="building" label={`${deploying} deploying`} />}
      {stopped > 0 && <Tone tone="neutral" label={`${stopped} stopped`} />}
      {idle > 0 && <Tone tone="neutral" label={`${idle} idle`} />}
      {services.length > 0 && failing.length === 0 && <span className="text-muted">none failing</span>}
      {failing.length > 0 && (
        <span className="inline-flex flex-wrap items-center gap-x-2">
          <Tone tone="failed" label={`${failing.length} failing:`} />
          {failing.slice(0, 3).map((s) => (
            <Link key={s.id} href={serviceHref(s.project_id, s.id)} className={link}>
              {s.name}
            </Link>
          ))}
          {failing.length > 3 && (
            <Link href={`/projects/${projectId}/services?status=failing`} className={link}>
              {failing.length - 3} more
            </Link>
          )}
        </span>
      )}
    </p>
  );
}

function Tone({ tone, label, current }: { tone: StatusTone; label: string; current?: boolean }) {
  return (
    <span className={cn("inline-flex flex-none items-center gap-1.5 text-xs font-medium sm:text-[13px]", current ? "text-fg" : TONE_CLASSES[tone].text)}>
      <span aria-hidden="true" className={cn("h-1.5 w-1.5 rounded-full", TONE_CLASSES[tone].dot)} />
      {label}
    </span>
  );
}

function ServiceRow({ s, env, last, now }: { s: Service; env?: Environment; last?: string; now: number }) {
  return (
    <li>
      <Link href={serviceHref(s.project_id, s.id)} className={cn(row, "items-start py-2")}>
        <ServiceMark slug={s.addon_slug} />
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="truncate font-medium">{s.name}</span>
          <span className="text-xs text-fg/75">
            {kindOf(s)}
            {env && <span className="text-muted"> · {env.name}</span>}
          </span>
        </span>
        <span className="flex flex-none flex-col items-end gap-0.5 text-xs">
          <StatusText status={s.runtime_status} className="text-xs" />
          {last && <span className="text-muted">{ago(last, now)}</span>}
        </span>
      </Link>
    </li>
  );
}

/** Open pull requests across the project's repositories and its urgent
 *  open issues, as the Service page's Connected work lists a service's. */
function ConnectedWork({ projectId, repos, reposFailed, retryRepos, openIssues, previews }: {
  projectId: string;
  /** Undefined until they're read: no count and no "None open" before that. */
  repos: { id: string; name: string; open_pulls: number }[] | undefined;
  reposFailed: boolean;
  retryRepos: () => void;
  openIssues: number;
  previews: { repository_id: string; pull_number: number }[];
}) {
  const withPulls = (repos ?? []).filter((r) => r.open_pulls > 0);
  const pulls = useQueries({
    queries: withPulls.map((r) => ({
      queryKey: ["repo-pulls", projectId, r.id, "open"],
      queryFn: () => repoService(projectId, r.id).pulls("open").then((p) => p.data),
      staleTime: 30_000,
    })),
  });
  const urgentRead = useQuery({
    queryKey: ["project-issues", projectId, "urgent-open"],
    queryFn: () => issueService(projectId).list({ priority: ["urgent"], status: OPEN, order: "updated" }, undefined, 3),
    staleTime: 30_000,
    enabled: openIssues > 0,
  });
  const urgent = urgentRead.data;
  const countsRead = useQuery({
    queryKey: ["project-issue-counts", projectId, "urgent"],
    queryFn: () => issueService(projectId).counts({ priority: ["urgent"] }),
    staleTime: 30_000,
    enabled: openIssues > 0,
  });
  const urgentCounts = countsRead.data;
  const urgentOpen = urgentCounts ? OPEN.reduce((n, s) => n + urgentCounts[s], 0) : null;
  const failedPulls = pulls.filter(readFailed);
  const totalPulls = withPulls.reduce((n, r) => n + r.open_pulls, 0);
  const rows = withPulls.flatMap((r, i) => (pulls[i]?.data ?? []).map((p) => ({ repo: r, p })));
  const hasPreview = (repoId: string, n: number) => previews.some((p) => p.repository_id === repoId && p.pull_number === n);
  if (!reposFailed && (repos?.length ?? 0) === 0 && openIssues === 0) return null;
  return (
    <section aria-labelledby="conn-h" className={card}>
      <div className={cardHead}>
        <h2 id="conn-h" className={h2}>Connected work</h2>
        {repos && repos.length > 0 && <span className={cn(mono, "text-muted")}>{repos.length === 1 ? repos[0].name : `${repos.length} repositories`}</span>}
      </div>
      <div className="pb-2">
        <div className={sub}>Open pull requests · {repos ? totalPulls : "—"}</div>
        {repos && totalPulls === 0 && <p className={cn(listRow, "text-muted")}>None open.</p>}
        {reposFailed && (
          <div className="px-4 py-1.5">
            <LoadFailed what="the repositories" onRetry={retryRepos} />
          </div>
        )}
        {failedPulls.length > 0 && (
          <div className="px-4 py-1.5">
            <LoadFailed what="the pull requests" onRetry={() => failedPulls.forEach((p) => void p.refetch())} />
          </div>
        )}
        <ul>
          {rows.slice(0, 5).map(({ repo, p }) => (
            <li key={`${repo.id}-${p.number}`}>
              <Link href={pullHref(repoBase(projectId, repo.name), p.number)} className={cn(listRow, "hover:bg-raised/50")}>
                <GitPullRequest className="h-[15px] w-[15px] flex-none text-accent-text" aria-hidden="true" />
                <span className={cn(mono, "text-muted")}>!{p.number}</span>
                <span className="min-w-0 flex-1 truncate">{p.title}</span>
                {hasPreview(repo.id, p.number) && <span className="text-xs text-status-running-fg">preview</span>}
              </Link>
            </li>
          ))}
        </ul>
        {totalPulls > 5 && (
          <p className="px-4 text-xs text-muted">and {totalPulls - 5} more</p>
        )}
        <div className={sub}>{openIssues === 0 ? "Urgent issues" : `Urgent issues · ${urgentOpen ?? "—"} of ${openIssues} open`}</div>
        {openIssues === 0 && <p className={cn(listRow, "text-muted")}>No open issues.</p>}
        {urgentCounts && openIssues > 0 && urgentOpen === 0 && <p className={cn(listRow, "text-muted")}>None urgent.</p>}
        {(readFailed(urgentRead) || readFailed(countsRead)) && (
          <div className="px-4 py-1.5">
            <LoadFailed
              what="the urgent issues"
              onRetry={() => {
                if (readFailed(urgentRead)) void urgentRead.refetch();
                if (readFailed(countsRead)) void countsRead.refetch();
              }}
            />
          </div>
        )}
        <ul>
          {(urgent?.data ?? []).map((iss) => (
            <li key={iss.number}>
              <Link href={`/projects/${projectId}/issues/${iss.number}`} className={cn(listRow, "hover:bg-raised/50")}>
                <CircleDot className="h-[15px] w-[15px] flex-none text-status-pending-fg" aria-hidden="true" />
                <span className={cn(mono, "flex-none text-muted")}>{iss.key}</span>
                <span className="min-w-0 flex-1">{iss.title}</span>
                <span className="text-xs text-status-pending-fg">Urgent</span>
              </Link>
            </li>
          ))}
        </ul>
      </div>
    </section>
  );
}

function NotFound({ id, workspace }: { id: string; workspace: string }) {
  return (
    <section className={cn(card, "flex max-w-[640px] flex-col gap-2 p-5")}>
      <h1 className={h2}>Project not found</h1>
      <p className="text-sm text-fg/70">
        No project in {workspace} has the ID{" "}
        <span className={cn(mono, "text-fg")}>{id}</span>. It may have been
        deleted, or it belongs to a workspace you&apos;re not a member of.
      </p>
      <div className="mt-1 flex flex-wrap gap-2">
        <Link href="/projects" className={outline}>
          All projects
        </Link>
      </div>
    </section>
  );
}
