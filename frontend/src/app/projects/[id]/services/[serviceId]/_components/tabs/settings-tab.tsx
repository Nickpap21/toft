"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";
import Link from "next/link";
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { toast } from "sonner";
import {
  AlertTriangle,
  Check,
  HelpCircle,
  Info,
  Minus,
  Plus,
  Tag,
  Trash2,
} from "lucide-react";
import { Read } from "@/components/shell/unreachable";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { PolicyViolationList } from "@/components/ui/policy-violations";
import {
  isValidComposeRouteService,
  serviceService,
  type Service,
  type VolumeInfo,
} from "@/lib/api/service.service";
import { capabilityService, serverService, type Server } from "@/lib/api/server.service";
import { orgService } from "@/lib/api/org.service";
import { useAuth, useOrgRole } from "@/context/AuthContext";
import { resourcesService, limitMB } from "@/lib/api/resources.service";
import { domainService } from "@/lib/api/domain.service";
import { versionService } from "@/lib/api/version.service";
import { envvarService, serviceScope, varsOf } from "@/lib/api/envvar.service";
import { secretService } from "@/lib/api/secret.service";
import { configFileService } from "@/lib/api/configfile.service";
import { sharedVolumeService } from "@/lib/api/sharedvolume.service";
import { allDeploymentsQuery } from "@/lib/api/deployment.service";
import {
  policyViolationsFromError,
  type PolicyViolation,
} from "@/lib/api/deploypolicy.service";
import { cn, possessive } from "@/lib/utils";
import { placeable } from "@/components/servers/agent";
import { isServing, statusLabel, statusTone, TONE_CLASSES } from "@/lib/status";
import {
  DeployPolicyBody,
  deployPolicyQuery,
  deployPolicyQueryKey,
  policyChip,
} from "../deploy-policy-card";
import { SharedMounts } from "../shared-mounts";
import { RepoSourceChoice } from "@/app/projects/_lib/repo-source";
import { DeleteData, useDeleteData } from "@/app/projects/_lib/delete-data";
import { useRepos } from "@/components/code/repo";
import { HealthCheckTest } from "../health-check-test";
import {
  ComposeResources,
  DeployTimeLimit,
  RollbackImagesKept,
  SingleResources,
  limitsSummary,
} from "../settings-resources";
import { DeployStrategyField } from "../deploy-strategy-field";
import { Domains } from "../domains";
import { AdminLoginRow } from "../admin-password-reset";
import type { Environment } from "@/lib/api/environment.service";
import { ConfigFiles } from "../config-files";
import { memoryText } from "../service-view";
import { composeServiceOf } from "../log-view";
import {
  CARD_FIELDS,
  changedFields,
  DEFAULT_PIDS_LIMIT,
  initForm,
  onlyEdited,
  parsePostDeployCmds,
  payloadOf,
  pidsLimitProblem,
  containerCheckProblem,
  deployTimeoutProblem,
  rollbackKeptProblem,
  resync,
  sectionOf,
  withCard,
  type CardId,
  type ConfigForm,
  type PostDeployCmdRow,
  type SectionId,
} from "../settings-form";

export {
  hasUnsavedChanges,
  initForm,
  parsePostDeployCmds,
} from "../settings-form";
import { ISOLATION_NAME } from "@/lib/isolation";

// The Settings tab (board: private/design/canvas/ReworkServiceSettings.dc.html,
// D-136, D-155): one scroll of open cards, each saved on its own, with an
// "On this page" index from 1200 px and a Jump to select below it.

const card = "overflow-hidden rounded-[10px] border border-subtle bg-surface";
const mono = "font-mono text-xs";
const input =
  "h-9 w-full rounded-md border border-strong/70 bg-ground px-2.5 text-[13px] text-fg placeholder:text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-60 pointer-coarse:min-h-11";
const label = "text-[13px] text-fg";
const help = "text-xs leading-[17px] text-muted";
const outline =
  "inline-flex h-8 items-center gap-1.5 rounded-lg border border-subtle bg-raised px-3 text-[13px] font-medium text-fg hover:bg-raised/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-50 pointer-coarse:min-h-11";
/** The board's small buttons, in card headers, rows and footers. */
const small =
  "inline-flex h-[30px] items-center justify-center gap-1.5 rounded-md border border-subtle bg-raised px-2.5 text-xs font-medium text-fg hover:bg-raised/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-50 pointer-coarse:min-h-11";
const smallGhost =
  "inline-flex h-[30px] items-center justify-center gap-1.5 rounded-md px-2.5 text-xs font-medium text-muted hover:bg-raised hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-50 pointer-coarse:min-h-11";

/** The save group whose edits a section's Unsaved chip and footer follow. */
const GROUP: Partial<Record<SectionId, CardId>> = {
  general: "general",
  source: "source",
  domains: "routing",
  access: "access",
  resources: "resources",
  deploy: "deploy",
  health: "health",
  storage: "volumes",
  hooks: "hooks",
  isolation: "isolation",
};

interface SettingsTabProps {
  service: Service;
  serviceId: string;
  canDeploy: boolean;
  /** Deploys the service; with moveTo, to that server ("" the panel host). */
  onDeploy: (moveTo?: string) => void;
  /** Deploys without the protected confirm, which the caller gave. */
  onDeployConfirmed: () => void;
  deployPending: boolean;
  onDeleteSuccess: () => void;
  /** Its environment's name, for the Domains card's summary. */
  envName?: string;
  /** Its environment, for the admin password Reset's protected confirm. */
  env?: Pick<Environment, "name" | "color" | "protected">;
  /** The section to open on arrival, for the old ?tab=domains links. */
  section?: string;
}

