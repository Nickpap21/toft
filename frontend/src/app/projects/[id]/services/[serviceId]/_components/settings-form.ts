import type {
  DeployStrategy,
  Service,
  UpdateServiceConfigInput,
} from "@/lib/api/service.service";

// The Settings tab's form (board: private/design/canvas/ReworkServiceSettings.dc.html):
// one set of fields, saved a card at a time.

export type ConfigForm = {
  name: string;
  git_repo_url: string;
  git_branch: string;
  /** One of the project's repositories, "" for the URLs (D-77). */
  repository_id: string;
  deploy_on_push: boolean;
  base_directory: string;
  docker_image: string;
  install_command: string;
  build_command: string;
  start_command: string;
  build_pack: string;
  port: number;
  cpu_limit: string;
  memory_limit: string;
  compose_content: string;
  compose_repo_url: string;
  compose_file_path: string;
  compose_route_service: string;
  deploy_strategy: DeployStrategy;
  /** The deploy time limit in minutes as typed; "" is the default (D-125). */
  deploy_timeout_minutes: string;
  /** How many live deployments keep their image, as typed; "" is the default (D-135). */
  rollback_images_kept: string;
  /** The process limit as typed; DEFAULT_PIDS_LIMIT stands for the default. */
  pids_limit: string;
  /** Its own isolation level, or "default": the workspace's (D-95). */
  isolation: "default" | "standard" | "sandboxed" | "sandboxed_strict";
  registry_server: string;
  registry_username: string;
  registry_password: string;
  git_username: string;
  git_token: string;
  pre_deploy_commands: string;
  post_deploy_commands: string;
  health_check_path: string;
  health_check_timeout: number;
  health_check_retries: number;
  /** The Docker check its containers get: the image's, none, or a command. */
  container_health: "image" | "off" | "command";
  container_health_command: string;
  required_env_vars: string;
  migrate_command: string;
  network_alias: string;
  dependencies: string[];
  volumes: {
    name: string;
    target: string;
    host_path?: string;
    read_only?: boolean;
  }[];
  basicAuthUser: string;
  basicAuthPassword: string;
  ipAllowlist: string;
};

export type PostDeployCmdRow = { container: string; command: string };

export function serializePostDeployCmds(rows: PostDeployCmdRow[]): string {
  return rows
    .filter((r) => r.command.trim())
    .map((r) => `${r.container}:${r.command}`)
    .join("\n");
}

export function parsePostDeployCmds(raw: string): PostDeployCmdRow[] {
  return (raw ?? "")
    .split("\n")
    .filter(Boolean)
    .map((line) => {
      const idx = line.indexOf(":");
      return idx >= 0
        ? {
            container: line.slice(0, idx).trim(),
            command: line.slice(idx + 1).trim(),
          }
        : { container: "", command: line.trim() };
    });
}

/** The platform's process limit (deployment.DefaultPidsLimit, D-93). */
export const DEFAULT_PIDS_LIMIT = 2048;

/** Why a typed process limit can't be saved; "" when it can. */
export function pidsLimitProblem(v: string): string {
  const n = Number(v.trim());
  return /^\d+$/.test(v.trim()) && n >= 256 && n <= 32768 ? "" : "The process limit is a whole number from 256 to 32768.";
}

/** The deploy time limit when a service sets none (deployment.DefaultTimeoutMinutes, D-125). */
export const DEFAULT_DEPLOY_TIMEOUT = 60;

/** Why a typed deploy time limit can't be saved; "" when it can. */
export function deployTimeoutProblem(v: string): string {
  const n = Number(v.trim());
  return !v.trim() || (/^\d+$/.test(v.trim()) && n >= 5 && n <= 240)
    ? ""
    : "The deploy time limit is a whole number of minutes from 5 to 240.";
}

/** The rollback images a service keeps when it sets none (disk.DefaultRollbackKept, D-135). */
export const DEFAULT_ROLLBACK_KEPT = 10;

/** Why a typed rollback image count can't be saved; "" when it can. */
export function rollbackKeptProblem(v: string): string {
  const n = Number(v.trim());
  return !v.trim() || (/^\d+$/.test(v.trim()) && n >= 1 && n <= 100)
    ? ""
    : "Rollback images kept is a whole number from 1 to 100.";
}

/** Why a container check can't be saved; "" when it can (service.validateContainerHealth). */
export function containerCheckProblem(mode: string, command: string): string {
  if (mode !== "command") return "";
  if (!command.trim()) return "The container check needs a command.";
  return /[\r\n\0]/.test(command) ? "The container check command is one line." : "";
}

