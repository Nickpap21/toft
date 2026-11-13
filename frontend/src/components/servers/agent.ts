// The node agent's state as the pages show it (D-106; boards ServerAgent,
// AgentOffline, AddServerAgent).
import type { AgentStatus, Server, ServerAgent } from "@/lib/api/server.service";
import { formatDateTime } from "@/lib/utils";

export type Tone = "running" | "pending" | "failed" | "building";

export const TONE: Record<Tone, { chip: string; text: string; dot: string }> = {
  running: { chip: "bg-status-running-soft text-status-running-fg", text: "text-status-running-fg", dot: "bg-status-running" },
  pending: { chip: "bg-status-pending-soft text-status-pending-fg", text: "text-status-pending-fg", dot: "bg-status-pending" },
  failed: { chip: "bg-status-failed-soft text-status-failed-fg", text: "text-status-failed-fg", dot: "bg-status-failed" },
  building: { chip: "bg-status-building-soft text-status-building-fg", text: "text-status-building-fg", dot: "bg-status-building" },
};

/** Whether the panel reaches the server through its agent. */
export function usesAgent(s: Pick<Server, "connection">): boolean {
  return s.connection === "agent";
}

/** A server Add a server made that no agent has joined yet (D-153). */
export function waitingForAgent(s: Pick<Server, "connection" | "agent" | "provisioned_at">): boolean {
  return s.connection === "agent" && !s.agent?.key_fingerprint && !s.provisioned_at;
}

/** The servers a service, volume or share can be put on: a waiting server
 *  runs nothing, and the API refuses it (409 server_not_ready). */
export function placeable<T extends Pick<Server, "connection" | "agent" | "provisioned_at">>(servers: T[]): T[] {
  return servers.filter((s) => !waitingForAgent(s));
}

/** The agent's status, or null for a server reached over SSH. */
export function agentStatus(s: Pick<Server, "connection" | "agent">): AgentStatus | null {
  if (!usesAgent(s)) return null;
  return s.agent?.status ?? "waiting";
}

/** The agent states in which the panel can't reach the server. */
export function agentAway(s: Pick<Server, "connection" | "agent">): boolean {
  const st = agentStatus(s);
  return st === "offline" || st === "waiting" || st === "revoked";
}

const LABEL: Record<AgentStatus, [string, Tone, boolean]> = {
  connected: ["Online", "running", false],
  reconnecting: ["Reconnecting", "pending", true],
  upgrading: ["Upgrading agent", "building", true],
  offline: ["Agent offline", "failed", false],
  waiting: ["Waiting for its agent", "building", true],
  revoked: ["Agent revoked", "failed", false],
};

/** The header and list chip of an agent server: label, tone, pulsing. A
 *  connected agent frozen after a restore (D-113) says so. */
export function agentChip(status: AgentStatus, frozen = false): { label: string; tone: Tone; pulse: boolean } {
  if (frozen && status === "connected") return { label: "Frozen after a restore", tone: "pending", pulse: false };
  const [label, tone, pulse] = LABEL[status];
  return { label, tone, pulse };
}

/** Whether the server's agent holds another panel epoch's state: the panel
 *  was restored from a backup and someone must choose which to keep. */
export function agentFrozen(s: Pick<Server, "connection" | "agent">): boolean {
  return usesAgent(s) && !!s.agent?.frozen;
}

/** "38 s", "14 min", "2 h 14 min", "3 d": how long since iso. */
export function awayFor(iso: string, now: number): string {
  const s = Math.max(0, Math.floor((now - new Date(iso).getTime()) / 1000));
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  if (h < 24) return m % 60 ? `${h} h ${m % 60} min` : `${h} h`;
  return `${Math.floor(h / 24)} d`;
}

/** "12:03" in the viewer's time today, else "2026-10-19 12:03". */
export function clock(iso: string): string {
  const d = new Date(iso);
  return d.toDateString() === new Date().toDateString() ? d.toTimeString().slice(0, 5) : formatDateTime(d);
}

/** Split an "ssh-ed25519 SHA256:…" style fingerprint for display: the
 *  agent's fingerprints come bare (SHA256:…). */
export function fingerprintParts(fp: string | null | undefined): { type: string; fp: string } | null {
  if (!fp) return null;
  return { type: "ssh-ed25519", fp };
}

type Desired = NonNullable<ServerAgent["desired"]>;

const DRIFT: Record<Desired["drift"][number]["kind"], string> = {
  missing: "isn’t running",
  stopped: "is stopped",
  crash_loop: "keeps restarting",
  orphan: "runs here, but the panel has no such service",
};

/** The Desired state row (D-111): what the agent holds against what should
 *  run here, the first 5 differences and how many more. names maps service
 *  IDs to names; a service the viewer can't see shows its ID's first 8
 *  characters (id set). */
