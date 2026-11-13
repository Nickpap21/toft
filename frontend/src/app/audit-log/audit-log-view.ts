import type { AuditLogFilter } from "@/lib/api/org.service";
import { actionGroups } from "@/lib/audit";

/** The Who select; "" is everyone. phrase ends the header's count. */
export const WHO = [
  { value: "", label: "People and agents", phrase: "people, agents and API keys" },
  { value: "people", label: "People", phrase: "people" },
  { value: "agents", label: "Agents", phrase: "agents" },
  { value: "keys", label: "API keys", phrase: "API keys" },
] as const;
export type Who = (typeof WHO)[number]["value"];

/** A Who as the API's parameters: people act with neither an agent nor a key. */
export function whoParams(who: Who): Pick<AuditLogFilter, "agent" | "api_key"> {
  switch (who) {
    case "people":
      return { agent: "none", api_key: "none" };
    case "agents":
      return { agent: "any" };
    case "keys":
      return { api_key: "any" };
    default:
      return {};
  }
}

/** A link's ?agent=: any and none are groups, anything else one connection. */
export function fromLink(agent: string): { who: Who; agent: string } {
  if (agent === "any") return { who: "agents", agent: "" };
  if (agent === "none") return { who: "people", agent: "" };
  return { who: "", agent };
}

const groups = actionGroups();
const actionsOf = (group: string) => groups.find((g) => g.group === group)!.actions.map((a) => a.action);

/** The Action select: the board's labels, then the catalogue's other groups. */
export const ACTION_FILTERS: { label: string; actions: string[] }[] = [
  { label: "Deploys and rollbacks", actions: actionsOf("Deploys") },
  { label: "Agent calls", actions: ["agent.call"] },
  { label: "Approvals", actions: ["agent.approved", "agent.denied"] },
  { label: "Access", actions: actionsOf("Access") },
  { label: "Variables and secrets", actions: actionsOf("Variables and secrets") },
  ...groups
    .filter((g) => !["Deploys", "Access", "Variables and secrets"].includes(g.group))
    .map((g) => ({ label: g.group, actions: g.actions.map((a) => a.action) })),
];

export const PERIODS = [
  { value: "today", label: "Today", phrase: "today" },
  { value: "7", label: "Last 7 days", phrase: "in the last 7 days" },
  { value: "30", label: "Last 30 days", phrase: "in the last 30 days" },
  { value: "all", label: "Everything", phrase: "kept" },
] as const;
export type Period = (typeof PERIODS)[number]["value"];

/** Where a period starts; Today from local midnight. */
export function periodSince(period: Period, now: Date): string | undefined {
  if (period === "all") return undefined;
  if (period === "today") return new Date(now.getFullYear(), now.getMonth(), now.getDate()).toISOString();
  return new Date(now.getTime() - Number(period) * 86_400_000).toISOString();
}

/** What the table says with no rows. While a next page exists it never says
 *  there is nothing: older entries can still match. */
export function emptyLine(o: { hasMore: boolean; search: string; filtered: boolean; phrase: string }): string {
  if (o.hasMore) return "Nothing loaded yet. Load older entries to look further back.";
  const when = o.phrase === "kept" ? "" : ` ${o.phrase}`;
  if (o.filtered) return `No entries match ${o.search ? `“${o.search}”` : "these filters"}${when}.`;
  return when ? `Nothing happened${when}.` : "Nothing has happened in this workspace yet.";
}
