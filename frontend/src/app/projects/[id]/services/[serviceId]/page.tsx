"use client";

import { useState, useEffect, useRef, useTransition } from "react";
import { useParams, useRouter, useSearchParams } from "next/navigation";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { serviceService } from "@/lib/api/service.service";
import { allDeploymentsQuery, deploymentService } from "@/lib/api/deployment.service";
import { deploymentHref } from "@/components/home/home";
import { projectService } from "@/lib/api/project.service";
import { environmentService } from "@/lib/api/environment.service";
import { serverService } from "@/lib/api/server.service";
import { domainService } from "@/lib/api/domain.service";
import { envvarService, varsOf } from "@/lib/api/envvar.service";
import { secretService } from "@/lib/api/secret.service";
import { versionService } from "@/lib/api/version.service";
import { resourcesService } from "@/lib/api/resources.service";
import { cn, possessive } from "@/lib/utils";
import { isStoppable } from "@/lib/status";
import { LoadFailed, ReadFailed, readFailed } from "@/components/shell/unreachable";
import { policyViolationsFromError, type PolicyViolation } from "@/lib/api/deploypolicy.service";
import { PolicyViolationList } from "@/components/ui/policy-violations";
import { CapacityDialog, capacityWarningFrom, type CapacityWarning } from "@/components/ui/capacity-dialog";
import { DeployConfirm } from "@/app/projects/_lib/deploy-confirm";
import { exposedAt } from "./_components/service-view";
import { environmentsQuery } from "@/lib/queries";
import { useOrgRole } from "@/context/AuthContext";
import { useBreadcrumbs } from "@/context/BreadcrumbContext";

import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { EnvChip } from "@/components/ui/env-chip";
import { ALL, withEnv } from "@/components/shell/env-context";
import { useDocumentTitle } from "@/lib/use-document-title";
import { ServiceHeader } from "./_components/service-header";
import { ServiceVariables } from "./_components/tabs/service-variables";
import { DeployLogPanel } from "./_components/deploy-log-panel";
import { ServiceDrawer } from "@/components/ui/service-drawer";
import { OverviewTab } from "./_components/tabs/overview-tab";
import { SettingsTab } from "./_components/tabs/settings-tab";
import { environmentScope, serviceScope } from "@/lib/api/envvar.service";
import { containerKeyCount } from "./_components/variables-view";
import { DeploymentsTab } from "./_components/tabs/deployments-tab";
import { LogsTab } from "./_components/tabs/logs-tab";
import { DeployVersionDialog, type VersionChoice } from "./_components/deploy-version-dialog";

// The board's five tabs (ReworkService, D-136), and "secrets", Variables'
// secrets view. Older ids still open: "environment" is Variables,
// "versions" the Deploy a version dialog, "metrics" Overview's Usage,
// "domains" and "config-files" sections of Settings.
type Tab = "overview" | "deployments" | "logs" | "variables" | "secrets" | "settings";
const TAB_LABEL: Record<Tab, string> = { overview: "Overview", deployments: "Deploys", logs: "Logs", variables: "Variables", secrets: "Variables", settings: "Settings" };
const MOVED: Record<string, { tab: Tab; section?: string }> = {
  environment: { tab: "variables" },
  versions: { tab: "overview" },
  metrics: { tab: "overview" },
  domains: { tab: "settings", section: "domains" },
  "config-files": { tab: "settings", section: "storage" },
};

