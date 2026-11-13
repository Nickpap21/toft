"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import {
  keepPreviousData,
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, Box, Copy, GitBranch, HardDrive, LayoutGrid, Layers, Lock, RotateCcw, X } from "lucide-react";
import { ServiceTypesGuide } from "@/components/ui/service-types-guide";
import { CapacityDialog, capacityWarningFrom, formatMemory, type CapacityWarning } from "@/components/ui/capacity-dialog";
import { PolicyViolationList } from "@/components/ui/policy-violations";
import { TemporaryChip, TEMPORARY_NOTE } from "@/components/ui/temporary-chip";
import { deploymentHref, serviceHref } from "@/components/home/home";
import { EnvChip } from "@/components/ui/env-chip";
import { useEnvContext } from "@/components/shell/env-context";
import { LoadFailed, Read, readFailed } from "@/components/shell/unreachable";
import type { EnvColor } from "@/lib/api/environment.service";
import {
  catalogService,
  type CatalogTemplate,
} from "@/lib/api/catalog.service";
import {
  policyViolationsFromError,
  type PolicyViolation,
} from "@/lib/api/deploypolicy.service";
import { fetchAPI } from "@/lib/api/client";
import { panelHostQuery } from "@/lib/api/instance.service";
import { orgService } from "@/lib/api/org.service";
import { projectService } from "@/lib/api/project.service";
import { resourcesService } from "@/lib/api/resources.service";
import { serverService } from "@/lib/api/server.service";
import { defaultNetworkAlias } from "@/lib/network-alias";
import {
  serviceService,
  type CreatedService,
  type CreateServiceInput,
  type ServiceScope,
} from "@/lib/api/service.service";
import { useAuth, useIsOperator, useOrgRole } from "@/context/AuthContext";
import {
  environmentsQuery,
  projectOverviewQuery,
  projectQuery,
  projectsQuery,
  serversQuery,
} from "@/lib/queries";
import { cn } from "@/lib/utils";
import { placeable } from "@/components/servers/agent";
import { CATALOG_LOGOS } from "@/lib/catalog-logos";
import { card, field, h2, label, mono, outline, primary, small } from "./ui";
import { ProtectedConfirm } from "./protected-confirm";
import { RepoSourceChoice } from "./repo-source";
import { errorCode, useRepos } from "@/components/code/repo";
import { repoService } from "@/lib/api/code.service";

// New service (board: private/design/canvas/ReworkNewService.dc.html, D-154):
// a source, the catalog when it is the source, the service, where it runs,
// and the Summary beside them with Create and deploy. The environment is the
// sidebar's. Without a project (/shared/new) it creates a workspace service.

type Source = "git" | "image" | "compose" | "catalog";

const CATEGORIES: Record<string, string> = {
  apps: "Apps",
  database: "Databases",
  cache: "Cache",
  messaging: "Messaging",
  storage: "Storage",
  tools: "Tools",
  observability: "Observability",
};

const KIND_WORDS: Record<string, string> = {
  database: "a database",
  cache: "a cache",
  tool: "a tool",
  app: "an app",
};

/** A random value for a catalog secret: 24 characters from a URL-safe set,
 *  shown until the service is created (write-once after). */
/** A secret's value, as its catalog spec asks (generate): a 24-character
 *  password, 64 characters ("long"), or 32 random bytes in hex ("hex64"). */
export function generatedSecret(generate?: string): string {
  if (generate === "hex64")
    return Array.from(crypto.getRandomValues(new Uint8Array(32)), (b) => b.toString(16).padStart(2, "0")).join("");
  const set = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789";
  const bytes = crypto.getRandomValues(new Uint8Array(generate === "long" ? 64 : 24));
  return Array.from(bytes, (b) => set[b % set.length]).join("");
}

/** The basic auth user in front of a setup_login app until its admin account is set up. */
const TEMP_LOGIN_USER = "toft";

/** What a catalog entry runs: its image (and port), or its compose stack. */
export function catalogRuns(
  t: Pick<CatalogTemplate, "kind" | "docker_image" | "default_port" | "compose_services">,
  withPort = true,
): string {
  if (t.kind !== "compose") return withPort ? `${t.docker_image} · ${t.default_port}` : t.docker_image;
  const n = t.compose_services ?? 1;
  return `Compose · ${n} service${n === 1 ? "" : "s"}`;
}

/** A catalog entry's logo (D-141), else lucide's Box. */
function CatalogMark({ t }: { t: CatalogTemplate }) {
  const d = CATALOG_LOGOS[t.slug];
  return (
    <span
      aria-hidden="true"
      className="flex h-9 w-9 flex-none items-center justify-center rounded-lg border border-strong bg-raised text-fg/75"
    >
      {d ? (
        <svg viewBox="0 0 24 24" className="h-6 w-6" fill="currentColor">
          <path d={d} />
        </svg>
      ) : (
        <Box className="h-6 w-6" strokeWidth={1.6} />
      )}
    </span>
  );
}

/** A picked entry's line: what it runs, its port, its volume. */
export function pickedSub(t: Pick<CatalogTemplate, "kind" | "docker_image" | "default_port" | "compose_services" | "volumes">): string {
  return [
    t.kind === "compose" ? catalogRuns(t) : t.docker_image,
    `port ${t.default_port}`,
    ...(t.volumes.length === 1 ? ["a volume for its data"] : t.volumes.length > 1 ? [`${t.volumes.length} volumes for its data`] : []),
  ].join(" · ");
}

/** A name as a service name: lower case, dashes for the rest. */
export const serviceNameOf = (s: string) =>
  s.toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "");

/** base, else base-2, base-3…: the first name the scope doesn't have. */
export function uniqueName(base: string, taken: ReadonlySet<string>): string {
  if (!base || !taken.has(base)) return base;
  for (let i = 2; ; i++) if (!taken.has(`${base}-${i}`)) return `${base}-${i}`;
}

/** The protected environments a new service would run in, so Create and
 *  deploy confirms first (D-143, D-154): its own environment, or every one a
 *  project service serves. A new project's first service has none. */
export function protectedServed<E extends { protected: boolean }>(
  scope: ServiceScope,
  env: E | undefined,
  environments: E[] | undefined,
  first: boolean,
): E[] {
  if (first || scope === "org") return [];
  if (scope === "environment") return env?.protected ? [env] : [];
  return (environments ?? []).filter((e) => e.protected);
}

/** A catalog entry's settings as the create takes them (D-164): its secrets,
 *  one left empty generated by the panel, and every other row as a variable,
 *  a cleared one sent empty so it stays empty instead of getting its default. */
export function catalogSettings(rows: { key: string; value: string; secret: boolean }[]) {
  const pair = ({ key, value }: { key: string; value: string }) => ({ key, value });
  return {
    secrets: rows.filter((c) => c.secret && c.value).map(pair),
    variables: rows.filter((c) => !c.secret && c.key).map(pair),
  };
}

/** The memory limits offered at creation, beside the kind's default. */
const MEMORY_MB = [256, 512, 1024, 2048, 4096];

/** "1 vCPU · 10.3 GiB free": what a server has left, as the deploy gate counts it. */
export function freeText(
  fit:
    | {
        capacity_known: boolean;
        effective_cpu_cores: number;
        allocated_cpu_cores: number;
        effective_memory_mb: number;
        allocated_memory_mb: number;
      }
    | undefined,
): string {
  if (!fit || !fit.capacity_known) return "";
  const cpu = Math.max(0, fit.effective_cpu_cores - fit.allocated_cpu_cores);
  const gib =
    Math.max(0, fit.effective_memory_mb - fit.allocated_memory_mb) / 1024;
  const n = (x: number) => (Number.isInteger(x) ? String(x) : x.toFixed(1));
  return `${n(Math.round(cpu * 10) / 10)} vCPU · ${n(Math.round(gib * 10) / 10)} GiB free`;
}

const radioCard = (on: boolean) =>
  cn(
    "flex cursor-pointer gap-2.5 rounded-lg border p-3",
    on
      ? "border-link bg-link/10"
      : "border-subtle bg-ground/40 hover:border-strong",
  );
const segment = (on: boolean) =>
  cn(
    "flex h-[30px] flex-1 cursor-pointer items-center justify-center gap-1.5 whitespace-nowrap rounded-md px-2 text-[13px] font-medium focus-within:ring-2 focus-within:ring-focus",
    on ? "bg-raised text-fg" : "text-fg/70",
  );
const sub = "rounded-lg border border-subtle bg-ground/60 px-3 py-2.5";

/** first: a new project's first service (7g, Home's Deploy your first app):
 *  the project is named after it, the service goes in its production and
 *  deploys once created, all from one create. */
