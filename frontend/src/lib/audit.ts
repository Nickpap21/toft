// The audit log's actions: one catalogue for the Audit log page (label,
// group) and Home's activity (phrase). audit-coverage.test.ts fails when
// the backend writes an action this list doesn't have.
import type { AuditEntry } from "@/lib/api/org.service";
import { formatDateTime, possessive } from "@/lib/utils";
import { ISOLATION_NAME } from "@/lib/isolation";
import { gb } from "@/components/servers/disk-view";

export interface AuditAction {
  action: string;
  /** The Audit log's column: "Deployed", "Revoked an API key". */
  label: string;
  /** Home's activity sentence: "<who> deployed <what>". */
  phrase: string;
  group: string;
}

export const AUDIT_ACTIONS: AuditAction[] = [
  {
    action: "deployment.triggered",
    label: "Deployed",
    phrase: "deployed",
    group: "Deploys",
  },
  {
    action: "deployment.rollback",
    label: "Rolled back",
    phrase: "rolled back",
    group: "Deploys",
  },
  {
    action: "deployment.redeploy",
    label: "Redeployed",
    phrase: "redeployed",
    group: "Deploys",
  },
  {
    action: "deployment.capacity_bypassed",
    label: "Deployed past capacity",
    phrase: "deployed past capacity",
    group: "Deploys",
  },
  {
    action: "service.move_started",
    label: "Moved a service",
    phrase: "started moving service",
    group: "Deploys",
  },
  {
    action: "project.created",
    label: "Created a project",
    phrase: "created project",
    group: "Services",
  },
  {
    action: "project.deleted",
    label: "Deleted a project",
    phrase: "deleted project",
    group: "Services",
  },
  { action: "environment.deleted", label: "Deleted an environment", phrase: "deleted the environment", group: "Services" },
  {
    action: "service.created",
    label: "Created a service",
    phrase: "created service",
    group: "Services",
  },
  {
    action: "service.created.project_scoped",
    label: "Created a project service",
    phrase: "created shared service (project)",
    group: "Services",
  },
  {
    action: "service.created.org_scoped",
    label: "Created a workspace service",
    phrase: "created shared service (workspace)",
    group: "Services",
  },
  {
    action: "service.settings_changed",
    label: "Changed a service’s settings",
    phrase: "changed the settings of",
    group: "Services",
  },
  // The rest of a service's Settings, each saved on its own.
  { action: "service.domain_added", label: "Added a domain", phrase: "added a domain to", group: "Services" },
  { action: "service.domain_removed", label: "Removed a domain", phrase: "removed a domain from", group: "Services" },
  { action: "service.config_file_saved", label: "Saved a config file", phrase: "saved a config file for", group: "Services" },
  { action: "service.config_file_deleted", label: "Deleted a config file", phrase: "deleted a config file of", group: "Services" },
  { action: "service.shared_volumes_changed", label: "Changed a service’s shared volumes", phrase: "changed the shared volumes of", group: "Services" },
  { action: "service.server_changed", label: "Changed a service’s server", phrase: "changed the server of", group: "Services" },
  { action: "service.moved_to_workspace", label: "Moved a service to the workspace", phrase: "moved to the workspace:", group: "Services" },
  { action: "service.volume_purged", label: "Destroyed a volume’s data", phrase: "destroyed the data in a volume of", group: "Services" },
  {
    action: "service.compose_limits.updated",
    label: "Changed compose limits",
    phrase: "changed the compose limits of",
    group: "Services",
  },
  {
    action: "service.stopped",
    label: "Stopped a service",
    phrase: "stopped service",
    group: "Services",
  },
  {
    action: "service.restarted",
    label: "Restarted a service",
    phrase: "restarted service",
    group: "Services",
  },
  {
    action: "service.deleted",
    label: "Deleted a service",
    phrase: "deleted service",
    group: "Services",
  },
  {
    action: "variables.saved",
    label: "Changed variables",
    phrase: "changed the variables of",
    group: "Variables and secrets",
  },
  {
    action: "secret.created",
    label: "Added a secret",
    phrase: "added a secret to",
    group: "Variables and secrets",
  },
  {
    action: "secret.replaced",
    label: "Replaced a secret",
    phrase: "replaced a secret of",
    group: "Variables and secrets",
  },
  // D-160: a catalog app's admin password, kept for its creator.
  {
    action: "service.admin_password_revealed",
    label: "Showed an admin password",
    phrase: "showed the admin password of",
    group: "Variables and secrets",
  },
  {
    action: "service.admin_password_reset",
    label: "Reset an admin password",
    phrase: "reset the admin password of",
    group: "Variables and secrets",
  },
  {
    action: "service.admin_password_dismissed",
    label: "Dismissed an admin password",
    phrase: "dismissed the admin password of",
    group: "Variables and secrets",
  },
  {
    action: "secret.deleted",
    label: "Deleted a secret",
    phrase: "deleted a secret of",
    group: "Variables and secrets",
  },
  {
    action: "server.created",
    label: "Added a server",
    phrase: "added server",
    group: "Servers",
  },
  { action: "server.renamed", label: "Renamed a server", phrase: "renamed server", group: "Servers" },
  { action: "server.removed_unused", label: "Removed a server nobody joined", phrase: "removed server nobody joined:", group: "Servers" },
  {
    action: "server.provisioned",
    label: "Provisioned a server",
    phrase: "provisioned server",
    group: "Servers",
  },
  {
    action: "server.gvisor_installed",
    label: "Installed gVisor on a server",
    phrase: "installed gVisor on",
    group: "Servers",
  },
  {
    action: "server.gvisor_install_failed",
    label: "A gVisor install failed",
    phrase: "tried to install gVisor on",
    group: "Servers",
  },
  {
    action: "server.host_key_reset",
    label: "Replaced the SSH host key",
    phrase: "replaced the SSH host key of",
    group: "Servers",
  },
  // The server agent (toft-agent), not an AI agent: those are under Agents.
  { action: "server.agent_token_created", label: "Made a server agent join command", phrase: "made a server agent join command for", group: "Servers" },
  { action: "server.agent_joined", label: "A server agent joined", phrase: "joined a server agent to", group: "Servers" },
  { action: "server.agent_rejoined", label: "A server agent rejoined", phrase: "rejoined a server agent to", group: "Servers" },
  { action: "server.agent_key_rotated", label: "Rotated a server agent's key", phrase: "rotated the server agent key of", group: "Servers" },
  { action: "server.agent_revoked", label: "Revoked a server agent", phrase: "revoked the server agent of", group: "Servers" },
  { action: "server.agent_state_adopted", label: "Adopted a server’s state", phrase: "adopted the state of", group: "Servers" },
  { action: "server.agent_state_discarded", label: "Used the panel’s state for a server", phrase: "used the panel’s state for", group: "Servers" },
  { action: "server.connection_moved", label: "Changed how a server connects", phrase: "changed how Toft connects to", group: "Servers" },
  // Updates to a server's host (D-133).
  { action: "server.updates_started", label: "Started updates on a server", phrase: "started updates on", group: "Servers" },
  { action: "server.updates_succeeded", label: "Finished updates on a server", phrase: "finished updates on", group: "Servers" },
  { action: "server.updates_failed", label: "Updates on a server failed", phrase: "tried to update", group: "Servers" },
  // A server's disk (D-135). A finished clean can have failed, so it doesn't say "cleaned".
  { action: "server.disk_clean_started", label: "Started a disk clean", phrase: "started a disk clean on", group: "Servers" },
  { action: "server.disk_cleaned", label: "Finished a disk clean", phrase: "finished a disk clean on", group: "Servers" },
  { action: "server.volume_removed", label: "Removed a volume", phrase: "removed a volume from", group: "Servers" },
  { action: "server.disk_policy_changed", label: "Changed a server’s disk policy", phrase: "changed the disk policy of", group: "Servers" },
  {
    action: "server.shell_opened",
    label: "Opened a host shell",
    phrase: "opened a host shell on",
    group: "Servers",
  },
  {
    action: "server.shell_closed",
    label: "Closed a host shell",
    phrase: "closed a host shell on",
    group: "Servers",
  },
  {
    action: "server.command_run",
    label: "Ran a command as root",
    phrase: "ran a command as root on",
    group: "Servers",
  },
  {
    action: "server.shared",
    label: "Shared a server",
    phrase: "shared server",
    group: "Servers",
  },
  {
    action: "server.unshared",
    label: "Stopped sharing a server",
    phrase: "stopped sharing server",
    group: "Servers",
  },
  {
    action: "server.deleted",
    label: "Removed a server",
    phrase: "removed server",
    group: "Servers",
  },
  {
    action: "server.backup_set",
    label: "Set a server’s backup",
    phrase: "set the backup of",
    group: "Backups",
  },
  {
    action: "server.backup_removed",
    label: "Stopped a server’s backups",
    phrase: "stopped the backups of",
    group: "Backups",
  },
  {
    action: "instance.backup_set",
    label: "Set the panel host’s backup",
    phrase: "set the backup of",
    group: "Backups",
  },
  {
    action: "instance.backup_removed",
    label: "Stopped the panel host’s backups",
    phrase: "stopped the backups of",
    group: "Backups",
  },
  {
    action: "member.invited",
    label: "Invited a member",
    phrase: "created an invite link for",
    group: "Access",
  },
  {
    action: "invite.revoked",
    label: "Revoked an invite",
    phrase: "revoked an invite to",
    group: "Access",
  },
  {
    action: "member.joined",
    label: "Joined the workspace",
    phrase: "joined",
    group: "Access",
  },
  {
    action: "member.added",
    label: "Added a member",
    phrase: "added",
    group: "Access",
  },
  {
    action: "member.role_changed",
    label: "Changed a role",
    phrase: "changed the role of",
    group: "Access",
  },
  {
    action: "member.removed",
    label: "Removed a member",
    phrase: "removed",
    group: "Access",
  },
  {
    action: "member.left",
    label: "Left the workspace",
    phrase: "left",
    group: "Access",
  },
  {
    action: "apikey.created",
    label: "Created an API key",
    phrase: "created API key",
    group: "Access",
  },
  {
    action: "apikey.revoked",
    label: "Revoked an API key",
    phrase: "revoked API key",
    group: "Access",
  },
  {
    action: "org.renamed",
    label: "Renamed the workspace",
    phrase: "renamed",
    group: "Access",
  },
  { action: "org.setup_hidden", label: "Hid the setup checklist", phrase: "hid the setup checklist of", group: "Access" },
  { action: "ssh_key.added", label: "Added an SSH key", phrase: "added the SSH key", group: "Access" },
  { action: "ssh_key.removed", label: "Removed an SSH key", phrase: "removed the SSH key", group: "Access" },
  {
    action: "environment.protection_changed",
    label: "Changed an environment's protection",
    phrase: "changed the protection of",
    group: "Deploys",
  },
  // The workspace's monitoring addresses: where logs and metrics go.
  { action: "observability.loki_changed", label: "Changed the Loki address", phrase: "changed the address of", group: "Observability" },
  { action: "observability.grafana_changed", label: "Changed the Grafana address", phrase: "changed the address of", group: "Observability" },
  { action: "observability.prometheus_changed", label: "Changed the Prometheus service", phrase: "changed the service of", group: "Observability" },
  {
    action: "org.isolation_changed",
    label: "Changed the workspace's isolation",
    phrase: "changed the default isolation of",
    group: "Access",
  },
  {
    action: "service.isolation_changed",
    label: "Changed a service's isolation",
    phrase: "changed the isolation of",
    group: "Services",
  },
  {
    action: "org.capacity_rule_changed",
    label: "Changed who can deploy past capacity",
    phrase: "changed who can deploy past capacity in",
    group: "Access",
  },
  {
    action: "alerts.channel_updated",
    label: "Changed the alert webhook",
    phrase: "changed the alert webhook",
    group: "Alerts",
  },
  {
    action: "alerts.secret_rotated",
    label: "Rotated the signing secret",
    phrase: "rotated the alert signing secret",
    group: "Alerts",
  },
  {
    action: "alerts.channel_deleted",
    label: "Removed the alert webhook",
    phrase: "removed the alert webhook",
    group: "Alerts",
  },
  {
    action: "deploy_policy.exception.granted",
    label: "Granted an exception",
    phrase: "granted a deploy policy exception to",
    group: "Deploy policy",
  },
  {
    action: "deploy_policy.exception.revoked",
    label: "Revoked an exception",
    phrase: "revoked the deploy policy exception of",
    group: "Deploy policy",
  },
  // Repositories (D-75, D-79).
  { action: "repository.created", label: "Created a repository", phrase: "created the repository", group: "Code" },
  { action: "repository.updated", label: "Changed a repository's settings", phrase: "changed the settings of", group: "Code" },
  { action: "repository.deleted", label: "Deleted a repository", phrase: "deleted the repository", group: "Code" },
  // Previews (D-97).
  { action: "preview.started", label: "A preview started", phrase: "started the preview of", group: "Code" },
  { action: "preview.approved", label: "Approved a preview", phrase: "approved the preview of", group: "Code" },
  { action: "preview.updated", label: "A preview updated", phrase: "updated the preview of", group: "Code" },
  { action: "preview.stopped", label: "Stopped a preview", phrase: "stopped the preview of", group: "Code" },
  { action: "preview.removed", label: "A preview was removed", phrase: "removed the preview of", group: "Code" },
  { action: "repository.import_started", label: "Started an import", phrase: "started importing into", group: "Code" },
  { action: "repository.import_cancelled", label: "Cancelled an import", phrase: "cancelled the import into", group: "Code" },
  { action: "repository.mirror_added", label: "Added a push mirror", phrase: "added a push mirror to", group: "Code" },
  { action: "repository.mirror_removed", label: "Removed a mirror", phrase: "removed a mirror of", group: "Code" },
  { action: "repository.webhook_created", label: "Added a webhook", phrase: "added a webhook to", group: "Code" },
  { action: "repository.webhook_updated", label: "Changed a webhook", phrase: "changed a webhook of", group: "Code" },
  { action: "repository.webhook_deleted", label: "Deleted a webhook", phrase: "deleted a webhook of", group: "Code" },
  { action: "repository.webhook_secret_rotated", label: "Rotated a webhook's secret", phrase: "rotated a webhook secret of", group: "Code" },
  { action: "doc.step_run", label: "Ran a page's command", phrase: "ran a command from the page", group: "Docs" },
  { action: "doc.deleted", label: "Deleted a page", phrase: "deleted the page", group: "Docs" },
  // Agents (D-100). agent.call is one row per tool call: the page shows the
  // tool as its action, with the outcome.
  { action: "agent.call", label: "Called a tool", phrase: "called a tool on", group: "Agents" },
  { action: "agent.approved", label: "Approved an agent's request", phrase: "approved an agent's request for", group: "Agents" },
  { action: "agent.denied", label: "Denied an agent's request", phrase: "denied an agent's request for", group: "Agents" },
  { action: "agent.connected", label: "Connected an agent", phrase: "connected the agent", group: "Agents" },
  { action: "agent.revoked", label: "Revoked an agent", phrase: "revoked the agent", group: "Agents" },
  { action: "agent.policy_changed", label: "Changed the agent policy", phrase: "changed the agent policy of", group: "Agents" },
  // A project's own rules (D-101) and an agent's push to its branch (D-102).
  { action: "agent.project_policy_changed", label: "Changed a project’s agent rules", phrase: "changed the agent rules of", group: "Agents" },
  { action: "agent.pushed", label: "Pushed to its branch", phrase: "pushed to a branch of", group: "Agents" },
  { action: "agent.paused", label: "Paused agents", phrase: "paused agents in", group: "Agents" },
  { action: "agent.resumed", label: "Resumed agents", phrase: "resumed agents in", group: "Agents" },
];