export function SettingsTab({
  service,
  serviceId,
  canDeploy,
  onDeploy,
  onDeployConfirmed,
  deployPending,
  onDeleteSuccess,
  envName,
  env,
  section,
}: SettingsTabProps) {
  const queryClient = useQueryClient();
  const isCompose = service.source_type === "docker-compose";
  const name = service.name;

  // ── The form: one set of fields, saved a card at a time ────────────────────
  const [form, setForm] = useState<ConfigForm>(() => initForm(service));
  const [cmds, setCmds] = useState<PostDeployCmdRow[]>(() =>
    parsePostDeployCmds(service.post_deploy_commands ?? ""),
  );
  const [syncedService, setSyncedService] = useState(service);
  const [justSaved, setJustSaved] = useState<{
    fields: Set<string>;
    cmds: boolean;
  } | null>(null);
  if (service !== syncedService) {
    const reset = justSaved?.fields ?? new Set<string>();
    setForm(resync(form, initForm(syncedService), initForm(service), reset));
    const savedCmds = parsePostDeployCmds(
      syncedService.post_deploy_commands ?? "",
    );
    if (justSaved?.cmds || JSON.stringify(cmds) === JSON.stringify(savedCmds))
      setCmds(parsePostDeployCmds(service.post_deploy_commands ?? ""));
    setSyncedService(service);
    setJustSaved(null);
  }
  const saved = initForm(service);
  const savedCmds = parsePostDeployCmds(service.post_deploy_commands ?? "");
  const set = (patch: Partial<ConfigForm>) =>
    setForm((f) => ({ ...f, ...patch }));
  const cmdsChanged = JSON.stringify(cmds) !== JSON.stringify(savedCmds);
  const changes = (c: CardId) =>
    changedFields(c, form, saved).length +
    (c === "hooks" && cmdsChanged ? 1 : 0);

  // ── Moving around the page ─────────────────────────────────────────────────
  // The section in view, marked in the index and shown by Jump to.
  const [current, setCurrent] = useState<string>("general");
  // An instant jump (only overlays move, DESIGN.md → Motion): the browser's
  // scroll anchoring then keeps the section in place while the cards above
  // it finish loading, which a smooth scroll's fixed target doesn't.
  const jump = (id: string) => {
    const to = sectionOf(id);
    requestAnimationFrame(() => {
      const el = document.getElementById(`set-${to}`);
      if (!el) return;
      setCurrent(to);
      el.scrollIntoView({ block: "start" });
      // The section takes focus: Tab goes on from there.
      el.focus({ preventScroll: true });
    });
  };
  // A link to #set-<section> (or an old id) lands there.
  const jumpRef = useRef(jump);
  useEffect(() => {
    jumpRef.current = jump;
  });
  useEffect(() => {
    const go = () => {
      const id = window.location.hash.replace(/^#set-/, "") || section;
      if (id) jumpRef.current(id);
    };
    go();
    window.addEventListener("hashchange", go);
    return () => window.removeEventListener("hashchange", go);
  }, [section]);

  useEffect(() => {
    const dirty =
      (Object.keys(CARD_FIELDS) as CardId[]).some(
        (c) => changedFields(c, form, initForm(service)).length > 0,
      ) || cmdsChanged;
    if (!dirty) return;
    const handler = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [form, cmdsChanged, service]);

  // ── Data ───────────────────────────────────────────────────────────────────
  const { data: servers } = useQuery({
    queryKey: ["servers"],
    queryFn: () => serverService.list(),
    staleTime: 60_000,
  });
  const server = servers?.find((s) => s.id === service.server_id);
  // Where the service can run Sandboxed (D-95): its server's probe, and
  // the workspace's default.
  const { data: orgSettings } = useQuery({
    queryKey: ["org-settings"],
    queryFn: () => orgService.getSettings(),
  });
  const { data: caps } = useQuery({
    queryKey: ["capabilities", service.server_id],
    queryFn: () => capabilityService.get(service.server_id!),
    enabled: !!service.server_id,
  });
  // The panel's own server's, which every member reads here (D-97).
  const { data: orgIsolation } = useQuery({
    queryKey: ["org-isolation"],
    queryFn: () => orgService.getIsolation(),
    enabled: !service.server_id,
  });
  const panelSandboxed = orgIsolation?.servers.find((s) => !s.id);
  const role = useOrgRole();
  const { user } = useAuth();
  const wsDefault = orgSettings?.default_isolation ?? "standard";
  const runsAt = service.isolation ?? wsDefault;
  const sandboxedLevel = service.server_id
    ? caps?.levels.find((l) => l.id === "sandboxed") ?? null
    : panelSandboxed ? { state: panelSandboxed.sandboxed, reason: panelSandboxed.reason } : null;
  const strictLevel = service.server_id
    ? caps?.levels.find((l) => l.id === "sandboxed_strict") ?? null
    : panelSandboxed ? { state: panelSandboxed.sandboxed_strict, reason: panelSandboxed.strict_reason } : null;
  const { data: resources } = useQuery({
    queryKey: ["service-resources", serviceId],
    queryFn: () => resourcesService.service(serviceId),
  });
  const { data: policy } = useQuery(deployPolicyQuery(serviceId));
  const { data: containers } = useQuery({
    queryKey: ["containers", serviceId],
    queryFn: () => serviceService.listContainers(serviceId),
    staleTime: 15_000,
    retry: false,
  });
  const envServicesRead = useQuery({
    queryKey: ["env-services", service.environment_id ?? ""],
    queryFn: () =>
      serviceService.listForEnvironment(service.environment_id ?? ""),
    enabled: !!service.environment_id,
    staleTime: 30_000,
    meta: { quietFailure: "Dependencies draws this read’s failure itself; the deploy summary only leaves the names out." },
  });
  const { data: envServices } = envServicesRead;
  const { data: domains } = useQuery({
    queryKey: ["domains", serviceId],
    queryFn: () => domainService.list(serviceId),
  });
  // The page's tab counts read these too: same keys, no extra requests.
  const scope = serviceScope(serviceId);
  const { data: deployments } = useQuery(allDeploymentsQuery(serviceId));
  const { data: vars } = useQuery({
    queryKey: ["envvars", scope.kind, scope.id],
    queryFn: () => envvarService.list(scope),
    select: varsOf,
  });
  const { data: secrets } = useQuery({
    queryKey: ["secrets", scope.kind, scope.id],
    queryFn: () => secretService.list(scope),
  });
  const { data: files } = useQuery({
    queryKey: ["config-files", serviceId],
    queryFn: () => configFileService.list(serviceId),
    meta: {
      quietFailure:
        "Here it only fills the Storage summary and count, which stay blank without it; ConfigFiles, in that card, shows this read's failure with Retry.",
    },
  });
  const counts: Counts = {
    deployments: deployments?.length,
    variables: vars?.length,
    secrets: secrets?.length,
    files: files?.length,
  };
  const { data: mounts } = useQuery({
    queryKey: ["shared-mounts", serviceId],
    queryFn: () => sharedVolumeService.serviceMounts(serviceId),
    select: (read) => read.data,
    meta: {
      quietFailure:
        "Here it only fills the Storage summary and count, which drop volumes without it, and names in sentences true without them; SharedMounts, in that card, shows this read's failure with Retry.",
    },
  });

  // ── Saving a card ──────────────────────────────────────────────────────────
  const [policyResult, setPolicyResult] = useState<{
    card: CardId;
    tone: "refused" | "warning";
    violations: PolicyViolation[];
  } | null>(null);
  const routeService = form.compose_route_service.trim();
  const routeServiceValid = isValidComposeRouteService(routeService);
  const save = useMutation({
    mutationFn: ({ c }: { c: CardId }) =>
      serviceService.updateConfig(
        serviceId,
        onlyEdited(
          c,
          form,
          saved,
          cmdsChanged,
          payloadOf(
            withCard(c, form, saved),
            c === "hooks" ? cmds : savedCmds,
            isCompose,
          ),
        ),
      ),
    onSuccess: (result, { c }) => {
      const warnings = result?.policy_violations ?? [];
      setPolicyResult(
        warnings.length > 0
          ? { card: c, tone: "warning", violations: warnings }
          : null,
      );
      setJustSaved({
        fields: new Set(CARD_FIELDS[c] as readonly string[]),
        cmds: c === "hooks",
      });
      const deployNow =
        canDeploy && service.runtime_status !== "idle" && c !== "general";
      if (warnings.length > 0)
        toast.warning(
          "Saved, but the deploy policy would refuse it in enforce mode",
        );
      else
        toast.success(
          "Saved",
          deployNow
            ? {
                description: "It applies from the next deploy.",
                action: { label: "Deploy now", onClick: () => onDeploy() },
              }
            : undefined,
        );
      queryClient.invalidateQueries({ queryKey: ["service", serviceId] });
      queryClient.invalidateQueries({
        queryKey: ["service-resources", serviceId],
      });
      queryClient.invalidateQueries({ queryKey: ["volumes", serviceId] });
      queryClient.invalidateQueries({
        queryKey: deployPolicyQueryKey(serviceId),
      });
    },
    onError: (err: unknown, { c }) => {
      const violations = policyViolationsFromError(err);
      if (violations) {
        setPolicyResult({ card: c, tone: "refused", violations });
        toast.error("Not saved: the deploy policy refuses this");
        return;
      }
      setPolicyResult(null);
      toast.error(
        err instanceof Error && err.message ? err.message : "Failed to save",
      );
    },
  });
  const discard = (c: CardId) => {
    setForm(
      (f) =>
        withCard(c, saved, f) && {
          ...f,
          ...Object.fromEntries(
            (CARD_FIELDS[c] as readonly (keyof ConfigForm)[]).map((k) => [
              k,
              saved[k],
            ]),
          ),
        },
    );
    if (c === "hooks") setCmds(savedCmds);
    if (policyResult?.card === c) setPolicyResult(null);
  };
  const footer = (c: CardId, blocked?: string) => {
    const n = changes(c);
    if (!canDeploy || n === 0) return null;
    return (
      <div className="flex flex-col gap-2 border-t border-subtle bg-raised/40 px-4 py-2.5">
        {policyResult?.card === c && (
          <PolicyViolationList
            violations={policyResult.violations}
            tone={policyResult.tone}
            title={
              policyResult.tone === "refused"
                ? "Not saved: the deploy policy refuses it"
                : "Saved, but would be refused in enforce mode"
            }
          />
        )}
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-[13px]">
          <span className="text-fg">
            {n === 1 ? "1 unsaved change" : `${n} unsaved changes`}
          </span>
          <span className={blocked ? "text-status-failed-fg" : "text-muted"}>
            {blocked ||
              (c === "general"
                ? "Shown everywhere once saved."
                : "Applies from the next deploy.")}
          </span>
          <span className="flex-1" />
          <Button
            variant="ghost"
            size="sm"
            onClick={() => discard(c)}
            disabled={save.isPending}
          >
            Discard
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => save.mutate({ c })}
            disabled={!!blocked || save.isPending}
          >
            {save.isPending && save.variables?.c === c ? "Saving…" : "Save"}
          </Button>
        </div>
      </div>
    );
  };

  // ── Summaries for the card headers ─────────────────────────────────────────
  const sourceShort =
    service.source_type === "git"
      ? [service.git_branch, service.base_directory]
          .filter(Boolean)
          .join(" · ") || "main"
      : service.source_type === "docker-image"
        ? (service.docker_image ?? "")
        : service.compose_repo_url
          ? [service.git_branch, service.compose_file_path]
              .filter(Boolean)
              .join(" · ")
          : "compose file in the panel";
  const composeNames = policy?.compose_services ?? null;
  const routeTarget = !service.port
    ? "no port"
    : !isCompose
      ? service.network_alias
        ? `${service.network_alias}:${service.port}`
        : "its container"
      : service.compose_route_service
        ? `${service.compose_route_service}:${service.port}`
        : composeNames && composeNames.length > 1
          ? "not routed: choose a compose service"
          : `${composeNames?.[0] ?? "its only service"}:${service.port}`;
  const inside =
    service.scope === "org"
      ? "the workspace"
      : service.scope === "project"
        ? "the project"
        : envName;
  const domainsShort = !service.port
    ? "no port"
    : `port ${service.port} · ${routeTarget}${!isCompose && inside ? ` inside ${inside}` : ""}`;
  const resShort = isCompose
    ? limitsSummary(resources?.rows)
    : [
        service.cpu_limit ? `${service.cpu_limit} vCPU` : "",
        memoryText(service.memory_limit) || "no memory limit",
      ]
        .filter(Boolean)
        .join(" · ");
  const healthShort = [
    (service.health_check_retries ?? 0) > 0
      ? `GET ${service.health_check_path || "/health"}`
      : "Off",
    service.container_health === "off"
      ? "container check off"
      : service.container_health === "command"
        ? "container check command"
        : "",
  ]
    .filter(Boolean)
    .join(" · ");
  const namedVols = (service.volumes ?? []).filter(
    (v) => v.host_path === undefined,
  );
  const volumeCount = isCompose
    ? 0
    : (service.volumes ?? []).length + (mounts ?? []).length;
  const storageShort = [
    isCompose || !mounts ? "" : volumeCount ? plural(volumeCount, "volume", "volumes") : "no volumes",
    files === undefined
      ? ""
      : files.length
        ? plural(files.length, "config file", "config files")
        : "no config files",
  ]
    .filter(Boolean)
    .join(" · ");
  const hooksShort = service.migrate_command
    ? `Migrate: ${service.migrate_command}`
    : service.pre_deploy_commands ||
        service.post_deploy_commands ||
        service.required_env_vars
      ? "Set"
      : "None";
  const depNames = (envServices ?? [])
    .filter((s) => (service.dependencies ?? []).includes(s.id))
    .map((s) => s.name);
  const deployShort = [
    isCompose
      ? ""
      : service.effective_deploy_strategy === "recreate"
        ? "Stop then start"
        : "Zero downtime",
    depNames.length
      ? `after ${depNames.join(", ")}`
      : (service.dependencies ?? []).length
        ? ""
        : "no dependencies",
  ]
    .filter(Boolean)
    .join(" · ");
  const accessOn =
    !!service.basic_auth_user || (service.ip_allowlist ?? []).length > 0;
  // No port: nothing reaches the service from outside, so Access waits.
  const noPort = !service.port;
  const accessShort = noPort && !accessOn
    ? "No port"
    : accessOn
    ? [
        service.basic_auth_user ? "Basic auth on" : "",
        (service.ip_allowlist ?? []).length
          ? `${service.ip_allowlist!.length} allowed ${service.ip_allowlist!.length === 1 ? "range" : "ranges"}`
          : "",
      ]
        .filter(Boolean)
        .join(" · ")
    : `Off: anyone can reach ${domains?.[0]?.hostname ?? name}`;
  const chip = policyChip(policy);
  const needsLimit = isCompose
    ? (resources?.rows ?? []).some((r) => !r.memory.limit)
    : !service.memory_limit;

  // The index, in page order (D-155): Delete last, set apart.
  const toc: { id: SectionId; label: string; count?: number }[] = [
    { id: "general", label: "General" },
    { id: "source", label: "Source" },
    { id: "domains", label: "Domains", count: domains?.length },
    { id: "access", label: "Access" },
    { id: "resources", label: "Resources" },
    { id: "deploy", label: "Deploy" },
    ...(!isCompose ? [{ id: "health" as const, label: "Health check" }] : []),
    {
      id: "storage",
      label: "Storage",
      count: files === undefined || (!isCompose && !mounts) ? undefined : volumeCount + files.length,
    },
    { id: "hooks", label: "Hooks" },
    { id: "isolation", label: "Isolation" },
    { id: "policy", label: "Deploy policy" },
    ...(canDeploy ? [{ id: "delete" as const, label: `Delete ${name}` }] : []),
  ];
  const noteOf = (id: SectionId) => {
    const g = GROUP[id];
    return g && changes(g) > 0
      ? "unsaved"
      : id === "resources" && needsLimit
        ? "needs a limit"
        : "";
  };
  // The section in view: the first in the band below the phone's top bar
  // and the Jump to bar, down to 45% of the window.
  const tocIds = toc.map((t) => t.id).join();
  useEffect(() => {
    const ids = tocIds.split(",");
    const seen = new Map<string, boolean>();
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) seen.set(e.target.id, e.isIntersecting);
        // At the page's end the last sections never reach the band: the
        // one jumped to stays marked.
        const main = document.getElementById("main");
        if (main && main.scrollTop > 0 && main.scrollTop + main.clientHeight >= main.scrollHeight - 2) return;
        const first = ids.find((id) => seen.get(`set-${id}`));
        if (first) setCurrent(first);
      },
      { rootMargin: "-128px 0px -55% 0px" },
    );
    for (const id of ids) {
      const el = document.getElementById(`set-${id}`);
      if (el) io.observe(el);
    }
    return () => io.disconnect();
  }, [tocIds]);

  const unsaved = (id: SectionId) => {
    const g = GROUP[id];
    return g && changes(g) > 0 ? <Unsaved /> : null;
  };

  const [moving, setMoving] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [addingDomain, setAddingDomain] = useState(false);
  // Isolation shows its summary until Change; the form stays open while it
  // has edits or a policy result, or the saved level can't run there.
  const [isoOpen, setIsoOpen] = useState(false);
  const isoRefused =
    (runsAt === "sandboxed" && !!sandboxedLevel && sandboxedLevel.state !== "available") ||
    (runsAt === "sandboxed_strict" && !!strictLevel && strictLevel.state !== "available");
  const isoShown =
    isoOpen ||
    changes("isolation") > 0 ||
    policyResult?.card === "isolation" ||
    isoRefused;
  const [addingFile, setAddingFile] = useState(false);

  return (
    <div className="flex flex-col gap-5">
      {/* Sticks at <main>'s top edge, above its padding (p-4, md:p-8). */}
      <label data-jump-bar className="sticky -top-4 z-10 -mt-2 flex items-center gap-2.5 border-b border-subtle bg-ground py-2 text-[13px] text-muted md:-top-8 min-[1200px]:hidden">
        Jump to
        <select
          aria-label="Jump to a settings section"
          className={cn(input, "max-w-[320px] flex-1 pointer-coarse:h-11")}
          value={current}
          onChange={(e) => jump(e.target.value)}
        >
          {toc.map((t) => (
            <option key={t.id} value={t.id}>
              {t.count ? `${t.label} (${t.count})` : t.label}
            </option>
          ))}
        </select>
      </label>

      <div className="grid items-start gap-6 min-[1200px]:grid-cols-[minmax(0,1fr)_240px]">
        <div className="flex min-w-0 flex-col gap-5">
          <Section
            id="general"
            title="General"
            chip={unsaved("general")}
            footer={footer(
              "general",
              !form.name.trim() ? "A name is required." : undefined,
            )}
          >
            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                id="set-name"
                labelText="Name"
                hint="Shown everywhere in the panel at once. The running container takes the new name on the next deploy."
              >
                <input
                  id="set-name"
                  className={input}
                  value={form.name}
                  disabled={!canDeploy}
                  onChange={(e) => set({ name: e.target.value })}
                />
              </Field>
              <div className="flex min-w-0 flex-col gap-1.5">
                <span className={label}>Runs on</span>
                <div className="flex min-h-9 flex-wrap items-center justify-between gap-2">
                  <span className="flex min-w-0 flex-wrap items-baseline gap-x-1.5">
                    <span className="break-all font-mono text-[13px] text-fg">
                      {server?.name ?? "the panel host"}
                    </span>
                    {server && (
                      <span
                        className={cn(
                          "text-xs",
                          server.status === "active"
                            ? "text-status-running-fg"
                            : "text-status-pending-fg",
                        )}
                      >
                        ·{" "}
                        {server.status === "active"
                          ? "Online"
                          : statusLabel(server.status)}
                      </span>
                    )}
                  </span>
                  {canDeploy && (
                    <button
                      type="button"
                      className={small}
                      onClick={() => setMoving(true)}
                      disabled={deployPending}
                    >
                      Move…
                    </button>
                  )}
                </div>
                <p className={help}>
                  {!server ? (
                    "The control-plane server"
                  ) : server.shared ? (
                    `Shared by ${server.shared_by}`
                  ) : (
                    <span className="font-mono">{server.ip_address}</span>
                  )}
                  . {name} reserves{" "}
                  <span className="font-mono text-fg/85">
                    {!service.server_id
                      ? "nothing here"
                      : isCompose
                        ? (() => {
                            const mb = (resources?.rows ?? [])
                              .filter((r) => r.memory.source === "panel")
                              .reduce((a, r) => a + limitMB(r.memory.limit), 0);
                            return mb ? memoryText(`${mb}m`) : "nothing yet";
                          })()
                        : [
                            service.cpu_limit ? `${service.cpu_limit} vCPU` : "",
                            memoryText(service.memory_limit),
                          ]
                            .filter(Boolean)
                            .join(" · ") || "nothing"}
                  </span>
                  ; volumes here:{" "}
                  {[
                    ...namedVols.map((v) => v.name),
                    ...(mounts ?? []).map((m) => `${m.name} (shared)`),
                  ].join(", ") || (mounts ? "none" : "—")}
                  . Moving changes where the next deploy runs: you see what
                  moves and what stays before anything changes.
                </p>
              </div>
            </div>
          </Section>

          <Section
            id="source"
            title="Source"
            summary={<span className={mono}>{sourceShort}</span>}
            chip={unsaved("source")}
            footer={footer("source")}
          >
            <div className="flex flex-col gap-4">
              <SourceCard
                service={service}
                serviceId={serviceId}
                form={form}
                set={set}
                canEdit={canDeploy}
              />
              <Credentials
                service={service}
                form={form}
                set={set}
                canEdit={canDeploy}
              />
            </div>
          </Section>

          <Section
            id="domains"
            title="Domains"
            summary={<span className={mono}>{domainsShort}</span>}
            chip={unsaved("domains")}
            action={
              canDeploy && service.port > 0 ? (
                <button
                  type="button"
                  className={small}
                  onClick={() => setAddingDomain(true)}
                >
                  Add domain
                </button>
              ) : undefined
            }
            flush
            footer={
              <>
                {footer(
                  "routing",
                  isCompose && !routeServiceValid
                    ? "The compose service name isn't valid."
                    : undefined,
                )}
                <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
                  <span>
                    {accessOn
                      ? `Access control is on: ${accessShort.toLowerCase()}.`
                      : noPort
                        ? `No port: nothing reaches ${name} from outside.`
                        : `Access control is off: anyone can reach ${name}.`}
                  </span>
                  {(accessOn || !noPort) && (
                    <button
                      type="button"
                      className={small}
                      onClick={() => jump("access")}
                    >
                      {accessOn ? "Change" : "Set up"}
                    </button>
                  )}
                </div>
              </>
            }
          >
            <Domains
              service={service}
              serviceId={serviceId}
              server={server}
              canDeploy={canDeploy}
              deployLabel={
                service.source_type === "git" && service.git_branch
                  ? `Deploy ${service.git_branch}`
                  : "Deploy"
              }
              deployBusy={
                service.runtime_status === "deploying" || deployPending
              }
              onDeploy={() => onDeploy()}
              deployConfirms={!!env?.protected}
              adding={addingDomain}
              onAdding={setAddingDomain}
            >
              {isCompose ? (
                <ComposeRouting
                  service={service}
                  serviceId={serviceId}
                  form={form}
                  set={set}
                  canEdit={canDeploy}
                />
              ) : (
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field
                    id="set-port"
                    labelText="Port"
                    hint={
                      form.port > 0
                        ? `The port ${name} listens on; domains route to it.`
                        : `No port: ${name} takes no domain, and other services can't call it by name.`
                    }
                  >
                    <input
                      id="set-port"
                      type="number"
                      min={0}
                      max={65535}
                      className={cn(input, "font-mono")}
                      value={form.port || ""}
                      placeholder="none"
                      disabled={!canDeploy}
                      onChange={(e) =>
                        set({ port: parseInt(e.target.value) || 0 })
                      }
                    />
                  </Field>
                  <Field
                    id="set-alias"
                    labelText="Network alias"
                    hint={
                      form.network_alias.trim() ? (
                      <>
                        {service.scope === "org"
                          ? "Services of every project on the same server"
                          : service.scope === "project"
                            ? "Services in every environment of the project, on the same server,"
                            : "Services in the environment"}{" "}
                        reach it at{" "}
                        <span className={mono}>
                          {form.network_alias.trim()}
                          {form.port > 0 ? `:${form.port}` : ""}
                        </span>
                        .
                      </>
                      ) : (
                        `No alias: other services can’t call ${name} by name.`
                      )
                    }
                  >
                    <input
                      id="set-alias"
                      className={cn(input, "font-mono")}
                      value={form.network_alias}
                      placeholder="none"
                      disabled={!canDeploy}
                      onChange={(e) => set({ network_alias: e.target.value })}
                    />
                  </Field>
                </div>
              )}
            </Domains>
          </Section>

          <Section
            id="access"
            title="Access"
            summary={accessShort}
            chip={unsaved("access")}
            footer={footer(
              "access",
              form.basicAuthUser.trim() &&
                !service.basic_auth_user &&
                !form.basicAuthPassword
                ? "Set a password for the new username."
                : undefined,
            )}
          >
            {noPort && !accessOn ? (
              <p className="text-[13px] text-fg/85">
                No port: nothing reaches {name} from outside, so there is
                nothing to protect. Set a port under Domains first.
              </p>
            ) : (
            <div className="flex flex-col gap-4">
              {noPort && (
                <p className="text-[13px] text-fg/85">
                  No port: nothing reaches {name} from outside. The settings
                  below stay stored; empty them to remove them.
                </p>
              )}
              <p className="text-[13px] text-fg/85">
                {isCompose
                  ? `Checked by the platform's routing before traffic reaches ${service.compose_route_service || "the compose service that receives the domains"}, on every domain of ${name}.`
                  : `Checked by Traefik on ${server?.name ?? "the panel host"} before traffic reaches ${name}, on every domain.`}
                {!accessOn && " Both are off."}
              </p>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field
                  id="set-ba-user"
                  labelText="Basic auth username"
                  hint="Empty turns basic auth off."
                >
                  <input
                    id="set-ba-user"
                    className={cn(input, "font-mono")}
                    value={form.basicAuthUser}
                    disabled={!canDeploy}
                    autoComplete="off"
                    onChange={(e) => set({ basicAuthUser: e.target.value })}
                  />
                </Field>
                <Field
                  id="set-ba-pass"
                  labelText="Basic auth password"
                  hint={
                    service.basic_auth_user
                      ? "A password is set. Leave empty to keep it."
                      : "No password set."
                  }
                >
                  <input
                    id="set-ba-pass"
                    type="password"
                    className={input}
                    value={form.basicAuthPassword}
                    disabled={!canDeploy}
                    autoComplete="new-password"
                    onChange={(e) => set({ basicAuthPassword: e.target.value })}
                  />
                </Field>
              </div>
              <Field
                id="set-allow"
                labelText="IP allowlist"
                hint={
                  <>
                    One address or range per line, for example{" "}
                    <span className={mono}>198.51.100.0/24</span>. Empty allows
                    every address.
                  </>
                }
              >
                <Textarea
                  id="set-allow"
                  rows={3}
                  className="font-mono text-xs"
                  value={form.ipAllowlist}
                  disabled={!canDeploy}
                  onChange={(e) => set({ ipAllowlist: e.target.value })}
                />
              </Field>
            </div>
            )}
            <AdminLoginRow service={service} serviceId={serviceId} env={env} onDeploy={onDeployConfirmed} />
          </Section>

          <Section
            id="resources"
            title="Resources"
            summary={<span className={mono}>{resShort}</span>}
            chip={unsaved("resources")}
            footer={
              isCompose
                ? undefined
                : footer(
                    "resources",
                    !form.memory_limit.trim()
                      ? "A memory limit is required."
                      : !/^\d+(\.\d+)?[mgMG]$/.test(form.memory_limit.trim())
                        ? "Write the memory limit as 512m or 1g."
                        : form.cpu_limit.trim() && !(Number(form.cpu_limit) > 0)
                          ? "Write the CPU limit in cores."
                          : undefined,
                  )
            }
          >
            {isCompose ? (
              <ComposeResources
                service={service}
                serviceId={serviceId}
                server={server}
                canEdit={canDeploy}
                onSaved={() =>
                  toast.success("Limits saved", {
                    description: "They apply from the next deploy.",
                    action: canDeploy
                      ? { label: "Deploy now", onClick: () => onDeploy() }
                      : undefined,
                  })
                }
              />
            ) : (
              <SingleResources
                service={service}
                serviceId={serviceId}
                server={server}
                form={form}
                set={set}
                canEdit={canDeploy}
              />
            )}
          </Section>

          <Section
            id="deploy"
            title="Deploy"
            summary={deployShort}
            chip={unsaved("deploy")}
            footer={footer(
              "deploy",
              deployTimeoutProblem(form.deploy_timeout_minutes) ||
                rollbackKeptProblem(form.rollback_images_kept) ||
                undefined,
            )}
          >
            <div className="flex flex-col gap-4">
              {isCompose ? (
                <p className={help}>
                  Compose replaces a changed container in place, stopping it
                  before its new one starts: a compose stack has no deploy
                  strategy.
                </p>
              ) : (
                <DeployStrategyField
                  service={service}
                  form={form}
                  set={set}
                  canEdit={canDeploy}
                />
              )}
              <div className="grid gap-4 border-t border-subtle pt-4 sm:grid-cols-2">
                <DeployTimeLimit form={form} set={set} canEdit={canDeploy} />
                <RollbackImagesKept form={form} set={set} canEdit={canDeploy} />
              </div>
              <div className="flex flex-col gap-2 border-t border-subtle pt-4">
                <span className={label}>Dependencies</span>
                <p className={help}>
                  Deploys of {name} are refused while any of these is stopped.
                </p>
                {service.environment_id && !envServices ? (
                  <Read query={envServicesRead} what="the services of this environment">{() => null}</Read>
                ) : (envServices ?? []).filter((x) => x.id !== serviceId)
                  .length === 0 ? (
                  <p className="text-[13px] text-muted">
                    {service.environment_id
                      ? "No other services in this environment."
                      : `${name} is a ${service.scope === "org" ? "workspace" : "project"} service: it belongs to no environment whose services it could wait for.`}
                  </p>
                ) : (
                  <ul className="overflow-hidden rounded-lg border border-subtle">
                    {(envServices ?? [])
                      .filter((x) => x.id !== serviceId)
                      .map((x, i) => {
                        const tone = statusTone(x.runtime_status);
                        return (
                          <li
                            key={x.id}
                            className={cn(i > 0 && "border-t border-subtle")}
                          >
                            <label className="flex cursor-pointer items-center gap-3 px-3 py-2 text-[13px] pointer-coarse:min-h-11">
                              <input
                                type="checkbox"
                                className="h-4 w-4 accent-[var(--link)]"
                                disabled={!canDeploy}
                                checked={form.dependencies.includes(x.id)}
                                onChange={(e) =>
                                  set({
                                    dependencies: e.target.checked
                                      ? [...form.dependencies, x.id]
                                      : form.dependencies.filter(
                                          (d) => d !== x.id,
                                        ),
                                  })
                                }
                              />
                              <span className="flex-1 font-mono text-xs text-fg">
                                {x.name}
                              </span>
                              <span
                                className={cn(
                                  "inline-flex items-center gap-1.5 text-xs",
                                  TONE_CLASSES[tone].text,
                                )}
                              >
                                <span
                                  className={cn(
                                    "h-1.5 w-1.5 rounded-full",
                                    TONE_CLASSES[tone].dot,
                                  )}
                                  aria-hidden="true"
                                />
                                {statusLabel(x.runtime_status)}
                              </span>
                            </label>
                          </li>
                        );
                      })}
                  </ul>
                )}
              </div>
            </div>
          </Section>

          {!isCompose && (
            <Section
              id="health"
              title="Health check"
              summary={<span className={mono}>{healthShort}</span>}
              chip={unsaved("health")}
              footer={footer(
                "health",
                containerCheckProblem(
                  form.container_health,
                  form.container_health_command,
                ) || undefined,
              )}
            >
              <HealthCard
                service={service}
                serviceId={serviceId}
                form={form}
                set={set}
                canEdit={canDeploy}
                saved={saved}
              />
            </Section>
          )}

          <Section
            id="storage"
            title="Storage"
            summary={storageShort}
            chip={unsaved("storage")}
            flush
            footer={
              <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-t border-subtle px-4 py-2.5 text-[13px] text-muted">
                <span>Config files are written into the container on each deploy.</span>
                {canDeploy && (
                  <span className="flex flex-wrap items-center gap-2">
                    {!isCompose && (
                      <button
                        type="button"
                        className={smallGhost}
                        onClick={() => {
                          set({
                            volumes: [...form.volumes, { name: "", target: "" }],
                          });
                          const row = form.volumes.length;
                          requestAnimationFrame(() =>
                            document.getElementById(`set-vol-${row}`)?.focus(),
                          );
                        }}
                      >
                        Add volume
                      </button>
                    )}
                    <button
                      type="button"
                      className={small}
                      disabled={addingFile}
                      onClick={() => setAddingFile(true)}
                    >
                      Add config file
                    </button>
                  </span>
                )}
              </div>
            }
          >
            {!isCompose && (
              <>
                <div className="p-4">
                  <VolumesCard
                    service={service}
                    serviceId={serviceId}
                    form={form}
                    set={set}
                    canEdit={canDeploy}
                  />
                </div>
                {footer("volumes")}
              </>
            )}
            <section
              aria-labelledby="set-files-h"
              className={cn(
                "flex flex-col gap-3 p-4",
                !isCompose && "border-t border-subtle",
              )}
            >
              <h3 id="set-files-h" className="text-[13px] font-semibold text-fg">
                Config files
              </h3>
              <ConfigFiles
                service={service}
                canDeploy={canDeploy}
                onDeploy={() => onDeploy()}
                adding={addingFile}
                onAdding={setAddingFile}
              />
            </section>
          </Section>

          <Section
            id="hooks"
            title="Hooks"
            summary={
              <span className={service.migrate_command ? mono : ""}>
                {hooksShort}
              </span>
            }
            chip={unsaved("hooks")}
            footer={footer("hooks")}
          >
            <HooksCard
              isCompose={isCompose}
              name={name}
              form={form}
              set={set}
              cmds={cmds}
              setCmds={setCmds}
              canEdit={canDeploy}
              containers={containers ?? []}
              serviceId={serviceId}
            />
          </Section>

          <Section
            id="isolation"
            title="Isolation"
            chip={unsaved("isolation")}
            action={
              !isoShown ? (
                <button
                  type="button"
                  className={small}
                  onClick={() => {
                    setIsoOpen(true);
                    requestAnimationFrame(() =>
                      document
                        .querySelector<HTMLElement>(
                          '#set-isolation [role="radio"][aria-checked="true"]',
                        )
                        ?.focus(),
                    );
                  }}
                >
                  {canDeploy ? "Change" : "Details"}
                </button>
              ) : undefined
            }
            footer={footer(
              "isolation",
              pidsLimitProblem(form.pids_limit) || undefined,
            )}
          >
            {!isoShown ? (
              <div className="flex flex-col gap-1.5">
                <p className="text-[13px] text-fg">
                  {ISOLATION_NAME[runsAt]}{" "}
                  <span className={cn(mono, "text-muted")}>
                    {ISOLATION_RUNTIME[runsAt]}
                  </span>{" "}
                  ·{" "}
                  {service.isolation
                    ? `set for ${name}; the workspace default is ${ISOLATION_NAME[wsDefault]}.`
                    : "the workspace default."}
                </p>
                <p className={help}>
                  Process limit{" "}
                  <span className="font-mono">
                    {service.pids_limit || DEFAULT_PIDS_LIMIT}
                  </span>
                  {service.pids_limit ? "." : ", the default."}
                </p>
              </div>
            ) : (
            <IsolationCard
              name={name}
              serverName={server?.name ?? "the panel’s own server"}
              serverId={service.server_id ?? ""}
              orgName={user?.orgName ?? "the workspace"}
              wsDefault={wsDefault}
              sandboxed={sandboxedLevel}
              strict={strictLevel}
              isOwner={role === "owner"}
              advice={service.isolation_advice}
              savedIsolation={service.isolation ?? "default"}
              form={form}
              set={set}
              canEdit={canDeploy}
              netRaw={!!policy?.exception?.allowed_rules.includes("net_raw")}
            />
            )}
          </Section>

          <Section
            id="policy"
            title="Deploy policy"
            chip={
              chip && (
                <span
                  className={cn(
                    "inline-flex h-[22px] items-center gap-1 rounded-full px-2 text-xs font-medium",
                    TONE_CLASSES[chip.tone].chip,
                  )}
                >
                  {chip.tone === "running" && (
                    <Check className="h-3 w-3" aria-hidden="true" />
                  )}
                  {chip.tone === "pending" && (
                    <AlertTriangle className="h-3 w-3" aria-hidden="true" />
                  )}
                  {chip.text}
                </span>
              )
            }
          >
            <DeployPolicyBody serviceId={serviceId} name={name} />
          </Section>

          {canDeploy && (
            <section
              id="set-delete"
              aria-labelledby="set-delete-h"
              tabIndex={-1}
              className="flex flex-wrap items-center gap-4 rounded-[10px] border border-status-failed/40 bg-surface p-4 outline-none"
            >
              <div className="min-w-0 flex-[1_1_320px]">
                <h2
                  id="set-delete-h"
                  className="text-[15px] font-semibold leading-[22px] text-fg"
                >
                  Delete {name}
                </h2>
                <p className="mt-0.5 text-[13px] leading-[19px] text-muted">
                  Deletes {name} and everything that belongs to it: its
                  containers on {server?.name ?? "the panel host"}
                  {namedVols.length > 0 && (
                    <>
                      , {namedVols.length === 1 ? "the volume" : "the volumes"}{" "}
                      <span className={mono}>
                        {namedVols.map((v) => v.name).join(", ")}
                      </span>{" "}
                      and {namedVols.length === 1 ? "its" : "their"} data
                    </>
                  )}
                  ,{" "}
                  {deleteList(
                    imagesOf(service.source_type),
                    counts,
                    (domains ?? []).map((d) => d.hostname),
                  )}
                  .
                  {(mounts ?? []).length > 0 ? (
                    <>
                      {" "}
                      Shared volumes it mounts stay, with their data:{" "}
                      {mounts!.map((m) => m.name).join(", ")}.
                    </>
                  ) : (
                    " Shared volumes it mounts stay."
                  )}
                </p>
              </div>
              <Button
                variant="destructive"
                className="h-9"
                onClick={() => setDeleting(true)}
              >
                Delete {name}…
              </Button>
            </section>
          )}
        </div>

        <aside className="sticky top-5 hidden min-[1200px]:block">
          <nav aria-labelledby="toc-h" className={card}>
            <div className="border-b border-subtle px-4 py-3">
              <h2
                id="toc-h"
                className="text-[15px] font-semibold leading-[22px] text-fg"
              >
                On this page
              </h2>
            </div>
            <ul className="flex flex-col p-1.5">
              {toc.map((t) => {
                const note = noteOf(t.id);
                return (
                  <li
                    key={t.id}
                    className={cn(
                      t.id === "delete" && "mt-1.5 border-t border-subtle pt-1.5",
                    )}
                  >
                    <button
                      type="button"
                      aria-current={current === t.id ? "true" : undefined}
                      onClick={() => jump(t.id)}
                      className={cn(
                        "flex min-h-[34px] w-full items-center gap-2 rounded-lg px-2.5 text-left text-[13px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:min-h-11",
                        current === t.id ? "bg-selected" : "hover:bg-raised",
                        t.id === "delete"
                          ? "text-status-failed-fg"
                          : current === t.id
                            ? "text-fg"
                            : "text-fg/85",
                      )}
                    >
                      {t.label}
                      {note ? (
                        <span className="ml-auto text-xs text-status-pending-fg">
                          {note}
                        </span>
                      ) : t.count ? (
                        <span className="ml-auto font-mono text-xs text-muted">
                          {t.count}
                        </span>
                      ) : null}
                    </button>
                  </li>
                );
              })}
            </ul>
          </nav>
        </aside>
      </div>

      <MoveDialog
        open={moving}
        onClose={() => setMoving(false)}
        service={service}
        servers={servers ?? []}
        domains={(domains ?? []).map((d) => d.hostname)}
        running={
          service.runtime_status !== "idle" &&
          service.runtime_status !== "stopped"
        }
        onDeploy={onDeploy}
        deployPending={deployPending}
      />
      <DeleteDialog
        open={deleting}
        onClose={() => setDeleting(false)}
        service={service}
        serviceId={serviceId}
        server={server}
        containers={containers ?? []}
        domains={(domains ?? []).map((d) => d.hostname)}
        mounts={(mounts ?? []).map((m) => m.name ?? "")}
        counts={counts}
        onDeleted={onDeleteSuccess}
      />
    </div>
  );
}