export default function ServiceDetailPage() {
  const params = useParams();
  const router = useRouter();
  // "" for a workspace service, whose page is /shared/<serviceId> (D-47).
  const projectId = (params.id as string | undefined) ?? "";
  const serviceId = params.serviceId as string;
  const queryClient = useQueryClient();
  const role = useOrgRole();
  const canDeploy = role === "owner" || role === "developer";

  const { set: setBreadcrumbs } = useBreadcrumbs();
  const searchParams = useSearchParams();
  const [, startTransition] = useTransition();

  const rawTab = searchParams.get("tab") ?? "overview";
  const moved = MOVED[rawTab];
  const tab = (moved?.tab ?? rawTab) as Tab;
  const [versionsOpen, setVersionsOpen] = useState(rawTab === "versions");
  // A deployment's page, on the Deployments tab (D-44).
  const deploymentParam = searchParams.get("deployment") ?? "";

  // card opens one of Settings' cards (#set-<card>).
  function setTab(newTab: Tab, card?: string) {
    // The new tab starts at its top: from below the tab bar, <main> scrolls
    // back to it (a card scrolls to itself instead).
    const main = document.getElementById("main");
    const bar = tabList.current;
    if (!card && main && bar) {
      const top = bar.getBoundingClientRect().top - main.getBoundingClientRect().top + main.scrollTop;
      if (main.scrollTop > top) main.scrollTop = top;
    }
    const next = new URLSearchParams(searchParams.toString());
    next.set("tab", newTab);
    next.delete("deployment");
    startTransition(() => {
      router.replace(`?${next.toString()}${card ? `#set-${card}` : ""}`, { scroll: false });
    });
  }

  // An older tab id: the address says where it is now.
  useEffect(() => {
    if (moved) {
      const next = new URLSearchParams(searchParams.toString());
      next.set("tab", moved.tab);
      startTransition(() => {
        router.replace(`?${next.toString()}${moved.section ? `#set-${moved.section}` : ""}`, { scroll: false });
      });
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rawTab]);

  // Terminal drawer state; a preview's Shell opens it through ?shell=1.
  const [terminalOpen, setTerminalOpen] = useState(searchParams.get("shell") === "1");
  const [confirmStop, setConfirmStop] = useState(false);
  const [confirmRestart, setConfirmRestart] = useState(false);
  // ⌘K's Restart… and Stop… open these confirmations through ?confirm= (D-50),
  // and its Deploy of a protected service the deploy's.
  const confirmParam = canDeploy ? searchParams.get("confirm") : null;
  const restartOpen = confirmRestart || confirmParam === "restart";
  const stopOpen = confirmStop || confirmParam === "stop";
  function closeConfirm(set: (open: boolean) => void) {
    set(false);
    if (!searchParams.has("confirm")) return;
    const next = new URLSearchParams(searchParams.toString());
    next.delete("confirm");
    router.replace(`?${next.toString()}`, { scroll: false });
  }

  // Capacity gate warning state
  const [capacityWarning, setCapacityWarning] = useState<CapacityWarning | null>(null);
  // A deploy that moves the service to another server ("" the panel host):
  // kept for "Deploy anyway" after a capacity warning.
  const moveToRef = useRef<string | null>(null);
  // A deploy of a recorded version ("Deploy <ref> again").
  const versionRef = useRef<(VersionChoice & { commitSha?: string }) | null>(null);
  // A deploy into a protected environment confirms first (D-55): the
  // arguments wait in confirmDeploy until the dialog says go. Protection
  // not read yet is read first, so nothing deploys while it's unknown.
  const confirmedRef = useRef(false);
  const [confirmDeploy, setConfirmDeploy] = useState<[string | undefined, (VersionChoice & { commitSha?: string }) | undefined] | null>(null);
  const deploy = (moveTo?: string, version?: VersionChoice & { commitSha?: string }, envs = environmentsData) => {
    // Called from handlers, after the render that declared the queries.
    if (service?.environment_id && !confirmedRef.current) {
      if (!envs) {
        queryClient.fetchQuery(environmentsQuery(projectId)).then(
          (read) => deploy(moveTo, version, read),
          () => toast.error(`Couldn’t read ${possessive(service.name)} environment to deploy it`),
        );
        return;
      }
      if (envs.find((e) => e.id === service.environment_id)?.protected) {
        setConfirmDeploy([moveTo, version]);
        return;
      }
    }
    moveToRef.current = moveTo ?? null;
    versionRef.current = version ?? null;
    triggerDeploy.mutate(false);
  };
  const cancelCapacity = () => {
    setCapacityWarning(null);
    moveToRef.current = null;
    versionRef.current = null;
  };

  // Deploy refused by the deploy policy (422 deploy_policy_violation)
  const [policyRefusal, setPolicyRefusal] = useState<PolicyViolation[] | null>(null);

  // Deploy SSE state
  const [deploymentId, setDeploymentId] = useState<string | null>(null);
  const [deployLog, setDeployLog] = useState<string[]>([]);
  const [deployStreaming, setDeployStreaming] = useState(false);
  const [deployDone, setDeployDone] = useState(false);
  const [deployFailed, setDeployFailed] = useState(false);
  const sseCleanupRef = useRef<(() => void) | null>(null);

  // ─── Queries ──────────────────────────────────────────────────────────────

  const { data: service, isLoading: serviceLoading, error: serviceError, refetch: refetchService } = useQuery({
    queryKey: ["service", serviceId],
    queryFn: () => serviceService.get(serviceId),
  });
  useDocumentTitle(service ? [tab === "deployments" && deploymentParam ? "Deployment" : TAB_LABEL[tab], service.name, "Toft"].filter(Boolean).join(" · ") : "Toft");

  const { data: project } = useQuery({
    queryKey: ["project", projectId],
    queryFn: () => projectService.get(projectId),
    enabled: !!projectId,
  });

  const environmentsRead = useQuery({
    queryKey: ["environments", projectId],
    queryFn: () => environmentService.list(projectId),
    enabled: !!projectId,
    staleTime: 60_000,
  });
  const { data: environmentsData } = environmentsRead;

  const { data: serversData } = useQuery({
    queryKey: ["servers"],
    queryFn: serverService.list,
    staleTime: 60_000,
  });

  // The header's domain and the tab counts, from the tabs' own queries.
  const scope = serviceScope(serviceId);
  const { data: domainsData } = useQuery({
    queryKey: ["domains", serviceId],
    queryFn: () => domainService.list(serviceId),
  });
  const { data: envVarsData } = useQuery({
    queryKey: ["envvars", scope.kind, scope.id],
    queryFn: () => envvarService.list(scope),
    select: varsOf,
  });
  const { data: secretsData } = useQuery({
    queryKey: ["secrets", scope.kind, scope.id],
    queryFn: () => secretService.list(scope),
  });
  // The environment's shared keys, for Variables' count (the tab reads the same).
  const envScope = service?.environment_id ? environmentScope(service.environment_id) : null;
  const { data: sharedVars } = useQuery({
    queryKey: ["envvars", "environment", envScope?.id ?? ""],
    queryFn: () => envvarService.list(envScope!),
    select: varsOf,
    enabled: !!envScope,
  });
  const { data: sharedSecrets } = useQuery({
    queryKey: ["secrets", "environment", envScope?.id ?? ""],
    queryFn: () => secretService.list(envScope!),
    enabled: !!envScope,
  });
  // Unsaved variables: the header's Deploy steps back to secondary (D-145).
  const [varsDirty, setVarsDirty] = useState(false);
  const { data: allDeployments, isError: deploymentsFailed } = useQuery(allDeploymentsQuery(serviceId));
  const hasVersions = service?.source_type === "git" || service?.source_type === "docker-image";
  const isComposeService = service?.source_type === "docker-compose";
  const { data: resources } = useQuery({
    queryKey: ["service-resources", serviceId],
    queryFn: () => resourcesService.service(serviceId),
    enabled: isComposeService,
    staleTime: 60_000,
  });
  const { data: versionInfo } = useQuery({
    queryKey: ["versions", serviceId],
    queryFn: () => versionService.get(serviceId),
    staleTime: 5 * 60 * 1000,
    enabled: hasVersions,
  });


  // On a phone the tab bar scrolls: keep the open tab in view, again when
  // the counts arrive and widen the bar.
  const tabList = useRef<HTMLElement>(null);
  const tabWidths = [!!service, allDeployments?.length, envVarsData?.length, secretsData?.length, sharedVars?.length, sharedSecrets?.length].join();
  useEffect(() => {
    const list = tabList.current;
    const on = list?.querySelector<HTMLElement>('[aria-current="page"]');
    if (!list || !on || list.scrollWidth <= list.clientWidth) return;
    const left = on.getBoundingClientRect().left - list.getBoundingClientRect().left + list.scrollLeft;
    list.scrollTo({ left: left - (list.clientWidth - on.offsetWidth) / 2 });
  }, [tab, tabWidths]);

  // Breadcrumbs
  useEffect(() => {
    if (!service) return;
    setBreadcrumbs(
      projectId
        ? [
            { label: "Projects", href: "/projects" },
            { label: project?.name ?? "Project", href: `/projects/${projectId}` },
            { label: service.name, href: "" },
          ]
        : [
            { label: "Workspace services", href: "/shared" },
            { label: service.name, href: "" },
          ],
    );
  }, [service, project, projectId, setBreadcrumbs]);

  // Cleanup SSE on unmount
  useEffect(() => {
    return () => {
      sseCleanupRef.current?.();
    };
  }, []);

  // ─── Mutations ────────────────────────────────────────────────────────────

  const triggerDeploy = useMutation({
    mutationFn: (force: boolean = false) =>
      deploymentService.trigger(
        serviceId,
        { ...versionRef.current, ...(moveToRef.current !== null ? { serverId: moveToRef.current } : {}) },
        force,
      ),
    onSuccess: (deployment) => {
      moveToRef.current = null;
      versionRef.current = null;
      // On the Deployments tab, the new deployment's page follows it.
      if (tab === "deployments") router.push(deploymentHref(projectId, serviceId, deployment.id));
      toast("Deployment queued");
      queryClient.invalidateQueries({ queryKey: ["deployments", serviceId] });
      setDeployLog([]);
      setDeployDone(false);
      setDeployFailed(false);
      setDeploymentId(deployment.id);
      setDeployStreaming(true);

      sseCleanupRef.current?.();

      const cleanup = deploymentService.streamLogs(
        serviceId,
        deployment.id,
        (entry) => setDeployLog((prev) => [...prev, entry.message]),
        (result) => {
          setDeployStreaming(false);
          // No result: the stream ended before the deploy did, so its outcome
          // is unknown, and the panel stays where the logs stopped.
          setDeployDone(result?.status === "running");
          setDeployFailed(!!result && result.status !== "running");
          if (result?.runtime_status) {
            queryClient.setQueryData(["service", serviceId], (prev: typeof service) =>
              prev ? { ...prev, runtime_status: result.runtime_status } : prev
            );
          } else {
            queryClient.invalidateQueries({ queryKey: ["service", serviceId] });
          }
          queryClient.invalidateQueries({ queryKey: ["deployments", serviceId] });
          queryClient.invalidateQueries({ queryKey: ["env-services", service?.environment_id] });
          if (result?.status === "running") toast.success("Deployment complete");
        },
        (msg) => {
          setDeployStreaming(false);
          toast.error(`Deploy log stream error: ${msg}`);
        },
      );

      sseCleanupRef.current = cleanup;
    },
    onError: (err: unknown) => {
      const warning = capacityWarningFrom(err);
      if (warning) {
        setCapacityWarning(warning);
        return;
      }
      moveToRef.current = null;
      versionRef.current = null;
      const violations = policyViolationsFromError(err);
      if (violations) {
        setPolicyRefusal(violations);
        return;
      }
      const message = err instanceof Error ? err.message : 'Failed to trigger deployment';
      toast.error(message);
    },
  });

  const restartService = useMutation({
    mutationFn: () => serviceService.restart(serviceId),
    onSuccess: () => {
      closeConfirm(setConfirmRestart);
      toast.success(`${service?.name ?? "Service"} restarted`);
      queryClient.invalidateQueries({ queryKey: ["service", serviceId] });
      queryClient.invalidateQueries({ queryKey: ["stats", serviceId] });
    },
    onError: (err: unknown) => {
      closeConfirm(setConfirmRestart);
      toast.error(err instanceof Error && err.message ? err.message : "Failed to restart the service");
    },
  });

  const stopService = useMutation({
    mutationFn: () => serviceService.stop(serviceId),
    onSuccess: () => {
      closeConfirm(setConfirmStop);
      toast.success("Service stopped");
      queryClient.invalidateQueries({ queryKey: ["service", serviceId] });
      queryClient.invalidateQueries({ queryKey: ["env-services", service?.environment_id] });
    },
    onError: () => toast.error("Failed to stop service"),
  });

  // ─── Loading skeleton ──────────────────────────────────────────────────────

  if (!service && serviceError) {
    return <ReadFailed error={serviceError} onRetry={() => void refetchService()} back={{ href: `/projects/${projectId}/services`, label: "Services" }} />;
  }
  if (serviceLoading || !service) {
    return (
      <div className="flex w-full max-w-[1320px] flex-col gap-6 motion-safe:animate-pulse" role="status">
        <span className="sr-only">Loading the service</span>
        <div className="h-16 w-1/3 rounded-[10px] bg-surface" />
        <div className="h-10 rounded-[10px] bg-surface" />
        <div className="h-64 rounded-[10px] bg-surface" />
      </div>
    );
  }

  const isDeploying = service.runtime_status === "deploying";
  const canStop = isStoppable(service.runtime_status);
  const isCompose = service.source_type === "docker-compose";
  const currentEnv = environmentsData?.find((e) => e.id === service.environment_id);
  const server = serversData?.find((s) => s.id === service.server_id);
  // An admin tool open to anyone at its address (the Inbox's exposed_tool).
  const exposed = exposedAt(service, (domainsData ?? []).map((d) => d.hostname));

  // Variables counts every key the container gets: its own and its
  // environment's, variables and secrets (D-145).
  type TabDef = { id: Tab; label: string; count?: number | "—" };
  const variablesCount =
    envVarsData && secretsData && (!envScope || (sharedVars && sharedSecrets))
      ? containerKeyCount([envVarsData, secretsData, sharedVars ?? [], sharedSecrets ?? []])
      : undefined;
  const tabs: TabDef[] = [
    { id: "overview",     label: TAB_LABEL.overview },
    { id: "deployments",  label: TAB_LABEL.deployments, count: allDeployments?.length ?? (deploymentsFailed ? "—" : undefined) },
    { id: "logs",         label: TAB_LABEL.logs },
    { id: "variables",    label: TAB_LABEL.variables, count: variablesCount },
    { id: "settings",     label: TAB_LABEL.settings },
  ];
  const selected = tab === "secrets" ? "variables" : tab;

  return (
    <div className="flex w-full max-w-[1600px] flex-col gap-5 pb-24">
      {readFailed(environmentsRead) && <LoadFailed what="this service’s environment" onRetry={() => void environmentsRead.refetch()} error={environmentsRead.error} />}
      <div className="flex flex-col gap-3">
      <ServiceHeader
        service={service}
        currentEnv={currentEnv}
        server={server}
        domain={domainsData?.[0]}
        composeLimits={isCompose ? resources?.rows : undefined}
        imageTag={envVarsData?.find((v) => v.key === "IMAGE_TAG" && !v.is_secret)?.value}
        projectId={projectId}
        canDeploy={canDeploy}
        isDeploying={isDeploying}
        isRunning={canStop}
        deployPending={triggerDeploy.isPending}
        deploySecondary={varsDirty && (tab === "variables" || tab === "secrets")}
        restartPending={restartService.isPending}
        stopPending={stopService.isPending}
        updateAvailable={!!versionInfo?.is_outdated}
        onVersions={hasVersions ? () => setVersionsOpen(true) : undefined}
        onDeploy={() => deploy()}
        onRestart={() => setConfirmRestart(true)}
        onStop={() => setConfirmStop(true)}
        onShell={() => setTerminalOpen(true)}
      />

      <nav
        ref={tabList}
        aria-label="Service sections"
        className="flex max-w-full gap-1 overflow-x-auto whitespace-nowrap border-b border-subtle"
      >
        {tabs.map((t) => (
          <button
            key={t.id}
            aria-current={selected === t.id ? "page" : undefined}
            onClick={() => setTab(t.id)}
            className={cn(
              "-mb-px inline-flex h-[38px] items-center gap-1.5 border-b-2 px-3 text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus pointer-coarse:min-h-11",
              selected === t.id ? "border-link text-fg" : "border-transparent text-fg/70 hover:text-fg",
            )}
          >
            {t.label}
            {t.count !== undefined && t.count !== 0 && <span className="font-mono text-xs font-normal text-muted">{t.count}</span>}
          </button>
        ))}
      </nav>
      </div>

      {currentEnv?.preview && (
        <p role="note" className="rounded-lg border border-subtle bg-surface px-3.5 py-2.5 text-[13px] leading-[19px] text-fg/80">
          A preview’s service: it follows its original’s settings and !{currentEnv.preview.pull_number}’s commits, and goes when the pull
          request merges or closes. Deploy, Stop and Settings act from the pull request’s Preview environment.
        </p>
      )}

      {/* Amber: a risk, not broken (D-142 keeps red for failing). */}
      {exposed && (
        <section
          aria-labelledby="na-h"
          className="flex flex-wrap items-baseline gap-x-3 gap-y-1.5 rounded-lg border border-status-pending/40 bg-status-pending-soft/20 px-3.5 py-2.5 text-[13px] leading-[19px]"
        >
          <h2 id="na-h" className="font-semibold text-status-pending-fg">Needs attention</h2>
          <p className="min-w-0 flex-1 text-fg/85">
            {service.name} is an admin tool anyone can open at <span className="font-mono text-xs">{exposed}</span>: it has no
            basic auth or IP allowlist.
          </p>
          {canDeploy && (
            <button
              type="button"
              onClick={() => setTab("settings", "access")}
              className="text-link hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
            >
              Turn on access control
            </button>
          )}
        </section>
      )}

      {/* The deploy started here, on every tab but Overview, whose live
          deployment card follows it, and Deployments, which opens its page. */}
      {tab !== "overview" && tab !== "deployments" && (
        <DeployLogPanel
          deployLog={deployLog}
          deployStreaming={deployStreaming}
          deployDone={deployDone}
          deployFailed={deployFailed}
          deploymentId={deploymentId}
          service={service}
          projectId={projectId}
          serviceId={serviceId}
          onClose={() => { setDeployLog([]); setDeployDone(false); }}
        />
      )}


      {/* Tab content */}
      {tab === "overview" && (
        <OverviewTab
          service={service}
          serviceId={serviceId}
          projectId={projectId}
          canDeploy={canDeploy}
        />
      )}
      {tab === "settings" && (
        <SettingsTab
          service={service}
          serviceId={serviceId}
          canDeploy={canDeploy}
          onDeploy={deploy}
          onDeployConfirmed={() => {
            confirmedRef.current = true;
            deploy();
            confirmedRef.current = false;
          }}
          deployPending={triggerDeploy.isPending}
          onDeleteSuccess={() => router.push(projectId ? withEnv(`/projects/${projectId}`, service.environment_id || ALL) : "/shared")}
          section={moved?.section}
          envName={currentEnv?.name}
          env={currentEnv}
        />
      )}
      {(tab === "variables" || tab === "secrets") && (
        <ServiceVariables
          service={service}
          serviceId={serviceId}
          projectId={projectId}
          projectName={project?.name}
          env={currentEnv}
          view={tab}
          canDeploy={canDeploy}
          live={allDeployments?.find((d) => d.id === service.active_deployment_id) ?? null}
          onDirty={setVarsDirty}
        />
      )}
      {tab === "deployments" && (
        <DeploymentsTab
          service={service}
          projectId={projectId}
          env={currentEnv}
          serverName={server?.name ?? "the panel host"}
          deploymentId={deploymentParam}
          canDeploy={canDeploy}
          onDeploy={(version) => deploy(undefined, version)}
          deployBusy={isDeploying || triggerDeploy.isPending}
        />
      )}
      {tab === "logs" && (
        <LogsTab
          service={service}
          serviceId={serviceId}
          projectId={projectId}
          serverName={server?.name ?? "the panel host"}
          serverId={server?.id}
          canDeploy={canDeploy}
          deployLabel={service.source_type === "git" && service.git_branch ? `Deploy ${service.git_branch}` : "Deploy"}
          onDeploy={() => deploy()}
          onDeployments={() => setTab("deployments")}
        />
      )}
      {currentEnv?.protected && (
        <DeployConfirm
          open={confirmDeploy !== null || confirmParam === "deploy"}
          service={service}
          env={currentEnv}
          version={confirmDeploy?.[1]}
          pending={triggerDeploy.isPending}
          onCancel={() => closeConfirm(() => setConfirmDeploy(null))}
          onConfirm={(commitSha) => {
            const [moveTo, version] = confirmDeploy ?? [];
            closeConfirm(() => setConfirmDeploy(null));
            confirmedRef.current = true;
            deploy(moveTo, commitSha ? { ...version, commitSha } : version);
            confirmedRef.current = false;
          }}
        />
      )}

      <ServiceDrawer
        open={terminalOpen}
        onClose={() => setTerminalOpen(false)}
        serviceId={serviceId}
        serviceName={service.name}
        serverId={service.server_id || undefined}
      />

      {/* Restart confirmation */}
      <Dialog open={restartOpen} onOpenChange={(open) => { if (!open && !restartService.isPending) closeConfirm(setConfirmRestart); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="flex flex-wrap items-center gap-2">
              Restart {service.name}{currentEnv ? <> in <EnvChip env={currentEnv} size="md" /></> : null}?
            </DialogTitle>
            <DialogDescription>
              {isCompose ? "Its containers" : "Its container"} on {server?.name ?? "the panel host"} {isCompose ? "restart" : "restarts"} in place: the same
              version, settings and data, with a few seconds of downtime.
            </DialogDescription>
          </DialogHeader>
          <p className="text-[13px] leading-5 text-muted">
            A restart doesn&apos;t apply settings, variables or secrets saved since the live deploy. Deploy for those.
          </p>
          <DialogFooter>
            <Button variant="outline" onClick={() => closeConfirm(setConfirmRestart)} disabled={restartService.isPending}>Cancel</Button>
            <Button onClick={() => restartService.mutate()} disabled={restartService.isPending}>
              {restartService.isPending ? "Restarting…" : `Restart ${service.name}`}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Stop confirmation */}
      <Dialog open={stopOpen} onOpenChange={(open) => { if (!open) closeConfirm(setConfirmStop); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              Stop {service.name}{currentEnv ? <> in {currentEnv.name}</> : null}?
            </DialogTitle>
            <DialogDescription>
              {isCompose ? "Its containers" : "Its container"} on {server?.name ?? "the panel host"} {isCompose ? "stop" : "stops"}.{" "}
              {domainsData?.[0]
                ? <>Requests to {domainsData[0].hostname} fail until you deploy {service.name} again.</>
                : <>Nothing of it runs until you deploy {service.name} again.</>}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => closeConfirm(setConfirmStop)}>Cancel</Button>
            <Button variant="destructive" onClick={() => stopService.mutate()} disabled={stopService.isPending}>
              {stopService.isPending ? "Stopping…" : `Stop ${service.name}`}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Deploy refused by the deploy policy */}
      <Dialog open={!!policyRefusal} onOpenChange={(open) => { if (!open) setPolicyRefusal(null); }}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Deploy refused</DialogTitle>
            <DialogDescription>
              The deploy policy does not allow this service&apos;s configuration. Change the settings below, or
              ask an instance operator to grant this service an exception for the rules it needs.
            </DialogDescription>
          </DialogHeader>
          {policyRefusal && <PolicyViolationList violations={policyRefusal} />}
          <DialogFooter className="mt-4">
            <Button variant="outline" onClick={() => setPolicyRefusal(null)}>
              Close
            </Button>
            <Button
              onClick={() => {
                setPolicyRefusal(null);
                setTab("settings");
              }}
            >
              Open settings
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {hasVersions && (
        <DeployVersionDialog
          open={versionsOpen}
          onOpenChange={setVersionsOpen}
          service={service}
          canDeploy={canDeploy}
          busy={isDeploying || triggerDeploy.isPending}
          onDeploy={(v) => {
            setVersionsOpen(false);
            deploy(undefined, v);
          }}
        />
      )}
      <CapacityDialog
        warning={capacityWarning}
        onCancel={cancelCapacity}
        onDeployAnyway={() => {
          setCapacityWarning(null);
          triggerDeploy.mutate(true);
        }}
      />
    </div>
  );
}