const BY_ACTION = new Map(AUDIT_ACTIONS.map((a) => [a.action, a]));

export function auditAction(action: string): AuditAction {
  return (
    BY_ACTION.get(action) ?? {
      action,
      label: action.replace(/[._]/g, " "),
      phrase: action.replace(/[._]/g, " "),
      group: "Other",
    }
  );
}

/** Home's phrases, by action. */
export const ACTION_LABELS: Record<string, string> = Object.fromEntries(
  AUDIT_ACTIONS.map((a) => [a.action, a.phrase]),
);

/** The verb phrase for an audit action; unknown actions are spelled out. */
export function humanizeAction(action: string): string {
  return auditAction(action).phrase;
}

/** The filter's groups, in the catalogue's order. */
export function actionGroups(): { group: string; actions: AuditAction[] }[] {
  const groups: { group: string; actions: AuditAction[] }[] = [];
  for (const a of AUDIT_ACTIONS) {
    const g = groups.find((x) => x.group === a.group);
    if (g) g.actions.push(a);
    else groups.push({ group: a.group, actions: [a] });
  }
  return groups;
}

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
const str = (v: unknown) => (typeof v === "string" ? v : "");
const list = (v: unknown) =>
  Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];

/** An agent.call's outcome chip (D-100). */
export function agentOutcome(e: AuditEntry): {
  label: string;
  tone: "neutral" | "green" | "amber" | "red" | "grey";
} {
  const d = (e.detail ?? {}) as Record<string, unknown>;
  switch (str(d.outcome)) {
    case "allowed":
      return { label: "Allowed", tone: "neutral" };
    case "asked":
      return { label: "Asked a person", tone: "amber" };
    case "approved":
      return { label: "Ran, approved", tone: "green" };
    case "denied":
      return { label: "Denied", tone: "red" };
    case "expired":
      return { label: "Expired", tone: "grey" };
    case "refused":
      // The policy working: red is for failures (D-147).
      return { label: "Refused", tone: "neutral" };
    default:
      return { label: "Failed", tone: "red" };
  }
}