type Counts = {
  deployments?: number;
  variables?: number;
  secrets?: number;
  files?: number;
};

const plural = (n: number, one: string, many: string) =>
  `${n} ${n === 1 ? one : many}`;

/** "the images built for it, 48 deployments, 12 variables, 4 secrets and
 *  its 3 domains, a, b and c":
 *  what deleting takes beyond containers and images, as the board words it. */
export function deleteList(
  images: string,
  c: Counts,
  domains: string[],
): string {
  const parts = [
    images,
    c.deployments === undefined
      ? "its deployments"
      : c.deployments
        ? plural(c.deployments, "deployment", "deployments")
        : "",
    c.variables === undefined
      ? "variables"
      : c.variables
        ? plural(c.variables, "variable", "variables")
        : "",
    c.secrets === undefined
      ? "secrets"
      : c.secrets
        ? plural(c.secrets, "secret", "secrets")
        : "",
    c.files === undefined
      ? "config files"
      : c.files
        ? plural(c.files, "config file", "config files")
        : "",
    domains.length === 0
      ? ""
      : domains.length === 1
        ? `its domain, ${domains[0]}`
        : `its ${domains.length} domains, ${and(domains)}`,
  ].filter(Boolean);
  return and(parts);
}

/** The images a deploy left on the server, by where they came from. */
const imagesOf = (source: Service["source_type"]) =>
  source === "docker-image"
    ? "the image pulled for it"
    : source === "docker-compose"
      ? "the images it pulled or built"
      : "the images built for it";

