// The server page's wording (board: Server), kept apart from the page so
// it is tested.

import { formatMiB, limitMB } from "@/lib/api/resources.service";
import type { HostUpdates, IsolationLevel, ServerCapabilities } from "@/lib/api/server.service";
import { missingList, type Problem } from "@/components/servers/capacity";
import { updateRows } from "@/components/servers/updates-view";

/** "21 days", "5 hours", "12 min". */
export function uptimeLabel(seconds: number): string {
  if (seconds < 3600) return `${Math.max(1, Math.floor(seconds / 60))} min`;
  if (seconds < 86400) {
    const h = Math.floor(seconds / 3600);
    return `${h} ${h === 1 ? "hour" : "hours"}`;
  }
  const d = Math.floor(seconds / 86400);
  return `${d} ${d === 1 ? "day" : "days"}`;
}

/** "traefik:v3.6.10" → "v3.6.10"; a digest-pinned image keeps its tag. */
export function imageTag(image: string): string {
  const name = image.split("@")[0];
  const i = name.lastIndexOf(":");
  return i > name.lastIndexOf("/") && i >= 0 ? name.slice(i + 1) : name;
}

/** A memory limit for display: "512 MiB", "2 GiB", "1.5 GiB"; "none" without one. */
export function memoryLabel(limit: string): string {
  if (!limit) return "none";
  const mb = limitMB(limit);
  return mb >= 1024 ? `${Math.round((mb / 1024) * 10) / 10} GiB` : formatMiB(mb);
}

// lego's names for the DNS providers people use most, as their companies
// write them. Any other provider reads as its lego name, in mono.
const DNS_PROVIDERS: Record<string, string> = {
  cloudflare: "Cloudflare",
  route53: "Amazon Route 53",
  digitalocean: "DigitalOcean",
  hetzner: "Hetzner",
  gcloud: "Google Cloud DNS",
  azuredns: "Azure DNS",
  ovh: "OVH",
  gandiv5: "Gandi",
  namecheap: "Namecheap",
  porkbun: "Porkbun",
  godaddy: "GoDaddy",
  linode: "Linode",
  vultr: "Vultr",
  scaleway: "Scaleway",
  ionos: "IONOS",
  desec: "deSEC",
  duckdns: "Duck DNS",
};

/** A DNS provider's name in a sentence ("Cloudflare"), or null for one
 *  without a known name. */
export function dnsProviderName(provider: string): string | null {
  return DNS_PROVIDERS[provider] ?? null;
}

export interface RemovePlan {
  /** Another workspace still has services here: the server refuses. */
  blockedBy: { name: string; services: number }[];
  /** This workspace's services removed with it. */
  services: number;
  /** Shares that end with it. */
  shares: string[];
  /** Type the server's name to confirm (services go with it). */
  typeName: boolean;
}

export function removePlan(
  ownServices: number,
  others: { org_id: string; org_name: string; services: number }[],
  shares: { org_id: string; org_name: string }[],
): RemovePlan {
  const blockedBy = others.filter((w) => w.services > 0).map((w) => ({ name: w.org_name, services: w.services }));
  return { blockedBy, services: ownServices, shares: shares.map((s) => s.org_name), typeName: ownServices > 0 };
}

const LEVELS = {
  standard: { name: "Standard", runtime: "runc" },
  sandboxed: { name: "Sandboxed", runtime: "gVisor runsc" },
  sandboxed_strict: { name: "Sandboxed, strict network", runtime: "gVisor runsc-strict" },
  microvm: { name: "MicroVM", runtime: "Kata" },
  dedicated: { name: "Dedicated", runtime: "a server of its own" },
} as const;

/** An isolation level's row (board: Server, isolation). */
export function levelRow(level: IsolationLevel): { name: string; runtime: string; state: string } {
  const { name, runtime } = LEVELS[level.id];
  const state =
    level.state === "available"
      ? level.id === "dedicated" ? "always available" : "runs here"
      : level.state === "not_installed" ? "not installed" : level.state === "failing" ? "failing" : "not available";
  return { name, runtime, state };
}

/** The probe's facts under the levels: name, value, a note after it. */
export function capabilityFacts(c: ServerCapabilities): { name: string; value: string; note: string; mono: boolean }[] {
  const security = [c.apparmor && "AppArmor", c.seccomp && "seccomp"].filter(Boolean).join(" · ");
  return [
    { name: "Kernel", value: c.kernel, note: `· ${c.arch}`, mono: true },
    { name: "Virtualisation", value: c.virtualization, note: c.kvm ? "· /dev/kvm" : "· no /dev/kvm", mono: true },
    { name: "cgroups", value: c.cgroup_v2 ? "v2" : "v1", note: c.cgroup_v2 ? "· process and memory limits enforced" : "", mono: true },
    { name: "Docker", value: c.docker_version, note: `· runtimes: ${c.runtimes.map((r) => r.name).join(", ") || "none"}`, mono: true },
    {
      name: "Security",
      value: security || "none",
      note: c.apparmor && c.seccomp ? "· both on" : `· ${c.apparmor ? "seccomp" : c.seccomp ? "AppArmor" : "AppArmor and seccomp"} off`,
      mono: false,
    },
  ];
}