export function initForm(service: Service): ConfigForm {
  return {
    name: service.name ?? "",
    git_repo_url: service.git_repo_url ?? "",
    git_branch: service.git_branch ?? "main",
    repository_id: service.repository_id ?? "",
    deploy_on_push: service.deploy_on_push ?? true,
    base_directory: service.base_directory ?? "",
    docker_image: service.docker_image ?? "",
    install_command: service.install_command ?? "",
    build_command: service.build_command ?? "",
    start_command: service.start_command ?? "",
    // The deploy builds with Nixpacks only for "nixpacks"; "auto" and ""
    // build the Dockerfile.
    build_pack: service.build_pack === "nixpacks" ? "nixpacks" : "dockerfile",
    port: service.port ?? 3000,
    cpu_limit: service.cpu_limit ?? "",
    memory_limit: service.memory_limit ?? "",
    compose_content: service.compose_content ?? "",
    compose_repo_url: service.compose_repo_url ?? "",
    compose_file_path: service.compose_file_path ?? "",
    compose_route_service: service.compose_route_service ?? "",
    deploy_strategy: service.deploy_strategy ?? "auto",
    deploy_timeout_minutes: service.deploy_timeout_minutes
      ? String(service.deploy_timeout_minutes)
      : "",
    rollback_images_kept: service.rollback_images_kept
      ? String(service.rollback_images_kept)
      : "",
    pids_limit: String(service.pids_limit || DEFAULT_PIDS_LIMIT),
    isolation: service.isolation ?? "default",
    registry_server: service.registry_server ?? "",
    registry_username: service.registry_username ?? "",
    registry_password: "",
    git_username: service.git_username ?? "",
    git_token: "",
    pre_deploy_commands: service.pre_deploy_commands ?? "",
    post_deploy_commands: service.post_deploy_commands ?? "",
    health_check_path: service.health_check_path ?? "/health",
    health_check_timeout: service.health_check_timeout ?? 5,
    health_check_retries: service.health_check_retries ?? 0, // 0 = off
    container_health: service.container_health ?? "image",
    container_health_command: service.container_health_command ?? "",
    required_env_vars: service.required_env_vars ?? "",
    migrate_command: service.migrate_command ?? "",
    network_alias: service.network_alias ?? "",
    dependencies: service.dependencies ?? [],
    volumes: service.volumes ?? [],
    basicAuthUser: service.basic_auth_user ?? "",
    basicAuthPassword: "",
    ipAllowlist: (service.ip_allowlist ?? []).join("\n"),
  };
}

/** The form holds edits the saved service does not have, compared by value
 *  with what initForm and parsePostDeployCmds make of it. */
export function hasUnsavedChanges(
  form: ConfigForm,
  cmds: PostDeployCmdRow[],
  service: Service,
): boolean {
  return (
    JSON.stringify(form) !== JSON.stringify(initForm(service)) ||
    JSON.stringify(cmds) !==
      JSON.stringify(parsePostDeployCmds(service.post_deploy_commands ?? ""))
  );
}

/** The groups that save form fields, and the fields each owns (D-155):
 *  Source saves its credentials, Domains the routing, Storage the volumes.
 *  Hooks also owns the post-deploy command rows; Resources on a compose
 *  stack saves its compose limits separately. */
export const CARD_FIELDS = {
  general: ["name"],
  source: [
    "git_repo_url",
    "git_branch",
    "repository_id",
    "deploy_on_push",
    "base_directory",
    "docker_image",
    "install_command",
    "build_command",
    "start_command",
    "build_pack",
    "compose_content",
    "compose_repo_url",
    "compose_file_path",
    "registry_server",
    "registry_username",
    "registry_password",
    "git_username",
    "git_token",
  ],
  routing: ["port", "network_alias", "compose_route_service"],
  resources: ["cpu_limit", "memory_limit"],
  deploy: [
    "deploy_strategy",
    "deploy_timeout_minutes",
    "rollback_images_kept",
    "dependencies",
  ],
  health: [
    "health_check_path",
    "health_check_timeout",
    "health_check_retries",
    "container_health",
    "container_health_command",
  ],
  volumes: ["volumes"],
  hooks: ["pre_deploy_commands", "required_env_vars", "migrate_command"],
  access: ["basicAuthUser", "basicAuthPassword", "ipAllowlist"],
  isolation: ["pids_limit", "isolation"],
} as const satisfies Record<string, readonly (keyof ConfigForm)[]>;

export type CardId = keyof typeof CARD_FIELDS;

/** The Settings sections in page order (D-155); each is #set-<id>. */
export type SectionId =
  | "general"
  | "source"
  | "domains"
  | "access"
  | "resources"
  | "deploy"
  | "health"
  | "storage"
  | "hooks"
  | "isolation"
  | "policy"
  | "delete";

/** Sections that moved, by their old ids: links to #set-volumes and the
 *  old ?tab=domains still land. */
const MOVED_SECTIONS: Record<string, SectionId> = {
  routing: "domains",
  creds: "source",
  deps: "deploy",
  volumes: "storage",
  "config-files": "storage",
};