export function NewServiceForm({ projectId, first = false }: { projectId: string; first?: boolean }) {
  const router = useRouter();
  const params = useSearchParams();
  const queryClient = useQueryClient();
  const role = useOrgRole();
  const operator = useIsOperator();
  const { user } = useAuth();
  const canCreate = role === "owner" || role === "developer";
  const workspace = user?.orgName ?? "this workspace";

  const { data: project } = useQuery(projectQuery(projectId));
  const environmentsRead = useQuery(environmentsQuery(projectId));
  const environments = environmentsRead.data;
  const { data: overview } = useQuery(projectOverviewQuery(projectId));
  const { data: envServices } = useQuery({
    queryKey: ["project-services", projectId],
    queryFn: () => serviceService.listForProject(projectId),
    enabled: !!projectId,
  });
  const { data: projectServices } = useQuery({
    queryKey: ["project-scoped-services", projectId],
    queryFn: () => serviceService.listProjectScoped(projectId),
    enabled: !!projectId,
  });
  const { data: workspaceServices } = useQuery({
    queryKey: ["org-scoped-services"],
    queryFn: () => serviceService.listOrgScoped(),
  });
  const serversRead = useQuery({ ...serversQuery, select: placeable });
  const servers = serversRead.data;
  // A failed read can't be drawn as none, and the form won't create without
  // it. errorUpdateCount: failed, and reading again after Retry.
  const serversFailed = servers === undefined && (serversRead.isError || serversRead.errorUpdateCount > 0);
  const envsFailed = environments === undefined && (environmentsRead.isError || environmentsRead.errorUpdateCount > 0);
  const catalog = useQuery({
    queryKey: ["catalog"],
    queryFn: catalogService.list,
    staleTime: Infinity,
  });
  const templates = catalog.data;
  const fits = useQueries({
    queries: (servers ?? []).map((s) => ({
      queryKey: ["server-fit", s.id],
      queryFn: () => serverService.fit(s.id),
      staleTime: 30_000,
    })),
  });

  const [guide, setGuide] = useState(false);
  // ?source= opens on one of Home's three ways (7g).
  const [source, setSource] = useState<Source>(() => {
    const s = params.get("source");
    return s === "image" || s === "compose" || s === "catalog" ? s : "git";
  });
  const [type, setType] = useState<"web" | "worker">("web");
  // null: the suggestion (the catalog entry's or repository's name), until typed.
  const [name, setName] = useState<string | null>(null);
  const [gitUrl, setGitUrl] = useState("");
  // null: the chosen repository's default branch (main for a URL), until typed.
  const [branch, setBranch] = useState<string | null>(null);
  const [baseDir, setBaseDir] = useState("");
  const [port, setPort] = useState(3000);
  const [gitUser, setGitUser] = useState("");
  const [gitToken, setGitToken] = useState("");
  const [image, setImage] = useState("");
  const [registry, setRegistry] = useState({
    server: "",
    user: "",
    password: "",
  });
  const [composeMode, setComposeMode] = useState<"repo" | "inline">("repo");
  const [composeRepo, setComposeRepo] = useState("");
  const [composePath, setComposePath] = useState("");
  const [composeContent, setComposeContent] = useState("");
  // One of the project's repositories (D-77), "" for a URL; null until
  // chosen, when the project's first is the default.
  const [repoId, setRepoId] = useState<string | null>(null);
  const [template, setTemplate] = useState<CatalogTemplate | null>(null);
  const [alias, setAlias] = useState("");
  const [config, setConfig] = useState<
    { key: string; value: string; secret: boolean; hint: string; generate?: string; generated?: boolean }[]
  >([]);
  // The generated password of the basic auth in front of an app whoever
  // opens first would own (setup_login), null for any other.
  const [tempLogin, setTempLogin] = useState<string | null>(null);
  const [category, setCategory] = useState("all");
  // The catalog shows its grid until an entry is picked, and again on Change.
  const [choosing, setChoosing] = useState(true);
  const [serverListOpen, setServerListOpen] = useState(false);
  // null: the default for its kind.
  const [memMB, setMemMB] = useState<number | null>(null);
  const [confirm, setConfirm] = useState(false);
  const initialScope: ServiceScope = first
    ? "environment"
    : !projectId || params.get("scope") === "workspace"
      ? "org"
      : params.get("scope") === "project"
        ? "project"
        : "environment";
  const [scope, setScope] = useState<ServiceScope>(initialScope);
  const [serverId, setServerId] = useState<string | null>(null);
  const [violations, setViolations] = useState<PolicyViolation[] | null>(null);
  // Create and deploy refused for room: nothing was created.
  const [room, setRoom] = useState<CapacityWarning | null>(null);
  // The Create and deploy clicked (the Summary's or the phone bar's): the refusal returns focus there.
  const deployButton = useRef<HTMLButtonElement | null>(null);

  const { repos } = useRepos(projectId);
  // A workspace service builds from a URL only.
  const ownRepos = scope !== "org" ? (repos ?? []) : [];
  const repoChoice = ownRepos.some((r) => r.id === repoId) || repoId === "" ? (repoId ?? "") : (ownRepos[0]?.id ?? "");
  // The code browser's query: its cache answers when the repository was just open.
  const { data: repoDetail } = useQuery({
    queryKey: ["repository", projectId, repoChoice],
    queryFn: () => repoService(projectId!, repoChoice).detail(),
    enabled: !!projectId && !!repoChoice,
  });
  const branchValue = branch ?? (repoChoice ? repoDetail?.default_branch : undefined) ?? "main";
  const branchName = branchValue.trim() || "main";
  // The sidebar's environment (D-154); "all" falls back to the first.
  const ctx = useEnvContext(projectId, environments);
  const env = projectId ? (ctx.env ?? environments?.[0]) : undefined;
  const envCounts = overview?.environments.find((e) => e.id === env?.id);
  // The legacy app network: only operators deploy to the panel host (D-19).
  const { data: envOverview } = useQuery({
    queryKey: ["environment-overview", env?.id],
    queryFn: () =>
      fetchAPI<{ network: string }>(`/api/v1/environments/${env?.id}/overview`),
    enabled: !!env,
  });
  const hostRead = useQuery(panelHostQuery);
  const { data: panelHost, isPending: hostPending } = hostRead;
  const hostFailed = readFailed(hostRead);
  const legacy = first ? panelHost?.app_networks === "legacy" : envOverview?.network === "";
  // Under Only Toft the panel host runs no apps (D-62): never offered, nor
  // until the panel host answers, so it can't appear and vanish.
  const panelApps = panelHost !== undefined && panelHost.role !== "control_plane";
  const noServer = panelHost?.role === "control_plane" && servers?.length === 0;
  // Who may create past the capacity check, as checkCapacity decides it.
  const { data: orgSettings } = useQuery({ queryKey: ["org-settings"], queryFn: orgService.getSettings });

  // The server the environment's services use, else the workspace's own, else the panel host.
  const own = (servers ?? []).filter((s) => !s.shared);
  const envServers = (envServices ?? [])
    .filter((s) => s.environment_id === env?.id)
    .map((s) => s.server_id ?? "")
    .filter((id) => panelApps || id !== "");
  const usual = envServers.sort(
    (a, b) =>
      envServers.filter((x) => x === b).length -
      envServers.filter((x) => x === a).length,
  )[0];
  const defaultServer = usual ?? own[0]?.id ?? servers?.[0]?.id ?? "";
  const server = serverId ?? defaultServer;

  // Names the scope already has: a new one must differ (D-154).
  const taken = new Set(
    (scope === "org"
      ? (workspaceServices ?? [])
      : scope === "project"
        ? (projectServices ?? [])
        : (envServices ?? []).filter((s) => s.environment_id === env?.id)
    ).map((s) => s.name),
  );
  const suggested =
    source === "catalog" && template
      ? uniqueName(serviceNameOf(template.name), taken)
      : source === "git" && !first
        ? uniqueName(serviceNameOf(ownRepos.find((r) => r.id === repoChoice)?.name ?? ""), taken)
        : "";
  const nm = (name ?? suggested).trim();
  // In first mode the name is a new project's, which only the create can
  // refuse (project_name_taken): the name it refused, then that project.
  const [refusedName, setRefusedName] = useState("");
  const { data: projects } = useQuery({ ...projectsQuery, enabled: refusedName !== "" });
  const existing = projects?.find((p) => p.name === refusedName);
  const nameTaken = first ? nm !== "" && nm === refusedName : taken.has(nm);

  // ?template=<slug> opens on that catalog entry (Workspace services' Loki, Grafana).
  const wanted = params.get("template");
  const [seeded, setSeeded] = useState(false);
  if (!seeded && wanted && templates) {
    setSeeded(true);
    const t = templates.find((x) => x.slug === wanted);
    if (t) pickTemplate(t);
  }

  // The memory limit the server will set (D-41), once the image settles.
  const [settledImage, setSettledImage] = useState("");
  const kindImage =
    source === "catalog"
      ? (template?.docker_image ?? "")
      : source === "image"
        ? image
        : "";
  useEffect(() => {
    const t = setTimeout(() => setSettledImage(kindImage), 400);
    return () => clearTimeout(t);
  }, [kindImage]);
  const slug = source === "catalog" ? (template?.slug ?? "") : "";

  // The address it would get (D-150), asked once the form settles: on by
  // default for an environment's web app, never for a shared service.
  const [giveAddress, setGiveAddress] = useState(true);
  const addressAsk =
    scope === "environment" && (env || first) && nm && !hostPending
      ? {
          env: first ? "" : (env?.id ?? ""),
          name: nm,
          type: source === "catalog" || source === "compose" ? ("web" as const) : type,
          server_id: server,
          addon_slug: slug,
          docker_image: kindImage,
          port: source === "catalog" ? (template?.default_port ?? 0) : type === "web" || source === "compose" ? port : 0,
        }
      : null;
  const addressKey = JSON.stringify(addressAsk);
  const [settledAddress, setSettledAddress] = useState("null");
  useEffect(() => {
    const t = setTimeout(() => setSettledAddress(addressKey), 400);
    return () => clearTimeout(t);
  }, [addressKey]);
  const { data: address, isFetching: addressLoading, isError: addressFailed } = useQuery({
    queryKey: ["new-service-address", settledAddress],
    queryFn: () => {
      const { env: id, ...q } = JSON.parse(settledAddress);
      return first ? serviceService.newProjectAddress(q) : serviceService.address(id, q);
    },
    enabled: settledAddress !== "null",
    staleTime: 30_000,
    placeholderData: keepPreviousData,
  });
  // The last answer stays, dimmed, while a changed form settles.
  const addressSettling = settledAddress !== addressKey || addressLoading;

  const limitRead = useQuery({
    queryKey: ["default-limit", slug, settledImage],
    queryFn: () => resourcesService.defaultLimit(slug, settledImage),
    staleTime: Infinity,
  });
  const limit = limitRead.data;
  const limitFailed = limit === undefined && (limitRead.isError || limitRead.errorUpdateCount > 0);

  function pickTemplate(t: CatalogTemplate) {
    setSource("catalog");
    setTemplate(t);
    setChoosing(false);
    setName(null);
    setAlias(t.network_alias ?? "");
    setConfig(
      [...t.env_var_specs].sort((a, b) => Number(a.is_secret) - Number(b.is_secret)).map((s) => ({
        key: s.key,
        value: s.is_secret ? generatedSecret(s.generate) : s.default,
        generated: s.is_secret,
        secret: s.is_secret,
        hint: s.description,
        generate: s.generate,
      })),
    );
    setTempLogin(t.setup_login ? generatedSecret() : null);
    if (t.is_observability && projectId) setScope("org");
    setViolations(null);
  }

  function composeLikeNow() {
    return source === "compose" || (source === "catalog" && template?.kind === "compose");
  }

  const create = useMutation({
    mutationFn: async ({ deploy, force = false }: { deploy: boolean; force?: boolean }): Promise<CreatedService> => {
      const base: CreateServiceInput =
        source === "catalog" && template?.kind === "compose"
          ? {
              // A compose app: an ordinary compose service from the
              // template's file, its domain on route_service (D-147).
              name: nm,
              type: "web",
              source_type: "docker-compose",
              compose_content: template.compose_content ?? "",
              compose_route_service: template.route_service ?? "",
              port: template.default_port,
              addon_slug: template.slug,
            }
          : source === "catalog" && template
          ? {
              name: nm,
              type: "web",
              source_type: "docker-image",
              docker_image: template.docker_image,
              port: template.default_port,
              addon_slug: template.slug,
              network_alias: alias.trim() || template.network_alias,
              volumes: template.volumes,
              ...(template.start_command
                ? { start_command: template.start_command }
                : {}),
            }
          : {
              name: nm,
              type: source === "compose" ? "web" : type,
              source_type:
                source === "image"
                  ? "docker-image"
                  : source === "compose"
                    ? "docker-compose"
                    : "git",
              port: type === "web" || source === "compose" ? port : 0,
              ...(source === "git"
                ? {
                    ...(repoChoice ? { repository_id: repoChoice } : { git_repo_url: gitUrl.trim() }),
                    git_branch: branchName,
                    base_directory: baseDir.trim(),
                  }
                : source === "image"
                  ? {
                      docker_image: image.trim(),
                      ...(registry.server
                        ? { registry_server: registry.server.trim() }
                        : {}),
                      ...(registry.user
                        ? { registry_username: registry.user.trim() }
                        : {}),
                      ...(registry.password
                        ? { registry_password: registry.password }
                        : {}),
                    }
                  : composeMode === "repo"
                    ? {
                        ...(repoChoice ? { repository_id: repoChoice } : { compose_repo_url: composeRepo.trim() }),
                        git_branch: branchName,
                        compose_file_path:
                          composePath.trim() || "docker-compose.yml",
                      }
                    : { compose_content: composeContent }),
              ...((source === "git" ||
                (source === "compose" && composeMode === "repo")) &&
              !repoChoice &&
              gitUser
                ? { git_username: gitUser.trim() }
                : {}),
              ...((source === "git" ||
                (source === "compose" && composeMode === "repo")) &&
              !repoChoice &&
              gitToken
                ? { git_token: gitToken }
                : {}),
            };
      const memoryLimit = memMB !== null && limit && memMB !== limit.memory_mb && !composeLikeNow() ? `${memMB}m` : null;
      const payload = {
        ...base,
        ...(server ? { server_id: server } : {}),
        ...(memoryLimit ? { memory_limit: memoryLimit } : {}),
        // The create starts the first deploy itself (D-164). Refused, and
        // not created, when it won't fit its server.
        ...(deploy ? { deploy: true } : {}),
        ...(force ? { force: true } : {}),
        // The panel names it again on create, so a click before the
        // card settles still gets the address the box promises.
        ...(scope === "environment" && giveAddress ? { address: true } : {}),
        // Catalog settings with the service in one create: secrets
        // write-once, the others as variables.
        ...(source === "catalog" ? catalogSettings(config) : {}),
        ...(source === "catalog" && tempLogin ? { basic_auth_user: TEMP_LOGIN_USER, basic_auth_password: tempLogin } : {}),
      };
      if (first) {
        // The project, its production and the service in one create (7g).
        const created = await projectService.create({ name: nm, service: payload });
        return created.service as CreatedService;
      }
      return scope === "org"
        ? serviceService.createOrgScoped(payload)
        : scope === "project"
          ? serviceService.createProjectScoped(projectId, payload)
          : serviceService.create(env!.id, payload);
    },
    onSuccess: (svc) => {
      setConfirm(false);
      if (first) {
        queryClient.invalidateQueries({ queryKey: ["projects"] });
        queryClient.invalidateQueries({ queryKey: ["dashboard"] });
        queryClient.invalidateQueries({ queryKey: ["workspace-services"] });
        if (svc.deploy_error) {
          toast.error(`${svc.name} created, but its deploy didn’t start: ${svc.deploy_error}`);
          router.push(serviceHref(svc.project_id, svc.id));
          return;
        }
        toast.success(svc.deployment ? `${svc.name} created and deploying` : `${svc.name} created`);
        router.push(svc.deployment ? "/dashboard" : serviceHref(svc.project_id, svc.id));
        return;
      }
      queryClient.invalidateQueries({ queryKey: ["project-services"] });
      queryClient.invalidateQueries({ queryKey: ["project-scoped-services"] });
      queryClient.invalidateQueries({ queryKey: ["org-scoped-services"] });
      queryClient.invalidateQueries({ queryKey: ["env-services"] });
      if (svc.deploy_error) toast.error(`${svc.name} created, but its deploy didn’t start: ${svc.deploy_error}`);
      else if (svc.deployment) toast.success(`${svc.name} created and deploying`);
      else toast.success(`${svc.name} created: set its variables and domains, then deploy`);
      const n = svc.policy_violations?.length ?? 0;
      if (n > 0)
        toast.warning(
          `${svc.name}: ${n} deploy policy ${n === 1 ? "violation" : "violations"}; see Settings → Deploy policy before deploying`,
        );
      router.push(svc.deployment ? deploymentHref(svc.project_id, svc.id) : serviceHref(svc.project_id, svc.id));
    },
    onError: (err: unknown) => {
      setConfirm(false);
      const w = capacityWarningFrom(err);
      if (w) {
        setRoom(w);
        return;
      }
      const v = policyViolationsFromError(err);
      if (v) {
        setViolations(v);
        toast.error(
          "Not created: the deploy policy refuses this configuration",
        );
        return;
      }
      if (errorCode(err) === "project_name_taken") {
        setRefusedName(nm);
        document.getElementById("ns-name")?.focus();
        return;
      }
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : "Failed to create the service",
      );
    },
  });

  if (!canCreate) {
    return (
      <section className={cn(card, "flex max-w-[640px] flex-col gap-2 p-5")}>
        <h1 className={h2}>New service</h1>
        <p className="text-sm text-fg/70">
          Owners and developers of {workspace} create services.
        </p>
      </section>
    );
  }

  const isCatalog = source === "catalog";
  const picking = isCatalog && (choosing || !template);
  const composeLike = source === "compose" || (isCatalog && template?.kind === "compose");
  const valid =
    nm !== "" &&
    !nameTaken &&
    !picking &&
    !hostPending &&
    !hostFailed &&
    !noServer &&
    !serversFailed &&
    !envsFailed &&
    (scope !== "environment" || !!env || first) &&
    (source === "git"
      ? !!repoChoice || gitUrl.trim() !== ""
      : source === "image"
        ? image.trim() !== ""
        : source === "compose"
          ? composeMode === "repo"
            ? !!repoChoice || composeRepo.trim() !== ""
            : composeContent.trim() !== ""
          : !!template && config.every((c) => !c.secret || c.value !== ""));
  // By category, in the filter's order, as the board lists them.
  const order = Object.keys(CATEGORIES);
  const shown = (templates ?? [])
    .filter((t) => category === "all" || t.category === category)
    .sort((a, b) => order.indexOf(a.category) - order.indexOf(b.category));
  const serverLabel = (id: string) =>
    id
      ? (servers?.find((s) => s.id === id)?.name ?? "its server")
      : "the panel host";
  const onServer = (id: string) => {
    const envs = [
      ...new Set(
        (envServices ?? [])
          .filter((s) => (s.server_id ?? "") === id)
          .map(
            (s) => environments?.find((e) => e.id === s.environment_id)?.name,
          )
          .filter(Boolean),
      ),
    ];
    const proj = (projectServices ?? [])
      .filter((s) => (s.server_id ?? "") === id)
      .map((s) => s.name);
    return [...envs, ...proj] as string[];
  };
  const reachable = [
    ...onServer(server),
    ...(workspaceServices ?? [])
      .filter((w) => (w.server_id ?? "") === server)
      .map((w) => w.name),
  ];
  const mib = (mb: number) => (mb >= 1024 ? `${mb / 1024} GiB` : `${mb} MiB`);
  const memory = memMB ?? limit?.memory_mb;
  // The deploy's capacity check, ahead of the click: what the server has left.
  const fit = server ? fits[(servers ?? []).findIndex((s) => s.id === server)]?.data : undefined;
  const memLeft = fit?.capacity_known ? Math.max(fit.effective_memory_mb - fit.allocated_memory_mb, 0) : null;
  const noRoom = !composeLike && !!memory && memLeft !== null && memory > memLeft;
  // ponytail: on a share only an owner of the server's workspace may bypass, which
  // this form can't see, so it reads as no; the refusal's can_bypass still decides.
  const canBypass = !fit?.share && (role === "owner" || orgSettings?.resource_bypass_min_role === "developer");
  const memOptions = [...new Set([...MEMORY_MB, ...(limit ? [limit.memory_mb] : [])])].sort((a, b) => a - b);
  // One server for the environment: shown as the fact, with Change (D-154).
  const serverList = serverListOpen || new Set(envServers).size !== 1;
  const back = first ? "/dashboard" : projectId ? `/projects/${projectId}/services` : "/shared";
  const envName = env?.name ?? "this environment";
  const connectsIn = scope === "environment" ? (first ? "production" : envName) : scope === "project" ? (project?.name ?? "the project") : workspace;
  const guarded = protectedServed(scope, env, environments, first);
  const protectedEnv = guarded.length > 0;
  const repo = ownRepos.find((r) => r.id === repoChoice);
  const sources: { id: Source; title: string; desc: string; Icon: typeof GitBranch }[] = [
    {
      id: "git",
      title: ownRepos[0] ? "This repository" : "Repository",
      desc: `Build ${ownRepos[0]?.full_name ?? "a repository"} with its Dockerfile or Nixpacks`,
      Icon: GitBranch,
    },
    { id: "image", title: "Image", desc: "Run an image from a registry, private ones too", Icon: Box },
    { id: "compose", title: "Compose", desc: "Several containers from one compose file", Icon: Layers },
    { id: "catalog", title: "Catalog", desc: "A ready-made app, database or tool", Icon: LayoutGrid },
  ];
  const sourceText = isCatalog
    ? `Catalog · ${template?.name ?? ""}`
    : source === "git"
      ? repo
        ? `This repository · ${repo.full_name}`
        : `Repository · ${gitUrl.trim() || "a URL"}`
      : source === "image"
        ? "Image"
        : "Compose";
  const servesWeb = isCatalog || source === "compose" || type === "web";
  const shownPort = isCatalog ? (template?.default_port ?? 0) : port;
  const submit = (deploy: boolean) => {
    if (!valid || create.isPending) return;
    if (deploy && protectedEnv) setConfirm(true);
    else create.mutate({ deploy });
  };
  // After a swap unmounts the focused button: focus what replaced it.
  const focusSoon = (selector: string) =>
    requestAnimationFrame(() => document.querySelector<HTMLElement>(selector)?.focus());
  // Why Create is off, when the name isn't the reason.
  const missing = picking || nameTaken
    ? ""
    : hostFailed
      ? "load the panel host again"
    : serversFailed
      ? "load the servers again"
      : envsFailed
        ? "load the environments again"
    : noServer
      ? role === "owner" ? "add a server" : "ask an owner to add a server"
      : !nm
      ? "give it a name"
      : source === "git" && !repoChoice && !gitUrl.trim()
        ? "give the repository’s URL"
        : source === "image" && !image.trim()
          ? "give the image"
          : source === "compose" && composeMode === "repo" && !repoChoice && !composeRepo.trim()
            ? "give the compose file’s repository"
            : source === "compose" && composeMode === "inline" && !composeContent.trim()
              ? "paste the compose file"
              : isCatalog && config.some((c) => c.secret && !c.value)
                ? "give every secret a value"
                : "";
  const gitUrlFields = (
    <>
      <div className="flex flex-col gap-1.5">
        <label htmlFor="ns-repo" className={label}>
          Repository URL
        </label>
        <input
          id="ns-repo"
          type="url"
          required
          value={gitUrl}
          onChange={(e) => setGitUrl(e.target.value)}
          placeholder="https://github.com/you/app.git"
          className={cn(field, "font-mono text-[13px]")}
        />
      </div>
      <Credentials user={gitUser} token={gitToken} onUser={setGitUser} onToken={setGitToken} />
    </>
  );
  const buttons = (
    <>
      <button
        type="button"
        className={cn(primary, "h-10 flex-1 justify-center px-[18px] text-sm")}
        disabled={!valid || create.isPending}
        onClick={(e) => {
          deployButton.current = e.currentTarget;
          submit(true);
        }}
      >
        {create.isPending && create.variables?.deploy ? "Creating…" : "Create and deploy"}
      </button>
      <button
        type="button"
        className={cn(outline, "h-10 px-3.5 text-sm")}
        disabled={!valid || create.isPending}
        onClick={() => submit(false)}
      >
        {create.isPending && !create.variables?.deploy ? "Creating…" : "Create only"}
      </button>
    </>
  );

  return (
    <div className="flex w-full max-w-[1320px] flex-col gap-5 pb-24 sm:pb-14 md:-mt-3">
      <ServiceTypesGuide open={guide} onOpenChange={setGuide} />
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-0.5">
          <span className="text-xs text-muted">
            <Link href={back} className="hover:text-fg hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center">
              {first ? "Home" : projectId ? "Services" : "Workspace services"}
            </Link>{" "}
            / New
          </span>
          <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
            <h1 className="text-xl font-semibold leading-7 tracking-[-0.01em] text-fg">
              {first ? "Deploy an app" : projectId ? "New service" : "New workspace service"}
            </h1>
            {projectId && scope === "environment" && env && (
              <span className="flex items-center gap-2 text-[13px] text-muted">
                in <EnvChip env={env} size="md" />
              </span>
            )}
          </div>
          {first && (
            <p className="text-[13px] text-muted">
              It goes in a new project named after it, in its production environment.
            </p>
          )}
        </div>
        <button
          type="button"
          onClick={() => setGuide(true)}
          className="text-[13px] text-link hover:underline pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center"
        >
          Service types guide
        </button>
      </div>

      <div className="grid items-start gap-5 min-[1200px]:grid-cols-[minmax(0,1fr)_320px]">
        <form
          id="ns-form"
          aria-label={
            first
              ? "Deploy an app in a new project"
              : projectId
                ? `New service in ${project?.name ?? "the project"}`
                : "New workspace service"
          }
          className="flex min-w-0 flex-col gap-4"
          // Enter in a field does nothing: creating (and deploying) is a button's.
          onSubmit={(e) => e.preventDefault()}
        >
          <fieldset className={cn(card, "flex flex-col gap-3 p-4")}>
            <legend className={cn(h2, "float-left mb-0.5 w-full")}>
              Source
            </legend>
            <div className="grid grid-cols-2 gap-2.5 lg:grid-cols-4">
              {sources.map((o) => (
                <label key={o.id} className={cn(radioCard(source === o.id), "flex-col gap-1 focus-within:ring-2 focus-within:ring-focus")}>
                  <input
                    type="radio"
                    name="source"
                    checked={source === o.id}
                    onChange={() => {
                      setSource(o.id);
                      setViolations(null);
                    }}
                    className="sr-only"
                  />
                  <span className="flex items-center gap-[7px] text-sm font-medium text-fg">
                    <o.Icon className="h-[15px] w-[15px]" aria-hidden="true" />
                    {o.title}
                  </span>
                  <span className="text-xs leading-[17px] text-fg/70">{o.desc}</span>
                </label>
              ))}
            </div>
          </fieldset>

          {isCatalog && (
            <section aria-labelledby="cat-h" className={card}>
              <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5 border-b border-subtle px-4 py-3">
                <h2 id="cat-h" className={h2}>
                  Catalog
                </h2>
                <span className="text-xs text-muted">an image or a compose stack, settings filled in</span>
              </div>
              {!picking && template ? (
                <div className="flex items-center gap-3 p-4">
                  <CatalogMark t={template} />
                  <span className="flex min-w-0 flex-1 flex-col">
                    <span className="text-sm font-medium text-fg">{template.name}</span>
                    <span className={cn(mono, "text-muted")}>{pickedSub(template)}</span>
                  </span>
                  <button
                    type="button"
                    className={small}
                    aria-label={`Change the catalog entry, ${template.name} picked`}
                    onClick={() => setChoosing(true)}
                  >
                    Change
                  </button>
                </div>
              ) : (
                <div className="flex flex-col gap-3.5 p-4">
                  <Read query={catalog} what="the catalog">
                  {() => (
                  <>
                  <div role="group" aria-label="Category" className="flex flex-wrap gap-1.5">
                    {[["all", "All"], ...Object.entries(CATEGORIES)].map(([id, l]) => {
                      const n = (templates ?? []).filter((t) => id === "all" || t.category === id).length;
                      return (
                        <button
                          key={id}
                          type="button"
                          aria-pressed={category === id}
                          onClick={() => setCategory(id)}
                          className={cn(
                            "touch-target inline-flex h-[30px] items-center gap-1.5 rounded-full border px-3 text-[13px]",
                            category === id ? "border-link bg-link/10 text-fg" : "border-subtle text-fg/80 hover:text-fg",
                          )}
                        >
                          {l}
                          <span className="text-xs text-muted">{n}</span>
                        </button>
                      );
                    })}
                  </div>
                  <div role="group" aria-label="Catalog entries" className="grid grid-cols-[repeat(auto-fill,minmax(210px,1fr))] gap-2">
                    {shown.map((t) => (
                      <button
                        key={t.slug}
                        type="button"
                        aria-pressed={template?.slug === t.slug}
                        aria-label={`${t.name} ${catalogRuns(t)}`}
                        onClick={() => pickTemplate(t)}
                        className={cn(
                          "flex min-w-0 items-center gap-3 rounded-lg border px-3 py-2.5 text-left",
                          template?.slug === t.slug ? "border-link bg-link/10" : "border-subtle bg-ground/40 hover:border-strong",
                        )}
                      >
                        <CatalogMark t={t} />
                        <span className="flex min-w-0 flex-col">
                          <span className="text-[13px] font-medium text-fg">{t.name}</span>
                          <span className="truncate font-mono text-[11px] text-muted">{catalogRuns(t, false)}</span>
                        </span>
                      </button>
                    ))}
                  </div>
                  </>
                  )}
                  </Read>
                </div>
              )}
            </section>
          )}

          {!picking && (
          <section
            aria-labelledby="svc-h"
            className={card}
          >
            <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5 border-b border-subtle px-4 py-3">
              <h2 id="svc-h" className={h2}>
                {isCatalog
                  ? template?.name
                  : source === "git"
                    ? repo
                      ? "This repository"
                      : "Repository"
                    : source === "image"
                      ? "Image"
                      : "Compose"}
              </h2>
              {isCatalog && template && (
                <span className={cn(mono, "text-muted")}>{catalogRuns(template)}</span>
              )}
              {source === "git" && repo && (
                <span className={cn(mono, "text-muted")}>
                  {repo.full_name} · {branchName}
                </span>
              )}
            </div>
            <div className="flex flex-col gap-4 p-4">
            {source === "git" &&
              (ownRepos.length === 1 && repoChoice ? (
                <div className="flex flex-col gap-1.5">
                  <span className={label}>Repository</span>
                  <div className="flex min-h-9 flex-wrap items-center justify-between gap-x-3 gap-y-1.5">
                    <span className="font-mono text-[13px] text-fg">{ownRepos[0].full_name}</span>
                    <button
                      id="ns-use-url"
                      type="button"
                      className={small}
                      onClick={() => {
                        setRepoId("");
                        focusSoon("#ns-repo");
                      }}
                    >
                      Use a URL instead
                    </button>
                  </div>
                  <span className="text-xs text-muted">
                    This project’s repository. It builds with its Dockerfile, or with Nixpacks when it has none.
                  </span>
                </div>
              ) : ownRepos.length === 1 ? (
                <div className="flex flex-col gap-3">
                  {gitUrlFields}
                  <button
                    type="button"
                    id="ns-use-repo"
                    className={cn(small, "self-start")}
                    onClick={() => {
                      setRepoId(ownRepos[0].id);
                      focusSoon("#ns-use-url");
                    }}
                  >
                    Use {ownRepos[0].full_name} instead
                  </button>
                </div>
              ) : (
                <RepoSourceChoice repos={ownRepos} value={repoChoice} onChange={setRepoId} name="ns-repo-source">
                  {gitUrlFields}
                </RepoSourceChoice>
              ))}
            <div className="grid grid-cols-[repeat(auto-fit,minmax(240px,1fr))] gap-4">
              <div className="flex flex-col gap-1.5">
                <label htmlFor="ns-name" className={label}>
                  Name
                </label>
                <input
                  id="ns-name"
                  required
                  value={nm}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="my-app"
                  aria-invalid={nameTaken}
                  aria-describedby="ns-name-help"
                  className={cn(field, "font-mono text-[13px]", nameTaken && "border-danger")}
                />
                <span id="ns-name-help" className={cn("text-xs [overflow-wrap:anywhere]", nameTaken ? "text-status-failed-fg" : "text-muted")}>
                  {nameTaken
                    ? first
                      ? <>A project named {nm} already exists. {existing ? <><Link href={`/projects/${existing.id}`} className="inline-link pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center">Open it</Link>, or pick another name.</> : "Pick another name."}</>
                      : `${connectsIn} already has a service named ${nm}. Pick another name.`
                    : !composeLike && !(isCatalog && template) && servesWeb && nm
                      ? <>Other services in {connectsIn} connect to <span className="font-mono text-fg">{defaultNetworkAlias(nm, scope === "environment" ? (first ? "production" : (env?.name ?? "")) : "")}:{shownPort}</span>.</>
                      : ""}
                </span>
              </div>
              {(source === "git" || source === "image") && (
                <fieldset className="flex min-w-0 flex-col gap-1.5">
                  <legend className={cn(label, "float-left mb-1.5 w-full")}>
                    Type
                  </legend>
                  <div className="flex gap-0.5 rounded-lg border border-strong bg-ground p-0.5">
                    {(
                      [
                        { id: "web", label: "Web", hint: "port and domains" },
                        {
                          id: "worker",
                          label: "Worker",
                          hint: "no inbound traffic",
                        },
                      ] as const
                    ).map((t) => (
                      <label key={t.id} className={cn(segment(type === t.id), "touch-target")}>
                        <input
                          type="radio"
                          name="svc-type"
                          checked={type === t.id}
                          onChange={() => setType(t.id)}
                          className="sr-only"
                        />
                        {t.label}{" "}
                        <span className="font-normal text-muted max-sm:hidden">{t.hint}</span>
                      </label>
                    ))}
                  </div>
                </fieldset>
              )}
              {isCatalog && template && template.kind !== "compose" && (
                <div className="flex flex-col gap-1.5">
                  <label htmlFor="ns-alias" className={label}>
                    Internal hostname
                  </label>
                  <input
                    id="ns-alias"
                    value={alias}
                    onChange={(e) => setAlias(e.target.value)}
                    placeholder={template.network_alias}
                    aria-describedby="ns-alias-hint"
                    className={cn(field, "font-mono text-[13px]")}
                  />
                  <span id="ns-alias-hint" className="text-xs text-muted">
                    Other services in {connectsIn} connect to{" "}
                    <span className="font-mono text-fg">
                      {alias || template.network_alias}:{template.default_port}
                    </span>
                    {template.secondary_port
                      ? `, and its second port ${template.secondary_port}`
                      : ""}
                  </span>
                </div>
              )}
            </div>

            {source === "git" && (
              <div className="grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-4">
                <div className="flex flex-col gap-1.5">
                  <label htmlFor="ns-branch" className={label}>
                    Branch
                  </label>
                  <input
                    id="ns-branch"
                    value={branchValue}
                    onChange={(e) => setBranch(e.target.value)}
                    aria-describedby={repoChoice ? "ns-branch-help" : undefined}
                    className={cn(field, "font-mono text-[13px]")}
                  />
                  {repoChoice && (
                    <span id="ns-branch-help" className="text-xs text-muted">
                      It deploys from {branchName}; pushes deploy it again.
                    </span>
                  )}
                </div>
                <div className="flex flex-col gap-1.5">
                  <label htmlFor="ns-dir" className={label}>
                    Base directory{" "}
                    <span className="font-normal text-muted">optional</span>
                  </label>
                  <input
                    id="ns-dir"
                    value={baseDir}
                    onChange={(e) => setBaseDir(e.target.value)}
                    placeholder="/"
                    className={cn(field, "font-mono text-[13px]")}
                  />
                </div>
                {type === "web" && (
                  <PortField value={port} onChange={setPort} />
                )}
              </div>
            )}
            {source === "image" && (
              <>
                <div className="grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-4">
                  <div className="flex min-w-0 flex-col gap-1.5 sm:col-span-2">
                    <label htmlFor="ns-image" className={label}>
                      Image
                    </label>
                    <input
                      id="ns-image"
                      required
                      value={image}
                      onChange={(e) => setImage(e.target.value)}
                      placeholder="ghcr.io/you/app:1.0.0"
                      className={cn(field, "font-mono text-[13px]")}
                    />
                  </div>
                  {type === "web" && (
                    <PortField value={port} onChange={setPort} />
                  )}
                </div>
                <details className={sub}>
                  <summary className="cursor-pointer text-[13px] text-fg/80">
                    Registry credentials for a private image
                  </summary>
                  <div className="mt-2.5 grid grid-cols-[repeat(auto-fit,minmax(180px,1fr))] gap-3">
                    {(
                      [
                        {
                          k: "server",
                          l: "Registry",
                          ph: "ghcr.io",
                          type: "text",
                        },
                        { k: "user", l: "Username", ph: "", type: "text" },
                        {
                          k: "password",
                          l: "Password or token",
                          ph: "",
                          type: "password",
                        },
                      ] as const
                    ).map((f) => (
                      <label
                        key={f.k}
                        className={cn(label, "flex flex-col gap-1.5")}
                      >
                        {f.l}
                        <input
                          type={f.type}
                          value={registry[f.k]}
                          placeholder={f.ph}
                          autoComplete="off"
                          onChange={(e) =>
                            setRegistry((r) => ({
                              ...r,
                              [f.k]: e.target.value,
                            }))
                          }
                          className={cn(
                            field,
                            "h-[34px] font-mono text-[13px] font-normal",
                          )}
                        />
                      </label>
                    ))}
                  </div>
                </details>
              </>
            )}

            {source === "compose" && (
              <>
                <fieldset className="flex flex-col gap-2">
                  <legend className={cn(label, "float-left mb-1.5 w-full")}>
                    Compose file
                  </legend>
                  <div className="flex gap-0.5 self-start rounded-lg border border-strong bg-ground p-0.5">
                    {(
                      [
                        { id: "repo", label: "From a repository" },
                        { id: "inline", label: "Paste the file" },
                      ] as const
                    ).map((m) => (
                      <label
                        key={m.id}
                        className={cn(
                          segment(composeMode === m.id),
                          "flex-none",
                        )}
                      >
                        <input
                          type="radio"
                          name="compose-mode"
                          checked={composeMode === m.id}
                          onChange={() => setComposeMode(m.id)}
                          className="sr-only"
                        />
                        {m.label}
                      </label>
                    ))}
                  </div>
                </fieldset>
                {composeMode === "repo" ? (
                  <>
                    <RepoSourceChoice repos={ownRepos} value={repoChoice} onChange={setRepoId} name="ns-compose-source">
                      <label className={cn(label, "flex flex-col gap-1.5")}>
                        Repository URL
                        <input
                          type="url"
                          required
                          value={composeRepo}
                          onChange={(e) => setComposeRepo(e.target.value)}
                          placeholder="https://github.com/you/infra.git"
                          className={cn(
                            field,
                            "font-mono text-[13px] font-normal",
                          )}
                        />
                      </label>
                      <Credentials
                        user={gitUser}
                        token={gitToken}
                        onUser={setGitUser}
                        onToken={setGitToken}
                      />
                    </RepoSourceChoice>
                    <div className="grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-4">
                      <label className={cn(label, "flex flex-col gap-1.5")}>
                        Branch
                        <input
                          value={branchValue}
                          onChange={(e) => setBranch(e.target.value)}
                          className={cn(
                            field,
                            "font-mono text-[13px] font-normal",
                          )}
                        />
                      </label>
                      <label className={cn(label, "flex flex-col gap-1.5")}>
                        Path in the repository
                        <input
                          value={composePath}
                          onChange={(e) => setComposePath(e.target.value)}
                          placeholder="docker-compose.yml"
                          className={cn(
                            field,
                            "font-mono text-[13px] font-normal",
                          )}
                        />
                      </label>
                    </div>
                  </>
                ) : (
                  <label className={cn(label, "flex flex-col gap-1.5")}>
                    docker-compose.yml
                    <textarea
                      rows={8}
                      required
                      value={composeContent}
                      onChange={(e) => setComposeContent(e.target.value)}
                      placeholder={
                        "services:\n  app:\n    image: ghcr.io/you/app:1.0.0\n    mem_limit: 512m"
                      }
                      className="resize-y rounded-lg border border-strong bg-ground px-2.5 py-2 font-mono text-xs font-normal leading-[18px] text-fg placeholder:text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus"
                    />
                  </label>
                )}
                <ul className="flex list-disc flex-col gap-1 pl-[18px] text-xs leading-[18px] text-fg/70">
                  <li>
                    Domains go to one compose service and port, chosen in
                    Settings → Domain routing after creating. With a single
                    service it is automatic.
                  </li>
                  <li>
                    The file can&apos;t set{" "}
                    <span className="font-mono text-fg">traefik.*</span>,{" "}
                    <span className="font-mono text-fg">paas.*</span> or{" "}
                    <span className="font-mono text-fg">
                      com.docker.compose.*
                    </span>{" "}
                    labels; the deploy policy refuses them. Variables you set
                    after creating reach it as a{" "}
                    <span className="font-mono text-fg">.env</span> file.
                  </li>
                </ul>
              </>
            )}

            {isCatalog && (config.length > 0 || tempLogin) && (
              <fieldset className="flex flex-col gap-3">
                <legend className="sr-only">Settings</legend>
                <div className="grid gap-x-4 gap-y-3 sm:grid-cols-2">
                  {config.map((c, i) => (
                    <div key={c.key} className={cn("flex min-w-0 flex-col gap-1.5", c.secret && "col-span-full")}>
                      <label htmlFor={`ns-cfg-${i}`} className="font-mono text-[13px] font-medium text-fg [overflow-wrap:anywhere]">
                        {c.key}
                      </label>
                      <span className="flex gap-2">
                        <input
                          id={`ns-cfg-${i}`}
                          type={c.secret ? "password" : "text"}
                          value={c.value}
                          placeholder={c.secret ? "Choose a value" : c.hint}
                          autoComplete="off"
                          spellCheck={false}
                          aria-describedby={c.secret ? `ns-cfg-${i}-help` : undefined}
                          onChange={(e) =>
                            setConfig((cs) =>
                              cs.map((x, j) =>
                                j === i ? { ...x, value: e.target.value, generated: false } : x,
                              ),
                            )
                          }
                          className={cn(field, "min-w-0 flex-1 font-mono text-[13px]")}
                        />
                        {c.secret && c.value && (
                          <button
                            type="button"
                            aria-label={`Copy ${c.key}`}
                            className={cn(outline, "px-2.5")}
                            onClick={() =>
                              navigator.clipboard.writeText(c.value).then(
                                () => toast.success(`${c.key} copied`),
                                () => toast.error("Couldn't copy"),
                              )
                            }
                          >
                            <Copy className="h-3.5 w-3.5" aria-hidden="true" />
                          </button>
                        )}
                        {c.secret && (
                          <button
                            type="button"
                            className={outline}
                            onClick={() =>
                              setConfig((cs) =>
                                cs.map((x, j) => (j === i ? { ...x, value: generatedSecret(x.generate), generated: true } : x)),
                              )
                            }
                          >
                            Generate again
                          </button>
                        )}
                      </span>
                      {c.secret && (
                        <span id={`ns-cfg-${i}-help`} className="inline-flex items-start gap-1.5 text-xs leading-[18px] text-muted">
                          <Lock className="mt-0.5 h-3 w-3 flex-none" aria-hidden="true" />
                          <span>
                            {c.generated ? `Generated, ${c.value.length} characters. ` : ""}Saved as a secret: encrypted
                            and write-once, so copy it now if you need it.
                            {config.some((x) => !x.secret) ? " The others are saved as variables." : ""}
                          </span>
                        </span>
                      )}
                    </div>
                  ))}
                  {tempLogin && (
                    <div className="col-span-full flex min-w-0 flex-col gap-1.5">
                      <label htmlFor="ns-temp-login" className="text-[13px] font-medium text-fg">
                        Temporary login
                      </label>
                      <span className="flex gap-2">
                        <input
                          id="ns-temp-login"
                          type="password"
                          value={tempLogin}
                          readOnly
                          autoComplete="off"
                          aria-describedby="ns-temp-login-help"
                          className={cn(field, "min-w-0 flex-1 font-mono text-[13px]")}
                        />
                        <button
                          type="button"
                          aria-label="Copy the temporary login’s password"
                          className={cn(outline, "px-2.5 pointer-coarse:min-w-11")}
                          onClick={() =>
                            navigator.clipboard.writeText(tempLogin).then(
                              () => toast.success("Temporary login’s password copied"),
                              () => toast.error("Couldn't copy"),
                            )
                          }
                        >
                          <Copy className="h-3.5 w-3.5" aria-hidden="true" />
                        </button>
                        <button type="button" className={outline} onClick={() => setTempLogin(generatedSecret())}>
                          Generate again
                        </button>
                      </span>
                      <span id="ns-temp-login-help" className="inline-flex items-start gap-1.5 text-xs leading-[18px] text-muted">
                        <Lock className="mt-0.5 h-3 w-3 flex-none" aria-hidden="true" />
                        <span>
                          User <span className="font-mono text-fg">{TEMP_LOGIN_USER}</span>, generated,{" "}
                          {tempLogin.length} characters. Whoever opens {template?.name ?? "it"} first becomes its admin:
                          this login keeps others out until you’ve set up that account, then turn it off in Settings →
                          Access. Copy it now: it isn’t shown again.
                        </span>
                      </span>
                    </div>
                  )}
                </div>
              </fieldset>
            )}

            {isCatalog && template && (template.kind === "compose" || template.volumes[0]) && (
              <p className="flex gap-2 rounded-lg border border-subtle px-3 py-2.5 text-[13px] text-fg/80">
                <HardDrive className="mt-0.5 h-4 w-4 flex-none text-muted" aria-hidden="true" />
                <span>
                  {template.kind === "compose" ? (
                    <>
                      Its compose file sets its image, volumes and memory limits. Edit it on the service’s page after
                      it’s created.
                    </>
                  ) : (
                    <>
                      Its data lives on the volume{template.volumes.length === 1 ? "" : "s"}{" "}
                      {template.volumes.map((v, i) => (
                        <span key={v.name}>
                          {i > 0 ? ", " : ""}
                          <span className="font-mono text-fg">{v.name}</span>, mounted at{" "}
                          <span className="font-mono text-fg">{v.target}</span>
                        </span>
                      ))}
                      . Deleting the service deletes its data with it.
                    </>
                  )}
                </span>
              </p>
            )}
            </div>
          </section>
          )}

          {!picking && (
          <section
            aria-labelledby="where-h"
            className={card}
          >
            <div className="border-b border-subtle px-4 py-3">
              <h2 id="where-h" className={h2}>
                Where it runs
              </h2>
            </div>
            <div className="flex flex-col gap-4 p-4">
            {envsFailed && <LoadFailed what="the environments" onRetry={() => void environmentsRead.refetch()} />}
            {projectId ? (
              <fieldset className="flex flex-col gap-1.5">
                <legend className={cn(label, "float-left mb-1.5 w-full")}>
                  Scope
                </legend>
                <div className="flex flex-col gap-2">
                  {(
                    [
                      {
                        id: "environment",
                        label: `${envName} only`,
                        desc: envCounts
                          ? `Inherits ${envName}’s ${envCounts.variables} ${envCounts.variables === 1 ? "variable" : "variables"} and ${envCounts.secrets} ${envCounts.secrets === 1 ? "secret" : "secrets"}`
                          : "",
                      },
                      {
                        id: "project",
                        label: "Every environment, as a project service",
                        desc: `Runs once; ${new Intl.ListFormat("en", { type: "conjunction" }).format((environments ?? []).map((e) => e.name)) || "every environment"} ${(environments?.length ?? 0) === 1 ? "reaches" : "reach"} it by alias`,
                      },
                    ] as const
                  ).map((c) => (
                    <label
                      key={c.id}
                      className={cn(
                        radioCard(scope === c.id),
                        "min-h-11 items-center rounded-lg px-3 py-2",
                      )}
                    >
                      <input
                        type="radio"
                        name="scope"
                        checked={scope === c.id}
                        onChange={() => setScope(c.id)}
                        className="h-4 w-4 flex-none accent-[var(--link)]"
                      />
                      <span className="flex min-w-0 flex-col">
                        <span className="text-sm font-medium text-fg">
                          {c.label}
                        </span>
                        {c.desc && (
                          <span className="text-xs leading-[17px] text-fg/70">
                            {c.desc}
                          </span>
                        )}
                      </span>
                    </label>
                  ))}
                </div>
                {scope === "org" && (
                  <p className="text-xs text-fg/70">
                    As a workspace service: every project in {workspace} reaches it by alias, from services on the
                    same server. Pick one above to keep it in {project?.name ?? "this project"}.
                  </p>
                )}
              </fieldset>
            ) : first ? (
              <p className="text-[13px] text-fg/70">
                In a new project,{" "}
                <span className="font-medium text-fg">{nm || "named after it"}</span>, in its{" "}
                <span className={mono}>production</span> environment. Rename the project any time.
              </p>
            ) : (
              <p className="text-[13px] text-fg/70">
                Scope <span className="font-medium text-fg">Workspace</span>:
                every project in {workspace} reaches it by alias, from services
                on the same server.
              </p>
            )}
            <div className="grid grid-cols-[repeat(auto-fit,minmax(240px,1fr))] gap-4">
            {hostFailed ? (
              <div className="col-span-full">
                <LoadFailed what="the panel host" onRetry={() => void hostRead.refetch()} />
              </div>
            ) : hostPending ? null : serversFailed ? (
              <div className="col-span-full">
                <LoadFailed what="the servers" onRetry={() => void serversRead.refetch()} />
              </div>
            ) : noServer ? (
              <div className="col-span-full flex flex-col gap-1.5">
                <span className={label}>Server</span>
                <p className="text-[13px] text-fg/70">
                  Toft’s own server runs only Toft, so the service needs a server of its own.{" "}
                  {role === "owner" ? (
                    <Link href="/servers/new" className="inline-link">
                      Add a server
                    </Link>
                  ) : (
                    `Owners of ${workspace} add servers.`
                  )}
                </p>
              </div>
            ) : serverList ? (
            <fieldset className="col-span-full flex flex-col gap-2">
              <legend className={cn(label, "float-left mb-1.5 w-full")}>
                Server
              </legend>
              {(servers ?? []).map((s, i) => {
                const runs = onServer(s.id);
                const free = freeText(fits[i]?.data);
                return (
                  <label
                    key={s.id}
                    className={cn(
                      radioCard(server === s.id),
                      "min-h-11 flex-wrap items-center gap-x-3 gap-y-1.5 rounded-lg px-3 py-1.5",
                    )}
                  >
                    <input
                      type="radio"
                      name="server"
                      checked={server === s.id}
                      onChange={() => setServerId(s.id)}
                      className="h-4 w-4 accent-[var(--link)]"
                    />
                    <span className="font-mono text-[13px] text-fg">
                      {s.name}
                    </span>
                    <span className={cn(mono, "text-muted")}>
                      {s.shared ? `shared by ${s.shared_by}` : s.ip_address}
                    </span>
                    <span className="flex-[1_1_160px] text-xs text-fg/70">
                      {runs.length ? `Runs ${runs.join(" and ")}` : ""}
                    </span>
                    <span
                      className={cn(
                        mono,
                        fits[i]?.data && !fits[i]?.data?.fits
                          ? "text-status-pending-fg"
                          : "text-fg/80",
                      )}
                    >
                      {free}
                    </span>
                  </label>
                );
              })}
              {panelApps && (
              <label
                className={cn(
                  radioCard(server === ""),
                  "min-h-11 flex-wrap items-center gap-x-3 gap-y-1.5 rounded-lg px-3 py-1.5",
                )}
              >
                <input
                  type="radio"
                  name="server"
                  checked={server === ""}
                  onChange={() => setServerId("")}
                  className="h-4 w-4 accent-[var(--link)]"
                />
                <span className="font-mono text-[13px] text-fg">
                  panel host
                </span>
                <span className="flex-[1_1_160px] text-xs text-fg/70">
                  This panel&apos;s server, beside the control plane.
                  {legacy && !operator
                    ? " Under the legacy app network only instance operators deploy here."
                    : ""}
                  {onServer("").length
                    ? ` Runs ${onServer("").join(" and ")}.`
                    : ""}
                </span>
              </label>
              )}
              <span className="text-xs text-muted">
                A service reaches other services by alias only on the same
                server.
                {reachable.length
                  ? ` ${reachable.join(", ")} run on ${serverLabel(server)}.`
                  : ` Nothing of ${project?.name ?? workspace} runs on ${serverLabel(server)} yet.`}
              </span>
            </fieldset>
            ) : (
              <div className="flex flex-col gap-1.5">
                <span className={label}>Server</span>
                <div className="flex min-h-9 items-center justify-between gap-3">
                  <span className="font-mono text-[13px] text-fg">{server ? serverLabel(server) : "panel host"}</span>
                  <button
                    type="button"
                    className={small}
                    aria-label={`Change the server, ${server ? serverLabel(server) : "panel host"}`}
                    onClick={() => {
                      setServerListOpen(true);
                      focusSoon('input[name="server"]:checked');
                    }}
                  >
                    Change
                  </button>
                </div>
                <span className="text-xs text-muted">
                  Where {envName} runs; aliases work only on one server
                </span>
              </div>
            )}
            <div className="flex flex-col gap-1.5">
              {composeLike ? (
                <>
                  <span className={label}>Memory limit</span>
                  <span className="text-xs leading-[18px] text-fg/70">
                    {isCatalog
                      ? "Set for each service in the catalog’s compose file"
                      : "The default for its kind, for each compose service without a limit in the file"}
                  </span>
                </>
              ) : (
                <>
                  <label htmlFor="ns-mem" className={label}>
                    Memory limit
                  </label>
                  <select
                    id="ns-mem"
                    value={memory ?? ""}
                    disabled={!limit}
                    onChange={(e) => setMemMB(Number(e.target.value))}
                    aria-describedby="ns-mem-help"
                    className={field}
                  >
                    {!limit && <option value="">{limitFailed ? "Its kind’s default" : "…"}</option>}
                    {limit &&
                      memOptions.map((mb) => (
                        <option key={mb} value={mb}>
                          {mib(mb)}
                          {mb === limit.memory_mb ? ` · the default for ${KIND_WORDS[limit.kind] ?? "an app"}` : ""}
                        </option>
                      ))}
                  </select>
                  <span id="ns-mem-help" className="text-xs text-muted">
                    After a day of use the panel suggests a better one
                  </span>
                  {limitFailed && <LoadFailed what="the default memory limit" onRetry={() => void limitRead.refetch()} />}
                </>
              )}
            </div>
            </div>
            </div>
          </section>
          )}

          {violations && (
            <PolicyViolationList
              violations={violations}
              title="Not created: refused by the deploy policy"
            />
          )}
        </form>

        <aside
          aria-labelledby="sum-h"
          className={cn(card, "min-[1200px]:sticky min-[1200px]:top-4")}
        >
          <div className="border-b border-subtle px-4 py-3">
            <h2 id="sum-h" className={h2}>
              Summary
            </h2>
          </div>
          {picking ? (
            <p className="p-4 text-[13px] leading-5 text-fg/70">
              Pick a catalog entry. Its image or compose file, port and settings fill in, and you can change them
              before it starts.
            </p>
          ) : (
            <div className="flex flex-col gap-3 p-4">
              <dl>
                <SumRow k="Source">{sourceText}</SumRow>
                <SumRow k="Name">{nm || "not named yet"}</SumRow>
                {isCatalog && template ? (
                  template.kind === "compose" ? (
                    <SumRow k="Runs">{catalogRuns(template)}</SumRow>
                  ) : (
                    <SumRow k="Image">{template.docker_image}</SumRow>
                  )
                ) : source === "git" ? (
                  <SumRow k="Builds">{branchName} · Dockerfile or Nixpacks</SumRow>
                ) : source === "image" ? (
                  <SumRow k="Image">{image.trim() || "not given yet"}</SumRow>
                ) : (
                  <SumRow k="Runs">Compose</SumRow>
                )}
                {servesWeb && shownPort > 0 && (
                  <SumRow k="Port">
                    {shownPort}
                    {address?.reason === "not_web" ? ", internal only" : ""}
                  </SumRow>
                )}
              </dl>
              {addressAsk && (
                <div
                  aria-live="polite"
                  aria-busy={addressSettling}
                  className={cn("flex flex-col gap-1.5 border-t border-subtle pt-3", addressSettling && address && "opacity-60")}
                >
                  {addressFailed && !addressSettling ? (
                    <span className="text-[13px] text-fg/70">
                      Couldn’t find its address. It gets none; add a domain on its Domains tab after creating it.
                    </span>
                  ) : !address ? (
                    <span className="text-[13px] text-muted">Finding its address…</span>
                  ) : address.hostname ? (
                    <>
                      <label className="flex items-start gap-2.5 text-[13px] pointer-coarse:min-h-11 pointer-coarse:items-center">
                        <input
                          type="checkbox"
                          checked={giveAddress}
                          onChange={(e) => setGiveAddress(e.target.checked)}
                          className="m-0 mt-0.5 h-4 w-4 accent-accent"
                        />
                        <span>Give it an address now</span>
                      </label>
                      <span className={cn("font-mono text-xs [overflow-wrap:anywhere]", giveAddress ? "text-fg" : "text-muted line-through")}>
                        https://{address.hostname}
                      </span>
                      {address.temporary && giveAddress && (
                        <>
                          <TemporaryChip />
                          <span className="text-xs text-fg/70">{TEMPORARY_NOTE}</span>
                        </>
                      )}
                      {!giveAddress && (
                        <span className="text-xs text-muted">No address: add a domain on its Domains tab when you want one.</span>
                      )}
                    </>
                  ) : (
                    <div className="flex justify-between gap-3 text-[13px]">
                      <span className="text-muted">Address</span>
                      <span className="text-right text-fg/80">
                        {address.reason === "no_public_address"
                          ? `None: ${serverLabel(server)} has no public address`
                          : !servesWeb
                            ? "None: a worker takes no traffic"
                            : `None: ${limit && limit.kind !== "app" ? KIND_WORDS[limit.kind] : "it"} stays internal`}
                      </span>
                    </div>
                  )}
                </div>
              )}
              <dl className="border-t border-subtle pt-1">
                {isCatalog && template?.volumes[0] && (
                  <SumRow k="Volume">{template.volumes.map((v) => v.name).join(", ")}</SumRow>
                )}
                <SumRow k="Environment">
                  {first ? (
                    "production, new project"
                  ) : scope === "environment" && env ? (
                    <EnvChip env={env} />
                  ) : scope === "project" ? (
                    "every environment"
                  ) : (
                    "workspace"
                  )}
                </SumRow>
                <SumRow k="Server">{hostFailed || serversFailed ? "—" : hostPending ? "…" : noServer ? "none yet" : server ? serverLabel(server) : "panel host"}</SumRow>
                <SumRow k="Memory">{composeLike ? "Set in its compose file" : memory ? mib(memory) : limitFailed ? "Its kind’s default" : "…"}</SumRow>
              </dl>
              {noRoom && fit ? (
                <p className="flex items-start gap-2 text-xs leading-[18px] text-status-pending-fg">
                  <AlertTriangle role="img" aria-label="Warning" className="mt-0.5 h-3.5 w-3.5 flex-none text-status-pending" />
                  {`It needs ${mib(memory)} and ${serverLabel(server)} has ${formatMemory(memLeft)} left of ${formatMemory(fit.effective_memory_mb)}: Create and deploy ${canBypass ? "asks before creating it" : "won’t create it"}. Lower its memory or pick another server.`}
                </p>
              ) : !noServer && !hostPending && !hostFailed && !serversFailed && <p className="text-xs leading-[18px] text-fg/70">
                {isCatalog
                  ? `Create and deploy starts it on ${server ? serverLabel(server) : "the panel host"} as soon as it’s created; everything it needs is filled in. Create only saves it, and you start it from its page.`
                  : `Create and deploy ${source === "git" ? `builds ${branchName} and starts it` : "starts it"} on ${server ? serverLabel(server) : "the panel host"} once it’s created. If it needs variables, use Create only and set them first.`}
              </p>}
              {protectedEnv && (
                <p className="text-xs leading-[18px] text-fg/70">
                  {new Intl.ListFormat("en", { type: "conjunction" }).format(guarded.map((e) => e.name))}{" "}
                  {guarded.length === 1 ? "is" : "are"} protected: Create and deploy asks you to confirm first.
                </p>
              )}
              <div className="flex gap-2 max-sm:hidden">{buttons}</div>
              {missing && <p className="text-xs text-muted">To create it, {missing}.</p>}
              <Link href={back} className="self-start text-[13px] text-link hover:underline max-sm:hidden">
                Cancel
              </Link>
            </div>
          )}
        </aside>
      </div>

      {!picking && (
        <div
          role="group"
          aria-label={`Create ${nm || "the service"}`}
          className="fixed inset-x-0 bottom-0 z-30 flex items-center gap-2 border-t border-subtle bg-surface px-4 pt-3 pb-[calc(12px+env(safe-area-inset-bottom))] sm:hidden"
        >
          {buttons}
          <Link href={back} aria-label="Cancel" className={cn(outline, "h-10 w-10 flex-none justify-center px-0")}>
            <X className="h-[18px] w-[18px]" aria-hidden="true" />
          </Link>
        </div>
      )}

      {protectedEnv && (
        <NewServiceConfirm
          open={confirm}
          name={nm}
          envs={guarded}
          projectService={scope === "project"}
          server={server ? serverLabel(server) : "the panel host"}
          deploys={
            isCatalog && template
              ? `Catalog · ${template.name}`
              : source === "git"
                ? `${repo ? repo.full_name : gitUrl.trim()} at ${branchName}`
                : source === "image"
                  ? image.trim()
                  : "Compose"
          }
          deploysImage={isCatalog && template ? template.docker_image : ""}
          address={addressAsk && giveAddress && address?.hostname ? { hostname: address.hostname, temporary: !!address.temporary } : null}
          counts={scope === "environment" ? (envCounts ?? null) : null}
          pending={create.isPending}
          onCancel={() => setConfirm(false)}
          onConfirm={() => create.mutate({ deploy: true })}
        />
      )}
      <CapacityDialog
        warning={room}
        serviceName={nm}
        create
        returnFocus={deployButton}
        onCancel={() => setRoom(null)}
        onDeployAnyway={() => {
          setRoom(null);
          create.mutate({ deploy: true, force: true });
        }}
      />
    </div>
  );
}

