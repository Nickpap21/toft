// Pure helpers for a project's Agents card (board ProjectAgents, D-104).
import type { AgentPolicyCell } from "@/lib/api/agents.service";
import type { Environment } from "@/lib/api/environment.service";
import type { Service } from "@/lib/api/service.service";
import {
  DECISION_LABEL,
  ENV_CLASSES,
  POLICY_ROWS,
  cellOf,
  classesOf,
  type Action,
  type Decision,
  type EnvClass,
} from "@/app/settings/agents/view";

type Cell = Pick<AgentPolicyCell, "action" | "env_class" | "decision">;

const and = (xs: string[]) => (xs.length <= 1 ? xs.join("") : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`);

/** The card's column hints: the project's environments of each class. */
export function envHints(envs: Pick<Environment, "name" | "preview" | "protected">[]): Record<"preview" | "other" | "protected", string> {
  const names = (f: (e: (typeof envs)[number]) => boolean) => envs.filter(f).map((e) => e.name).join(", ");
  return {
    preview: names((e) => !!e.preview) || "pull requests’ previews",
    other: names((e) => !e.preview && !e.protected) || "none yet",
    protected: names((e) => !e.preview && !!e.protected) || "none yet",
  };
}

/** The chip beside the title: "Follows Northwind" or "2 rules of its own". */
export function chipText(own: Cell[], workspace: string): string {
  if (own.length === 0) return `Follows ${workspace}`;
  return own.length === 1 ? "1 rule of its own" : `${own.length} rules of its own`;
}

/** The card's lede for an owner with rules of its own: each row's rule in
 *  words, classes that agree said together. */
export function ownLede(own: Cell[], ws: Cell[], workspace: string): string {
  const parts = POLICY_ROWS.flatMap((r) => {
    const groups = new Map<string, string[]>();
    for (const cls of classesOf(r.action)) {
      const d = cellOf(own, r.action, cls);
      if (!d) continue;
      const key = `${d}:${cellOf(ws, r.action, cls) ?? d}`;
      groups.set(key, [...(groups.get(key) ?? []), ENV_CLASSES.find((e) => e.cls === cls)?.name ?? ""]);
    }
    return [...groups].map(([key, classes]) => {
      const [d, w] = key.split(":") as [Decision, Decision];
      const where = classes.filter(Boolean);
      return `${r.short} is ${DECISION_LABEL[d]}${where.length ? ` in ${and(where)}` : ""} here, where ${workspace} says ${DECISION_LABEL[w]}`;
    });
  });
  return `${parts.join("; ")}. Everything else follows ${workspace}.`;
}

/** The note on databases and caches: the project's own by name, if any. */
export function dataNote(services: Pick<Service, "name" | "environment_id" | "isolation_advice" | "docker_image" | "addon_slug">[],
  envs: Pick<Environment, "id" | "name" | "preview" | "protected">[]): string {
  const own = new Map(envs.filter((e) => !e.preview).map((e) => [e.id, e]));
  const data = services.filter((s) => s.isolation_advice?.reason === "database" && s.environment_id && own.has(s.environment_id));
  const names = [...new Set(data.map((s) => s.name))];
  const tail = "follow the Protected column";
  if (names.length === 0) return `Databases and caches count as Protected: their deploys, variables, secrets and runbook steps ${tail} wherever they run. A preview’s copy follows Previews.`;
  const kind = (s: (typeof data)[number]) => (/redis|valkey/.test(`${s.addon_slug ?? ""} ${s.docker_image ?? ""}`) ? "cache" : "database");
  const unprotected = [...new Set(data.filter((s) => !own.get(s.environment_id ?? "")?.protected).map((s) => own.get(s.environment_id ?? "")?.name ?? ""))];
  const where = unprotected.length ? `, in ${and(unprotected)} too` : "";
  const who = names.length === 1 ? `${names[0]} is a ${kind(data[0])}: its` : `${and(names)} are databases or caches: their`;
  return `${who} deploys, variables, secrets and runbook steps ${tail}${where}. A preview’s copy follows Previews.`;
}

const VERB: Record<Action, string> = {
  deploy: "deploy or roll back",
  code: "push and open pull requests",
  issues: "open issues and comment",
  variables: "change variables",
  secrets: "set secrets",
  runbooks: "run runbook steps",
};

const WHERE: Record<EnvClass, string> = { preview: "previews", other: "other environments", protected: "protected environments", any: "" };

/** The password dialog for loosening a cell: its title, what changes, and
 *  the button (board ProjectAgents, 5). to "" is "As <workspace>". */
export function loosenAsk(project: string, workspace: string, action: Action, cls: EnvClass, from: Decision, to: Decision | "",
  base: Decision, envs: string): { title: string; why: string; button: string } {
  const after = to || base;
  const row = POLICY_ROWS.find((r) => r.action === action);
  const place = WHERE[cls] ? ` in ${project}’s ${WHERE[cls]}` : ` in ${project}`;
  const does = after === "allow" ? VERB[action] : `ask a person to ${VERB[action]}`;
  const label = to ? DECISION_LABEL[to] : `As ${workspace} (${DECISION_LABEL[base]})`;
  const named = ENV_CLASSES.find((e) => e.cls === cls)?.name;
  const target = cls === "any" || !envs ? "" : ` in ${envs}`;
  const effect = after === "allow"
    ? `Agents connected to this project ${VERB[action]}${target} without asking.`
    : `Agents connected to this project may ask a person to ${VERB[action]}${target}.`;
  return {
    title: `Let agents ${does}${place}?`,
    why: `${row?.short ?? action}${named ? `, ${named}` : ""}: ${DECISION_LABEL[from]} → ${label}. ${effect} Confirm with your password.`,
    button: "Confirm and loosen",
  };
}