const and = (xs: string[]) =>
  xs.length <= 1
    ? xs.join("")
    : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`;

// ─── Building blocks ──────────────────────────────────────────────────────────

/** One open card (board .card): a header with the title, a chip and the
 *  summary, or an action with the summary beside the title; the body; the
 *  footers. flush leaves the body's padding to the children. */
function Section({
  id,
  title,
  summary,
  chip,
  action,
  flush,
  footer,
  children,
}: {
  id: SectionId;
  title: string;
  summary?: ReactNode;
  chip?: ReactNode;
  action?: ReactNode;
  flush?: boolean;
  footer?: ReactNode;
  children: ReactNode;
}) {
  const sum = summary && (
    <span className="min-w-0 break-words text-xs text-muted">{summary}</span>
  );
  return (
    <section
      id={`set-${id}`}
      aria-labelledby={`set-${id}-h`}
      tabIndex={-1}
      className={cn(card, "outline-none")}
    >
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-subtle px-4 py-3">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
          <h2
            id={`set-${id}-h`}
            className="text-[15px] font-semibold leading-[22px] text-fg"
          >
            {title}
          </h2>
          {chip}
          {action && sum}
        </div>
        {action ?? sum}
      </div>
      {flush ? children : <div className="p-4">{children}</div>}
      {footer}
    </section>
  );
}

function Unsaved() {
  return (
    <span className="inline-flex items-center gap-1 text-xs text-status-pending-fg">
      <span
        className="h-1.5 w-1.5 rounded-full bg-current"
        aria-hidden="true"
      />
      Unsaved
    </span>
  );
}

function Field({
  id,
  labelText,
  hint,
  children,
}: {
  id: string;
  labelText: string;
  hint?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <label htmlFor={id} className={label}>
        {labelText}
      </label>
      {children}
      {hint && <p className={help}>{hint}</p>}
    </div>
  );
}

// ─── Isolation ───────────────────────────────────────────────────────────────

/** The docs site's Sandboxed benchmark (D-95). */
const SANDBOX_BENCHMARK = "https://nickpap21.github.io/toft/reference/isolation/#the-sandboxed-benchmark";
/** And the strict network's (D-115). */
const STRICT_BENCHMARK = "https://nickpap21.github.io/toft/reference/isolation/#the-strict-network-benchmark";

/** The runtime each level runs on, as the level cards name it. */
const ISOLATION_RUNTIME = {
  standard: "runc",
  sandboxed: "gVisor runsc",
  sandboxed_strict: "gVisor runsc-strict",
};

/** Below the workspace's default is the owners' (D-95), in this order (D-115). */
const ISOLATION_RANK = { standard: 0, sandboxed: 1, sandboxed_strict: 2 };

/** How the service is walled off (boards: Service, isolation; ServiceIsolation;
 *  D-93, D-95, D-115): the workspace's default, Standard, Sandboxed or
 *  Sandboxed with a strict network, the rules the level sets, and the
 *  process limit. */
function IsolationCard({
  name,
  serverName,
  serverId,
  orgName,
  wsDefault,
  sandboxed,
  strict,
  isOwner,
  advice,
  savedIsolation,
  form,
  set,
  canEdit,
  netRaw,
}: {
  name: string;
  serverName: string;
  serverId: string;
  orgName: string;
  wsDefault: keyof typeof ISOLATION_NAME;
  /** The server's Sandboxed level (serverId "" the panel's own, D-97); null before the probe answered. */
  sandboxed: { state: string; reason: string } | null;
  /** Its Sandboxed, strict network level (D-115), the same way. */
  strict: { state: string; reason: string } | null;
  isOwner: boolean;
  /** The level recommended for the service, and why (D-99). */
  advice: Service["isolation_advice"];
  savedIsolation: ConfigForm["isolation"];
  form: ConfigForm;
  set: (p: Partial<ConfigForm>) => void;
  canEdit: boolean;
  netRaw: boolean;
}) {
  const limit = pidsLimitProblem(form.pids_limit)
    ? "the saved number of"
    : form.pids_limit.trim();
  const level = form.isolation === "default" ? wsDefault : form.isolation;
  const canSandbox = sandboxed?.state === "available";
  const canStrict = strict?.state === "available";
  // Below the default is the owners' (D-95); a service already there keeps
  // its choice.
  const locked = (k: "standard" | "sandboxed") =>
    !isOwner && ISOLATION_RANK[k] < ISOLATION_RANK[wsDefault] && savedIsolation !== k;
  const below = "Below the workspace’s default: only owners can choose it.";
  // The panel's own server has its Isolation card on Instance → Server (D-97).
  const page = serverId ? "its Server page" : "Instance → Server";
  const notHere = !sandboxed || sandboxed.state === "unknown"
    ? serverId
      ? `${serverName} hasn’t been checked yet: Check again on its Server page.`
      : "The panel’s own server hasn’t been checked yet: an instance operator checks it on Instance → Server."
    : sandboxed.state === "failing"
      ? `gVisor fails on ${serverName}: ${sandboxed.reason}`
      : sandboxed.state === "unavailable"
        ? sandboxed.reason
        : `Not on ${serverName} yet: an instance operator installs gVisor on ${page}.`;
  // gVisor is here, so the strict network waits on the agent or a newer install.
  const gvisor = sandboxed?.state === "available" || sandboxed?.state === "failing";
  const needsAgent = strict?.state === "unavailable" && sandboxed?.state !== "unavailable";
  const reinstall = strict?.state === "not_installed" && gvisor;
  const strictNotHere = !strict || strict.state === "unknown"
    ? notHere
    : !serverId && needsAgent
      ? "Needs Toft’s agent, and the panel’s own server runs without one."
      : needsAgent
        ? `Needs Toft’s agent: ${serverName} connects over SSH. An owner moves it to the agent on its Server page${gvisor ? "" : ", then an instance operator installs gVisor there"}.`
        : reinstall
          ? `Not on ${serverName} yet: its gVisor came before the strict network. An instance operator installs gVisor again on ${page}.`
          : strict.state === "failing"
            ? `The strict network fails on ${serverName}: ${strict.reason}`
            : notHere;
  const levels = [
    {
      key: "default" as const,
      name: "Workspace default",
      runtime: ISOLATION_NAME[wsDefault],
      detail: `Follows ${possessive(orgName)} default, which its owners set under Settings → Workspace.`,
      disabled: false,
    },
    {
      key: "standard" as const,
      name: "Standard",
      runtime: "runc",
      detail: locked("standard")
        ? below
        : "Containers share the server’s kernel, behind seccomp and AppArmor.",
      disabled: locked("standard"),
    },
    {
      key: "sandboxed" as const,
      name: "Sandboxed",
      runtime: "gVisor runsc",
      detail: locked("sandboxed")
        ? below
        : canSandbox
          ? "Each container gets its own kernel in user space. Slower for databases and small requests."
          : notHere,
      disabled: locked("sandboxed") || (!canSandbox && form.isolation !== "sandboxed"),
    },
    {
      key: "sandboxed_strict" as const,
      name: ISOLATION_NAME.sandboxed_strict,
      runtime: "gVisor runsc-strict",
      detail: canStrict
        ? "gVisor’s own network stack too: network calls never reach the server’s. Slower again for databases."
        : strictNotHere,
      disabled: !canStrict && form.isolation !== "sandboxed_strict",
    },
  ];
  const refused = (level === "sandboxed" && !canSandbox) || (level === "sandboxed_strict" && !canStrict);
  const refusal = level === "sandboxed_strict" ? strictNotHere : notHere;
  // The strict network chosen, or Sandboxed where it waits on the agent or
  // an install (board: ServiceIsolation): a note in place of the advice.
  const strictNote =
    level === "sandboxed_strict"
      ? "The strict network is for code nobody here has reviewed that talks to the internet: a bug in the server’s TCP/IP stack can’t be reached from it. Its network calls are slower than Sandboxed’s; how much, measured on this kind of server, is in the release note."
      : level === "sandboxed" && serverId && canSandbox && needsAgent
        ? "The strict network needs Toft’s agent on the server: the agent answers its containers’ name lookups, which gVisor’s own network stack can’t send to Docker."
        : level === "sandboxed" && serverId && reinstall
          ? `Installing gVisor again adds the strict network to ${serverName}. Docker reloads, and no container restarts.`
          : null;
  const advised = advice.level === "sandboxed" ? "Sandboxed" : "Standard";
  const why = {
    database: "Standard is recommended for databases and caches: gVisor slows the system calls and disk writes they make most. How much, measured on this kind of server, is in the release note.",
    tool: `Sandboxed is recommended for ${name}: an admin tool, open to the internet, that holds your databases’ passwords. It serves a few people, so gVisor’s slower system calls cost it little, and it walls off the tool’s next vulnerability.`,
    panel_server: "Sandboxed is recommended on the panel’s own server: a container that breaks out there reaches the panel, with every server’s SSH key and every secret. gVisor slows system calls, so a busy app is better on a server of its own.",
    app: "Standard is recommended for code your team writes and images you trust: it’s hardened, and runs at full speed. Choose Sandboxed for code nobody here has reviewed, such as plugins, user uploads or an agent’s commits.",
  }[advice.reason];
  // One click to the recommended level, when it isn't the one chosen and can
  // be; never down from Sandboxed for an app, a choice the note offers.
  const adviceTo = advice.level === wsDefault ? "default" : advice.level;
  const canTake =
    level !== advice.level && advice.reason !== "app" && canEdit && !levels.find((l) => l.key === advice.level)?.disabled;
  const rules: { text: string; minus?: boolean }[] =
    level === "sandboxed_strict"
      ? [
          { text: "Its own kernel: gVisor answers every system call in user space" },
          { text: "Its own network stack: network calls stay in gVisor and never reach the server’s TCP/IP stack" },
          { text: `Names through Toft’s agent on ${serverName}, from the networks ${name} is on, as on Standard` },
          { text: "Docker’s default seccomp profile, applied inside the sandbox" },
          { text: "Can’t gain privileges (no-new-privileges)" },
          { text: "Raw sockets off, whatever Deploy policy grants" },
          { text: `At most ${limit} processes, gVisor’s own included` },
          { text: `Health checks ask ${name} on its network address: it must listen on more than 127.0.0.1` },
          { text: "Builds still run on Standard (runc)", minus: true },
        ]
      : level === "sandboxed"
      ? [
          { text: "Its own kernel: gVisor answers every system call in user space" },
          { text: "Docker’s default seccomp profile, applied inside the sandbox" },
          { text: "Can’t gain privileges (no-new-privileges)" },
          { text: "Raw sockets off, whatever Deploy policy grants" },
          { text: `At most ${limit} processes, gVisor’s own included` },
          {
            text: `Network through the server’s TCP/IP stack, in ${name}’s own network namespace: names, domains and the firewall work as on Standard`,
          },
          { text: "Builds still run on Standard (runc)", minus: true },
        ]
      : [
          { text: "Can’t gain privileges (no-new-privileges)" },
          { text: netRaw ? "Raw sockets on (net_raw granted)" : "Raw sockets off (NET_RAW dropped)" },
          { text: `At most ${limit} processes` },
          { text: "Docker’s default seccomp profile" },
          { text: "AppArmor docker-default" },
          { text: "Never privileged, host namespaces, devices or the Docker socket (Deploy policy)" },
        ];
  return (
    <div className="flex flex-col gap-3.5">
      <p className="text-[13px] text-fg/70">
        How {name} is walled off from {serverName} and from the other services
        there. Applies from the next deploy.
      </p>
      <div
        role="radiogroup"
        aria-label="Level"
        className="flex flex-wrap gap-2"
      >
        {levels.map((l) => {
          const on = form.isolation === l.key;
          const off = l.disabled || !canEdit;
          return (
            <button
              key={l.key}
              type="button"
              role="radio"
              aria-checked={on}
              aria-disabled={off}
              onClick={() => !off && set({ isolation: l.key })}
              className={cn(
                "flex flex-[1_1_180px] items-start gap-2.5 rounded-lg border px-3 py-2.5 text-left",
                on
                  ? "border-link bg-accent-soft"
                  : "border-subtle bg-ground",
                l.disabled && !on && "cursor-not-allowed opacity-70",
                !l.disabled && !on && canEdit && "hover:border-strong",
              )}
            >
              <span
                aria-hidden="true"
                className={cn(
                  "mt-0.5 h-4 w-4 flex-none rounded-full bg-ground",
                  on ? "border-[5px] border-link" : "border border-strong",
                )}
              />
              <span className="flex flex-col gap-0.5">
                <span className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
                  <span className="text-[13px] font-medium text-fg">
                    {l.name}
                  </span>
                  <span className={cn(mono, "text-muted")}>{l.runtime}</span>
                  {l.key === advice.level && (
                    <span className="rounded-full bg-status-running-soft px-1.5 text-[11px] font-medium leading-[18px] text-status-running-fg">
                      Recommended
                    </span>
                  )}
                </span>
                <span className="text-xs text-fg/70">{l.detail}</span>
              </span>
            </button>
          );
        })}
      </div>
      {refused ? (
        <div role="alert" className="flex gap-2.5 rounded-lg border border-status-pending/40 bg-status-pending-soft px-3 py-2.5 text-[13px] leading-[19px] text-fg">
          <AlertTriangle className="mt-0.5 h-[15px] w-[15px] flex-none text-status-pending" aria-hidden="true" />
          <span>
            {name}’s next deploy will be refused: {form.isolation === "default" ? `the workspace’s default is ${ISOLATION_NAME[wsDefault]}, and ` : ""}
            {refusal.charAt(0).toLowerCase() + refusal.slice(1)}{" "}
            {serverId && (
              <Link href={`/servers/${serverId}`} className="inline-link">
                {serverName} →
              </Link>
            )}
          </span>
        </div>
      ) : strictNote ? (
        <div className="flex gap-2.5 rounded-lg border border-subtle bg-ground px-3 py-2.5 text-[13px] leading-[19px] text-fg">
          <Info className="mt-0.5 h-[15px] w-[15px] flex-none text-muted" aria-hidden="true" />
          <span>
            {strictNote}{" "}
            {level === "sandboxed_strict" ? (
              <a href={STRICT_BENCHMARK} target="_blank" rel="noreferrer" className="inline-link">
                Release note ↗
              </a>
            ) : (
              <Link href={`/servers/${serverId}`} className="inline-link">
                {serverName} →
              </Link>
            )}
          </span>
        </div>
      ) : (
        <div className="flex flex-wrap items-start gap-x-2.5 gap-y-2 rounded-lg border border-subtle bg-ground px-3 py-2.5 text-[13px] leading-[19px] text-fg">
          <span className="flex min-w-0 flex-[1_1_300px] gap-2.5">
            <Info className="mt-0.5 h-[15px] w-[15px] flex-none text-muted" aria-hidden="true" />
            <span>
              {why}{" "}
              {advice.reason === "database" && (
                <a href={SANDBOX_BENCHMARK} target="_blank" rel="noreferrer" className="inline-link">
                  Release note ↗
                </a>
              )}
            </span>
          </span>
          {canTake && (
            <Button variant="outline" size="sm" className="ml-auto" onClick={() => set({ isolation: adviceTo })}>
              Use {advised}
            </Button>
          )}
        </div>
      )}
      <div className="flex flex-col gap-1.5 rounded-lg border border-subtle bg-ground px-3 py-2.5">
        <span className="text-xs font-medium text-muted">
          What {ISOLATION_NAME[level]} does to {name}’s containers
        </span>
        <ul className="grid grid-cols-[repeat(auto-fit,minmax(230px,1fr))] gap-x-4 gap-y-1.5 text-[13px]">
          {rules.map((r) => (
            <li key={r.text} className="flex gap-2">
              {r.minus ? (
                <Minus className="mt-[3px] h-[13px] w-[13px] flex-none text-muted" strokeWidth={2.4} aria-hidden="true" />
              ) : (
                <Check
                  className="mt-[3px] h-[13px] w-[13px] flex-none text-status-running"
                  strokeWidth={2.4}
                  aria-hidden="true"
                />
              )}
              <span>{r.text}</span>
            </li>
          ))}
        </ul>
      </div>
      <div className="flex flex-wrap gap-x-6 gap-y-4">
        <div className="flex-[0_1_260px]">
          <Field
            id="set-pids"
            labelText="Process limit"
            hint={
              level !== "standard"
                ? "Processes and threads together, gVisor’s own included, 256 to 32768. A service that reaches it is stopped and restarted."
                : "Processes and threads together, 256 to 32768. A fork bomb stops here."
            }
          >
            <input
              id="set-pids"
              inputMode="numeric"
              className={cn(input, "font-mono")}
              value={form.pids_limit}
              disabled={!canEdit}
              onChange={(e) => set({ pids_limit: e.target.value })}
            />
          </Field>
        </div>
        <div className="flex flex-[1_1_300px] flex-col gap-1.5">
          <span className={label}>Raw sockets</span>
          <span className="text-[13px] text-fg/70">
            {level !== "standard" ? (
              <>
                Off, and ping with them: gVisor refuses raw sockets to Sandboxed services, even with Deploy policy’s{" "}
                <span className={mono}>net_raw</span> exception.
              </>
            ) : netRaw ? (
              <>
                On: an instance operator granted this service{" "}
                <span className={mono}>net_raw</span> under Deploy policy.
              </>
            ) : (
              <>
                Off. Ping works without them; tools that capture or craft
                packets, such as tcpdump, need them. An instance operator can
                grant them to this service under Deploy policy (
                <span className={mono}>net_raw</span>).
              </>
            )}
          </span>
        </div>
      </div>
    </div>
  );
}

// ─── Source ──────────────────────────────────────────────────────────────────

function SourceCard({
  service,
  serviceId,
  form,
  set,
  canEdit,
}: {
  service: Service;
  serviceId: string;
  form: ConfigForm;
  set: (p: Partial<ConfigForm>) => void;
  canEdit: boolean;
}) {
  const name = service.name;
  // The project's repositories a git build or a compose stack's files may
  // come from (D-77); a workspace service builds from a URL only.
  const { repos } = useRepos(service.scope === "org" ? "" : service.project_id);
  const ownRepos = repos ?? [];
  const deployOnPush = (
    <DeployOnPushSwitch
      on={form.deploy_on_push}
      disabled={!canEdit}
      onChange={(v) => set({ deploy_on_push: v })}
      text={`A push to ${form.git_branch || "main"} deploys ${name}${form.repository_id ? "" : " through the project’s GitHub webhook"}. Off: it deploys only when someone deploys it.`}
    />
  );
  if (service.source_type === "git") {
    const nix = form.build_pack === "nixpacks";
    const urlField = (
      <Field id="set-repo" labelText="Repository URL">
        <input
          id="set-repo"
          className={cn(input, "font-mono")}
          value={form.git_repo_url}
          disabled={!canEdit}
          onChange={(e) => set({ git_repo_url: e.target.value })}
          spellCheck={false}
        />
      </Field>
    );
    return (
      <div className="flex flex-col gap-4">
        <p className="text-[13px] text-fg/85">
          Each deploy builds an image of the branch on the server that runs the
          service.
          {ownRepos.length > 0 &&
            " From this project’s repository the server gets the files only: no URL, no token."}
        </p>
        {ownRepos.length > 0 ? (
          <RepoSourceChoice
            repos={ownRepos}
            value={form.repository_id}
            onChange={(id) => set({ repository_id: id })}
            name="set-repo-source"
          >
            {urlField}
          </RepoSourceChoice>
        ) : (
          urlField
        )}
        <div className="grid gap-4 sm:grid-cols-[minmax(0,280px)]">
          <Field id="set-branch" labelText="Branch">
            <input
              id="set-branch"
              className={cn(input, "font-mono")}
              value={form.git_branch}
              disabled={!canEdit}
              onChange={(e) => set({ git_branch: e.target.value })}
              spellCheck={false}
            />
          </Field>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            id="set-basedir"
            labelText="Base directory"
            hint="Relative to the repository root."
          >
            <input
              id="set-basedir"
              className={cn(input, "font-mono")}
              value={form.base_directory}
              disabled={!canEdit}
              onChange={(e) => set({ base_directory: e.target.value })}
              spellCheck={false}
            />
          </Field>
          <fieldset className="flex flex-col gap-1.5">
            <legend className={cn(label, "mb-1.5")}>Build with</legend>
            <div
              role="radiogroup"
              aria-label="Build with"
              className="inline-flex gap-0.5 self-start rounded-lg border border-subtle bg-ground p-0.5"
            >
              {[
                ["dockerfile", "Dockerfile"],
                ["nixpacks", "Nixpacks"],
              ].map(([v, l]) => (
                <label
                  key={v}
                  className={cn(
                    "inline-flex h-8 cursor-pointer items-center gap-2 rounded-md px-2.5 text-[13px]",
                    form.build_pack === v
                      ? "bg-raised text-fg"
                      : "text-muted hover:text-fg",
                  )}
                >
                  <input
                    type="radio"
                    name="build-pack"
                    value={v}
                    checked={form.build_pack === v}
                    disabled={!canEdit}
                    onChange={() => set({ build_pack: v })}
                    className="h-3.5 w-3.5 accent-[var(--link)]"
                  />
                  {l}
                </label>
              ))}
            </div>
            <p className={help}>
              {nix ? (
                "Nixpacks detects the language and builds it; it must be installed on the server."
              ) : (
                <>
                  Builds{" "}
                  <span className={mono}>
                    {[form.base_directory.replace(/\/+$/, ""), "Dockerfile"]
                      .filter(Boolean)
                      .join("/")}
                  </span>
                  .
                </>
              )}
            </p>
          </fieldset>
        </div>
        {nix && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field
              id="set-install"
              labelText="Install command"
              hint="Replaces the one Nixpacks detects; empty keeps it. For example npm ci."
            >
              <input
                id="set-install"
                className={cn(input, "font-mono")}
                value={form.install_command}
                disabled={!canEdit}
                onChange={(e) => set({ install_command: e.target.value })}
                spellCheck={false}
              />
            </Field>
            <Field
              id="set-build"
              labelText="Build command"
              hint="Replaces the one Nixpacks detects; empty keeps it. For example npm run build."
            >
              <input
                id="set-build"
                className={cn(input, "font-mono")}
                value={form.build_command}
                disabled={!canEdit}
                onChange={(e) => set({ build_command: e.target.value })}
                spellCheck={false}
              />
            </Field>
          </div>
        )}
        <Field
          id="set-start"
          labelText="Start command"
          hint={`Replaces the image's command. Empty runs the image as built${form.start_command ? "" : `, which ${name} does`}.`}
        >
          <input
            id="set-start"
            className={cn(input, "font-mono")}
            value={form.start_command}
            disabled={!canEdit}
            onChange={(e) => set({ start_command: e.target.value })}
            spellCheck={false}
          />
        </Field>
        {deployOnPush}
      </div>
    );
  }
  if (service.source_type === "docker-image") {
    return (
      <div className="flex flex-col gap-4">
        <p className="text-[13px] text-fg/85">
          Each deploy pulls the image on the server that runs the service.
        </p>
        <Field id="set-image" labelText="Image">
          <div className="flex gap-2">
            <input
              id="set-image"
              className={cn(input, "flex-1 font-mono")}
              value={form.docker_image}
              disabled={!canEdit}
              onChange={(e) => set({ docker_image: e.target.value })}
              spellCheck={false}
            />
            {canEdit && (
              <TagPicker
                serviceId={serviceId}
                currentImage={form.docker_image}
                onSelect={(img) => set({ docker_image: img })}
              />
            )}
          </div>
        </Field>
        <Field
          id="set-start"
          labelText="Start command"
          hint="Replaces the image's command. Empty runs the image as built."
        >
          <input
            id="set-start"
            className={cn(input, "font-mono")}
            value={form.start_command}
            disabled={!canEdit}
            onChange={(e) => set({ start_command: e.target.value })}
            spellCheck={false}
          />
        </Field>
      </div>
    );
  }
  const fromRepo =
    form.compose_repo_url !== "" ||
    form.repository_id !== "" ||
    ((!!service.compose_repo_url || !!service.repository_id) &&
      form.compose_repo_url === "" &&
      form.repository_id === "");
  const repoOn = form.compose_repo_url !== "" || form.repository_id !== "";
  return (
    <div className="flex flex-col gap-4">
      <label className="inline-flex cursor-pointer items-center gap-2.5 text-[13px] text-fg">
        <input
          type="checkbox"
          role="switch"
          className="h-4 w-4 accent-[var(--link)]"
          checked={repoOn}
          disabled={!canEdit}
          onChange={(e) =>
            set(
              e.target.checked
                ? service.compose_repo_url || !ownRepos.length
                  ? { compose_repo_url: service.compose_repo_url || "https://" }
                  : { repository_id: service.repository_id || ownRepos[0].id }
                : { compose_repo_url: "", repository_id: "" },
            )
          }
        />
        Compose file from a repository
      </label>
      {repoOn ? (
        <>
          {ownRepos.length > 0 ? (
            <RepoSourceChoice
              repos={ownRepos}
              value={form.repository_id}
              onChange={(id) =>
                set(
                  id
                    ? { repository_id: id }
                    : {
                        repository_id: "",
                        compose_repo_url:
                          service.compose_repo_url || "https://",
                      },
                )
              }
              name="set-compose-source"
            >
              <Field id="set-crepo" labelText="Repository URL">
                <input
                  id="set-crepo"
                  className={cn(input, "font-mono")}
                  value={form.compose_repo_url}
                  disabled={!canEdit}
                  onChange={(e) => set({ compose_repo_url: e.target.value })}
                  spellCheck={false}
                />
              </Field>
            </RepoSourceChoice>
          ) : (
            <Field id="set-crepo" labelText="Repository URL">
              <input
                id="set-crepo"
                className={cn(input, "font-mono")}
                value={form.compose_repo_url}
                disabled={!canEdit}
                onChange={(e) => set({ compose_repo_url: e.target.value })}
                spellCheck={false}
              />
            </Field>
          )}
          <div className="grid gap-4 sm:grid-cols-[minmax(0,280px)]">
            <Field id="set-cbranch" labelText="Branch">
              <input
                id="set-cbranch"
                className={cn(input, "font-mono")}
                value={form.git_branch}
                disabled={!canEdit}
                onChange={(e) => set({ git_branch: e.target.value })}
                spellCheck={false}
              />
            </Field>
          </div>
          <Field
            id="set-cfile"
            labelText="Compose file"
            hint="Relative to the repository root."
          >
            <input
              id="set-cfile"
              className={cn(input, "font-mono")}
              value={form.compose_file_path}
              disabled={!canEdit}
              onChange={(e) => set({ compose_file_path: e.target.value })}
              spellCheck={false}
            />
          </Field>
        </>
      ) : (
        <Field id="set-compose" labelText="Compose file">
          <Textarea
            id="set-compose"
            className="min-h-[240px] font-mono text-xs"
            value={form.compose_content}
            disabled={!canEdit}
            onChange={(e) => set({ compose_content: e.target.value })}
            spellCheck={false}
          />
        </Field>
      )}
      <p className={help}>
        Labels starting with <span className={mono}>traefik.</span>,{" "}
        <span className={mono}>paas.</span> or{" "}
        <span className={mono}>com.docker.compose.</span> are refused. The
        platform routes the domains: see Domains.
        {fromRepo &&
          !repoOn &&
          " Turning the repository off uses the compose file below instead."}
      </p>
      {repoOn && deployOnPush}
    </div>
  );
}