/** Why an agent's call was refused, by the API's code. */
const REFUSAL: Record<string, string> = {
  agents_paused: "Agents were paused",
  insufficient_scope: "Its connection doesn't allow this",
  agent_policy_never: "The policy says Never",
  approval_required: "Needs a person; called without a reason",
  reason_required: "Needs a reason",
  too_many_approvals: "Too many requests for a person",
};

// The agent policy's words (D-100, D-102), as Settings → Agents shows them.
const POLICY_ACTION: Record<string, string> = {
  deploy: "Deploy or roll back",
  code: "Branches and pull requests",
  issues: "Issues and comments",
  variables: "Change variables",
  secrets: "Set a secret",
  runbooks: "Runbook steps",
};
const POLICY_CLASS: Record<string, string> = {
  preview: "previews",
  other: "other environments",
  protected: "protected environments",
};
const DECISION: Record<string, string> = { allow: "Allow", ask: "Ask a person", never: "Never" };

/** "deploy, other: ask → allow" → "Deploy or roll back, other environments:
 *  Ask a person → Allow"; a project's "as the workspace" names the workspace. */
function policyChange(c: string, workspace: string): string {
  const m = c.match(/^(\w+), (\w+): (.+) → (.+)$/);
  if (!m) return c;
  const [, action, cls, from, to] = m;
  const word = (d: string) => DECISION[d] ?? (d === "as the workspace" && workspace ? `as ${workspace}` : d);
  const where = cls === "any" ? "" : `, ${POLICY_CLASS[cls] ?? cls}`;
  return `${POLICY_ACTION[action] ?? action}${where}: ${word(from)} → ${word(to)}`;
}