const list = (names: string[]) => new Intl.ListFormat("en", { type: "conjunction" }).format(names);

/** A row of the Summary: what, then its value. */
function SumRow({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <div className="flex justify-between gap-3 border-t border-subtle py-2 text-[13px] first:border-t-0 first:pt-0">
      <dt className="flex-none text-muted">{k}</dt>
      <dd className="min-w-0 text-right font-mono text-xs leading-5 text-fg [overflow-wrap:anywhere]">{children}</dd>
    </div>
  );
}

/** Create and deploy in a protected environment confirms first (D-143,
 *  D-154): what deploys, its address, and that nothing else changes. On a
 *  phone it is a full-screen sheet; Cancel has the focus. */
function NewServiceConfirm({
  open,
  name,
  envs,
  projectService,
  server,
  deploys,
  deploysImage,
  address,
  counts,
  pending,
  onCancel,
  onConfirm,
}: {
  open: boolean;
  name: string;
  envs: { id: string; name: string; color: EnvColor; protected: boolean }[];
  /** A project service runs once for every environment of the project. */
  projectService: boolean;
  server: string;
  deploys: string;
  deploysImage: string;
  address: { hostname: string; temporary: boolean } | null;
  counts: { variables: number; secrets: number } | null;
  pending: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <ProtectedConfirm
      open={open}
      title={
        <>
          Deploy {name} to{" "}
          {envs.map((e, i) => (
            <span key={e.id} className="inline-flex items-center">
              <EnvChip env={e} size="md" />
              {i === envs.length - 1 ? "?" : i === envs.length - 2 ? " and" : ","}
            </span>
          ))}
        </>
      }
      description={
        projectService
          ? `A project service on ${server}: it runs once and serves every environment; ${list(envs.map((e) => e.name))} ${envs.length === 1 ? "is" : "are"} protected.`
          : `A protected environment: a new service on ${server}.`
      }
      action={`Create and deploy to ${list(envs.map((e) => e.name))}`}
      pending={pending}
      pendingAction="Creating…"
      onCancel={onCancel}
      onConfirm={onConfirm}
    >
      <dl className="grid grid-cols-[84px_minmax(0,1fr)] gap-x-3 gap-y-2 text-[13px]">
        <dt className="text-muted">Live now</dt>
        <dd className="text-muted">Nothing: it’s new</dd>
        <dt className="text-muted">Deploys</dt>
        <dd className="text-fg [overflow-wrap:anywhere]">
          {deploys}
          {deploysImage && <span className="ml-1.5 font-mono text-xs text-muted">{deploysImage}</span>}
        </dd>
        {address && (
          <>
            <dt className="text-muted">Address</dt>
            <dd className="flex flex-wrap items-center gap-2">
              <span className="font-mono text-xs text-fg [overflow-wrap:anywhere]">https://{address.hostname}</span>
              {address.temporary && <TemporaryChip />}
            </dd>
          </>
        )}
      </dl>
      <div className="flex flex-col gap-1">
        <span className="text-[13px] font-medium text-fg">Also goes live</span>
        <p className="text-xs leading-[18px] text-fg/70">
          Nothing else: the services already running keep running as they are.
          {counts && counts.variables + counts.secrets > 0
            ? ` It starts with ${envs[0].name}’s ${counts.variables} ${counts.variables === 1 ? "variable" : "variables"} and ${counts.secrets} ${counts.secrets === 1 ? "secret" : "secrets"}.`
            : ""}
        </p>
      </div>
      <p className="flex gap-2 rounded-lg border border-subtle px-3 py-2.5 text-[13px] text-fg/80">
        <RotateCcw className="mt-0.5 h-4 w-4 flex-none text-muted" aria-hidden="true" />
        <span>If its health check fails, {name} stays created with nothing live; fix it on its page and deploy again.</span>
      </p>
    </ProtectedConfirm>
  );
}