/** Deploy on push (D-77, board ServiceSettings' Source card). */
function DeployOnPushSwitch({
  on,
  disabled,
  onChange,
  text,
}: {
  on: boolean;
  disabled: boolean;
  onChange: (on: boolean) => void;
  text: string;
}) {
  return (
    <div className="flex items-start justify-between gap-4 border-t border-subtle pt-3">
      <div className="flex flex-col gap-0.5">
        <span id="dop-h" className="text-[13px] font-medium text-fg">
          Deploy on push
        </span>
        <span id="dop-d" className="text-xs leading-[17px] text-muted">
          {text}
        </span>
      </div>
      <button
        type="button"
        role="switch"
        aria-checked={on}
        aria-labelledby="dop-h"
        aria-describedby="dop-d"
        disabled={disabled}
        onClick={() => onChange(!on)}
        className={cn(
          "relative mt-0.5 h-[22px] w-10 flex-none rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-60",
          on ? "bg-accent" : "bg-strong",
        )}
      >
        <span
          className={cn(
            "absolute top-[3px] h-4 w-4 rounded-full bg-on-accent transition-[translate] left-[3px]",
            on && "translate-x-[18px]",
          )}
        />
      </button>
    </div>
  );
}

function TagPicker({
  serviceId,
  currentImage,
  onSelect,
}: {
  serviceId: string;
  currentImage: string;
  onSelect: (image: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const { data, isFetching, refetch } = useQuery({
    queryKey: ["versions", serviceId],
    queryFn: () => versionService.get(serviceId),
    enabled: false,
    staleTime: 5 * 60 * 1000,
  });
  const lastSlash = currentImage.lastIndexOf("/");
  const lastColon = currentImage.lastIndexOf(":");
  const base =
    lastColon > lastSlash ? currentImage.slice(0, lastColon) : currentImage;
  const tags =
    (data?.source_type === "docker-image" ? data.tags : undefined) ?? [];
  return (
    <div className="relative">
      <button
        type="button"
        className={cn(outline, "h-9")}
        onClick={() => {
          setOpen((o) => !o);
          refetch();
        }}
        aria-expanded={open}
      >
        <Tag className="h-3.5 w-3.5" aria-hidden="true" />
        Tags
      </button>
      {open && (
        <div className="absolute right-0 top-10 z-30 w-72 overflow-hidden rounded-lg border border-subtle bg-surface shadow-menu">
          {isFetching ? (
            <p className="px-3 py-2.5 text-[13px] text-muted">Loading tags…</p>
          ) : tags.length === 0 ? (
            <p className="px-3 py-2.5 text-[13px] text-muted">No tags found.</p>
          ) : (
            <ul className="max-h-64 overflow-y-auto">
              {tags.map((t) => (
                <li key={t.name}>
                  <button
                    type="button"
                    className="flex w-full items-center justify-between gap-2 px-3 py-2 text-left text-[13px] hover:bg-raised"
                    onClick={() => {
                      onSelect(`${base}:${t.name}`);
                      setOpen(false);
                    }}
                  >
                    <span
                      className="truncate font-mono text-xs text-fg"
                      title={t.name}
                    >
                      {t.name.length > 24 ? `${t.name.slice(0, 12)}…` : t.name}
                    </span>
                    {t.pushed_at && t.pushed_at !== "0001-01-01T00:00:00Z" && (
                      <span className="flex-none font-mono text-xs text-muted">
                        {t.pushed_at.slice(0, 16).replace("T", " ")}
                      </span>
                    )}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}

// ─── Domain routing (compose), in the Domains card ──────────────────────────

function ComposeRouting({
  service,
  serviceId,
  form,
  set,
  canEdit,
}: {
  service: Service;
  serviceId: string;
  form: ConfigForm;
  set: (p: Partial<ConfigForm>) => void;
  canEdit: boolean;
}) {
  const {
    data: policy,
    isPending,
    isError,
  } = useQuery(deployPolicyQuery(serviceId));
  const fromRepo = form.compose_repo_url !== "" || !!service.compose_repo_url;
  const names = !fromRepo ? (policy?.compose_services ?? null) : null;
  const asSelect = !fromRepo && (names !== null || isPending);
  const route = form.compose_route_service.trim();
  const valid = isValidComposeRouteService(route);
  const textHint = fromRepo
    ? "The file comes from the repository, so the name is checked at each deploy."
    : isError
      ? "Could not load the compose file's service names."
      : "The saved compose file could not be read, so its service names are not known.";
  const target = route || (names?.length === 1 ? names[0] : "");
  return (
    <div className="flex flex-col gap-4">
      <p className="text-[13px] text-fg/85">
        The platform routes this service&apos;s domains to one compose service
        and port. Compose files can&apos;t set{" "}
        <span className={mono}>traefik.*</span> labels.
      </p>
      <div className="grid gap-4 sm:grid-cols-[1fr_180px]">
        <Field
          id="set-route"
          labelText="Compose service"
          hint={
            !valid
              ? undefined
              : asSelect
                ? "Names from the saved compose file. Empty uses the file's only service."
                : `${textHint} Empty uses the file's only service.`
          }
        >
          {asSelect ? (
            <select
              id="set-route"
              className={cn(input, "font-mono")}
              value={form.compose_route_service}
              disabled={!canEdit}
              onChange={(e) => set({ compose_route_service: e.target.value })}
            >
              <option value="">
                {names && names.length > 1
                  ? "None chosen"
                  : "Automatic (the only service)"}
              </option>
              {(names ?? []).map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
              {form.compose_route_service &&
                !(names ?? []).includes(form.compose_route_service) && (
                  <option value={form.compose_route_service}>
                    {form.compose_route_service}
                    {names ? " (not in the compose file)" : ""}
                  </option>
                )}
            </select>
          ) : (
            <input
              id="set-route"
              className={cn(
                input,
                "font-mono",
                !valid && "border-status-failed",
              )}
              value={form.compose_route_service}
              maxLength={63}
              disabled={!canEdit}
              onChange={(e) => set({ compose_route_service: e.target.value })}
              aria-invalid={!valid}
              spellCheck={false}
            />
          )}
          {!valid && (
            <p role="alert" className="text-xs text-status-failed-fg">
              Letters, digits, ., _ and -, starting with a letter or digit, at
              most 63 characters.
            </p>
          )}
        </Field>
        <Field
          id="set-cport"
          labelText="Container port"
          hint={
            target
              ? `The port ${target} listens on.`
              : "The port it listens on."
          }
        >
          <input
            id="set-cport"
            type="number"
            min={0}
            max={65535}
            className={cn(input, "font-mono")}
            value={form.port || ""}
            placeholder="none"
            disabled={!canEdit}
            onChange={(e) => set({ port: parseInt(e.target.value) || 0 })}
          />
        </Field>
      </div>
      {names !== null && valid && route === "" && names.length > 1 && (
        <p className="flex items-start gap-2 text-xs text-status-pending-fg">
          <AlertTriangle
            className="mt-px h-3.5 w-3.5 flex-none"
            aria-hidden="true"
          />
          The compose file has {names.length} services, so the domains
          aren&apos;t routed until you choose one.
        </p>
      )}
      {names !== null && valid && route !== "" && !names.includes(route) && (
        <p className="flex items-start gap-2 text-xs text-status-pending-fg">
          <AlertTriangle
            className="mt-px h-3.5 w-3.5 flex-none"
            aria-hidden="true"
          />
          <span>
            <span className={mono}>{route}</span> is not a service in the saved
            compose file. Deploys fail until it is, or until you choose another.
          </span>
        </p>
      )}
      {form.port === 0 && (
        <p className="flex items-start gap-2 text-xs text-status-pending-fg">
          <AlertTriangle
            className="mt-px h-3.5 w-3.5 flex-none"
            aria-hidden="true"
          />
          With no port, the domains are not routed.
        </p>
      )}
    </div>
  );
}

// ─── Health check ─────────────────────────────────────────────────────────────

function HealthCard({
  service,
  serviceId,
  form,
  set,
  canEdit,
  saved,
}: {
  service: Service;
  serviceId: string;
  form: ConfigForm;
  set: (p: Partial<ConfigForm>) => void;
  canEdit: boolean;
  saved: ConfigForm;
}) {
  const last = service.last_health_check;
  const hhmm = (iso: string) => new Date(iso).toTimeString().slice(0, 5);
  return (
    <div className="flex flex-col gap-4">
      <p className="text-[13px] leading-5 text-fg/85">
        Each deploy waits for this path to answer before the new version takes
        traffic. While live it is checked every minute: a failure marks{" "}
        {service.name} unhealthy and alerts, without taking it offline.
        Requested from inside the container&apos;s network, so the image needs
        no curl.
      </p>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field
          id="set-hpath"
          labelText="Path"
          hint="400 or higher fails; a redirect passes."
        >
          <input
            id="set-hpath"
            className={cn(input, "font-mono")}
            value={form.health_check_path}
            disabled={!canEdit}
            onChange={(e) => set({ health_check_path: e.target.value })}
            spellCheck={false}
          />
        </Field>
        <Field
          id="set-htimeout"
          labelText="Timeout per attempt"
          hint={
            form.health_check_timeout !== saved.health_check_timeout
              ? `Seconds. Saved: ${saved.health_check_timeout}.`
              : "Seconds."
          }
        >
          <input
            id="set-htimeout"
            type="number"
            min={1}
            max={60}
            className={cn(
              input,
              "font-mono",
              form.health_check_timeout !== saved.health_check_timeout &&
                "border-status-pending",
            )}
            value={form.health_check_timeout}
            disabled={!canEdit}
            onChange={(e) =>
              set({ health_check_timeout: parseInt(e.target.value) || 5 })
            }
          />
        </Field>
        <Field
          id="set-hretries"
          labelText="Attempts"
          hint="0 turns both checks off."
        >
          <input
            id="set-hretries"
            type="number"
            min={0}
            max={10}
            className={cn(input, "font-mono")}
            value={form.health_check_retries}
            disabled={!canEdit}
            onChange={(e) =>
              set({ health_check_retries: parseInt(e.target.value) || 0 })
            }
          />
        </Field>
      </div>
      {canEdit && (
        <div className="rounded-lg border border-subtle p-3">
          <HealthCheckTest
            serviceId={serviceId}
            path={form.health_check_path}
            port={form.port}
            timeout={Math.min(30, Math.max(1, form.health_check_timeout || 5))}
            running={isServing(service.runtime_status)}
          />
          <p className="mt-2 text-xs leading-[17px] text-muted">
            Asks {service.name}&apos;s running container once, as a deploy
            would, with the values above:{" "}
            <span className={mono}>{form.health_check_path}</span> on port{" "}
            <span className={mono}>{form.port}</span>,{" "}
            {form.health_check_timeout} s timeout. Nothing is saved.
          </p>
        </div>
      )}
      <p className="text-[13px] text-fg/85">
        {(service.health_check_retries ?? 0) === 0 ? (
          <span className="text-muted">
            Off: no check at deploys or while live.
          </span>
        ) : last ? (
          <>
            <span className="text-muted">Last check </span>
            <span
              className={cn(
                mono,
                last.healthy ? "text-fg" : "text-status-failed-fg",
              )}
            >
              {last.status
                ? `${last.status} in ${last.ms} ms`
                : `no answer${last.ms ? ` after ${last.ms} ms` : ""}`}
            </span>
            <span className="text-muted">
              {" "}
              at {hhmm(last.at)}, every minute since {hhmm(last.since)}
            </span>
            {!last.healthy && last.detail && (
              <span className="text-status-failed-fg">: {last.detail}</span>
            )}
          </>
        ) : (
          <span className="text-muted">
            No check taken yet: the first runs within a minute of the service
            running.
          </span>
        )}
      </p>
      <ContainerCheckField
        service={service}
        form={form}
        set={set}
        canEdit={canEdit}
      />
    </div>
  );
}

/** Docker's own health check, apart from Toft's HTTP check: an image's
 *  HEALTHCHECK goes with every service built from it, a worker included. */
export function ContainerCheckField({
  service,
  form,
  set,
  canEdit,
}: {
  service: Service;
  form: ConfigForm;
  set: (p: Partial<ConfigForm>) => void;
  canEdit: boolean;
}) {
  const mode = form.container_health;
  return (
    <fieldset className="flex flex-col gap-1.5 border-t border-subtle pt-4">
      <legend className={cn(label, "mb-1.5")}>Container check</legend>
      <div
        role="radiogroup"
        aria-label="Container check"
        className="inline-flex gap-0.5 self-start rounded-lg border border-subtle bg-ground p-0.5"
      >
        {(
          [
            ["image", "The image's"],
            ["off", "Off"],
            ["command", "Command"],
          ] as const
        ).map(([v, l]) => (
          <label
            key={v}
            className={cn(
              "inline-flex h-8 cursor-pointer items-center gap-2 rounded-md px-2.5 text-[13px] pointer-coarse:min-h-11",
              mode === v ? "bg-raised text-fg" : "text-muted hover:text-fg",
            )}
          >
            <input
              type="radio"
              name="container-health"
              value={v}
              checked={mode === v}
              disabled={!canEdit}
              onChange={() => set({ container_health: v })}
              className="h-3.5 w-3.5 accent-[var(--link)]"
            />
            {l}
          </label>
        ))}
      </div>
      <p className={help}>
        {mode === "image"
          ? `Docker runs the image's HEALTHCHECK, if it has one, and ${service.name} shows unhealthy while it fails. A worker built from a web service's Dockerfile inherits that service's.`
          : mode === "off"
            ? `Docker runs no check in ${service.name}'s containers, whatever the image says. Toft's check above still runs if it is on.`
            : `Docker runs this in ${service.name}'s containers with /bin/sh, at the image's interval and retries (every 30 s, 3 tries, if it sets none); exit 0 is healthy.`}{" "}
        Applies from the next deploy.
      </p>
      {mode === "command" && (
        <Field
          id="set-ccheck"
          labelText="Command"
          hint="One line, run by the container's shell."
        >
          <input
            id="set-ccheck"
            className={cn(input, "font-mono")}
            value={form.container_health_command}
            maxLength={1024}
            disabled={!canEdit}
            onChange={(e) => set({ container_health_command: e.target.value })}
            spellCheck={false}
          />
        </Field>
      )}
    </fieldset>
  );
}

// ─── Volumes ────────────────────────────────────────────────────────────────

function VolumesCard({
  service,
  serviceId,
  form,
  set,
  canEdit,
}: {
  service: Service;
  serviceId: string;
  form: ConfigForm;
  set: (p: Partial<ConfigForm>) => void;
  canEdit: boolean;
}) {
  const queryClient = useQueryClient();
  const [help, setHelp] = useState(false);
  const [purging, setPurging] = useState<string | null>(null);
  const { data: live } = useQuery<VolumeInfo[]>({
    queryKey: ["volumes", serviceId],
    queryFn: () => serviceService.listVolumes(serviceId),
    staleTime: 30_000,
    retry: false,
  });
  const purge = useMutation({
    mutationFn: (mount: string) => serviceService.pruneVolume(serviceId, mount),
    onSuccess: () => {
      toast.success("Volume data deleted");
      setPurging(null);
      queryClient.invalidateQueries({ queryKey: ["volumes", serviceId] });
    },
    onError: () => toast.error("Failed to delete the volume's data"),
  });
  const update = (i: number, v: ConfigForm["volumes"][number]) =>
    set({ volumes: form.volumes.map((x, j) => (j === i ? v : x)) });
  const small =
    "h-8 rounded-md border border-strong/70 bg-ground px-2 font-mono text-xs text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus disabled:opacity-60";
  return (
    <div className="flex flex-col gap-4">
      <p className="text-[13px] leading-5 text-fg/85">
        A named volume keeps data for {service.name} only, across deploys. A
        shared volume (Settings → Storage) is for files several services use,
        such as uploaded images. Mounting a server folder needs an operator
        exception.{" "}
        <button
          type="button"
          onClick={() => setHelp(true)}
          className="inline-flex items-center gap-1 inline-link"
        >
          <HelpCircle className="h-3.5 w-3.5" aria-hidden="true" />
          How volumes work
        </button>
      </p>
      {form.volumes.length > 0 && (
        <ul className="overflow-hidden rounded-lg border border-subtle">
          {form.volumes.map((v, i) => {
            const bind = v.host_path !== undefined;
            const docker = bind
              ? null
              : `paas-vol-${serviceId.slice(0, 8)}-${v.name}`;
            const info = live?.find((d) =>
              bind ? d.host_path === v.host_path : d.name === docker,
            );
            const isSaved = (service.volumes ?? []).some((sv) =>
              bind
                ? sv.host_path === v.host_path && sv.target === v.target
                : sv.name === v.name && sv.target === v.target,
            );
            return (
              <li
                key={i}
                className={cn(
                  "flex flex-wrap items-center gap-2 px-3 py-2",
                  i > 0 && "border-t border-subtle",
                )}
              >
                <select
                  aria-label="Kind"
                  className={small}
                  value={bind ? "bind" : "volume"}
                  disabled={!canEdit}
                  onChange={(e) =>
                    update(
                      i,
                      e.target.value === "bind"
                        ? { ...v, host_path: "" }
                        : { name: v.name, target: v.target },
                    )
                  }
                >
                  <option value="volume">Named volume</option>
                  <option value="bind">Server folder</option>
                </select>
                <input
                  id={`set-vol-${i}`}
                  aria-label={bind ? "Folder on the server" : "Volume name"}
                  className={cn(small, "min-w-[120px] flex-1")}
                  value={bind ? (v.host_path ?? "") : v.name}
                  disabled={!canEdit}
                  onChange={(e) =>
                    update(
                      i,
                      bind
                        ? { ...v, host_path: e.target.value }
                        : { ...v, name: e.target.value },
                    )
                  }
                  spellCheck={false}
                />
                <span className="text-xs text-muted">→</span>
                <input
                  aria-label="Path in the container"
                  className={cn(small, "min-w-[120px] flex-1")}
                  value={v.target}
                  disabled={!canEdit}
                  placeholder="/data"
                  onChange={(e) => update(i, { ...v, target: e.target.value })}
                  spellCheck={false}
                />
                {bind && (
                  <label className="flex items-center gap-1 text-xs text-muted">
                    <input
                      type="checkbox"
                      className="accent-[var(--link)]"
                      checked={!!v.read_only}
                      disabled={!canEdit}
                      onChange={(e) =>
                        update(i, { ...v, read_only: e.target.checked })
                      }
                    />
                    read-only
                  </label>
                )}
                <span className="text-xs text-muted">
                  {!isSaved ? (
                    <span className="text-status-pending-fg">unsaved</span>
                  ) : bind ? (
                    "set"
                  ) : info?.exists ? (
                    "created"
                  ) : (
                    "created at the next deploy"
                  )}
                </span>
                {canEdit && info?.exists && !bind && (
                  <button
                    type="button"
                    className="text-xs text-status-failed-fg hover:underline"
                    onClick={() => setPurging(info.mount_name)}
                  >
                    Delete data…
                  </button>
                )}
                {canEdit && (
                  <button
                    type="button"
                    aria-label={`Remove ${bind ? v.host_path : v.name || "the volume"}`}
                    onClick={() =>
                      set({ volumes: form.volumes.filter((_, j) => j !== i) })
                    }
                    className="inline-flex h-7 w-7 items-center justify-center rounded-md text-muted hover:bg-raised hover:text-status-failed-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                  >
                    <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {form.volumes.some((v) => v.host_path !== undefined) && (
        <p className="text-xs text-status-pending-fg">
          Server folders need an operator exception (the deploy policy). For
          files several services share, use a shared volume below; for files the
          internet reads, object storage.
        </p>
      )}
      <div className="border-t border-subtle pt-4">
        <SharedMounts
          serviceId={serviceId}
          serverId={service.server_id ?? ""}
          canEdit={canEdit}
        />
      </div>

      <Dialog open={help} onOpenChange={setHelp}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>How volumes work</DialogTitle>
            <DialogDescription>
              Volumes keep data across restarts and deploys. There are three
              kinds.
            </DialogDescription>
          </DialogHeader>
          <dl className="flex flex-col gap-3 text-[13px]">
            <div>
              <dt className="font-medium text-fg">Named volume</dt>
              <dd className="text-fg/85">
                Docker keeps the data on the server; you give a name and where
                it mounts in the container. For a database, for example{" "}
                <span className={mono}>pg-data</span> →{" "}
                <span className={mono}>/var/lib/postgresql/data</span>.
              </dd>
            </div>
            <div>
              <dt className="font-medium text-fg">Shared volume</dt>
              <dd className="text-fg/85">
                One volume several services on the same server mount, read-write
                or read-only. Created in Settings → Storage.
              </dd>
            </div>
            <div>
              <dt className="font-medium text-fg">Server folder</dt>
              <dd className="text-fg/85">
                A folder of the server mounted into the container. It gives the
                container the server&apos;s files, so the deploy policy refuses
                it unless an operator grants an exception. The folder must
                exist; use an absolute path.
              </dd>
            </div>
          </dl>
          <DialogFooter>
            <Button variant="outline" onClick={() => setHelp(false)}>
              Close
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!purging}
        onOpenChange={(o) => {
          if (!o && !purge.isPending) setPurging(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete the data in {purging}?</DialogTitle>
            <DialogDescription>
              Every file in{" "}
              <span className={mono}>
                paas-vol-{serviceId.slice(0, 8)}-{purging}
              </span>{" "}
              is deleted. The service must be stopped first. This can&apos;t be
              undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setPurging(null)}
              disabled={purge.isPending}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => purging && purge.mutate(purging)}
              disabled={purge.isPending}
            >
              {purge.isPending ? "Deleting…" : "Delete data"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// ─── Lifecycle hooks ──────────────────────────────────────────────────────────

function HooksCard({
  isCompose,
  name,
  form,
  set,
  cmds,
  setCmds,
  canEdit,
  containers,
  serviceId,
}: {
  isCompose: boolean;
  name: string;
  form: ConfigForm;
  set: (p: Partial<ConfigForm>) => void;
  cmds: PostDeployCmdRow[];
  setCmds: (c: PostDeployCmdRow[]) => void;
  canEdit: boolean;
  containers: string[];
  serviceId: string;
}) {
  return (
    <div className="flex flex-col gap-5">
      <p className="text-[13px] text-fg/85">
        Commands that run around each deploy.
      </p>
      {!isCompose && (
        <Field
          id="set-migrate"
          labelText="Migrate, in a one-off container"
          hint="Runs from the new image before the new container starts; a failure stops the deploy. The image's ENTRYPOINT is skipped, so name the program itself, for example /server migrate. A plain command needs no shell in the image; one with $VAR, pipes or quotes runs through sh -c."
        >
          <input
            id="set-migrate"
            className={cn(input, "font-mono")}
            value={form.migrate_command}
            disabled={!canEdit}
            onChange={(e) => set({ migrate_command: e.target.value })}
            spellCheck={false}
          />
        </Field>
      )}
      <Field
        id="set-pre"
        labelText="Before deploy, on the host"
        hint={
          <>
            One per line, as root on the server before the pipeline. The deploy
            policy refuses them unless an operator grants {name} the{" "}
            <span className={mono}>host_exec</span> exception.
          </>
        }
      >
        <Textarea
          id="set-pre"
          rows={3}
          className="font-mono text-xs"
          value={form.pre_deploy_commands}
          disabled={!canEdit}
          onChange={(e) => set({ pre_deploy_commands: e.target.value })}
          spellCheck={false}
        />
      </Field>
      <div className="flex flex-col gap-1.5">
        <span className={label}>After deploy, in a container</span>
        {cmds.map((r, i) => (
          <div key={i} className="flex flex-wrap items-center gap-2">
            <select
              aria-label="Container"
              className={cn(input, "h-8 w-auto max-w-[240px] text-xs")}
              value={r.container}
              disabled={!canEdit}
              onChange={(e) =>
                setCmds(
                  cmds.map((x, j) =>
                    j === i ? { ...x, container: e.target.value } : x,
                  ),
                )
              }
            >
              <option value="">the service&apos;s container</option>
              {containers.map((c) => (
                <option key={c} value={c}>
                  {composeServiceOf(c, serviceId)
                    ? `${composeServiceOf(c, serviceId)} · ${c}`
                    : c}
                </option>
              ))}
              {r.container && !containers.includes(r.container) && (
                <option value={r.container}>{r.container}</option>
              )}
            </select>
            <input
              aria-label="Command"
              className={cn(
                input,
                "h-8 min-w-[200px] flex-1 font-mono text-xs",
              )}
              value={r.command}
              disabled={!canEdit}
              onChange={(e) =>
                setCmds(
                  cmds.map((x, j) =>
                    j === i ? { ...x, command: e.target.value } : x,
                  ),
                )
              }
              spellCheck={false}
            />
            {canEdit && (
              <button
                type="button"
                aria-label="Remove the command"
                onClick={() => setCmds(cmds.filter((_, j) => j !== i))}
                className="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted hover:bg-raised hover:text-status-failed-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
              >
                <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
              </button>
            )}
          </div>
        ))}
        {canEdit && (
          <button
            type="button"
            className={cn(outline, "self-start")}
            onClick={() => setCmds([...cmds, { container: "", command: "" }])}
          >
            <Plus className="h-3.5 w-3.5" aria-hidden="true" />
            Add command
          </button>
        )}
        <p className={help}>
          Run inside the running container once the deploy succeeds.{" "}
          {cmds.length === 0 ? "None set." : ""}
        </p>
      </div>
      {!isCompose && (
        <Field
          id="set-required"
          labelText="Required variables"
          hint="One name per line: a deploy stops if any is missing from the variables and secrets."
        >
          <Textarea
            id="set-required"
            rows={3}
            className="font-mono text-xs"
            value={form.required_env_vars}
            disabled={!canEdit}
            onChange={(e) => set({ required_env_vars: e.target.value })}
            spellCheck={false}
          />
        </Field>
      )}
    </div>
  );
}

// ─── Credentials, in the Source card ─────────────────────────────────────────

/** Git token and registry login (D-155): write-once rows; Add… or Replace…
 *  opens the fields, which the Source card saves. */
function Credentials({
  service,
  form,
  set,
  canEdit,
}: {
  service: Service;
  form: ConfigForm;
  set: (p: Partial<ConfigForm>) => void;
  canEdit: boolean;
}) {
  const [opened, setOpened] = useState({ git: false, registry: false });
  const registry =
    service.source_type === "docker-image" ||
    service.source_type === "docker-compose";
  // A compose file kept in the panel clones nothing.
  const git =
    service.source_type === "git" ||
    (service.source_type === "docker-compose" &&
      (form.compose_repo_url !== "" || form.repository_id !== ""));
  // Opening the fields takes the button away: focus goes to the first.
  const open = (k: "git" | "registry", first: string) => {
    setOpened((o) => ({ ...o, [k]: true }));
    requestAnimationFrame(() => document.getElementById(first)?.focus());
  };
  // Edited fields stay open, whatever opened them.
  const gitOpen =
    opened.git ||
    !!form.git_token ||
    form.git_username !== (service.git_username ?? "");
  const registryOpen =
    opened.registry ||
    !!form.registry_password ||
    form.registry_server !== (service.registry_server ?? "") ||
    form.registry_username !== (service.registry_username ?? "");
  const row = (
    title: string,
    why: string,
    stored: boolean,
    open: boolean,
    onOpen: () => void,
  ) => (
    <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
      <span className="text-[13px] text-fg">
        {title} <span className="text-muted">· {why}</span>
      </span>
      <span className="flex items-center gap-2.5">
        <span
          className={cn(
            "text-xs",
            stored ? "text-status-running-fg" : "text-muted",
          )}
        >
          {stored ? "Stored" : "None"}
        </span>
        {canEdit && !open && (
          <button type="button" className={smallGhost} onClick={onOpen}>
            {stored ? "Replace…" : "Add…"}
          </button>
        )}
      </span>
    </div>
  );
  return (
    <div className="flex flex-col gap-3 border-t border-subtle pt-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-[13px] font-semibold text-fg">Credentials</h3>
        <span className={help}>
          Write-once: stored encrypted, never shown again.
        </span>
      </div>
      {git && (
        <>
          {row(
            "Git token",
            "for a private repository",
            !!service.has_git_token,
            gitOpen,
            () => open("git", "set-guser"),
          )}
          {gitOpen && (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                id="set-guser"
                labelText="Username"
                hint="Optional: git, or your username."
              >
                <input
                  id="set-guser"
                  className={cn(input, "font-mono")}
                  value={form.git_username}
                  disabled={!canEdit}
                  onChange={(e) => set({ git_username: e.target.value })}
                  autoComplete="off"
                  spellCheck={false}
                />
              </Field>
              <Field
                id="set-gtoken"
                labelText={service.has_git_token ? "Replace the token" : "Token"}
                hint={
                  service.has_git_token
                    ? "Empty keeps the stored one."
                    : "A personal access token that can read the repository."
                }
              >
                <input
                  id="set-gtoken"
                  type="password"
                  className={input}
                  value={form.git_token}
                  disabled={!canEdit}
                  onChange={(e) => set({ git_token: e.target.value })}
                  autoComplete="new-password"
                />
              </Field>
            </div>
          )}
        </>
      )}
      {registry && (
        <>
          {row(
            "Registry login",
            "to pull private images",
            !!service.has_registry_password || !!service.registry_username,
            registryOpen,
            () => open("registry", "set-rserver"),
          )}
          {registryOpen && (
            <div className="grid gap-4 sm:grid-cols-3">
              <Field
                id="set-rserver"
                labelText="Registry"
                hint="For example ghcr.io."
              >
                <input
                  id="set-rserver"
                  className={cn(input, "font-mono")}
                  value={form.registry_server}
                  disabled={!canEdit}
                  onChange={(e) => set({ registry_server: e.target.value })}
                  spellCheck={false}
                />
              </Field>
              <Field id="set-ruser" labelText="Username">
                <input
                  id="set-ruser"
                  className={cn(input, "font-mono")}
                  value={form.registry_username}
                  disabled={!canEdit}
                  onChange={(e) => set({ registry_username: e.target.value })}
                  autoComplete="off"
                  spellCheck={false}
                />
              </Field>
              <Field
                id="set-rpass"
                labelText={
                  service.has_registry_password
                    ? "Replace the password"
                    : "Password or token"
                }
                hint={
                  service.has_registry_password
                    ? "Empty keeps the stored one."
                    : undefined
                }
              >
                <input
                  id="set-rpass"
                  type="password"
                  className={input}
                  value={form.registry_password}
                  disabled={!canEdit}
                  onChange={(e) => set({ registry_password: e.target.value })}
                  autoComplete="new-password"
                />
              </Field>
            </div>
          )}
        </>
      )}
    </div>
  );
}

// ─── Moving ───────────────────────────────────────────────────────────────────

function MoveDialog({
  open,
  onClose,
  service,
  servers,
  domains,
  running,
  onDeploy,
  deployPending,
}: {
  open: boolean;
  onClose: () => void;
  service: Service;
  servers: Server[];
  domains: string[];
  running: boolean;
  onDeploy: (moveTo?: string) => void;
  deployPending: boolean;
}) {
  const queryClient = useQueryClient();
  const [target, setTarget] = useState<string | null>(null);
  const cpu = parseFloat(service.cpu_limit ?? "") || 0;
  const mem = limitMB(service.memory_limit ?? "");
  const others = placeable(servers).filter((s) => s.id !== service.server_id);
  const fits = useQueries({
    queries: others.map((s) => ({
      queryKey: ["server-fit", s.id, cpu, mem],
      queryFn: () => serverService.fit(s.id, cpu, mem),
      enabled: open,
      staleTime: 30_000,
    })),
  });
  const assign = useMutation({
    mutationFn: (id: string) =>
      serviceService.assignServer(service.id, id || null),
    onSuccess: () => {
      toast.success("Moved: its next deploy runs there");
      queryClient.invalidateQueries({ queryKey: ["service", service.id] });
      close();
    },
    onError: (err) =>
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : "Failed to move the service",
      ),
  });
  const close = () => {
    onClose();
    setTarget(null);
  };
  const from =
    servers.find((s) => s.id === service.server_id)?.name ?? "the panel host";
  const chosen =
    target === null
      ? null
      : target === ""
        ? { name: "the panel host", ip: "" }
        : (() => {
            const s = servers.find((x) => x.id === target);
            return s
              ? { name: s.name, ip: s.shared ? "" : s.ip_address }
              : null;
          })();
  const named = (service.volumes ?? []).filter(
    (v) => v.host_path === undefined,
  );
  const options = [
    ...others.map((s, i) => ({
      id: s.id,
      name: s.name,
      sub: s.shared ? `shared by ${s.shared_by}` : s.ip_address,
      fit: fits[i]?.data,
      loading: fits[i]?.isPending,
    })),
    ...(service.server_id
      ? [
          {
            id: "",
            name: "the panel host",
            sub: "control plane",
            fit: undefined,
            loading: false,
          },
        ]
      : []),
  ];
  const confirm = () => {
    if (target === null) return;
    if (running) {
      close();
      onDeploy(target);
    } else assign.mutate(target);
  };
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o && !assign.isPending) close();
      }}
    >
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>Move {service.name} to another server</DialogTitle>
          <DialogDescription>
            {running
              ? `Moving deploys ${service.name} on the server you choose. Once it runs there it is removed from ${from}; if the deploy fails, it stays on ${from} as it is.`
              : `Nothing of ${service.name} runs now: its next deploy starts it on the server you choose.`}
          </DialogDescription>
        </DialogHeader>
        <div className="mb-2 flex flex-col gap-3">
          <fieldset className="flex flex-col gap-2">
            <legend className="mb-1.5 text-[13px] font-medium text-fg">
              Move to
            </legend>
            {options.length === 0 ? (
              <p className="text-[13px] text-muted">
                There is no other server.{" "}
                <Link href="/servers" className="inline-link">
                  Add one in Servers
                </Link>
                .
              </p>
            ) : (
              <div className="overflow-hidden rounded-lg border border-subtle">
                {options.map((o, i) => {
                  const f = o.fit;
                  const verdict =
                    o.id === ""
                      ? {
                          ok: true,
                          text: "Capacity isn't counted on the panel host",
                        }
                      : o.loading
                        ? { ok: true, text: "Checking room…" }
                        : !f || !f.capacity_known
                          ? {
                              ok: true,
                              text: "Room unknown: capacity not scanned",
                            }
                          : f.fits
                            ? {
                                ok: true,
                                text: `Fits · after the move ${+(f.allocated_cpu_cores + cpu).toFixed(2)} of ${+f.effective_cpu_cores.toFixed(2)} vCPU and ${((f.allocated_memory_mb + mem) / 1024).toFixed(1)} of ${(f.effective_memory_mb / 1024).toFixed(1)} GiB reserved`,
                              }
                            : {
                                ok: false,
                                text: `No · ${!f.memory_ok ? `needs ${mem} MiB, ${Math.max(0, f.effective_memory_mb - f.allocated_memory_mb)} MiB free` : `needs ${cpu} vCPU, ${+Math.max(0, f.effective_cpu_cores - f.allocated_cpu_cores).toFixed(2)} free`}`,
                              };
                  return (
                    <label
                      key={o.id || "panel"}
                      className={cn(
                        "flex cursor-pointer items-start gap-3 px-3 py-2.5 text-[13px]",
                        i > 0 && "border-t border-subtle",
                        target === o.id && "bg-accent-soft",
                      )}
                    >
                      <input
                        type="radio"
                        name="move-target"
                        className="mt-0.5 h-4 w-4 accent-[var(--link)]"
                        checked={target === o.id}
                        onChange={() => setTarget(o.id)}
                      />
                      <span className="flex min-w-0 flex-col gap-0.5">
                        <span>
                          <span className="font-mono text-xs text-fg">
                            {o.name}
                          </span>{" "}
                          <span className="text-xs text-muted">{o.sub}</span>
                        </span>
                        <span
                          className={cn(
                            "text-xs",
                            verdict.ok ? "text-muted" : "text-status-failed-fg",
                          )}
                        >
                          {verdict.text}
                        </span>
                      </span>
                    </label>
                  );
                })}
              </div>
            )}
          </fieldset>
          {chosen && (
            <dl className="flex flex-col overflow-hidden rounded-lg border border-subtle text-[13px]">
              <div className="grid grid-cols-[110px_1fr] gap-3 px-3 py-2.5">
                <dt className="text-muted">Domains</dt>
                <dd className="text-fg/85">
                  {domains.length === 0 ? (
                    "None."
                  ) : (
                    <>
                      Point the DNS of{" "}
                      {domains.length === 1
                        ? domains[0]
                        : `its ${domains.length} domains`}{" "}
                      at{" "}
                      {chosen.ip ? (
                        <span className={mono}>{chosen.ip}</span>
                      ) : (
                        chosen.name
                      )}
                      . Until DNS changes, requests still reach {from}.
                    </>
                  )}
                </dd>
              </div>
              <div className="grid grid-cols-[110px_1fr] gap-3 border-t border-subtle px-3 py-2.5">
                <dt className="text-muted">Volumes</dt>
                <dd className="text-fg/85">
                  {named.length === 0 &&
                  service.source_type !== "docker-compose" ? (
                    "None of its own."
                  ) : (
                    <>
                      {named.length ? (
                        <>
                          <span className={mono}>
                            {named.map((v) => v.name).join(", ")}
                          </span>{" "}
                          and {named.length === 1 ? "its" : "their"} data stay
                        </>
                      ) : (
                        "Its compose volumes and their data stay"
                      )}{" "}
                      on {from}; {chosen.name} starts with empty volumes. A
                      volume&apos;s data never moves with its service.
                    </>
                  )}
                </dd>
              </div>
              <div className="grid grid-cols-[110px_1fr] gap-3 border-t border-subtle px-3 py-2.5">
                <dt className="text-muted">Config files</dt>
                <dd className="text-fg/85">
                  Written on {chosen.name} at the deploy.
                </dd>
              </div>
              <div className="grid grid-cols-[110px_1fr] gap-3 border-t border-subtle px-3 py-2.5">
                <dt className="text-muted">Variables</dt>
                <dd className="text-fg/85">
                  Variables, secrets and domains move with it.
                </dd>
              </div>
            </dl>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={close} disabled={assign.isPending}>
            {options.length === 0 ? "Close" : "Cancel"}
          </Button>
          {options.length > 0 && (
            <Button
              onClick={confirm}
              disabled={target === null || assign.isPending || deployPending}
            >
              {assign.isPending
                ? "Moving…"
                : running
                  ? "Move and deploy"
                  : "Move"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ─── Deleting ─────────────────────────────────────────────────────────────────

function DeleteDialog({
  open,
  onClose,
  service,
  serviceId,
  server,
  containers,
  domains,
  mounts,
  counts,
  onDeleted,
}: {
  open: boolean;
  onClose: () => void;
  service: Service;
  serviceId: string;
  server?: Server;
  containers: string[];
  domains: string[];
  mounts: string[];
  counts: Counts;
  onDeleted: () => void;
}) {
  const queryClient = useQueryClient();
  const [typed, setTyped] = useState("");
  const data = useDeleteData("services", serviceId, open);
  const del = useMutation({
    mutationFn: () => serviceService.delete(serviceId, typed),
    onSuccess: () => {
      toast.success(`${service.name} deleted`);
      queryClient.invalidateQueries({ queryKey: ["env-services"] });
      onDeleted();
    },
    onError: (err: Error) =>
      toast.error(err.message || "Failed to delete the service", {
        duration: 15_000,
      }),
  });
  const n = (count: number | undefined, one: string, many: string) =>
    count === undefined ? "…" : `${count} ${count === 1 ? one : many}`;
  const rows: [string, ReactNode][] = [
    [
      "Containers",
      containers.length ? (
        <span className={mono}>{containers.join(", ")}</span>
      ) : (
        "none running"
      ),
    ],
    [
      "Volumes and data",
      <DeleteData
        key="data"
        {...data}
        perService={(data.data?.length ?? 0) > 1}
      />,
    ],
    ["Images", imagesOf(service.source_type)],
    [
      "History",
      `${n(counts.deployments, "deployment", "deployments")} and their logs`,
    ],
    [
      "Configuration",
      `${n(counts.variables, "variable", "variables")}, ${n(counts.secrets, "secret", "secrets")}, ${n(counts.files, "config file", "config files")}`,
    ],
    ["Domains", domains.length ? domains.join(", ") : "none"],
  ];
  const close = () => {
    onClose();
    setTyped("");
  };
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o && !del.isPending) close();
      }}
    >
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Delete {service.name}?</DialogTitle>
          <DialogDescription>
            This removes {service.name} from {server?.name ?? "the panel host"}{" "}
            and deletes everything below. It can&apos;t be undone.
          </DialogDescription>
        </DialogHeader>
        <div className="mb-2 flex flex-col gap-3">
          <dl className="overflow-hidden rounded-lg border border-subtle text-[13px]">
            {rows.map(([k, v], i) => (
              <div
                key={k}
                className={cn(
                  "grid grid-cols-[130px_1fr] gap-3 px-3 py-2",
                  i > 0 && "border-t border-subtle",
                )}
              >
                <dt className="text-muted">{k}</dt>
                <dd className="min-w-0 break-words text-fg/85">{v}</dd>
              </div>
            ))}
          </dl>
          <p className="text-[13px] text-fg/85">
            {mounts.length ? (
              <>
                Shared volumes it mounts stay, with their data:{" "}
                {mounts.join(", ")}.
              </>
            ) : (
              "Shared volumes it mounts stay, with their data."
            )}
          </p>
          <div className="flex flex-col gap-1.5">
            <label htmlFor="del-svc" className="text-[13px] text-fg">
              Type <span className="font-mono">{service.name}</span> to confirm
            </label>
            <Input
              id="del-svc"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={close} disabled={del.isPending}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            onClick={() => del.mutate()}
            disabled={typed !== service.name || del.isPending}
          >
            {del.isPending ? "Deleting…" : `Delete ${service.name}`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