export function desiredRow(
  desired: ServerAgent["desired"] | undefined,
  status: AgentStatus,
  names: Record<string, string>,
  now: number,
): { text: string; tone: Tone | null; list: { who: string | null; id: boolean; what: string }[]; more: number; note: string } {
  if (!desired) return { text: "Not reported yet", tone: null, list: [], more: 0, note: "Agents from v0.18.0 report it once connected." };
  const list = desired.drift.map((d) => {
    const name = d.service_id ? names[d.service_id] : undefined;
    if (d.kind === "orphan" || !d.service_id) return { who: d.container, id: false, what: DRIFT[d.kind] };
    return name ? { who: name, id: false, what: DRIFT[d.kind] } : { who: d.service_id.slice(0, 8), id: true, what: DRIFT[d.kind] };
  });
  const checked = status === "connected" ? `Checked ${awayFor(desired.checked_at, now)} ago.` : `As of ${clock(desired.checked_at)}.`;
  const live = status === "connected";
  if (desired.observed_generation < desired.spec_generation)
    return { text: "Catching up", tone: live ? "pending" : null, list: [], more: 0, note: `${checked} The agent gets the latest change within 30 s.` };
  if (list.length) {
    const orphans = desired.drift.filter((d) => d.kind === "orphan").length;
    const services = desired.drift.length - orphans;
    const steps = [
      services ? `redeploy ${services === 1 ? "the service" : "those services"}` : "",
      orphans ? `remove ${orphans === 1 ? "the container" : "those containers"}` : "",
    ].filter(Boolean);
    return {
      text: `${list.length} ${list.length === 1 ? "difference" : "differences"}`,
      tone: live ? "pending" : null,
      list: list.slice(0, 5),
      more: Math.max(0, list.length - 5),
      note: `${checked} Toft doesn’t fix this on its own yet: ${steps.join(", or ")}.`,
    };
  }
  return {
    text: "In step",
    tone: live ? "running" : null,
    list: [],
    more: 0,
    note: live ? `${checked} The agent holds what should run here and finishes deploys if the panel restarts.` : `${checked} The agent keeps what should run here while the panel can’t reach it.`,
  };
}

type FrozenChange = NonNullable<ServerAgent["frozen"]>["changes"][number];

/** A frozen change's service name, or its ID's first 8 characters for one
 *  the viewer can't see (id set), or the container of a service the panel
 *  doesn't have; as desiredRow's list. */
function changeName(c: FrozenChange, names: Record<string, string>): { who: string | null; id: boolean } {
  if (!c.service_id) return { who: c.container, id: false };
  const name = names[c.service_id];
  return name ? { who: name, id: false } : { who: c.service_id.slice(0, 8), id: true };
}

/** changeName as one label, for the dialogs. */
function changeLabel(c: FrozenChange, names: Record<string, string>): string {
  const { who, id } = changeName(c, names);
  return id ? `Service ${who}` : (who ?? "A container");
}

// The boards' order: what changes first, then what stays, runs or is missing.
const ORDER: FrozenChange["kind"][] = ["newer", "same", "unknown", "missing"];
const byKind = (changes: FrozenChange[]) => [...changes].sort((a, b) => ORDER.indexOf(a.kind) - ORDER.indexOf(b.kind));

const FROZEN_DIFF: Record<Exclude<FrozenChange["kind"], "same">, (c: FrozenChange) => string> = {
  newer: (c) => (c.started_at ? `runs a deploy from ${formatDateTime(c.started_at).slice(0, 16)}, newer than the backup` : "runs a deploy newer than the backup"),
  unknown: () => "runs here, but the backup has no such service",
  missing: () => "is in the backup, but nothing of it runs here",
};

/** The Desired state row of a frozen server (D-113): how what runs differs
 *  from the restored panel. */
export function frozenRow(
  frozen: NonNullable<ServerAgent["frozen"]>,
  names: Record<string, string>,
): ReturnType<typeof desiredRow> {
  const list = byKind(frozen.changes).flatMap((c) => (c.kind === "same" ? [] : [{ ...changeName(c, names), what: FROZEN_DIFF[c.kind](c) }]));
  return {
    text: list.length ? `Frozen · ${list.length} ${list.length === 1 ? "difference" : "differences"}` : "Frozen · no differences",
    tone: "pending",
    list: list.slice(0, 5),
    more: Math.max(0, list.length - 5),
    note: "Between this server and the restored panel. The agent keeps everything running as it was and takes no order from the restored panel until someone chooses which state to keep.",
  };
}

const ADOPT: Record<Exclude<FrozenChange["kind"], "same">, (c: FrozenChange) => string> = {
  newer: (c) =>
    `becomes the deploy that runs here${c.started_at ? `, ${formatDateTime(c.started_at).slice(0, 16)}` : ""}${c.image ? ` (${c.image})` : ""}. The panel records it as “Adopted · you”, since the backup predates it.`,
  unknown: () => "keeps running. The backup has no service for it, so it shows as a difference until you remove it.",
  missing: () => "stays in the panel with nothing running. Deploy it again to start it.",
};

const USE_PANEL: Record<Exclude<FrozenChange["kind"], "same">, string> = {
  newer: "keeps running its newer deploy until you deploy it again, which goes back to what the backup has.",
  unknown: "keeps running until you remove it.",
  missing: "stays stopped until you deploy it.",
};

/** What adopting the server's state, or using the panel's, does to each
 *  service (the board's dialog items). Services that match share one line;
 *  using the panel's state leaves them out. */
export function frozenItems(
  frozen: NonNullable<ServerAgent["frozen"]>,
  names: Record<string, string>,
  adopt: boolean,
): { t: string; d: string }[] {
  const sorted = byKind(frozen.changes);
  const item = (c: FrozenChange) => ({ t: changeLabel(c, names), d: c.kind === "same" ? "" : adopt ? ADOPT[c.kind](c) : USE_PANEL[c.kind] });
  const differ = sorted.filter((c) => c.kind !== "same").map(item);
  const same = sorted.filter((c) => c.kind === "same").map((c) => changeLabel(c, names));
  if (!adopt || !same.length) return differ;
  const newer = sorted.filter((c) => c.kind === "newer").length;
  const kept = { t: same.join(", "), d: same.length === 1 ? "already matches the backup: kept as it is." : "already match the backup: kept as they are." };
  return [...differ.slice(0, newer), kept, ...differ.slice(newer)];
}