function PortField({
  value,
  onChange,
}: {
  value: number;
  onChange: (n: number) => void;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor="ns-port" className={label}>
        Port
      </label>
      <input
        id="ns-port"
        type="number"
        min={1}
        max={65535}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        aria-describedby="ns-port-hint"
        className={cn(field, "font-mono text-[13px]")}
      />
      <span id="ns-port-hint" className="text-xs text-muted">
        The port the app listens on in its container
      </span>
    </div>
  );
}

function Credentials({
  user,
  token,
  onUser,
  onToken,
}: {
  user: string;
  token: string;
  onUser: (v: string) => void;
  onToken: (v: string) => void;
}) {
  return (
    <details className={sub}>
      <summary className="cursor-pointer text-[13px] text-fg/80 pointer-coarse:py-3">
        Credentials for a private repository
      </summary>
      <div className="mt-2.5 grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-3">
        <label className={cn(label, "flex flex-col gap-1.5")}>
          <span>
            Username <span className="font-normal text-muted">optional</span>
          </span>
          <input
            value={user}
            onChange={(e) => onUser(e.target.value)}
            autoComplete="off"
            className={cn(field, "h-[34px] font-mono text-[13px] font-normal")}
          />
        </label>
        <label className={cn(label, "flex flex-col gap-1.5")}>
          Access token
          <input
            type="password"
            value={token}
            onChange={(e) => onToken(e.target.value)}
            autoComplete="new-password"
            className={cn(field, "h-[34px] font-mono text-[13px] font-normal")}
          />
        </label>
      </div>
      <p className="mt-2 text-xs text-muted">
        GitHub: leave the username empty and paste a personal access token.
        GitLab and Gitea: your username and a token. Stored encrypted; never
        shown again.
      </p>
    </details>
  );
}