/** The gVisor install's steps as the board names them (D-95, D-115); the
 *  backend logs "[n/6] …" as each starts ("[n/5]" before the strict
 *  network). */
export const GVISOR_STEPS = [
  { label: "Check", note: "arch · apt" },
  { label: "Repository", note: "release" },
  { label: "Install runsc", note: "apt" },
  { label: "Register", note: "reload" },
  { label: "Test", note: "sandboxed" },
  { label: "Strict", note: "agent DNS" },
] as const;

export type GvisorStepState = "done" | "now" | "failed" | "todo";

/** Each step's state from the run's log: the last "[n/6]" line is the
 *  step running, or the one that failed. */
export function gvisorSteps(lines: string[], outcome: "running" | "ok" | "failed"): GvisorStepState[] {
  let current = 0;
  for (const l of lines) {
    const m = /^\[(\d)\/[56]\]/.exec(l);
    if (m) current = Number(m[1]);
  }
  return GVISOR_STEPS.map((_, i) => {
    const n = i + 1;
    if (outcome === "ok" || n < current) return "done";
    if (n > current) return "todo";
    return outcome === "failed" ? "failed" : "now";
  });
}

/** What a failed install left on the server, by the step that failed. */
export function gvisorLeft(states: GvisorStepState[]): string {
  const failed = states.indexOf("failed") + 1;
  if (failed <= 1) return "Nothing on the server changed.";
  if (failed === 2) return "Docker wasn’t changed, and nothing that runs here changed.";
  if (failed === 3) return "The repository is added; Docker wasn’t changed, and nothing that runs here changed.";
  if (failed === 4) return "runsc is installed; nothing that runs here changed.";
  if (failed === 5) return "runsc is installed and registered with Docker; nothing that runs here changed.";
  return "runsc is installed and Sandboxed works; the strict network doesn’t yet, and nothing that runs here changed.";
}

// ─── Tabs and Needs attention (board: ReworkServer) ─────────────────────────

export type ServerTab = "overview" | "maintenance" | "settings";

/** Where an old link's anchor now lives. */
const ANCHOR_TAB: Record<string, ServerTab> = {
  updates: "maintenance",
  disk: "maintenance",
  provisioning: "maintenance",
  backup: "settings",
  "dns-h": "settings",
  connection: "settings",
  agent: "settings",
  isolation: "settings",
  traefik: "settings",
  dns: "settings",
  firewall: "settings",
  overrides: "settings",
  sharing: "settings",
  remove: "settings",
};

