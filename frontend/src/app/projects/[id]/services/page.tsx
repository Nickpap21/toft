"use client";

import { Suspense, useEffect, useState } from "react";
import Link from "next/link";
import { useParams, useRouter, useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { Plus, Search } from "lucide-react";
import { serviceHref } from "@/components/home/home";
import { EnvChip } from "@/components/ui/env-chip";
import { ALL, useEnvContext } from "@/components/shell/env-context";
import { ProjectPreviewsSection } from "@/components/previews/previews-table";
import { useRepos } from "@/components/code/repo";
import { observabilityService } from "@/lib/api/observability.service";
import { serviceService, type Service } from "@/lib/api/service.service";
import { connectTo } from "@/lib/network-alias";
import { useOrgRole } from "@/context/AuthContext";
import { useBreadcrumbs } from "@/context/BreadcrumbContext";
import {
  environmentsQuery,
  projectOverviewQuery,
  projectQuery,
  serversQuery,
} from "@/lib/queries";
import { safeHref } from "@/lib/routes";
import { notFound } from "@/lib/api/client";
import { LoadFailed, Read, ReadFailed, allRead, readFailed } from "@/components/shell/unreachable";
import { TONE_CLASSES } from "@/lib/status";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { envState, inStatus, matchesText, serverName, type StatusFilter } from "../../_lib/projects-view";
import { ServiceMark, ServiceTable } from "../../_lib/service-table";
import { card, h1, h2, mono, outline, primary } from "../../_lib/ui";

// A project's Services tab (board: private/design/canvas/ReworkServices.dc.html):
// the environment looked at, or every one, a card each; the project
// services and the previews in the same shape; beside them the other
// environments and the workspace services they reach.

const byName = (a: Service, b: Service) => a.name.localeCompare(b.name);
const DEPLOYING = new Set(["deploying", "building", "pending", "queued"]);
const cardHead = "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3";
const sum = "text-xs text-muted";
const link = "text-[13px] text-link hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center";

export default function ProjectServicesPage() {
  // useSearchParams needs a boundary on a page Next prerenders.
  return (
    <Suspense>
      <ProjectServices />
    </Suspense>
  );
}

function ProjectServices() {
  const { id: projectId } = useParams<{ id: string }>();
  const router = useRouter();
  const params = useSearchParams();
  const role = useOrgRole();
  const canManage = role === "owner" || role === "developer";
  const { set: setBreadcrumbs } = useBreadcrumbs();
  const { data: project, isLoading, error, refetch } = useQuery(projectQuery(projectId));
  const environmentsRead = useQuery({
    ...environmentsQuery(projectId),
    enabled: !!project,
  });
  const environments = environmentsRead.data;
  const ctx = useEnvContext(projectId, environments);
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
  const sharedRead = useQuery({
    queryKey: ["project-scoped-services", projectId],
    queryFn: () => serviceService.listProjectScoped(projectId),
    enabled: !!project,
  });
  const sharedServices = sharedRead.data;
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
  const { repos } = useRepos(projectId);
  const grafanaUrl = safeHref(
    obs && "grafana_url" in obs ? obs.grafana_url : undefined,
  );
  // Times are relative to the last fetch of what they belong to; the
  // previews' still need a clock when the overview hasn't answered.
  const [opened] = useState(() => Date.now());
  const now = dataUpdatedAt || opened;

  useDocumentTitle(project ? `Services · ${project.name} · Toft` : "Toft");
  useEffect(() => {
    if (!project) return;
    setBreadcrumbs([
      { label: "Projects", href: "/projects" },
      { label: project.name, href: `/projects/${projectId}` },
      { label: "Services", href: "" },
    ]);
  }, [project, projectId, setBreadcrumbs]);

  if (isLoading)
    return (
      <div
        className="h-64 w-full max-w-[1600px] rounded-[10px] bg-surface motion-safe:animate-pulse"
        aria-label="Loading the services"
      />
    );
  if (error && !notFound(error) && !project) {
    return <ReadFailed error={error} onRetry={() => void refetch()} />;
  }
  if (notFound(error) || !project) {
    return (
      <section className={cn(card, "flex max-w-[640px] flex-col gap-2 p-5")}>
        <h1 className={h2}>Project not found</h1>
        <Link href="/projects" className={cn(outline, "self-start")}>
          All projects
        </Link>
      </section>
    );
  }

  const ownEnvs = (environments ?? []).filter((e) => !e.preview);
  const svcsOf = (envId: string) =>
    (envServices ?? []).filter((s) => s.environment_id === envId).sort(byName);
  const cards = ctx.env ? [ctx.env] : ownEnvs;
  const others = ctx.env ? ownEnvs.filter((e) => e.id !== ctx.env!.id) : [];
  const shownServices: Service[] = [...cards.flatMap((e) => svcsOf(e.id)), ...(sharedServices ?? [])];
  const serverIds = [...new Set(shownServices.map((s) => s.server_id ?? ""))];
  const serversText = serverIds.map((id) => serverName(id, servers)).join(", ");
  const allIds = new Set([...(envServices ?? []), ...(sharedServices ?? [])].map((s) => s.server_id ?? ""));
  const reachable = workspaceServices && envServices && sharedServices
    ? workspaceServices.filter((w) => allIds.has(w.server_id ?? ""))
    : undefined;
  const reachText = [...allIds].map((id) => serverName(id, servers)).join(", ");
  const byService = overview && new Map(overview.services.map((o) => [o.service_id, o]));
  const repoNames = new Map((repos ?? []).map((r) => [r.id, r.name]));
  const total = (envServices?.length ?? 0) + (sharedServices?.length ?? 0);
  const segEnv = ctx.env ?? ownEnvs[0];
  // Over more than 8 services in view, a filter box and a status toggle,
  // kept in the URL (?q=, ?status=); fewer, and the URL's are ignored.
  const filtering = shownServices.length > 8;
  const q = filtering ? (params.get("q") ?? "") : "";
  const rawStatus = params.get("status");
  const status: StatusFilter = filtering && (rawStatus === "failing" || rawStatus === "stopped") ? rawStatus : "";
  const setFilter = (patch: { q?: string; status?: StatusFilter }) => {
    const next = new URLSearchParams(params.toString());
    for (const [k, v] of Object.entries(patch)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    const qs = next.toString();
    router.replace(qs ? `?${qs}` : "?", { scroll: false });
  };
  const textOk = (s: Service) => matchesText(s, byService?.get(s.id)?.domains ?? [], q);
  const pass = (s: Service) => textOk(s) && inStatus(s, status);
  const active = q.trim() !== "" || status !== "";
  const counted = shownServices.filter(textOk);
  const toggles: { id: StatusFilter; label: string; n: number }[] = [
    { id: "", label: "All", n: counted.length },
    { id: "failing", label: "Failing", n: counted.filter((s) => inStatus(s, "failing")).length },
    { id: "stopped", label: "Stopped", n: counted.filter((s) => inStatus(s, "stopped")).length },
  ];
  const matched = shownServices.filter(pass).length;
  const shownShared = (sharedServices ?? []).filter(pass);
  const newHref = `/projects/${projectId}/services/new${ctx.env ? `?env=${ctx.env.id}` : ""}`;
  // The environment cards need both: a failed read draws neither a card nor a zero.
  const cardsRead = allRead(envServices && environments, servicesRead, environmentsRead);
  const reachRead = allRead(reachable, workspaceRead, servicesRead, sharedRead);

  return (
    <div className="flex w-full max-w-[1600px] flex-col gap-5 pb-14 max-sm:gap-4 md:-mt-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-3 gap-y-1">
          <h1 className={h1}>Services</h1>
          <span className={sum}>
            in {ctx.env ? ctx.env.name : "every environment"}
            {serversText && ` · on ${serversText}`}
          </span>
        </div>
        <div className="flex min-w-0 items-center gap-2 max-sm:w-full">
          {ownEnvs.length > 1 && segEnv && (
            <span role="group" aria-label="Show" className="inline-flex h-9 min-w-0 items-center gap-0.5 rounded-lg border border-subtle p-0.5 pointer-coarse:h-11 max-sm:h-11">
              {[
                { id: segEnv.id, label: segEnv.name },
                { id: ALL, label: "All environments" },
              ].map((o) => (
                <button
                  key={o.id}
                  type="button"
                  title={o.label}
                  aria-pressed={ctx.envId === o.id}
                  onClick={() => ctx.set(o.id)}
                  className={cn(
                    "h-full min-w-0 max-w-[10rem] truncate whitespace-nowrap rounded-md px-2.5 text-[13px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus",
                    ctx.envId === o.id ? "bg-raised font-medium text-fg" : "text-fg/75 hover:text-fg",
                  )}
                >
                  {o.label}
                </button>
              ))}
            </span>
          )}
          {canManage && (
            <Link href={newHref} className={cn(primary, "flex-none max-sm:h-11 max-sm:flex-1 max-sm:justify-center")}>
              <Plus className="h-[15px] w-[15px]" aria-hidden="true" />
              Add service
            </Link>
          )}
        </div>
      </div>

      {filtering && (
        <div className="-mt-1 flex flex-wrap items-center gap-x-3 gap-y-2">
          <label className="inline-flex h-8 w-[300px] max-w-full items-center gap-1.5 rounded-lg border border-strong bg-surface px-2.5 text-muted focus-within:ring-2 focus-within:ring-focus pointer-coarse:h-11 max-sm:w-full">
            <Search className="h-[15px] w-[15px] flex-none" aria-hidden="true" />
            <input
              type="search"
              value={q}
              onChange={(e) => setFilter({ q: e.target.value })}
              placeholder="Filter by name, image or domain"
              aria-label="Filter services by name, image or domain"
              className="min-w-0 flex-1 bg-transparent text-[13px] text-fg placeholder:text-muted focus:outline-none"
            />
          </label>
          <span role="group" aria-label="Status" className="inline-flex h-8 items-center gap-0.5 rounded-lg border border-subtle p-0.5 pointer-coarse:h-11">
            {toggles.map((t) => (
              <button
                key={t.id || "all"}
                type="button"
                aria-pressed={status === t.id}
                onClick={() => setFilter({ status: t.id })}
                className={cn(
                  "h-full whitespace-nowrap rounded-md px-2.5 text-[13px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus",
                  status === t.id ? "bg-raised font-medium text-fg" : "text-fg/75 hover:text-fg",
                )}
              >
                {t.label}
                <span className="ml-1.5 font-mono text-[11px] text-muted">{t.n}</span>
              </button>
            ))}
          </span>
        </div>
      )}

      {envServices && sharedServices && total === 0 ? (
        <section className={cn(card, "flex flex-col items-start gap-2 p-5")}>
          <h2 className={h2}>No services in {project.name} yet</h2>
          <p className="max-w-[620px] text-sm text-fg/75">
            Add one from a Git repository, a Docker image, a compose file or the
            catalog. It is saved, not deployed: you deploy it from its page.
          </p>
          {canManage && (
            <Link href={newHref} className={cn(primary, "mt-1")}>
              <Plus className="h-[15px] w-[15px]" aria-hidden="true" />
              Add service
            </Link>
          )}
        </section>
      ) : (
        <div className="grid items-start gap-6 max-sm:gap-4 lg:grid-cols-[minmax(0,1040px)_minmax(320px,1fr)]">
          <div className="flex min-w-0 flex-col gap-5 max-sm:gap-4">
            {active && matched === 0 && (
              <section className={cn(card, "flex flex-wrap items-center gap-3 px-4 py-3.5 text-[13px]")}>
                <span className="text-fg/75">No service matches the filter.</span>
                <button type="button" className={link} onClick={() => setFilter({ q: "", status: "" })}>
                  Clear filter
                </button>
              </section>
            )}
            {readFailed(overviewRead) && (
              <section className={cn(card, "px-4 py-3")}>
                <LoadFailed what="each service’s last deploy" onRetry={() => void overviewRead.refetch()} />
              </section>
            )}
            <Read
              query={cardsRead}
              what="the services"
              heading="h2"
              loading={<div className={cn(card, "h-40 motion-safe:animate-pulse")} role="status" aria-label="Loading the services" />}
            >
            {() => cards.map((env) => {
              const all = svcsOf(env.id);
              const svcs = all.filter(pass);
              if (active && svcs.length === 0) return null;
              const deploying = svcs.filter((s) => DEPLOYING.has(s.runtime_status)).length;
              return (
                <section key={env.id} aria-labelledby={`env-${env.id}-h`} className={card}>
                  <div className={cardHead}>
                    <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
                      <h2 id={`env-${env.id}-h`} className={h2}>{env.name}</h2>
                      <span className={sum}>
                        {active ? `${svcs.length} of ${all.length}` : svcs.length} {all.length === 1 ? "service" : "services"}
                        {deploying > 0 && ` · ${deploying} deploying`}
                      </span>
                    </div>
                  </div>
                  {svcs.length === 0 ? (
                    <p className="px-4 py-3.5 text-[13px] text-muted">
                      No services in <span className={cn(mono, "text-fg")}>{env.name}</span> yet.
                    </p>
                  ) : (
                    <ServiceTable
                      label={`Services in ${env.name}`}
                      services={svcs}
                      byService={byService}
                      servers={servers}
                      env={env}
                      canDeploy={canManage}
                      grafanaUrl={grafanaUrl}
                      now={now}
                    />
                  )}
                </section>
              );
            })}
            </Read>

            {readFailed(sharedRead) ? (
              <section className={cn(card, "px-4 py-3")}>
                <LoadFailed what="the project services" onRetry={() => void sharedRead.refetch()} />
              </section>
            ) : shownShared.length > 0 && (
              <section aria-labelledby="ps-h" className={card}>
                <div className={cardHead}>
                  <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
                    <h2 id="ps-h" className={h2}>Project services</h2>
                    <span className={sum}>run once; every environment of {project.name} reaches them by alias</span>
                  </div>
                </div>
                <ServiceTable
                  label="Project services"
                  services={shownShared}
                  byService={byService}
                  servers={servers}
                  canDeploy={canManage}
                  grafanaUrl={grafanaUrl}
                  now={now}
                />
              </section>
            )}

            <ProjectPreviewsSection projectId={projectId} repoNames={repoNames} now={now} />
          </div>

          <aside className="flex min-w-0 flex-col gap-5 max-sm:gap-4" aria-label="Other environments and workspace services">
            {others.length > 0 && (
              <section aria-labelledby="oe-h" className={card}>
                <div className={cardHead}>
                  <h2 id="oe-h" className={h2}>Other environments</h2>
                  <span className={sum}>{others.length}</span>
                </div>
                <ul>
                  {others.map((env, i) => {
                    const svcs = svcsOf(env.id);
                    const ids = [...new Set(svcs.map((s) => s.server_id ?? ""))];
                    const st = envState(svcs);
                    return (
                      <li key={env.id} className={cn("flex min-h-11 items-center gap-3 px-4 py-2", i > 0 && "border-t border-subtle")}>
                        <span className="flex min-w-0 flex-1 flex-col items-start gap-1">
                          <EnvChip env={env} size="md" />
                          <span className={cn(sum, "[overflow-wrap:anywhere]")}>
                            {envServices && (ids.length === 0 ? "no services yet" : `${ids.length === 1 ? serverName(ids[0], servers) : `${ids.length} servers`} · `)}
                            {svcs.length > 0 && <span className={TONE_CLASSES[st.tone].text}>{st.label}</span>}
                          </span>
                        </span>
                        <button type="button" className={link} onClick={() => ctx.set(env.id)} aria-label={`Switch to ${env.name}`}>
                          Switch
                        </button>
                      </li>
                    );
                  })}
                </ul>
              </section>
            )}

            <section aria-labelledby="ws-h" className={card}>
              <div className={cardHead}>
                <h2 id="ws-h" className={h2}>Workspace services</h2>
                {(reachable?.length ?? 0) > 0 && <span className={sum}>by alias on {reachText}</span>}
              </div>
              <Read
                query={reachRead}
                what="the workspace services"
                isEmpty={(list) => list.length === 0}
                empty={
                  <p className="px-4 py-3 text-[13px] text-muted">
                    {(workspaceServices?.length ?? 0) === 0
                      ? "The workspace has no shared services yet."
                      : `None runs on ${reachText || "a server of this project"}.`}
                  </p>
                }
              >
                {(list) => (
                  <ul className="py-1">
                    {list.map((w) => (
                      <li key={w.id}>
                        <Link href={serviceHref("", w.id)} className="flex min-h-9 items-center gap-2.5 px-4 py-1 text-[13px] text-fg hover:bg-raised/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus pointer-coarse:min-h-11">
                          <ServiceMark slug={w.addon_slug} />
                          <span className="min-w-0 flex-1 truncate">{w.name}</span>
                          <span className={cn(mono, "truncate text-muted")}>{w.connect_to?.port ? connectTo(w) : ""}</span>
                        </Link>
                      </li>
                    ))}
                  </ul>
                )}
              </Read>
              <div className="border-t border-subtle px-4 py-2.5">
                <Link href="/shared" className={link}>All workspace services</Link>
              </div>
            </section>
          </aside>
        </div>
      )}
    </div>
  );
}