/** Which rule decided an agent's call (D-101): "Founders’ rule",
 *  "billing-api’s rule" or "The default"; "" for entries without one. */
export function agentRule(e: AuditEntry, workspace: string): string {
  const d = (e.detail ?? {}) as Record<string, unknown>;
  switch (str(d.level)) {
    case "workspace":
      return `${possessive(workspace || "The workspace")} rule`;
    case "project": {
      // A service's or an environment's name starts with its project's.
      const project = ["service", "environment", "project"].includes(e.target.kind) ? e.target.name.split(" / ")[0] : "";
      return `${possessive(project || "The project")} rule`;
    }
    case "default":
      return "The default";
    default:
      return "";
  }
}

/** The rule and what it said: "Founders’ rule: Ask a person". */
function ruleSaid(e: AuditEntry, d: Record<string, unknown>, workspace: string): string {
  const rule = agentRule(e, workspace);
  if (!rule) return "";
  switch (str(d.outcome)) {
    case "allowed":
      return `${rule}: Allow`;
    case "asked":
    case "approved":
    case "denied":
    case "expired":
      return `${rule}: Ask a person`;
    default:
      return "";
  }
}

// A service's settings by their API names (changedSettings in
// backend/internal/service/service.go), in the Settings page's words.
const SETTING: Record<string, string> = {
  name: "name",
  git_repo_url: "repository URL",
  git_branch: "branch",
  repository_id: "repository",
  deploy_on_push: "deploy on push",
  base_directory: "base directory",
  docker_image: "image",
  install_command: "install command",
  build_command: "build command",
  start_command: "start command",
  build_pack: "build pack",
  port: "port",
  cpu_limit: "CPU limit",
  memory_limit: "memory limit",
  pids_limit: "process limit",
  deploy_timeout_minutes: "deploy timeout",
  rollback_images_kept: "rollback images",
  compose_content: "compose file",
  compose_repo_url: "compose repository URL",
  compose_file_path: "compose file path",
  compose_route_service: "routed compose service",
  deploy_strategy: "deploy strategy",
  registry_server: "registry",
  registry_username: "registry username",
  registry_password: "registry password",
  network_alias: "network alias",
  volumes: "volumes",
  git_username: "git username",
  git_token: "git token",
  pre_deploy_commands: "host commands before deploy",
  post_deploy_commands: "commands after deploy",
  health_check_path: "health check path",
  health_check_timeout: "health check timeout",
  health_check_retries: "health check attempts",
  required_env_vars: "required variables",
  migrate_command: "migrate command",
  dependencies: "dependencies",
  basic_auth_user: "basic auth username",
  basic_auth_password: "basic auth password",
  ip_allowlist: "IP allowlist",
};