/** The open tab: ?tab=, else the tab an old link's #anchor is on. */
export function serverTab(param: string | null, hash: string): ServerTab {
  if (param === "overview" || param === "maintenance" || param === "settings") return param;
  return ANCHOR_TAB[hash.replace(/^#/, "")] ?? "overview";
}

export interface AttentionItem {
  key: string;
  level: Problem["level"];
  title: string;
  sub: string;
  /** The row's link: its label, and the tab and card it opens. */
  action?: string;
  tab?: ServerTab;
  anchor?: string;
}

const RANK = { problem: 0, warning: 1, info: 2 } as const;

/** Needs attention's row for the reads that failed, so an empty list is never
 *  a claim about them. */
export function unreadItem(unread: string[]): { key: string; level: "warning"; title: string; sub: string } {
  const many = unread.length > 1;
  return { key: "unread", level: "warning", title: `Couldn’t check ${missingList(unread)}`, sub: `What ${many ? "they’d" : "it’d"} show is missing here. Reload the page to read ${many ? "them" : "it"} again.` };
}

// Where each of serverProblems' kinds is acted on. Memory suggestions are
// Running here's, so they aren't repeated.
const PROBLEM_LINK: Partial<Record<Problem["kind"], [string, ServerTab, string] | null>> = {
  backup: ["Backup settings", "settings", "backup"],
  no_backup: ["Set up backups", "settings", "backup"],
  provision: ["Re-provision", "maintenance", "provisioning"],
  disk: ["Free space", "maintenance", "disk"],
  cpu_wait: null,
  room: null,
};

/** Overview's Needs attention, worst first: what its updates read found,
 *  the server's problems (as on Servers), and the suggestion to move to the agent. poolFix is
 *  the provisioning run's fix for a missing address pool. Without canAdmin
 *  (an owner or an instance operator, who act on Maintenance and Settings),
 *  each row's link is Review: it opens the card, which doesn't offer the act.
 *  unread names the reads that failed: "nothing needs attention" would be a
 *  claim about them. */
export function attentionItems(f: { problems: Problem[]; updates?: HostUpdates; poolFix?: string; moveToAgent: boolean; canAdmin: boolean; unread?: string[] }): AttentionItem[] {
  const out: AttentionItem[] = f.unread?.length ? [unreadItem(f.unread)] : [];
  const u = f.updates;
  if (u) {
    const rows = Object.fromEntries(updateRows(u).map((r) => [r.key, r]));
    const at = { tab: "maintenance" as const, anchor: "updates" };
    const d = u.docker;
    if (!d.version) {
      out.push({ key: "docker", level: "problem", title: "Docker didn’t answer the updates read", sub: rows.docker.detail, action: "Review", ...at });
    } else if (d.advisories.length) {
      const n = d.advisories.length;
      const high = d.advisories.filter((a) => a.severity === "high" || a.severity === "critical").length;
      const critical = d.advisories.some((a) => a.severity === "critical");
      out.push({
        key: "docker",
        level: high ? "problem" : "warning",
        title: `Docker ${d.version} is affected by ${n} ${n === 1 ? "advisory" : "advisories"}${high ? (n === 1 ? `, ${critical ? "critical" : "high"}` : `, ${high} of them ${critical ? "high or critical" : "high"}`) : ""}`,
        sub: `${d.candidate ? `${d.candidate} is available. ` : ""}Updating Docker restarts its containers.`,
        action: "Update Docker",
        ...at,
      });
    }
    if (rows.lists) {
      out.push({ key: "lists", level: "warning", title: rows.lists.title, sub: rows.lists.detail, action: "Review", ...at });
    }
    if (u.reboot.required) {
      const st = rows.reboot.state;
      out.push({ key: "reboot", level: "warning", title: `Reboot ${st.charAt(0).toLowerCase()}${st.slice(1)}`, sub: rows.reboot.detail, action: "Reboot", ...at });
    }
    if (u.security) {
      out.push({ key: "security", level: "warning", title: `${u.security} security ${u.security === 1 ? "update" : "updates"} waiting`, sub: rows.security.detail, action: "Review", ...at });
    }
    const uu = rows.unattended;
    if (uu.tone !== "ok") {
      out.push({ key: "unattended", level: uu.tone === "bad" ? "problem" : "warning", title: `unattended-upgrades: ${uu.state.toLowerCase()}`, sub: uu.detail, action: "Review", ...at });
    }
  }
  for (const p of f.problems) {
    if (!(p.kind in PROBLEM_LINK)) continue;
    const link = PROBLEM_LINK[p.kind];
    // "Disk 87% full. 3 images…": the first sentence is the title.
    const i = p.text.search(/\.\s/);
    const title = i > 0 ? p.text.slice(0, i) : p.text;
    let sub = i > 0 ? p.text.slice(i + 2) : "";
    if (p.kind === "provision" && f.poolFix) sub = f.poolFix;
    if (p.kind === "cpu_wait") sub = "Set CPU limits on its busiest services, or move one.";
    out.push({ key: p.kind, level: p.level, title, sub, ...(link ? { action: link[0], tab: link[1], anchor: link[2] } : {}) });
  }
  if (f.moveToAgent) {
    out.push({
      key: "agent",
      level: "info",
      title: "Suggested: move to the agent",
      sub: "Nothing inbound to open, and deploys survive a panel restart.",
      action: "Move to the agent",
      tab: "settings",
      anchor: "agent",
    });
  }
  if (!f.canAdmin) for (const it of out) if (it.action) it.action = "Review";
  return out.sort((a, b) => RANK[a.level] - RANK[b.level]);
}

/** The Maintenance tab's count: what needs action there (not a
 *  suggestion), coloured by the worst of it. Its index counts each card
 *  with the items whose anchor is that card. */
export function maintenanceCount(items: AttentionItem[]): { n: number; tone: "bad" | "warn" } {
  const due = items.filter((i) => i.level !== "info" && i.tab === "maintenance");
  return { n: due.length, tone: due.some((i) => i.level === "problem") ? "bad" : "warn" };
}

/** "4 items and a suggestion", "1 item", "a suggestion". */
export function attentionSummary(items: AttentionItem[]): string {
  const n = items.filter((i) => i.level !== "info").length;
  const s = items.length - n;
  const sugg = s === 0 ? "" : s === 1 ? "a suggestion" : `${s} suggestions`;
  if (!n) return sugg || "nothing to do";
  const its = `${n} ${n === 1 ? "item" : "items"}`;
  return sugg ? `${its} and ${sugg}` : its;
}
