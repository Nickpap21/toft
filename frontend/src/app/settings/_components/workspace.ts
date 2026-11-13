// Settings → Workspace's wording, kept apart from the page so it is tested.

/** "a", "a and b", "a, b and c". */
export function listOf(items: string[]): string {
  if (items.length <= 1) return items.join("");
  return `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`;
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/** Undefined: its read failed or hasn't answered, so it isn't a count of none. */
export interface DeleteFacts {
  projects?: string[];
  services?: number;
  volumes?: number;
  /** The workspace's own servers; shared ones aren't deleted. */
  servers?: string[];
  members?: number;
  invites: number;
}

const UNKNOWN = "Unknown";

/** What deleting the workspace removes, as the Delete dialog lists it. */
export function deleteRows(f: DeleteFacts): { k: string; v: string }[] {
  const people = [f.members === undefined ? UNKNOWN : `${plural(f.members, "member loses", "members lose")} access`];
  if (f.invites > 0) people.push(`${plural(f.invites, "pending invite stops", "pending invites stop")} working`);
  return [
    { k: "Projects", v: f.projects === undefined ? UNKNOWN : f.projects.length ? `${f.projects.length}: ${f.projects.join(", ")}` : "None" },
    { k: "Services", v: f.services === undefined ? UNKNOWN : f.services ? `${f.services}, removed from their servers with their volumes and data` : "None" },
    { k: "Shared volumes", v: f.volumes === undefined ? UNKNOWN : f.volumes ? `${f.volumes}, with every file in them` : "None" },
    {
      k: "Servers",
      v: f.servers === undefined
        ? UNKNOWN
        : f.servers.length === 0
        ? "None of its own"
        : f.servers.length === 1
          ? `The entry for ${f.servers[0]}. The machine is not touched.`
          : `The entries for ${listOf(f.servers)}. The machines are not touched.`,
    },
    { k: "People", v: people.join("; ") },
    { k: "Also", v: "Settings, the alert webhook and the audit log" },
  ];
}