const clip = (s: string, n: number) => (s.length > n ? `${s.slice(0, n)}…` : s);

function agentCall(e: AuditEntry, d: Record<string, unknown>, workspace: string): { text: string; code?: string } {
  const code = str(d.detail);
  const env = e.target.environment ?? "";
  // "production: billing-api’s rule says Never", as the board words it.
  if (code === "agent_policy_never") {
    const rule = agentRule(e, workspace);
    const never = rule ? `${rule} says Never` : REFUSAL[code];
    return { text: env ? `${env}: ${never.charAt(0).toLowerCase()}${never.slice(1)}` : never };
  }
  if (REFUSAL[code]) return { text: REFUSAL[code] };
  const args = (d.args ?? {}) as Record<string, unknown>;
  const num = (v: unknown) => (typeof v === "number" ? v : Number(v) || 0);
  let text = "";
  switch (str(d.tool)) {
    case "deploy":
    case "rollback": {
      const reason = str(args.reason);
      text = [env && `${str(d.tool) === "deploy" ? "To" : "In"} ${env}`, reason && `“${clip(reason, 120)}”`]
        .filter(Boolean)
        .join(". ");
      break;
    }
    case "get_deploy_logs":
      // 200 is the tool's default (openapi.yaml, x-toft-tools).
      text = `${env ? `${env}, ` : ""}last ${typeof args.lines === "number" ? args.lines : 200} lines`;
      break;
    case "set_secret":
      text = [env, `${str(args.key)} (value never recorded)`].filter(Boolean).join(", ");
      break;
    case "set_env_var":
      text = [env, str(args.key)].filter(Boolean).join(", ");
      break;
    case "create_branch":
      text = `${str(args.name)}${str(args.from) ? ` from ${str(args.from)}` : ""}`;
      break;
    case "open_pr":
      // An agent's pull request is untrusted (D-102): its preview waits.
      text = `${str(args.head)} into ${str(args.base) || "the default branch"}${["allowed", "approved"].includes(str(d.outcome)) ? "; its preview waits for approval" : ""}`;
      break;
    case "create_issue":
      text = str(args.title) && `“${clip(str(args.title), 120)}”`;
      break;
    case "comment":
      text = args.issue_number ? `On issue ${num(args.issue_number)}` : args.pull_number ? `On !${num(args.pull_number)}` : "";
      break;
    case "run_runbook_step":
      text = args.step !== undefined ? `Step ${num(args.step) + 1}` : "";
      break;
    case "create_preview":
      text = args.pull_number ? `!${num(args.pull_number)}` : "";
      break;
  }
  const by = str(d.outcome) === "approved" ? code.match(/^approved by (.+)$/)?.[1] : undefined;
  text = [text, ruleSaid(e, d, workspace), by && `With ${possessive(by)} approval`].filter(Boolean).join(". ");
  // A code the page has no words for yet: shown as it came.
  const unknown = ["refused", "failed"].includes(str(d.outcome)) && code && !code.startsWith("deploy ");
  return unknown ? { text, code } : { text };
}

