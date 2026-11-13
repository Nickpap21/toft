import type { AgentConsentInput } from "@/lib/api/agents.service";

// The consent screen's rules (board AgentConsent, D-100), kept out of the
// page so they can be tested without rendering it.

export type Role = "owner" | "developer" | "viewer";

/** The authorization request's fields the screen sends back with the answer. */
const FIELDS = ["response_type", "client_id", "redirect_uri", "code_challenge", "code_challenge_method", "state", "scope", "resource"] as const;

/** A program on this computer: the redirect goes to localhost or a loopback address. */
export function isLocal(redirectHost: string): boolean {
  const host = redirectHost.replace(/:\d+$/, "").replace(/^\[|\]$/g, "");
  return host === "localhost" || host === "::1" || /^127\.\d+\.\d+\.\d+$/.test(host);
}

export type Scope = NonNullable<AgentConsentInput["scopes"]>[number];

/** The abilities above Read, in the board's order (AgentConsent, D-102), with
 *  the reason a viewer can't give each. */
export const ABILITIES: { scope: Scope; name: string; hint: string; viewer: string }[] = [
  { scope: "deploy", name: "Deploy and roll back", hint: "Its services, and previews of pull requests.", viewer: "Viewers can’t deploy." },
  {
    scope: "code", name: "Branches and pull requests",
    hint: "Create branches and push to them, never to main or a branch a service deploys from; open pull requests. Their previews wait for a person to approve each commit, and run Sandboxed with no secrets.",
    viewer: "Viewers can’t push or open pull requests.",
  },
  { scope: "issues", name: "Issues and comments", hint: "Open issues and comment on issues and pull requests.", viewer: "Viewers can’t open issues or comment." },
  {
    scope: "variables", name: "Variables and secrets",
    hint: "Change variables and set secrets. It can never read a secret back, and a person never sees the value it sends.",
    viewer: "Viewers can’t change variables or secrets.",
  },
  { scope: "runbooks", name: "Runbook steps", hint: "Run a runbook’s steps in a service’s containers, never on a server’s host.", viewer: "Viewers can’t run a page’s commands." },
];

/** An ability starts checked when the client asked for it; when it named no
 *  scopes, deploy, code and issues do, for owners and developers. A viewer
 *  can never give one. */
export function abilityDefault(scope: Scope, requested: string[], role: Role | undefined): boolean {
  if (role === "viewer" || !role) return false;
  return requested.length === 0 ? ["deploy", "code", "issues"].includes(scope) : requested.includes(scope);
}

export const roleLabel = (role: string) => role.charAt(0).toUpperCase() + role.slice(1);

/** The day a connection made now ends: 90 days on (D-100), as YYYY-MM-DD. */
export function until(now: Date): string {
  return new Date(now.getTime() + 90 * 86_400_000).toISOString().slice(0, 10);
}

/** "a, b and c". */
export function listNames(names: string[]): string {
  return names.length < 2 ? (names[0] ?? "") : `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

/** "Founders’", "acme’s". */
export const possessive = (name: string) => (/s$/i.test(name) ? `${name}’` : `${name}’s`);

/** The API's message as a sentence. */
export function sentence(message: string): string {
  const s = message.trim();
  return s ? s.charAt(0).toUpperCase() + s.slice(1) + (/[.!?]$/.test(s) ? "" : ".") : "";
}

/** The POST /oauth/authorize body: the request's own fields and the person's answer. */
export function consentInput(
  query: URLSearchParams,
  answer: { allow: boolean; orgId?: string; allProjects: boolean; projectIds: string[]; scopes: Scope[] },
): AgentConsentInput {
  const q = Object.fromEntries(FIELDS.map((f) => [f, query.get(f) ?? ""])) as Record<(typeof FIELDS)[number], string>;
  return {
    ...q,
    allow: answer.allow,
    org_id: answer.orgId,
    all_projects: answer.allProjects,
    project_ids: answer.allProjects ? [] : answer.projectIds,
    scopes: ["read", ...answer.scopes.filter((s) => s !== "read")],
  };
}