/** The section an id names, old ids included. */
export function sectionOf(id: string): string {
  return MOVED_SECTIONS[id] ?? id;
}

const same = (a: unknown, b: unknown) =>
  JSON.stringify(a) === JSON.stringify(b);

/** The card's fields that differ from the saved service. */
export function changedFields(
  card: CardId,
  form: ConfigForm,
  saved: ConfigForm,
): (keyof ConfigForm)[] {
  return (CARD_FIELDS[card] as readonly (keyof ConfigForm)[]).filter(
    (k) => !same(form[k], saved[k]),
  );
}

/** The saved form with one card's edits on it: what saving that card sends,
 *  so the other cards' unsaved edits stay out of it. */
export function withCard(
  card: CardId,
  form: ConfigForm,
  saved: ConfigForm,
): ConfigForm {
  const out = { ...saved };
  for (const k of CARD_FIELDS[card] as readonly (keyof ConfigForm)[]) {
    (out as Record<string, unknown>)[k] = form[k];
  }
  return out;
}

/** After the service changes (a save, a refetch): the fresh form, keeping
 *  the edits of cards that were not saved. reset are fields to take fresh
 *  whatever they hold (the card just saved). */
export function resync(
  prev: ConfigForm,
  oldSaved: ConfigForm,
  fresh: ConfigForm,
  reset: ReadonlySet<string>,
): ConfigForm {
  const out = { ...fresh };
  for (const k of Object.keys(fresh) as (keyof ConfigForm)[]) {
    if (!reset.has(k) && !same(prev[k], oldSaved[k]))
      (out as Record<string, unknown>)[k] = prev[k];
  }
  return out;
}

/** Form fields whose payload key differs. */
const PAYLOAD_KEY: Partial<Record<keyof ConfigForm, keyof UpdateServiceConfigInput>> = {
  basicAuthUser: "basic_auth_user",
  basicAuthPassword: "basic_auth_password",
  ipAllowlist: "ip_allowlist",
};

/** A payload cut to the card's edited fields: PUT /config keeps every field
 *  a body leaves out, so a save changes only what was edited, never another
 *  field to what initForm made of it (an "auto" build pack as "dockerfile"). */
export function onlyEdited(
  card: CardId,
  form: ConfigForm,
  saved: ConfigForm,
  cmdsChanged: boolean,
  p: UpdateServiceConfigInput,
): UpdateServiceConfigInput {
  const keys: string[] = changedFields(card, form, saved).map(
    (k) => PAYLOAD_KEY[k] ?? k,
  );
  // A repository source takes the URLs' place (payloadOf).
  if (keys.includes("repository_id"))
    keys.push("git_repo_url", "compose_repo_url");
  if (card === "hooks" && cmdsChanged) keys.push("post_deploy_commands");
  return Object.fromEntries(
    Object.entries(p).filter(([k]) => keys.includes(k)),
  ) as UpdateServiceConfigInput;
}

/** What PUT /config gets for a form. */
export function payloadOf(
  f: ConfigForm,
  cmds: PostDeployCmdRow[],
  isCompose: boolean,
): UpdateServiceConfigInput {
  return {
    ...f,
    name: f.name.trim(),
    // A repository source takes the place of the URLs (D-77).
    git_repo_url: f.repository_id ? "" : f.git_repo_url.trim(),
    compose_repo_url: f.repository_id ? "" : f.compose_repo_url.trim(),
    git_branch: f.git_branch.trim(),
    base_directory: f.base_directory.trim(),
    docker_image: f.docker_image.trim(),
    install_command: f.install_command.trim(),
    build_command: f.build_command.trim(),
    compose_route_service: isCompose
      ? f.compose_route_service.trim()
      : undefined,
    network_alias: f.network_alias.trim(),
    // The default stays the default (0), so it moves with the platform's.
    pids_limit: Number(f.pids_limit) === DEFAULT_PIDS_LIMIT ? 0 : Number(f.pids_limit),
    // Empty is the default (0), which moves with the platform's.
    deploy_timeout_minutes: Number(f.deploy_timeout_minutes.trim() || 0),
    rollback_images_kept: Number(f.rollback_images_kept.trim() || 0),
    health_check_path: f.health_check_path.trim(),
    container_health_command: f.container_health_command.trim(),
    migrate_command: f.migrate_command.trim(),
    post_deploy_commands: serializePostDeployCmds(cmds),
    dependencies: f.dependencies,
    volumes: f.volumes.map((v) => ({
      ...v,
      name: v.name.trim(),
      target: v.target.trim(),
      host_path: v.host_path?.trim(),
    })),
    basic_auth_user: f.basicAuthUser.trim(),
    basic_auth_password: f.basicAuthPassword || undefined,
    ip_allowlist: f.ipAllowlist
      .split("\n")
      .map((s) => s.trim())
      .filter(Boolean),
  };
}