/** The Details column: text, a code part, and text after it. workspace
 *  names the workspace's agent rule. */
export function auditDetail(e: AuditEntry, workspace = ""): {
  text: string;
  code?: string;
  after?: string;
  also?: string;
} {
  const d = (e.detail ?? {}) as Record<string, unknown>;
  switch (e.action) {
    case "deployment.triggered":
      // Toft's own redeploy says why (D-150): "certificate issued: switch to HTTPS".
      if (str(d.reason)) return { text: str(d.reason).charAt(0).toUpperCase() + str(d.reason).slice(1) };
      return { text: e.target.environment ? `To ${e.target.environment}` : "" };
    case "deployment.rollback":
    case "deployment.redeploy":
      return { text: e.target.environment ? `In ${e.target.environment}` : "" };
    case "deployment.capacity_bypassed":
      return { text: "Confirmed the warning that its server is full" };
    case "apikey.created":
      return str(d.project_name)
        ? {
            text: `${str(d.project_name)} only:`,
            code: list(d.scopes).join(", "),
          }
        : { text: "Full account key" };
    case "apikey.revoked": {
      const created = str(d.created_at) ? `created ${formatDateTime(str(d.created_at)).slice(0, 10)}` : "";
      return str(d.project_name)
        ? { text: `${str(d.project_name)} only:`, code: list(d.scopes).join(", "), after: created && `· ${created}` }
        : { text: created ? `Full account key, ${created}` : "Full account key" };
    }
    case "member.invited":
      return {
        text: `${cap(str(d.role))}${str(d.expires_at) ? `, link expires ${formatDateTime(str(d.expires_at))}` : ""}`,
      };
    case "member.role_changed":
      return { text: `${cap(str(d.from))} → ${cap(str(d.to))}` };
    case "member.removed":
      return { text: str(d.role) ? `Was ${str(d.role)}` : "" };
    case "member.left":
    case "member.joined":
    case "member.added":
      return { text: str(d.role) ? `As ${str(d.role)}` : "" };
    case "org.renamed":
      return { text: `From “${str(d.from)}” to “${str(d.to)}”` };
    case "org.capacity_rule_changed":
      return {
        text:
          str(d.to) === "developer"
            ? "Owners and developers can deploy past capacity"
            : "Only owners can deploy past capacity",
      };
    case "preview.started":
    case "preview.approved":
    case "preview.updated":
      return { text: `!${typeof d.pull_request === "number" ? d.pull_request : ""} in ${str(d.repository)}`, code: str(d.sha).slice(0, 7) };
    case "preview.stopped":
      return { text: str(d.note) || (str(d.reason) === "manual" ? "Stopped by hand" : "") };
    case "preview.removed":
      return { text: str(d.reason) ? cap(str(d.reason)) : "" };
    case "server.gvisor_installed":
      return { text: `gVisor ${str(d.release)}${str(d.platform) ? `, ${str(d.platform)}` : ""}` };
    case "server.gvisor_install_failed":
      return { text: str(d.error) ? `gVisor ${str(d.release)}: ${str(d.error)}` : "" };
    case "server.updates_failed":
      return { text: str(d.error) };
    case "server.disk_clean_started":
      return { text: d.automatic ? "The automatic clean" : "" };
    case "server.disk_cleaned":
      if (str(d.outcome) === "failed") return { text: str(d.error) ? `Failed: ${str(d.error)}` : "Failed" };
      return { text: typeof d.freed_bytes === "number" ? `Freed ${gb(d.freed_bytes)}` : "" };
    case "server.volume_removed":
      // -1 when Docker didn't report its size.
      return { text: "", code: str(d.name), after: typeof d.bytes === "number" && d.bytes >= 0 ? gb(d.bytes) : "" };
    case "environment.protection_changed":
      return { text: d.protected === true ? "Protected" : d.protected === false ? "No longer protected" : "" };
    case "observability.loki_changed":
    case "observability.grafana_changed":
      return str(d.url) ? { text: "Now", code: str(d.url) } : { text: "" };
    case "service.settings_changed":
      return { text: cap(list(d.fields).map((f) => SETTING[f] ?? f.replace(/_/g, " ")).join(", ")) };
    case "service.domain_added":
    case "service.domain_removed":
      return { text: "", code: str(d.hostname) };
    case "service.config_file_saved":
    case "service.config_file_deleted":
      return { text: "", code: str(d.path) };
    case "service.volume_purged":
      return { text: "", code: str(d.mount) };
    case "service.shared_volumes_changed": {
      const mounts = Array.isArray(d.mounts) ? (d.mounts as Record<string, unknown>[]) : [];
      return {
        text: mounts.length
          ? mounts.map((m) => `${str(m.name)} at ${str(m.target)}${m.read_only ? " (read-only)" : ""}`).join(", ")
          : "None now",
      };
    }
    case "service.server_changed":
      // The entry has the servers' IDs; "" is the panel host.
      return {
        text: !str(d.to) ? "To the panel host" : !str(d.from) ? "From the panel host to another server" : "To another server",
      };
    case "org.isolation_changed":
    case "service.isolation_changed": {
      const name = (l: string) => ISOLATION_NAME[l as keyof typeof ISOLATION_NAME] ?? "the workspace's default";
      return { text: `${name(str(d.from))} → ${name(str(d.to))}` };
    }
    case "server.host_key_reset":
      return str(d.fingerprint)
        ? { text: "Now trusts", code: str(d.fingerprint) }
        : { text: "" };
    case "server.shell_opened":
      return { text: str(d.from) ? `From ${str(d.from)}` : "" };
    case "server.agent_joined":
    case "server.agent_rejoined":
      return { text: [str(d.version) && `Agent ${str(d.version)}`, str(d.from) && `from ${str(d.from)}`].filter(Boolean).join(" ") || (str(d.fingerprint) ? "Key" : ""), code: str(d.fingerprint) };
    case "server.agent_key_rotated":
      return str(d.fingerprint) ? { text: "Now trusts", code: str(d.fingerprint) } : { text: "" };
    case "server.agent_revoked":
      return { text: d.uninstalled ? "Uninstalled from the server" : "" };
    case "server.shell_closed": {
      const s = typeof d.seconds === "number" ? d.seconds : 0;
      const open = `open ${s < 60 ? `${s} s` : `${Math.round(s / 60)} min`}`;
      return { text: d.restart ? `Ended by a restart, ${open}` : cap(open) };
    }
    case "server.command_run":
      return str(d.command) ? { text: "", code: str(d.command) } : { text: "" };
    case "server.shared":
      return {
        text: e.target.other_workspace
          ? `With ${e.target.other_workspace}`
          : "",
      };
    case "server.unshared":
      return {
        text: e.target.other_workspace
          ? `No longer with ${e.target.other_workspace}`
          : "",
      };
    case "alerts.channel_updated":
      return str(d.host)
        ? {
            text: `${str(d.host)}, ${d.enabled === false ? "paused" : "sending on"}`,
          }
        : { text: "" };
    case "deploy_policy.exception.granted":
    case "deploy_policy.exception.revoked": {
      const reason = str(d.reason);
      const quoted = reason
        ? `“${reason.length > 60 ? `${reason.slice(0, 60)}…` : reason}”`
        : "";
      return {
        text: e.action.endsWith("granted") ? "Allows" : "Was",
        code: list(d.allowed_rules).join(", "),
        after: quoted,
      };
    }
    case "agent.call":
      return agentCall(e, d, workspace);
    case "agent.connected": {
      const n = typeof d.projects === "number" ? d.projects : 0;
      const where = d.all_projects ? "Every project" : `${n} ${n === 1 ? "project" : "projects"} only`;
      // A connection lasts 90 days (ConnectionTTL, internal/agent/model.go).
      const until = formatDateTime(new Date(new Date(e.created_at).getTime() + 90 * 86_400_000).toISOString()).slice(0, 10);
      return { text: `${where}: ${list(d.scopes).join(", ")}. Until ${until}` };
    }
    case "agent.revoked":
      return { text: d.by_agent ? "The agent disconnected itself" : "" };
    case "agent.policy_changed":
      return { text: list(d.changes).map((c) => policyChange(c, workspace)).join("; ") };
    case "agent.project_policy_changed":
      return { text: list(d.changes).map((c) => policyChange(c, workspace)).join("; ") };
    case "agent.pushed": {
      const short = (sha: string) => sha.slice(0, 7);
      const created = /^0+$/.test(str(d.before));
      return {
        text: `${created ? "Created " : ""}${str(d.branch)}${created ? " at" : ":"}`,
        code: created ? short(str(d.after)) : `${short(str(d.before))}..${short(str(d.after))}`,
      };
    }
    case "doc.step_run": {
      // The page numbers its commands from 1; the entry records the index.
      const n = typeof d.step === "number" ? d.step + 1 : 0;
      const where = d.host ? "on its server’s host" : "in its service’s container";
      return { text: `${n ? `Step ${n} ` : ""}${where}${str(d.outcome) === "failed" ? ": it failed" : ""}` };
    }
    case "agent.approved":
    case "agent.denied": {
      const note = str(d.note);
      // Written at approval, before the deploy goes live: no clock time (D-146).
      const also = d.also_allows as Record<string, unknown> | undefined;
      const commit = also && str(also.rollback_commit).slice(0, 7);
      return {
        text: `${str(d.agent)} for ${str(d.for)}:`,
        code: str(d.tool),
        after: `${str(d.target)}${note ? ` “${clip(note, 120)}”` : ""}`,
        also: also && `Also allows: rolling back to ${commit || "the deployment that was live"} once, for ${str(also.window).replace(/, once$/, "")}`,
      };
    }
    default:
      return { text: "" };
  }
}

/** Where an entry's target opens, when it still exists. */
export function auditHref(e: AuditEntry): string | undefined {
  const t = e.target;
  if (t.deleted) return undefined;
  switch (t.kind) {
    case "service":
      return t.project_id
        ? `/projects/${t.project_id}/services/${t.service_id}`
        : undefined;
    case "deployment":
      return t.project_id
        ? `/projects/${t.project_id}/services/${t.service_id}?tab=deployments&deployment=${t.id}`
        : undefined;
    case "server":
      return `/servers/${t.id}`;
    case "project":
      return `/projects/${t.id}`;
    case "environment":
      return t.project_id
        ? `/projects/${t.project_id}/environments/${t.id}`
        : undefined;
    case "apikey":
      return "/settings/api-keys";
    case "user":
    case "invite":
      return "/settings";
    case "org":
      return e.action.startsWith("alerts.")
        ? "/settings/alerts"
        : e.action.startsWith("agent.")
          ? "/settings/agents"
          : "/settings";
    case "instance":
      return "/settings/backups#panel-host";
    case "observability":
      return "/shared?tab=logs";
    case "agent":
      return "/settings/agents";
    case "agent_approval":
      return `/settings/agents/requests/${t.id}`;
    case "repository":
      return t.project_id ? `/projects/${t.project_id}/code/${encodeURIComponent(t.name.split(" / ").pop() ?? "")}` : undefined;
    case "doc_page":
      return t.project_id ? `/projects/${t.project_id}/docs/${t.id}` : undefined;
    default:
      return undefined;
  }
}

const KIND_NAMES: Record<string, string> = { apikey: "API key", doc_page: "page", agent_approval: "request" };

/** A deleted target's name: what the entry recorded, else "a deleted
 *  <kind>" (older entries recorded only an ID). */
export function auditTargetName(e: AuditEntry): string {
  const t = e.target;
  if (t.deleted && t.name === t.id)
    return `a deleted ${KIND_NAMES[t.kind] ?? t.kind}`;
  return t.name;
}
